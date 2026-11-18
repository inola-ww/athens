package module

import (
	"context"

	"github.com/gomods/athens/pkg/errors"
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
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, errors.E(op, ctx.Err())
	}
	defer func() { <-l.sem }()
	return l.lister.List(ctx, mod)
}
