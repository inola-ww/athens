package storage

import (
	"context"
	"io"
	"time"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/observ"
)

// MeteredBackend is a Backend that also checks existence, so wrapping keeps
// the backend's own (usually cheaper) Exists.
type MeteredBackend interface {
	Backend
	Checker
}

type metered struct {
	b       Backend
	checker Checker
}

// WithMetrics records the duration and result of every storage call in
// storage_operation_duration_seconds. For Zip it measures opening the object,
// not streaming it.
func WithMetrics(b Backend) MeteredBackend {
	return &metered{b: b, checker: WithChecker(b)}
}

func record(ctx context.Context, op string, start time.Time, err error) {
	result := "ok"
	switch {
	case err == nil:
	case errors.IsNotFoundErr(err):
		result = "not_found"
	default:
		result = "error"
	}
	observ.RecordStorageOperation(ctx, op, result, time.Since(start))
}

func (m *metered) List(ctx context.Context, module string) ([]string, error) {
	start := time.Now()
	v, err := m.b.List(ctx, module)
	record(ctx, "list", start, err)
	return v, err
}

func (m *metered) Info(ctx context.Context, module, vsn string) ([]byte, error) {
	start := time.Now()
	v, err := m.b.Info(ctx, module, vsn)
	record(ctx, "info", start, err)
	return v, err
}

func (m *metered) GoMod(ctx context.Context, module, vsn string) ([]byte, error) {
	start := time.Now()
	v, err := m.b.GoMod(ctx, module, vsn)
	record(ctx, "gomod", start, err)
	return v, err
}

func (m *metered) Zip(ctx context.Context, module, vsn string) (SizeReadCloser, error) {
	start := time.Now()
	v, err := m.b.Zip(ctx, module, vsn)
	record(ctx, "zip", start, err)
	return v, err
}

func (m *metered) Save(ctx context.Context, module, version string, mod []byte, zip io.Reader, zipMD5, info []byte) error {
	start := time.Now()
	err := m.b.Save(ctx, module, version, mod, zip, zipMD5, info)
	record(ctx, "save", start, err)
	return err
}

func (m *metered) Delete(ctx context.Context, module, vsn string) error {
	start := time.Now()
	err := m.b.Delete(ctx, module, vsn)
	record(ctx, "delete", start, err)
	return err
}

func (m *metered) Exists(ctx context.Context, module, version string) (bool, error) {
	start := time.Now()
	ok, err := m.checker.Exists(ctx, module, version)
	record(ctx, "exists", start, err)
	return ok, err
}
