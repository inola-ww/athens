package module

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec
	"io"
	"path/filepath"
	"testing"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/storage"
	"github.com/stretchr/testify/require"
)

func moduleZip(t *testing.T, prefix string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"go.mod": "module example.com/a\n",
		"a.go":   "package a\n",
	} {
		w, err := zw.Create(prefix + name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

type recordingFetcher struct{ calls []string }

func (r *recordingFetcher) Fetch(_ context.Context, mod, ver string) (*storage.Version, error) {
	r.calls = append(r.calls, mod+"@"+ver)
	return &storage.Version{Semver: ver}, nil
}

func TestHTTPFetcherOK(t *testing.T) {
	z := moduleZip(t, "example.com/a@v1.0.0/")
	srv, _ := newUpstream(t, map[string]string{
		"/example.com/a/@v/master.info": `{"Version":"v1.0.0","Time":"2024-01-02T03:04:05Z"}`,
		"/example.com/a/@v/v1.0.0.mod":  "module example.com/a\n",
		"/example.com/a/@v/v1.0.0.zip":  string(z),
	})
	tmp := t.TempDir()
	f := NewHTTPFetcher(srv.URL, srv.Client(), tmp, "", nil)

	v, err := f.Fetch(t.Context(), "example.com/a", "master")
	require.NoError(t, err)
	require.Equal(t, "v1.0.0", v.Semver)
	require.Equal(t, "module example.com/a\n", string(v.Mod))
	require.Contains(t, string(v.Info), `"v1.0.0"`)
	sum := md5.Sum(z) //nolint:gosec
	require.Equal(t, sum[:], v.ZipMD5)

	got, err := io.ReadAll(v.Zip)
	require.NoError(t, err)
	require.Equal(t, z, got)
	require.NoError(t, v.Zip.Close())
	left, _ := filepath.Glob(filepath.Join(tmp, "*"))
	require.Empty(t, left, "temp zip must be removed on Close")
}

func TestHTTPFetcherNotFound(t *testing.T) {
	srv, _ := newUpstream(t, nil)
	f := NewHTTPFetcher(srv.URL, srv.Client(), t.TempDir(), "", nil)

	_, err := f.Fetch(t.Context(), "example.com/a", "v1.0.0")
	require.True(t, errors.Is(err, errors.KindNotFound), "got %v", err)
}

func TestHTTPFetcherRejectsInvalidZip(t *testing.T) {
	srv, _ := newUpstream(t, map[string]string{
		"/example.com/a/@v/v1.0.0.info": `{"Version":"v1.0.0"}`,
		"/example.com/a/@v/v1.0.0.mod":  "module example.com/a\n",
		"/example.com/a/@v/v1.0.0.zip":  string(moduleZip(t, "example.com/evil@v6.6.6/")),
	})
	tmp := t.TempDir()
	f := NewHTTPFetcher(srv.URL, srv.Client(), tmp, "", nil)

	_, err := f.Fetch(t.Context(), "example.com/a", "v1.0.0")
	require.ErrorContains(t, err, "invalid module zip")
	left, _ := filepath.Glob(filepath.Join(tmp, "*"))
	require.Empty(t, left)
}

func TestHTTPFetcherUpstreamError(t *testing.T) {
	srv, _ := newUpstream(t, map[string]string{
		"/example.com/a/@v/v1.0.0.info": `{"Version":"v1.0.0"}`,
		"/example.com/a/@v/v1.0.0.mod":  "module example.com/a\n",
		"/example.com/a/@v/v1.0.0.zip":  "500",
	})
	tmp := t.TempDir()
	f := NewHTTPFetcher(srv.URL, srv.Client(), tmp, "", nil)

	_, err := f.Fetch(t.Context(), "example.com/a", "v1.0.0")
	require.Error(t, err)
	require.False(t, errors.Is(err, errors.KindNotFound))
	left, _ := filepath.Glob(filepath.Join(tmp, "*"))
	require.Empty(t, left)
}

func TestHTTPFetcherPrivateFallback(t *testing.T) {
	srv, hits := newUpstream(t, nil)
	fallback := &recordingFetcher{}
	f := NewHTTPFetcher(srv.URL, srv.Client(), t.TempDir(), "*.corp.example.com", fallback)

	_, err := f.Fetch(t.Context(), "git.corp.example.com/team/repo", "v1.2.3")
	require.NoError(t, err)
	require.Equal(t, []string{"git.corp.example.com/team/repo@v1.2.3"}, fallback.calls)
	require.Empty(t, *hits)
}
