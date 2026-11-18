package stash

import (
	"context"
	"errors"
	"testing"
	"time"
)

type blockStasher struct {
	started chan struct{}
	block   chan struct{}
}

func (b *blockStasher) Stash(ctx context.Context, mod, ver string) (string, error) {
	b.started <- struct{}{}
	<-b.block
	return ver, nil
}

func TestPoolHonorsContext(t *testing.T) {
	b := &blockStasher{started: make(chan struct{}, 1), block: make(chan struct{})}
	defer close(b.block)
	s := WithPool(1)(b)
	go func() { _, _ = s.Stash(t.Context(), "mod", "v1.0.0") }()
	<-b.started

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := s.Stash(ctx, "mod", "v1.0.1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded but got %v", err)
	}
}
