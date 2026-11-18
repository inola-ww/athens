package module

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/storage"
	"github.com/stretchr/testify/require"
)

type fakeLister struct {
	calls atomic.Int32
	err   error
}

func (f *fakeLister) List(_ context.Context, mod string) (*storage.RevInfo, []string, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, nil, f.err
	}
	return &storage.RevInfo{Version: "v1.1.0"}, []string{"v1.0.0", "v1.1.0"}, nil
}

const testTTL = 50 * time.Millisecond

func newTestCachedLister(inner UpstreamLister, opts CachedListerOpts) *cachedLister {
	return NewCachedLister(inner, opts).(*cachedLister)
}

func TestCachedListerCachesHits(t *testing.T) {
	inner := &fakeLister{}
	c := newTestCachedLister(inner, CachedListerOpts{TTL: testTTL, Size: 10})

	for range 3 {
		rev, versions, err := c.List(t.Context(), "example.com/a")
		require.NoError(t, err)
		require.Equal(t, "v1.1.0", rev.Version)
		require.Equal(t, []string{"v1.0.0", "v1.1.0"}, versions)
	}
	require.EqualValues(t, 1, inner.calls.Load())

	time.Sleep(2 * testTTL)
	_, _, err := c.List(t.Context(), "example.com/a")
	require.NoError(t, err)
	require.EqualValues(t, 2, inner.calls.Load())
}

func TestCachedListerReturnsCopies(t *testing.T) {
	c := newTestCachedLister(&fakeLister{}, CachedListerOpts{TTL: time.Minute, Size: 10})

	rev, versions, err := c.List(t.Context(), "example.com/a")
	require.NoError(t, err)
	rev.Version = "mutated"
	versions[0] = "mutated"

	rev, versions, err = c.List(t.Context(), "example.com/a")
	require.NoError(t, err)
	require.Equal(t, "v1.1.0", rev.Version)
	require.Equal(t, "v1.0.0", versions[0])
}

func TestCachedListerNegativeCache(t *testing.T) {
	const op errors.Op = "test"
	inner := &fakeLister{err: errors.E(op, "no such module", errors.KindNotFound)}
	c := newTestCachedLister(inner, CachedListerOpts{TTL: time.Hour, NegativeTTL: testTTL, Size: 10})

	for range 3 {
		_, _, err := c.List(t.Context(), "example.com/missing")
		require.True(t, errors.Is(err, errors.KindNotFound))
	}
	require.EqualValues(t, 1, inner.calls.Load())

	time.Sleep(2 * testTTL)
	_, _, _ = c.List(t.Context(), "example.com/missing")
	require.EqualValues(t, 2, inner.calls.Load())
}

func TestCachedListerNegativeCacheDisabled(t *testing.T) {
	const op errors.Op = "test"
	inner := &fakeLister{err: errors.E(op, "no such module", errors.KindNotFound)}
	c := newTestCachedLister(inner, CachedListerOpts{TTL: time.Hour, Size: 10})

	_, _, _ = c.List(t.Context(), "example.com/missing")
	_, _, _ = c.List(t.Context(), "example.com/missing")
	require.EqualValues(t, 2, inner.calls.Load())
}

func TestCachedListerSkipsOtherErrors(t *testing.T) {
	const op errors.Op = "test"
	inner := &fakeLister{err: errors.E(op, "timeout", errors.KindGatewayTimeout)}
	c := newTestCachedLister(inner, CachedListerOpts{TTL: time.Hour, NegativeTTL: time.Hour, Size: 10})

	_, _, _ = c.List(t.Context(), "example.com/a")
	_, _, err := c.List(t.Context(), "example.com/a")
	require.True(t, errors.Is(err, errors.KindGatewayTimeout))
	require.EqualValues(t, 2, inner.calls.Load())
}

func TestCachedListerSkipsCanceledRequests(t *testing.T) {
	const op errors.Op = "test"
	inner := &fakeLister{err: errors.E(op, "killed", errors.KindNotFound)}
	c := newTestCachedLister(inner, CachedListerOpts{TTL: time.Hour, NegativeTTL: time.Hour, Size: 10})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, _ = c.List(ctx, "example.com/a")
	_, _, _ = c.List(t.Context(), "example.com/a")
	require.EqualValues(t, 2, inner.calls.Load())
}

func TestCachedListerEvictsOldest(t *testing.T) {
	inner := &fakeLister{}
	c := newTestCachedLister(inner, CachedListerOpts{TTL: time.Hour, Size: 2})

	for _, mod := range []string{"example.com/a", "example.com/b", "example.com/c"} {
		_, _, err := c.List(t.Context(), mod)
		require.NoError(t, err)
	}
	require.Equal(t, 2, c.hits.Len())
	require.EqualValues(t, 3, inner.calls.Load())

	_, _, _ = c.List(t.Context(), "example.com/c")
	require.EqualValues(t, 3, inner.calls.Load())
	_, _, _ = c.List(t.Context(), "example.com/a")
	require.EqualValues(t, 4, inner.calls.Load())
}
