package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/protean-labs/dead-drop/daemon/internal/tsauth"
)

type fakeAuth struct{ err error }

func (f fakeAuth) Authorize(context.Context, string) (tsauth.Identity, error) {
	return tsauth.Identity{LoginName: "me"}, f.err
}
func setup(t *testing.T, auth tsauth.Authorizer) (string, http.Handler) {
	t.Helper()
	root := t.TempDir()
	h, e := New(Options{Name: "host", Version: "test", Roots: []string{root}, Authorizer: auth})
	if e != nil {
		t.Fatal(e)
	}
	return root, h
}
func req(h http.Handler, m, u string, b io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(m, u, b)
	r.RemoteAddr = "100.64.0.2:22"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func code(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var v apiError
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	return v.Error.Code
}
func TestAuthBeforeRouting(t *testing.T) {
	_, h := setup(t, fakeAuth{tsauth.ErrForbidden})
	w := req(h, "GET", "/v1/info", nil)
	if w.Code != 403 || code(t, w) != "forbidden" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	_, h = setup(t, fakeAuth{})
	if w = req(h, "GET", "/nope", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestTraversalAndSymlinkEscape(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0600)
	os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape"))
	for _, p := range []string{root + "/../secret", filepath.Join(root, "escape")} {
		w := req(h, "GET", "/v1/read?path="+p, nil)
		if w.Code == 200 {
			t.Fatalf("read %q succeeded", p)
		}
	}
}
func TestAbsoluteInRootSymlinkReadAndStat(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	real := filepath.Join(root, "real")
	os.Mkdir(real, 0700)
	os.WriteFile(filepath.Join(real, "file"), []byte("inside"), 0600)
	link := filepath.Join(root, "link")
	os.Symlink(filepath.Join(real, "file"), link)
	w := req(h, "GET", "/v1/stat?path="+link, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var ent Entry
	if e := json.Unmarshal(w.Body.Bytes(), &ent); e != nil {
		t.Fatal(e)
	}
	if ent.Type != "symlink" || ent.LinkType != "file" {
		t.Fatalf("entry=%+v", ent)
	}
	w = req(h, "GET", "/v1/read?path="+link, nil)
	if w.Code != 200 || w.Body.String() != "inside" {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}
func TestListStatAndMkdir(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	os.WriteFile(filepath.Join(root, "z"), []byte("x"), 0600)
	os.Mkdir(filepath.Join(root, "A"), 0700)
	w := req(h, "GET", "/v1/ls?path="+root, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got struct{ Entries []Entry }
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Entries) != 2 || got.Entries[0].Name != "A" {
		t.Fatalf("%+v", got)
	}
	body := bytes.NewBufferString(`{"path":"` + filepath.Join(root, "new") + `"}`)
	if w = req(h, "POST", "/v1/mkdir", body); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	body = bytes.NewBufferString(`{"path":"` + filepath.Join(root, "new") + `"}`)
	if w = req(h, "POST", "/v1/mkdir", body); w.Code != 409 || code(t, w) != "exists" {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestReadRange(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	p := filepath.Join(root, "f.txt")
	os.WriteFile(p, []byte("abcdef"), 0600)
	r := httptest.NewRequest("GET", "/v1/read?path="+p, nil)
	r.RemoteAddr = "100.64.0.2:1"
	r.Header.Set("Range", "bytes=2-")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "cdef" || w.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("%d %q %+v", w.Code, w.Body.String(), w.Header())
	}
}
func TestWriteResumeHashOverwriteAndDelete(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	p := filepath.Join(root, "file")
	w := req(h, "PUT", "/v1/write?path="+p, bytes.NewBufferString("abc"))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if string(mustRead(t, p)) != "abc" {
		t.Fatal("wrong content")
	}
	w = req(h, "PUT", "/v1/write?path="+p, bytes.NewBufferString("x"))
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	p2 := filepath.Join(root, "partial")
	r := httptest.NewRequest("PUT", "/v1/write?path="+p2, bytes.NewBufferString("abc"))
	r.RemoteAddr = "100.64.0.2:1"
	r.Header.Set("X-Expected-Size", "6")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"partial":true`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = req(h, "HEAD", "/v1/write?path="+p2, nil)
	if w.Header().Get("X-Partial-Size") != "3" {
		t.Fatal(w.Header())
	}
	s := sha256.Sum256([]byte("abcdef"))
	r = httptest.NewRequest("PUT", "/v1/write?path="+p2+"&offset=3", bytes.NewBufferString("def"))
	r.RemoteAddr = "100.64.0.2:1"
	r.Header.Set("X-Expected-Size", "6")
	r.Header.Set("X-Content-SHA256", hex.EncodeToString(s[:]))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || string(mustRead(t, p2)) != "abcdef" {
		t.Fatal(w.Code, w.Body.String())
	}
	p3 := filepath.Join(root, "cancel")
	os.WriteFile(p3+".deaddrop-part", []byte("x"), 0600)
	w = req(h, "DELETE", "/v1/write?path="+p3, nil)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if _, e := os.Stat(p3 + ".deaddrop-part"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
}

func TestWriteRejectsHostilePartialSymlink(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	outside := filepath.Join(t.TempDir(), "outside")
	if e := os.WriteFile(outside, []byte("safe"), 0600); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(root, "file")
	if e := os.Symlink(outside, p+".deaddrop-part"); e != nil {
		t.Fatal(e)
	}
	w := req(h, "PUT", "/v1/write?path="+p, bytes.NewBufferString("evil"))
	if w.Code == 200 {
		t.Fatal("symlink upload succeeded")
	}
	if string(mustRead(t, outside)) != "safe" {
		t.Fatal("outside file modified")
	}
}
func TestWriteRejectsPartialFIFOWithoutBlocking(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	p := filepath.Join(root, "fifo")
	if e := syscall.Mkfifo(p+".deaddrop-part", 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan int, 1)
	go func() { done <- req(h, "PUT", "/v1/write?path="+p, bytes.NewBufferString("x")).Code }()
	select {
	case status := <-done:
		if status != 400 {
			t.Fatalf("status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO upload blocked")
	}
}

type gatedReader struct {
	started chan struct{}
	release chan struct{}
	sent    bool
}

func (g *gatedReader) Read(p []byte) (int, error) {
	if !g.sent {
		g.sent = true
		close(g.started)
		<-g.release
		p[0] = 'x'
		return 1, nil
	}
	return 0, io.EOF
}
func TestWritesLockPerDestination(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	first := &gatedReader{make(chan struct{}), make(chan struct{}), false}
	r := httptest.NewRequest("PUT", "/v1/write?path="+filepath.Join(root, "one"), first)
	r.RemoteAddr = "100.64.0.2:1"
	r.ContentLength = 1
	done := make(chan struct{})
	go func() { h.ServeHTTP(httptest.NewRecorder(), r); close(done) }()
	<-first.started
	w := req(h, "PUT", "/v1/write?path="+filepath.Join(root, "two"), bytes.NewBufferString("y"))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-done:
		t.Fatal("blocked first write unexpectedly completed")
	default:
	}
	close(first.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("first write did not finish")
	}
}
func TestSameDestinationWritesSerialize(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	p := filepath.Join(root, "same")
	first := &gatedReader{make(chan struct{}), make(chan struct{}), false}
	r := httptest.NewRequest("PUT", "/v1/write?path="+p, first)
	r.RemoteAddr = "100.64.0.2:1"
	r.ContentLength = 1
	firstDone := make(chan struct{})
	go func() { h.ServeHTTP(httptest.NewRecorder(), r); close(firstDone) }()
	<-first.started
	secondDone := make(chan int, 1)
	go func() { secondDone <- req(h, "PUT", "/v1/write?path="+p, bytes.NewBufferString("y")).Code }()
	select {
	case <-secondDone:
		t.Fatal("same-path write was not serialized")
	case <-time.After(50 * time.Millisecond):
	}
	close(first.release)
	select {
	case code := <-secondDone:
		if code != 409 {
			t.Fatalf("second status=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("second write did not finish")
	}
	<-firstDone
}
func TestWriteLengthRequired(t *testing.T) {
	root, h := setup(t, fakeAuth{})
	r := httptest.NewRequest("PUT", "/v1/write?path="+filepath.Join(root, "x"), bytes.NewBufferString("x"))
	r.RemoteAddr = "100.64.0.2:1"
	r.ContentLength = -1
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 411 || code(t, w) != "bad_request" {
		t.Fatal(w.Code, w.Body.String())
	}
}
func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
