package actions

import (
	"archive/zip"
	"crypto/md5" //nolint:gosec // Storage backends take the MD5 as an integrity hint, not for security.
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	athenserrors "github.com/gomods/athens/pkg/errors"
	"github.com/gomods/athens/pkg/index"
	"github.com/gomods/athens/pkg/log"
	"github.com/gomods/athens/pkg/paths"
	"github.com/gomods/athens/pkg/storage"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	modzip "golang.org/x/mod/zip"
)

// uploadInfo is the .info file Athens stores for an uploaded version.
type uploadInfo struct {
	Version string
	Time    time.Time
}

type uploader struct {
	token    []byte
	patterns []string
	storage  storage.Backend
	checker  storage.Checker
	indexer  index.Indexer
	tmpDir   string

	// mu makes the exists check and the save one step. Uploads are rare, so a
	// single lock is enough; it does not cover several Athens replicas.
	mu sync.Mutex
}

// uploadHandler serves PUT /{module}/@v/{version}.zip. The body is a module
// zip as `go mod download` or golang.org/x/mod/zip.CreateFromDir make it.
// Athens takes the .mod file from the zip and writes the .info file itself.
// A version that is already in storage is never replaced.
func uploadHandler(u *uploader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lggr := log.EntryFromContext(r.Context())
		status, msg := u.upload(w, r)
		if status != http.StatusCreated {
			if status >= http.StatusInternalServerError {
				lggr.Errorf("upload %s: %s", paths.ProtocolPath(r), msg)
			}
			http.Error(w, msg, status)
			return
		}
		lggr.Infof("upload %s: stored", paths.ProtocolPath(r))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, msg)
	})
}

// upload returns the HTTP status and the response body (the .info JSON on
// success, else the reason).
func (u *uploader) upload(w http.ResponseWriter, r *http.Request) (int, string) {
	if !u.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="athens upload"`)
		return http.StatusUnauthorized, "missing or invalid upload token"
	}

	params, err := paths.GetAllParams(r)
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	mod, ver := params.Module, params.Version
	if err := module.Check(mod, ver); err != nil {
		return http.StatusBadRequest, err.Error()
	}
	if semver.Canonical(ver) != ver || module.IsPseudoVersion(ver) {
		return http.StatusBadRequest, fmt.Sprintf("%s is not a canonical release version", ver)
	}
	if !u.allowed(mod) {
		return http.StatusForbidden, fmt.Sprintf("uploads for %s are not allowed", mod)
	}

	infoTime := time.Now().UTC()
	if t := r.URL.Query().Get("time"); t != "" {
		if infoTime, err = time.Parse(time.RFC3339, t); err != nil {
			return http.StatusBadRequest, "time must be RFC 3339: " + err.Error()
		}
		infoTime = infoTime.UTC()
	}

	zipPath, zipMD5, status, err := u.spool(w, r)
	if err != nil {
		return status, err.Error()
	}
	defer func() { _ = os.Remove(zipPath) }()

	modFile, err := checkModuleZip(mod, ver, zipPath)
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	info, err := json.Marshal(uploadInfo{Version: ver, Time: infoTime})
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	exists, err := u.checker.Exists(r.Context(), mod, ver)
	if err != nil {
		return http.StatusInternalServerError, "checking storage: " + err.Error()
	}
	if exists {
		return http.StatusConflict, fmt.Sprintf("%s@%s already exists; versions are immutable", mod, ver)
	}

	f, err := os.Open(zipPath) //nolint:gosec // Path comes from os.CreateTemp.
	if err != nil {
		return http.StatusInternalServerError, err.Error()
	}
	defer func() { _ = f.Close() }()
	if err := u.storage.Save(r.Context(), mod, ver, modFile, f, zipMD5, info); err != nil {
		return http.StatusInternalServerError, "saving: " + err.Error()
	}
	if err := u.indexer.Index(r.Context(), mod, ver); err != nil && !athenserrors.Is(err, athenserrors.KindAlreadyExists) {
		return http.StatusInternalServerError, "indexing: " + err.Error()
	}
	return http.StatusCreated, string(info)
}

func (u *uploader) authorized(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(tok), u.token) == 1
}

func (u *uploader) allowed(mod string) bool {
	for _, p := range u.patterns {
		if paths.MatchesPattern(p, mod) {
			return true
		}
	}
	return false
}

// spool writes the request body to a temporary file, because checking a zip
// needs random access. It returns the file path and the MD5 of the body.
func (u *uploader) spool(w http.ResponseWriter, r *http.Request) (string, []byte, int, error) {
	f, err := os.CreateTemp(u.tmpDir, "athens-upload-*.zip")
	if err != nil {
		return "", nil, http.StatusInternalServerError, err
	}
	h := md5.New() //nolint:gosec // See import.
	body := http.MaxBytesReader(w, r.Body, modzip.MaxZipFile)
	_, err = io.Copy(io.MultiWriter(f, h), body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return "", nil, http.StatusRequestEntityTooLarge, fmt.Errorf("zip is larger than %d bytes", modzip.MaxZipFile)
		}
		return "", nil, http.StatusBadRequest, fmt.Errorf("reading body: %w", err)
	}
	return f.Name(), h.Sum(nil), 0, nil
}

// checkModuleZip validates the zip with the rules of the go command and
// returns its go.mod. The go.mod is required and must declare mod.
func checkModuleZip(mod, ver, zipPath string) ([]byte, error) {
	mv := module.Version{Path: mod, Version: ver}
	cf, err := modzip.CheckZip(mv, zipPath)
	if err == nil {
		err = cf.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("invalid module zip: %w", err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("invalid module zip: %w", err)
	}
	defer func() { _ = zr.Close() }()
	name := mod + "@" + ver + "/go.mod"
	for _, zf := range zr.File {
		if zf.Name != name {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("reading go.mod: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, modzip.MaxGoMod))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading go.mod: %w", err)
		}
		if got := modfile.ModulePath(data); got != mod {
			return nil, fmt.Errorf("go.mod declares module %q, want %q", got, mod)
		}
		return data, nil
	}
	return nil, fmt.Errorf("zip has no %s", name)
}
