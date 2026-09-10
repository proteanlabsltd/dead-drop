package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/protean-labs/dead-drop/daemon/internal/jail"
	"github.com/protean-labs/dead-drop/daemon/internal/tsauth"
)

type Options struct {
	Name, Version string
	Roots         []string
	Authorizer    tsauth.Authorizer
	MaxEntries    int
}
type handler struct {
	opt     Options
	j       *jail.Jail
	writeMu sync.Mutex
	writes  map[string]*pathLock
}
type pathLock struct {
	mu    sync.Mutex
	users int
}

func (h *handler) lockWrite(path string) func() {
	h.writeMu.Lock()
	if h.writes == nil {
		h.writes = make(map[string]*pathLock)
	}
	lock := h.writes[path]
	if lock == nil {
		lock = &pathLock{}
		h.writes[path] = lock
	}
	lock.users++
	h.writeMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		h.writeMu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(h.writes, path)
		}
		h.writeMu.Unlock()
	}
}

type Handler struct{ h *handler }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.h.auth(http.HandlerFunc(h.h.route)).ServeHTTP(w, r)
}
func (h *Handler) Close() error { return h.h.j.Close() }

type ctxKey struct{}
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
type Entry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Size     int64     `json:"size"`
	MTime    time.Time `json:"mtime"`
	Mode     string    `json:"mode"`
	LinkType string    `json:"link_type,omitempty"`
}

func New(o Options) (*Handler, error) {
	if o.Authorizer == nil {
		return nil, fmt.Errorf("authorizer is required")
	}
	j, err := jail.Open(o.Roots)
	if err != nil {
		return nil, err
	}
	if o.MaxEntries == 0 {
		o.MaxEntries = 5000
	}
	h := &handler{opt: o, j: j}
	return &Handler{h: h}, nil
}
func (h *handler) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := h.opt.Authorizer.Authorize(r.Context(), r.RemoteAddr)
		if err != nil {
			msg := "peer is not allowed"
			if id.LoginName != "" {
				msg = fmt.Sprintf("peer %q is not allowed; add %q to allow_users", id.LoginName, id.LoginName)
			}
			writeErr(w, http.StatusForbidden, "forbidden", msg)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}
func (h *handler) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/info":
		h.info(w, r)
	case r.Method == "GET" && r.URL.Path == "/v1/ls":
		h.ls(w, r)
	case r.Method == "GET" && r.URL.Path == "/v1/stat":
		h.stat(w, r)
	case r.Method == "GET" && r.URL.Path == "/v1/read":
		h.read(w, r)
	case (r.Method == "PUT" || r.Method == "HEAD" || r.Method == "DELETE") && r.URL.Path == "/v1/write":
		h.write(w, r)
	case r.Method == "POST" && r.URL.Path == "/v1/mkdir":
		h.mkdir(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var v apiError
	v.Error.Code = code
	v.Error.Message = msg
	jsonOut(w, status, v)
}
func mapErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jail.ErrInvalidPath):
		writeErr(w, 400, "invalid_path", "path is outside configured roots")
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, 404, "not_found", "path not found")
	case errors.Is(err, fs.ErrExist):
		writeErr(w, 409, "exists", "path already exists")
	case errors.Is(err, fs.ErrPermission):
		writeErr(w, 403, "forbidden", "permission denied")
	default:
		writeErr(w, 500, "io", "filesystem operation failed")
	}
}
func pathArg(r *http.Request) (string, error) {
	p := r.URL.Query().Get("path")
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) {
		return "", jail.ErrInvalidPath
	}
	return p, nil
}
func (h *handler) info(w http.ResponseWriter, r *http.Request) {
	id, _ := r.Context().Value(ctxKey{}).(tsauth.Identity)
	jsonOut(w, 200, map[string]any{"name": h.opt.Name, "version": h.opt.Version, "protocol": 1, "roots": h.j.Roots(), "user": id.LoginName})
}
func makeEntry(name string, fi fs.FileInfo, linkType string) Entry {
	t := "other"
	if fi.Mode()&fs.ModeSymlink != 0 {
		t = "symlink"
	} else if fi.IsDir() {
		t = "dir"
	} else if fi.Mode().IsRegular() {
		t = "file"
	}
	return Entry{name, t, fi.Size(), fi.ModTime().UTC(), fi.Mode().String(), linkType}
}
func (h *handler) entry(path string) (Entry, error) {
	fi, err := h.j.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	lt := ""
	if fi.Mode()&fs.ModeSymlink != 0 {
		if target, e := h.j.Stat(path); e == nil {
			if target.IsDir() {
				lt = "dir"
			} else if target.Mode().IsRegular() {
				lt = "file"
			} else {
				lt = "other"
			}
		}
	}
	return makeEntry(filepath.Base(path), fi, lt), nil
}
func (h *handler) stat(w http.ResponseWriter, r *http.Request) {
	p, e := pathArg(r)
	if e != nil {
		mapErr(w, e)
		return
	}
	v, e := h.entry(p)
	if e != nil {
		mapErr(w, e)
		return
	}
	jsonOut(w, 200, v)
}
func (h *handler) ls(w http.ResponseWriter, r *http.Request) {
	p, e := pathArg(r)
	if e != nil {
		mapErr(w, e)
		return
	}
	f, e := h.j.Open(p)
	if e != nil {
		mapErr(w, e)
		return
	}
	defer f.Close()
	des, e := f.ReadDir(h.opt.MaxEntries + 1)
	if e != nil {
		mapErr(w, e)
		return
	}
	trunc := len(des) > h.opt.MaxEntries
	if trunc {
		des = des[:h.opt.MaxEntries]
	}
	ents := make([]Entry, 0, len(des))
	for _, de := range des {
		e, er := h.entry(filepath.Join(p, de.Name()))
		if er != nil {
			mapErr(w, er)
			return
		}
		ents = append(ents, e)
	}
	sort.Slice(ents, func(i, j int) bool {
		di, dj := ents[i].Type == "dir", ents[j].Type == "dir"
		if di != dj {
			return di
		}
		return strings.ToLower(ents[i].Name) < strings.ToLower(ents[j].Name)
	})
	jsonOut(w, 200, map[string]any{"path": p, "entries": ents, "truncated": trunc})
}
func (h *handler) read(w http.ResponseWriter, r *http.Request) {
	p, e := pathArg(r)
	if e != nil {
		mapErr(w, e)
		return
	}
	f, e := h.j.Open(p)
	if e != nil {
		mapErr(w, e)
		return
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		mapErr(w, e)
		return
	}
	if !st.Mode().IsRegular() {
		writeErr(w, 400, "bad_request", "path is not a regular file")
		return
	}
	if ct := mime.TypeByExtension(filepath.Ext(p)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%x-%x\"", st.Size(), st.ModTime().UnixNano()))
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}
func (h *handler) write(w http.ResponseWriter, r *http.Request) {
	p, e := pathArg(r)
	if e != nil {
		mapErr(w, e)
		return
	}
	if strings.HasSuffix(p, ".deaddrop-part") {
		writeErr(w, 400, "invalid_path", "upload paths cannot use the reserved .deaddrop-part suffix")
		return
	}
	unlock := h.lockWrite(p)
	defer unlock()
	part := p + ".deaddrop-part"
	if r.Method == "HEAD" {
		st, e := h.j.Lstat(part)
		if errors.Is(e, fs.ErrNotExist) {
			w.Header().Set("X-Partial-Size", "0")
			w.WriteHeader(200)
			return
		}
		if e != nil {
			mapErr(w, e)
			return
		}
		if !st.Mode().IsRegular() {
			writeErr(w, 400, "invalid_path", "upload partial is not a regular file")
			return
		}
		w.Header().Set("X-Partial-Size", strconv.FormatInt(st.Size(), 10))
		w.WriteHeader(200)
		return
	}
	if r.Method == "DELETE" {
		e := h.j.Remove(part)
		if errors.Is(e, fs.ErrNotExist) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if e != nil {
			mapErr(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.ContentLength < 0 {
		writeErr(w, http.StatusLengthRequired, "bad_request", "Content-Length is required")
		return
	}
	offset, e := parseNonnegative(r.URL.Query().Get("offset"), 0)
	if e != nil {
		writeErr(w, 400, "bad_request", "invalid offset")
		return
	}
	overwrite := r.URL.Query().Get("overwrite") == "1"
	if _, e = h.j.Stat(p); e == nil && !overwrite {
		writeErr(w, 409, "exists", "path already exists")
		return
	} else if e != nil && !errors.Is(e, fs.ErrNotExist) {
		mapErr(w, e)
		return
	}
	flags := os.O_RDWR | os.O_CREATE
	if offset == 0 {
		if existing, statErr := h.j.Lstat(part); statErr == nil {
			if !existing.Mode().IsRegular() {
				writeErr(w, 400, "invalid_path", "upload partial is not a regular file")
				return
			}
			if e = h.j.Remove(part); e != nil {
				mapErr(w, e)
				return
			}
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			mapErr(w, statErr)
			return
		}
		flags |= os.O_EXCL
	}
	flags |= syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	f, e := h.j.OpenFile(part, flags, 0600)
	if e != nil {
		mapErr(w, e)
		return
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		mapErr(w, e)
		return
	}
	if !st.Mode().IsRegular() {
		writeErr(w, 400, "invalid_path", "upload partial is not a regular file")
		return
	}
	if st.Size() != offset {
		writeErr(w, 409, "exists", fmt.Sprintf("partial size is %d, not requested offset %d", st.Size(), offset))
		return
	}
	if _, e = f.Seek(offset, io.SeekStart); e != nil {
		mapErr(w, e)
		return
	}
	n, e := io.Copy(f, r.Body)
	if e != nil {
		mapErr(w, e)
		return
	}
	size := offset + n
	expected := -int64(1)
	if v := r.Header.Get("X-Expected-Size"); v != "" {
		expected, e = strconv.ParseInt(v, 10, 64)
		if e != nil || expected < 0 || offset > expected || r.ContentLength > expected-offset || size > expected {
			writeErr(w, 400, "bad_request", "invalid expected size")
			return
		}
	}
	if expected >= 0 && size < expected {
		jsonOut(w, 200, map[string]any{"partial": true, "size": size})
		return
	}
	if e = f.Sync(); e != nil {
		mapErr(w, e)
		return
	}
	if sum := r.Header.Get("X-Content-SHA256"); sum != "" {
		if e = verifySHA(f, sum); e != nil {
			writeErr(w, 400, "bad_request", e.Error())
			return
		}
	}
	if overwrite {
		e = h.j.Rename(part, p)
	} else if e = h.j.Link(part, p); e == nil {
		e = h.j.Remove(part)
	}
	if e != nil {
		mapErr(w, e)
		return
	}
	v, e := h.entry(p)
	if e != nil {
		mapErr(w, e)
		return
	}
	jsonOut(w, 200, v)
}
func verifySHA(f *os.File, want string) error {
	if _, e := f.Seek(0, io.SeekStart); e != nil {
		return e
	}
	x := sha256.New()
	if _, e := io.Copy(x, f); e != nil {
		return e
	}
	got := hex.EncodeToString(x.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("sha256 mismatch")
	}
	return nil
}
func parseNonnegative(v string, d int64) (int64, error) {
	if v == "" {
		return d, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 0 {
		return 0, fmt.Errorf("invalid number")
	}
	return n, nil
}
func (h *handler) mkdir(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Path string `json:"path"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&b); e != nil {
		writeErr(w, 400, "bad_request", "invalid JSON body")
		return
	}
	if b.Path == "" || !filepath.IsAbs(b.Path) || filepath.Clean(b.Path) != b.Path {
		mapErr(w, jail.ErrInvalidPath)
		return
	}
	if e := h.j.Mkdir(b.Path, 0755); e != nil {
		mapErr(w, e)
		return
	}
	v, e := h.entry(b.Path)
	if e != nil {
		mapErr(w, e)
		return
	}
	jsonOut(w, 201, v)
}
