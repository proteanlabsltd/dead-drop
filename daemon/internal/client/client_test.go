package client

import (
	"bytes"
	"context"
	"github.com/protean-labs/dead-drop/daemon/internal/api"
	"github.com/protean-labs/dead-drop/daemon/internal/tsauth"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type allow struct{}

func (allow) Authorize(context.Context, string) (tsauth.Identity, error) {
	return tsauth.Identity{LoginName: "test@example.com"}, nil
}
func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	h, e := api.New(api.Options{Roots: []string{root}, Authorizer: allow{}, Version: "0.1.0"})
	if e != nil {
		t.Fatal(e)
	}
	s := httptest.NewServer(h)
	defer s.Close()
	c := New(strings.TrimPrefix(s.URL, "http://"))
	ctx := context.Background()
	payload := bytes.Repeat([]byte("streaming round trip\n"), 100000)
	src := filepath.Join(t.TempDir(), "input")
	if e = os.WriteFile(src, payload, 0600); e != nil {
		t.Fatal(e)
	}
	remote := filepath.Join(root, "output")
	if e = c.Put(ctx, src, remote, false); e != nil {
		t.Fatal(e)
	}
	if e = c.Put(ctx, src, remote, false); e == nil {
		t.Fatal("overwrite accepted")
	}
	dest := filepath.Join(t.TempDir(), "download")
	if e = c.Get(ctx, remote, dest); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(dest)
	if e != nil || !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch: %v", e)
	}
	if e = c.Mkdir(ctx, filepath.Join(root, "folder")); e != nil {
		t.Fatal(e)
	}
	ls, e := c.List(ctx, root)
	if e != nil || len(ls.Entries) != 2 {
		t.Fatalf("listing: %+v %v", ls, e)
	}
}
func TestPutResumesPartial(t *testing.T) {
	root := t.TempDir()
	h, e := api.New(api.Options{Roots: []string{root}, Authorizer: allow{}})
	if e != nil {
		t.Fatal(e)
	}
	s := httptest.NewServer(h)
	defer s.Close()
	c := New(strings.TrimPrefix(s.URL, "http://"))
	payload := []byte("abcdefghi")
	remote := filepath.Join(root, "resume")
	os.WriteFile(remote+".deaddrop-part", payload[:4], 0600)
	local := filepath.Join(t.TempDir(), "src")
	os.WriteFile(local, payload, 0600)
	if e = c.Put(context.Background(), local, remote, false); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(remote)
	if e != nil || !bytes.Equal(b, payload) {
		t.Fatal("resume mismatch", e)
	}
}
func TestPutDiscardsStalePartialAfterHashMismatch(t *testing.T) {
	root := t.TempDir()
	h, e := api.New(api.Options{Roots: []string{root}, Authorizer: allow{}})
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	s := httptest.NewServer(h)
	defer s.Close()
	payload := []byte("correct payload")
	remote := filepath.Join(root, "target")
	if e = os.WriteFile(remote+".deaddrop-part", []byte("WRONG"), 0600); e != nil {
		t.Fatal(e)
	}
	local := filepath.Join(t.TempDir(), "src")
	os.WriteFile(local, payload, 0600)
	c := New(strings.TrimPrefix(s.URL, "http://"))
	if e = c.Put(context.Background(), local, remote, false); e != nil {
		t.Fatal(e)
	}
	if got := mustRead(t, remote); !bytes.Equal(got, payload) {
		t.Fatalf("got %q", got)
	}
}
func TestDownloadRejectsWrongETag(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stat" {
			io.WriteString(w, `{"name":"a","type":"file","size":3,"mtime":"2026-01-01T00:00:00Z"}`)
			return
		}
		w.Header().Set("ETag", `"wrong"`)
		w.Header().Set("Content-Length", "3")
		io.WriteString(w, "abc")
	}))
	defer s.Close()
	c := New(strings.TrimPrefix(s.URL, "http://"))
	e := c.Get(context.Background(), "/a", filepath.Join(t.TempDir(), "a"))
	if e == nil || !strings.Contains(e.Error(), "revision changed") {
		t.Fatalf("error=%v", e)
	}
}
func TestDownloadRejectsMetadataSymlink(t *testing.T) {
	root, h := setupAPI(t)
	defer h.Close()
	s := httptest.NewServer(h)
	defer s.Close()
	remote := filepath.Join(root, "f")
	os.WriteFile(remote, []byte("abc"), 0600)
	dst := filepath.Join(t.TempDir(), "dst")
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("safe"), 0600)
	os.Symlink(victim, dst+".deaddrop-part.json")
	e := New(strings.TrimPrefix(s.URL, "http://")).Get(context.Background(), remote, dst)
	if e == nil || !strings.Contains(e.Error(), "metadata is not a regular file") {
		t.Fatalf("error=%v", e)
	}
	if string(mustRead(t, victim)) != "safe" {
		t.Fatal("victim modified")
	}
}
func setupAPI(t *testing.T) (string, *api.Handler) {
	t.Helper()
	root := t.TempDir()
	h, e := api.New(api.Options{Roots: []string{root}, Authorizer: allow{}})
	if e != nil {
		t.Fatal(e)
	}
	return root, h
}
func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestDownloadRejectsIgnoredRange(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stat" {
			io.WriteString(w, `{"name":"a","type":"file","size":6}`)
			return
		}
		io.WriteString(w, "abcdef")
	}))
	defer s.Close()
	c := New(strings.TrimPrefix(s.URL, "http://"))
	dst := filepath.Join(t.TempDir(), "a")
	os.WriteFile(dst+".deaddrop-part", []byte("abc"), 0600)
	os.WriteFile(dst+".deaddrop-part.json", []byte(`{"Remote":"/a","Base":"`+c.Base+`","Size":6,"Mtime":"0001-01-01T00:00:00Z"}`), 0600)
	if e := c.Get(context.Background(), "/a", dst); e == nil || !strings.Contains(e.Error(), "ignored resume range") {
		t.Fatalf("expected ignored range error: %v", e)
	}
}
func TestRemoteAndRedirect(t *testing.T) {
	for _, s := range []string{"host:/a", "host:7777:/a", "[::1]:/a"} {
		if _, _, e := ParseRemote(s); e != nil {
			t.Fatal(e)
		}
	}
	if _, _, e := ParseRemote("/tmp/a"); e == nil {
		t.Fatal("invalid accepted")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com", http.StatusFound)
	}))
	defer s.Close()
	if _, e := New(strings.TrimPrefix(s.URL, "http://")).Info(context.Background()); e == nil {
		t.Fatal("redirect accepted")
	}
}

func TestClientRestrictsDestination(t *testing.T) {
	for raw, want := range map[string]bool{"100.64.0.1": true, "100.127.255.254": true, "fd7a:115c:a1e0::1": true, "127.0.0.1": true, "::1": true, "192.168.1.1": false, "8.8.8.8": false, "100.128.0.1": false, "fd00::1": false} {
		if got := allowedIP(raw); got != want {
			t.Errorf("%s allowed=%v want %v", raw, got, want)
		}
	}
	if _, err := New("8.8.8.8").Info(context.Background()); err == nil {
		t.Fatal("public address allowed")
	}
}
