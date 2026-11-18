package module

import (
	"testing"

	"github.com/gomods/athens/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestHTTPMetaCachesCanonicalVersions(t *testing.T) {
	srv, hits := newUpstream(t, map[string]string{
		"/github.com/!burnt!sushi/toml/@v/v1.0.0.info": `{"Version":"v1.0.0"}`,
		"/github.com/!burnt!sushi/toml/@v/v1.0.0.mod":  "module github.com/BurntSushi/toml\n",
		"/github.com/!burnt!sushi/toml/@v/master.info": `{"Version":"v1.0.1-0.20240101000000-abcdefabcdef"}`,
	})
	m, err := NewHTTPMeta(srv.URL, srv.Client(), "", 10)
	require.NoError(t, err)
	ctx := t.Context()

	for range 2 {
		b, err := m.Info(ctx, "github.com/BurntSushi/toml", "v1.0.0")
		require.NoError(t, err)
		require.Equal(t, `{"Version":"v1.0.0"}`, string(b))
		b, err = m.GoMod(ctx, "github.com/BurntSushi/toml", "v1.0.0")
		require.NoError(t, err)
		require.Equal(t, "module github.com/BurntSushi/toml\n", string(b))
		_, err = m.Info(ctx, "github.com/BurntSushi/toml", "master")
		require.NoError(t, err)
	}
	require.Equal(t, []string{
		"/github.com/!burnt!sushi/toml/@v/v1.0.0.info",
		"/github.com/!burnt!sushi/toml/@v/v1.0.0.mod",
		"/github.com/!burnt!sushi/toml/@v/master.info",
		"/github.com/!burnt!sushi/toml/@v/master.info",
	}, *hits, "canonical versions are cached, queries are not")
}

func TestHTTPMetaNotFoundIsNotCached(t *testing.T) {
	srv, hits := newUpstream(t, nil)
	m, err := NewHTTPMeta(srv.URL, srv.Client(), "", 10)
	require.NoError(t, err)

	for range 2 {
		_, err := m.Info(t.Context(), "example.com/a", "v1.0.0")
		require.True(t, errors.Is(err, errors.KindNotFound), "got %v", err)
	}
	require.Len(t, *hits, 2)
}

func TestHTTPMetaSkipsPrivate(t *testing.T) {
	srv, hits := newUpstream(t, nil)
	m, err := NewHTTPMeta(srv.URL, srv.Client(), "*.corp.example.com", 10)
	require.NoError(t, err)

	_, err = m.Info(t.Context(), "git.corp.example.com/a", "v1.0.0")
	require.ErrorIs(t, err, ErrMetaSkipped)
	require.Empty(t, *hits)
}
