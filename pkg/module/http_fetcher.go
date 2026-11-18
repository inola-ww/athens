package module

import (
	"context"
	"crypto/md5" //nolint:gosec // storage backends expect an MD5 of the zip
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/observ"
	"github.com/gomods/athens/pkg/storage"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
	"golang.org/x/sync/errgroup"
)

type httpFetcher struct {
	baseURL  string
	client   *http.Client
	tmpDir   string
	private  string
	fallback Fetcher
}

// NewHTTPFetcher creates a Fetcher that downloads .info, .mod and .zip
// straight from an upstream module proxy (e.g. https://proxy.golang.org)
// instead of running `go mod download`. The zip is spooled to a temporary
// file in tmpDir ("" for the OS default) and validated with
// golang.org/x/mod/zip.CheckZip. Modules matching the comma-separated
// privatePatterns (GOPRIVATE/GONOPROXY syntax) are handed to fallback.
func NewHTTPFetcher(baseURL string, client *http.Client, tmpDir, privatePatterns string, fallback Fetcher) Fetcher {
	return &httpFetcher{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		client:   client,
		tmpDir:   tmpDir,
		private:  privatePatterns,
		fallback: fallback,
	}
}

func (f *httpFetcher) Fetch(ctx context.Context, mod, ver string) (*storage.Version, error) {
	const op errors.Op = "httpFetcher.Fetch"
	if f.private != "" && module.MatchPrefixPatterns(f.private, mod) {
		return f.fallback.Fetch(ctx, mod, ver)
	}
	ctx, span := observ.StartSpan(ctx, op.String())
	defer span.End()

	escMod, err := module.EscapePath(mod)
	if err != nil {
		return nil, errors.E(op, err, errors.KindBadRequest, errors.M(mod), errors.V(ver))
	}
	escVer, err := module.EscapeVersion(ver)
	if err != nil {
		return nil, errors.E(op, err, errors.KindBadRequest, errors.M(mod), errors.V(ver))
	}
	base := f.baseURL + "/" + escMod + "/@v/"

	// .info resolves a query (branch, short hash) to the canonical version
	// that .mod and .zip have to be requested with.
	info, err := upstreamGetBytes(ctx, f.client, base+escVer+".info")
	if err != nil {
		return nil, errors.E(op, err, errors.M(mod), errors.V(ver))
	}
	var rev storage.RevInfo
	if err := json.Unmarshal(info, &rev); err != nil {
		return nil, errors.E(op, fmt.Errorf("decoding .info: %w", err), errors.M(mod), errors.V(ver))
	}
	mv := module.Version{Path: mod, Version: rev.Version}
	if err := module.Check(mv.Path, mv.Version); err != nil {
		return nil, errors.E(op, err, errors.M(mod), errors.V(ver))
	}
	if escVer, err = module.EscapeVersion(mv.Version); err != nil {
		return nil, errors.E(op, err, errors.M(mod), errors.V(ver))
	}

	var gomod []byte
	var zipFile *os.File
	var zipMD5 []byte
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		gomod, err = upstreamGetBytes(gctx, f.client, base+escVer+".mod")
		return err
	})
	g.Go(func() error {
		var err error
		zipFile, zipMD5, err = f.spoolZip(gctx, base+escVer+".zip")
		return err
	})
	if err := g.Wait(); err != nil {
		if zipFile != nil {
			removeTemp(zipFile)
		}
		return nil, errors.E(op, err, errors.M(mod), errors.V(ver))
	}

	if _, err := modzip.CheckZip(mv, zipFile.Name()); err != nil {
		removeTemp(zipFile)
		return nil, errors.E(op, fmt.Errorf("invalid module zip: %w", err), errors.M(mod), errors.V(ver))
	}
	if _, err := zipFile.Seek(0, io.SeekStart); err != nil {
		removeTemp(zipFile)
		return nil, errors.E(op, err)
	}

	return &storage.Version{
		Semver: mv.Version,
		Info:   info,
		Mod:    gomod,
		Zip:    &tempFileReadCloser{zipFile},
		ZipMD5: zipMD5,
	}, nil
}

// spoolZip downloads url into a temporary file and returns it with its MD5.
func (f *httpFetcher) spoolZip(ctx context.Context, url string) (*os.File, []byte, error) {
	const op errors.Op = "httpFetcher.spoolZip"
	body, err := upstreamGet(ctx, f.client, url)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = body.Close() }()

	tmp, err := os.CreateTemp(f.tmpDir, "athens-zip-*")
	if err != nil {
		return nil, nil, errors.E(op, err)
	}
	h := md5.New() //nolint:gosec
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, modzip.MaxZipFile+1))
	if err != nil {
		return tmp, nil, errors.E(op, err)
	}
	if n > modzip.MaxZipFile {
		return tmp, nil, errors.E(op, fmt.Sprintf("%s: zip larger than %d bytes", url, int64(modzip.MaxZipFile)))
	}
	return tmp, h.Sum(nil), nil
}

// tempFileReadCloser deletes its file on Close.
type tempFileReadCloser struct{ f *os.File }

func (t *tempFileReadCloser) Read(p []byte) (int, error) { return t.f.Read(p) }

func (t *tempFileReadCloser) Close() error {
	removeTemp(t.f)
	return nil
}

func removeTemp(f *os.File) {
	_ = f.Close()
	_ = os.Remove(f.Name())
}
