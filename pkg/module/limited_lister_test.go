package module

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gomods/athens/pkg/storage"
)

type blockingLister struct {
	started chan struct{}
	block   chan struct{}
}

func (b *blockingLister) List(context.Context, string) (*storage.RevInfo, []string, error) {
	b.started <- struct{}{}
	<-b.block
	return nil, nil, nil
}

func TestLimitedLister(t *testing.T) {
	inner := &blockingLister{started: make(chan struct{}, 2), block: make(chan struct{})}
	defer close(inner.block)
	l := NewLimitedLister(inner, 2)

	for range 2 {
		go l.List(t.Context(), "example.com/a")
		<-inner.started
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, _, err := l.List(ctx, "example.com/a")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded but got %v", err)
	}
}
