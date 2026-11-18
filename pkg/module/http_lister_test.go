package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/storage"
	"github.com/stretchr/testify/require"
)

func newUpstream(t *testing.T, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var hits []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		body, ok := routes[r.URL.Path]
		if !ok {
			http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
			return
		}
		if body == "500" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

type recordingLister struct{ calls []string }

func (r *recordingLister) List(_ context.Context, mod string) (*storage.RevInfo, []string, error) {
	r.calls = append(r.calls, mod)
	return &storage.RevInfo{Version: "v9.9.9"}, []string{"v9.9.9"}, nil
}

func TestHTTPListerOK(t *testing.T) {
	srv, _ := newUpstream(t, map[string]string{
		"/github.com/!burnt!sushi/toml/@v/list": "v1.0.0\nv1.1.0\nnot-a-version\n",
		"/github.com/!burnt!sushi/toml/@latest": `{"Version":"v1.1.0","Time":"2024-01-02T03:04:05Z"}`,
	})
	l := NewHTTPLister(srv.URL+"/", srv.Client(), "", nil)

	rev, versions, err := l.List(t.Context(), "github.com/BurntSushi/toml")
	require.NoError(t, err)
	require.Equal(t, []string{"v1.0.0", "v1.1.0"}, versions)
	require.Equal(t, "v1.1.0", rev.Version)
	require.Equal(t, 2024, rev.Time.Year())
}

func TestHTTPListerNotFound(t *testing.T) {
	srv, _ := newUpstream(t, nil)
	l := NewHTTPLister(srv.URL, srv.Client(), "", nil)

	_, _, err := l.List(t.Context(), "example.com/nope")
	require.True(t, errors.Is(err, errors.KindNotFound), "got %v", err)
}

func TestHTTPListerLatestFromList(t *testing.T) {
	srv, hits := newUpstream(t, map[string]string{
		"/example.com/a/@v/list":        "v1.0.0\nv1.2.0\nv2.0.0-rc.1\n",
		"/example.com/a/@v/v1.2.0.info": `{"Version":"v1.2.0"}`,
	})
	l := NewHTTPLister(srv.URL, srv.Client(), "", nil)

	rev, _, err := l.List(t.Context(), "example.com/a")
	require.NoError(t, err)
	require.Equal(t, "v1.2.0", rev.Version)
	require.Contains(t, *hits, "/example.com/a/@v/v1.2.0.info")
}

func TestHTTPListerUnexpectedStatus(t *testing.T) {
	srv, _ := newUpstream(t, map[string]string{
		"/example.com/a/@v/list": "500",
		"/example.com/a/@latest": `{"Version":"v1.0.0"}`,
	})
	l := NewHTTPLister(srv.URL, srv.Client(), "", nil)

	_, _, err := l.List(t.Context(), "example.com/a")
	require.Error(t, err)
	require.False(t, errors.Is(err, errors.KindNotFound), "a 500 must not look like not-found")
}

func TestHTTPListerPrivateFallback(t *testing.T) {
	srv, hits := newUpstream(t, nil)
	fallback := &recordingLister{}
	l := NewHTTPLister(srv.URL, srv.Client(), "*.corp.example.com,example.org/private", fallback)

	rev, _, err := l.List(t.Context(), "git.corp.example.com/team/repo")
	require.NoError(t, err)
	require.Equal(t, "v9.9.9", rev.Version)
	_, _, _ = l.List(t.Context(), "example.org/private/sub")
	require.Equal(t, []string{"git.corp.example.com/team/repo", "example.org/private/sub"}, fallback.calls)
	require.Empty(t, *hits)
}

func TestHighestVersion(t *testing.T) {
	require.Equal(t, "v1.10.0", highestVersion([]string{"v1.2.0", "v1.10.0", "v2.0.0-rc.1"}))
	require.Equal(t, "v2.0.0-rc.2", highestVersion([]string{"v2.0.0-rc.1", "v2.0.0-rc.2"}))
	require.Empty(t, highestVersion(nil))
}
