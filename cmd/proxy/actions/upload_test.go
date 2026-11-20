package actions

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gomods/athens/pkg/config"
	"github.com/gomods/athens/pkg/log"
	"github.com/gomods/athens/pkg/storage"
	"github.com/gomods/athens/pkg/storage/mem"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

const testUploadToken = "s3cret-upload-token"

func newUploadRouter(t *testing.T, token string, patterns ...string) (*mux.Router, storage.Backend) {
	t.Helper()
	s, err := mem.NewStorage()
	require.NoError(t, err)
	c, err := config.Load("")
	require.NoError(t, err)
	c.UploadToken = token
	c.UploadPatterns = patterns
	r := mux.NewRouter()
	require.NoError(t, addProxyRoutes(t.Context(), r, s, log.NoOpLogger(), c))
	return r, s
}

// moduleZip builds a module zip for mod@ver whose go.mod declares goModPath.
func moduleZip(t *testing.T, mod, ver, goModPath string) []byte {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+goModPath+"\n\ngo 1.26\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o600))
	var buf bytes.Buffer
	require.NoError(t, modzip.CreateFromDir(&buf, module.Version{Path: mod, Version: ver}, dir))
	return buf.Bytes()
}

func putZip(t *testing.T, r http.Handler, path, token string, body []byte) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Result()
}

func TestUploadStoresRelease(t *testing.T) {
	r, s := newUploadRouter(t, testUploadToken, "go.example.com/*")
	zipData := moduleZip(t, "go.example.com/lib", "v1.2.3", "go.example.com/lib")

	resp := putZip(t, r, "/go.example.com/lib/@v/v1.2.3.zip?time=2026-11-20T06:00:00%2B01:00", testUploadToken, zipData)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(body))

	var info uploadInfo
	require.NoError(t, json.Unmarshal(body, &info))
	assert.Equal(t, "v1.2.3", info.Version)
	assert.Equal(t, time.Date(2026, 11, 20, 5, 0, 0, 0, time.UTC), info.Time)

	ctx := t.Context()
	stored, err := s.Info(ctx, "go.example.com/lib", "v1.2.3")
	require.NoError(t, err)
	assert.JSONEq(t, string(body), string(stored))
	goMod, err := s.GoMod(ctx, "go.example.com/lib", "v1.2.3")
	require.NoError(t, err)
	assert.Contains(t, string(goMod), "module go.example.com/lib")
	zr, err := s.Zip(ctx, "go.example.com/lib", "v1.2.3")
	require.NoError(t, err)
	got, err := io.ReadAll(zr)
	require.NoError(t, err)
	_ = zr.Close()
	assert.Equal(t, zipData, got)

	resp = putZip(t, r, "/go.example.com/lib/@v/v1.2.3.zip", testUploadToken, zipData)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestUploadEscapedModulePath(t *testing.T) {
	r, s := newUploadRouter(t, testUploadToken, "go.example.com/*")
	zipData := moduleZip(t, "go.example.com/Lib", "v1.0.0", "go.example.com/Lib")

	resp := putZip(t, r, "/go.example.com/!lib/@v/v1.0.0.zip", testUploadToken, zipData)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	_, err := s.Info(t.Context(), "go.example.com/Lib", "v1.0.0")
	require.NoError(t, err)
}

func TestUploadRejects(t *testing.T) {
	r, s := newUploadRouter(t, testUploadToken, "go.example.com/*")
	good := moduleZip(t, "go.example.com/lib", "v1.0.0", "go.example.com/lib")

	tests := []struct {
		name  string
		path  string
		token string
		body  []byte
		want  int
	}{
		{"no token", "/go.example.com/lib/@v/v1.0.0.zip", "", good, http.StatusUnauthorized},
		{"wrong token", "/go.example.com/lib/@v/v1.0.0.zip", "nope", good, http.StatusUnauthorized},
		{"module not allowed", "/github.com/x/lib/@v/v1.0.0.zip", testUploadToken, moduleZip(t, "github.com/x/lib", "v1.0.0", "github.com/x/lib"), http.StatusForbidden},
		{"pseudo version", "/go.example.com/lib/@v/v0.0.0-20260101000000-abcdefabcdef.zip", testUploadToken, good, http.StatusBadRequest},
		{"non-canonical version", "/go.example.com/lib/@v/v1.0.zip", testUploadToken, good, http.StatusBadRequest},
		{"major without suffix", "/go.example.com/lib/@v/v2.0.0.zip", testUploadToken, good, http.StatusBadRequest},
		{"zip for other version", "/go.example.com/lib/@v/v1.0.1.zip", testUploadToken, good, http.StatusBadRequest},
		{"go.mod declares other module", "/go.example.com/lib/@v/v1.0.0.zip", testUploadToken, moduleZip(t, "go.example.com/lib", "v1.0.0", "go.example.com/other"), http.StatusBadRequest},
		{"not a zip", "/go.example.com/lib/@v/v1.0.0.zip", testUploadToken, []byte("hello"), http.StatusBadRequest},
		{"bad time", "/go.example.com/lib/@v/v1.0.0.zip?time=yesterday", testUploadToken, good, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := putZip(t, r, tt.path, tt.token, tt.body)
			body, _ := io.ReadAll(resp.Body)
			assert.Equal(t, tt.want, resp.StatusCode, string(body))
		})
	}

	exists, err := storage.WithChecker(s).Exists(t.Context(), "go.example.com/lib", "v1.0.0")
	require.NoError(t, err)
	assert.False(t, exists, "a rejected upload must not reach storage")
}

func TestUploadDisabled(t *testing.T) {
	r, _ := newUploadRouter(t, "")
	resp := putZip(t, r, "/go.example.com/lib/@v/v1.0.0.zip", testUploadToken, moduleZip(t, "go.example.com/lib", "v1.0.0", "go.example.com/lib"))
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestUploadTokenNeedsPatterns(t *testing.T) {
	s, err := mem.NewStorage()
	require.NoError(t, err)
	c, err := config.Load("")
	require.NoError(t, err)
	c.UploadToken = testUploadToken
	err = addProxyRoutes(t.Context(), mux.NewRouter(), s, log.NoOpLogger(), c)
	assert.ErrorContains(t, err, "UploadPatterns")
}
