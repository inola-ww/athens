package module

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/observ"
	"github.com/gomods/athens/pkg/storage"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"golang.org/x/sync/errgroup"
)

// maxListBody caps how much of an upstream /@v/list or .info response is read.
const maxListBody = 4 << 20

type httpLister struct {
	baseURL  string
	client   *http.Client
	private  string
	fallback UpstreamLister
}

// NewHTTPLister creates an UpstreamLister that asks an upstream module proxy
// (e.g. https://proxy.golang.org) for /@v/list and /@latest over HTTP instead
// of running `go list`. Modules matching the comma-separated privatePatterns
// (GOPRIVATE/GONOPROXY syntax) are handed to fallback.
func NewHTTPLister(baseURL string, client *http.Client, privatePatterns string, fallback UpstreamLister) UpstreamLister {
	return &httpLister{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		client:   client,
		private:  privatePatterns,
		fallback: fallback,
	}
}

func (l *httpLister) List(ctx context.Context, mod string) (*storage.RevInfo, []string, error) {
	if l.private != "" && module.MatchPrefixPatterns(l.private, mod) {
		return l.fallback.List(ctx, mod)
	}
	start := time.Now()
	rev, versions, err := l.lookup(ctx, mod)
	observ.RecordUpstreamList(ctx, "http", listResult(ctx, err), time.Since(start))
	return rev, versions, err
}

func (l *httpLister) lookup(ctx context.Context, mod string) (*storage.RevInfo, []string, error) {
	const op errors.Op = "httpLister.List"
	escaped, err := module.EscapePath(mod)
	if err != nil {
		return nil, nil, errors.E(op, err, errors.KindBadRequest, errors.M(mod))
	}
	base := l.baseURL + "/" + escaped

	var versions []string
	var rev *storage.RevInfo
	var listErr, latestErr error
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		versions, listErr = l.list(gctx, base)
		return nil
	})
	g.Go(func() error {
		rev, latestErr = l.info(gctx, base+"/@latest")
		return nil
	})
	_ = g.Wait()

	if listErr != nil {
		return nil, nil, errors.E(op, listErr, errors.M(mod))
	}
	if latestErr != nil {
		// Like `go list -m mod@latest`, prefer the highest listed version when
		// the upstream has no /@latest answer for it.
		latest := highestVersion(versions)
		if latest == "" {
			return nil, nil, errors.E(op, latestErr, errors.M(mod))
		}
		v, err := module.EscapeVersion(latest)
		if err != nil {
			return nil, nil, errors.E(op, err, errors.M(mod))
		}
		if rev, err = l.info(ctx, base+"/@v/"+v+".info"); err != nil {
			return nil, nil, errors.E(op, err, errors.M(mod))
		}
	}
	return rev, versions, nil
}

func (l *httpLister) list(ctx context.Context, base string) ([]string, error) {
	body, err := l.get(ctx, base+"/@v/list")
	if err != nil {
		return nil, err
	}
	var versions []string
	for v := range strings.FieldsSeq(string(body)) {
		if semver.IsValid(v) {
			versions = append(versions, v)
		}
	}
	return versions, nil
}

func (l *httpLister) info(ctx context.Context, url string) (*storage.RevInfo, error) {
	body, err := l.get(ctx, url)
	if err != nil {
		return nil, err
	}
	var rev storage.RevInfo
	if err := json.Unmarshal(body, &rev); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", url, err)
	}
	return &rev, nil
}

func (l *httpLister) get(ctx context.Context, url string) ([]byte, error) {
	const op errors.Op = "httpLister.get"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.E(op, err)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		if errors.IsErr(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.E(op, err, errors.KindGatewayTimeout)
		}
		return nil, errors.E(op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBody))
	if err != nil {
		return nil, errors.E(op, err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return body, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, errors.E(op, fmt.Sprintf("%s: %s", url, strings.TrimSpace(string(body))), errors.KindNotFound)
	default:
		return nil, errors.E(op, fmt.Sprintf("%s: unexpected status %d", url, resp.StatusCode), errors.KindUnexpected)
	}
}

// highestVersion returns the highest release, or the highest pre-release when
// there is none, matching how the go command picks @latest from a list.
func highestVersion(versions []string) string {
	var release, pre string
	for _, v := range versions {
		if semver.Prerelease(v) == "" {
			if semver.Compare(v, release) > 0 {
				release = v
			}
		} else if semver.Compare(v, pre) > 0 {
			pre = v
		}
	}
	if release != "" {
		return release
	}
	return pre
}
