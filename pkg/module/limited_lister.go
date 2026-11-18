package module

import (
	"context"
	"time"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/observ"
	"github.com/gomods/athens/pkg/storage"
)

type limitedLister struct {
	lister UpstreamLister
	sem    chan struct{}
}

// NewLimitedLister caps the number of concurrent upstream lookups. Waiting
// callers give up when their context is done.
func NewLimitedLister(lister UpstreamLister, limit int) UpstreamLister {
	return &limitedLister{lister: lister, sem: make(chan struct{}, limit)}
}

func (l *limitedLister) List(ctx context.Context, mod string) (*storage.RevInfo, []string, error) {
	const op errors.Op = "limitedLister.List"
	waitStart := time.Now()
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		observ.RecordUpstreamListWait(ctx, time.Since(waitStart))
		return nil, nil, errors.E(op, ctx.Err())
	}
	observ.RecordUpstreamListWait(ctx, time.Since(waitStart))
	defer func() { <-l.sem }()

	observ.AddUpstreamListInFlight(ctx, 1)
	defer observ.AddUpstreamListInFlight(ctx, -1)
	start := time.Now()
	rev, versions, err := l.lister.List(ctx, mod)
	observ.RecordUpstreamList(ctx, listResult(ctx, err), time.Since(start))
	return rev, versions, err
}

func listResult(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return "ok"
	case ctx.Err() != nil:
		return "canceled"
	case errors.Is(err, errors.KindGatewayTimeout):
		return "timeout"
	case errors.Is(err, errors.KindNotFound):
		return "not_found"
	default:
		return "error"
	}
}
