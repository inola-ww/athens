package module

import (
	"context"
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/observ"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/mod/module"
)

// ErrMetaSkipped is returned by UpstreamMeta for modules it does not serve
// (GONOPROXY/GOPRIVATE matches); callers fall back to their normal miss handling.
var ErrMetaSkipped = stderrors.New("module not served by upstream meta")

// UpstreamMeta serves .info and .mod files straight from an upstream module
// proxy, for storage misses that should not trigger a full module download.
type UpstreamMeta interface {
	Info(ctx context.Context, mod, ver string) ([]byte, error)
	GoMod(ctx context.Context, mod, ver string) ([]byte, error)
}

type httpMeta struct {
	baseURL string
	client  *http.Client
	private string
	cache   *lru.Cache[string, []byte]
}

// NewHTTPMeta creates an UpstreamMeta against baseURL (e.g.
// https://proxy.golang.org). Responses for canonical versions never change and
// are kept in an LRU of cacheSize entries; 0 disables the cache.
func NewHTTPMeta(baseURL string, client *http.Client, privatePatterns string, cacheSize int) (UpstreamMeta, error) {
	m := &httpMeta{baseURL: strings.TrimSuffix(baseURL, "/"), client: client, private: privatePatterns}
	if cacheSize > 0 {
		c, err := lru.New[string, []byte](cacheSize)
		if err != nil {
			return nil, err
		}
		m.cache = c
	}
	return m, nil
}

func (m *httpMeta) Info(ctx context.Context, mod, ver string) ([]byte, error) {
	return m.get(ctx, mod, ver, ".info", "upstream_info")
}

func (m *httpMeta) GoMod(ctx context.Context, mod, ver string) ([]byte, error) {
	return m.get(ctx, mod, ver, ".mod", "upstream_mod")
}

func (m *httpMeta) get(ctx context.Context, mod, ver, ext, cacheType string) ([]byte, error) {
	const op errors.Op = "httpMeta.get"
	if m.private != "" && module.MatchPrefixPatterns(m.private, mod) {
		return nil, ErrMetaSkipped
	}
	// Queries like "master" resolve differently over time; only canonical
	// versions are immutable and safe to cache.
	cacheable := m.cache != nil && module.Check(mod, ver) == nil
	key := mod + "@" + ver + ext
	if cacheable {
		if b, ok := m.cache.Get(key); ok {
			observ.RecordCacheLookup(ctx, "hit", cacheType)
			return b, nil
		}
		observ.RecordCacheLookup(ctx, "miss", cacheType)
	}
	escMod, err := module.EscapePath(mod)
	if err != nil {
		return nil, errors.E(op, err, errors.KindBadRequest, errors.M(mod), errors.V(ver))
	}
	escVer, err := module.EscapeVersion(ver)
	if err != nil {
		return nil, errors.E(op, err, errors.KindBadRequest, errors.M(mod), errors.V(ver))
	}
	b, err := upstreamGetBytes(ctx, m.client, m.baseURL+"/"+escMod+"/@v/"+escVer+ext)
	if err != nil {
		return nil, errors.E(op, err, errors.M(mod), errors.V(ver))
	}
	if cacheable {
		m.cache.Add(key, b)
	}
	return b, nil
}
