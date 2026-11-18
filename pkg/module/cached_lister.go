package module

import (
	"context"
	"slices"
	"time"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/storage"
	"github.com/hashicorp/golang-lru/v2/expirable"
)

// CachedListerOpts configures NewCachedLister.
type CachedListerOpts struct {
	// TTL is how long a successful upstream listing is reused.
	TTL time.Duration
	// NegativeTTL is how long a not-found listing is reused. Zero disables
	// negative caching.
	NegativeTTL time.Duration
	// Size caps the number of cached modules, separately for found and
	// not-found results.
	Size int
}

type cachedLister struct {
	lister UpstreamLister
	hits   *expirable.LRU[string, listing]
	misses *expirable.LRU[string, error]
}

type listing struct {
	rev      *storage.RevInfo
	versions []string
}

// NewCachedLister wraps an UpstreamLister with an in-memory TTL cache so that
// repeated /@v/list and /@latest requests for the same module don't each
// trigger an upstream lookup. Errors other than not-found are never cached.
func NewCachedLister(lister UpstreamLister, opts CachedListerOpts) UpstreamLister {
	c := &cachedLister{
		lister: lister,
		hits:   expirable.NewLRU[string, listing](opts.Size, nil, opts.TTL),
	}
	if opts.NegativeTTL > 0 {
		c.misses = expirable.NewLRU[string, error](opts.Size, nil, opts.NegativeTTL)
	}
	return c
}

func (c *cachedLister) List(ctx context.Context, mod string) (*storage.RevInfo, []string, error) {
	if l, ok := c.hits.Get(mod); ok {
		return l.copy()
	}
	if c.misses != nil {
		if err, ok := c.misses.Get(mod); ok {
			return nil, nil, err
		}
	}

	rev, versions, err := c.lister.List(ctx, mod)
	// A canceled request can surface as a not-found from the go command; don't
	// let one client's disconnect poison the cache for everyone else.
	if ctx.Err() != nil {
		return rev, versions, err
	}
	if err != nil {
		if c.misses != nil && errors.Is(err, errors.KindNotFound) {
			c.misses.Add(mod, err)
		}
		return rev, versions, err
	}

	if c.misses != nil {
		c.misses.Remove(mod)
	}
	l := listing{rev: rev, versions: versions}
	c.hits.Add(mod, l)
	return l.copy()
}

// copy hands out copies so callers can't mutate cached state.
func (l listing) copy() (*storage.RevInfo, []string, error) {
	var rev *storage.RevInfo
	if l.rev != nil {
		rev = new(*l.rev)
	}
	return rev, slices.Clone(l.versions), nil
}
