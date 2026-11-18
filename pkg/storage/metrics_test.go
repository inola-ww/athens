package storage_test

import (
	"bytes"
	"testing"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/storage"
	"github.com/gomods/athens/pkg/storage/mem"
	"github.com/stretchr/testify/require"
)

func TestWithMetricsPassesThrough(t *testing.T) {
	b, err := mem.NewStorage()
	require.NoError(t, err)
	m := storage.WithMetrics(b)
	ctx := t.Context()

	_, err = m.Info(ctx, "example.com/a", "v1.0.0")
	require.True(t, errors.Is(err, errors.KindNotFound))
	ok, err := m.Exists(ctx, "example.com/a", "v1.0.0")
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, m.Save(ctx, "example.com/a", "v1.0.0", []byte("mod"), bytes.NewReader([]byte("zip")), nil, []byte("info")))
	ok, err = m.Exists(ctx, "example.com/a", "v1.0.0")
	require.NoError(t, err)
	require.True(t, ok)
	info, err := m.Info(ctx, "example.com/a", "v1.0.0")
	require.NoError(t, err)
	require.Equal(t, "info", string(info))
	vers, err := m.List(ctx, "example.com/a")
	require.NoError(t, err)
	require.Equal(t, []string{"v1.0.0"}, vers)
}
