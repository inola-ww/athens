package addons

import (
	"context"

	"github.com/gomods/athens/pkg/download"
	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/storage"
)

// withpool limits how many download.Protocol calls run at once. A caller
// waiting for a slot gives up when its context is done, so disconnected
// clients don't queue for (and then occupy) a slot.
type withpool struct {
	dp  download.Protocol
	sem chan struct{}
}

// WithPool takes a download Protocol and a number of workers
// and limits all download.Protocol methods to that many
// concurrent calls.
func WithPool(workers int) download.Wrapper {
	return func(dp download.Protocol) download.Protocol {
		return &withpool{dp: dp, sem: make(chan struct{}, workers)}
	}
}

func run[T any](ctx context.Context, p *withpool, op errors.Op, f func() (T, error)) (T, error) {
	var zero T
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return zero, errors.E(op, ctx.Err())
	}
	defer func() { <-p.sem }()
	v, err := f()
	if err != nil {
		return zero, errors.E(op, err)
	}
	return v, nil
}

func (p *withpool) List(ctx context.Context, mod string) ([]string, error) {
	return run(ctx, p, "pool.List", func() ([]string, error) { return p.dp.List(ctx, mod) })
}

func (p *withpool) Info(ctx context.Context, mod, ver string) ([]byte, error) {
	return run(ctx, p, "pool.Info", func() ([]byte, error) { return p.dp.Info(ctx, mod, ver) })
}

func (p *withpool) Latest(ctx context.Context, mod string) (*storage.RevInfo, error) {
	return run(ctx, p, "pool.Latest", func() (*storage.RevInfo, error) { return p.dp.Latest(ctx, mod) })
}

func (p *withpool) GoMod(ctx context.Context, mod, ver string) ([]byte, error) {
	return run(ctx, p, "pool.GoMod", func() ([]byte, error) { return p.dp.GoMod(ctx, mod, ver) })
}

func (p *withpool) Zip(ctx context.Context, mod, ver string) (storage.SizeReadCloser, error) {
	return run(ctx, p, "pool.Zip", func() (storage.SizeReadCloser, error) { return p.dp.Zip(ctx, mod, ver) })
}
