package tsauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsTailscaleIP(t *testing.T) {
	for s, w := range map[string]bool{"100.64.0.1": true, "100.127.255.254": true, "100.128.0.1": false, "192.168.1.1": false, "fd7a:115c:a1e0::1": true} {
		if got := IsTailscaleIP(net.ParseIP(s)); got != w {
			t.Errorf("%s got %v", s, got)
		}
	}
}
func TestAuthorizationCacheTTLs(t *testing.T) {
	d := t.TempDir()
	sock := filepath.Join(d, "sock")
	ln, e := net.Listen("unix", sock)
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	var calls atomic.Int32
	allowed := atomic.Bool{}
	allowed.Store(true)
	sv := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !allowed.Load() {
			http.Error(w, "no", 500)
			return
		}
		fmt.Fprint(w, `{"UserProfile":{"LoginName":"me"},"Node":{"Tags":[]}}`)
	})}
	defer sv.Close()
	go sv.Serve(ln)
	l := NewLocal(sock, []string{"me"}, nil)
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	if _, e = l.Authorize(context.Background(), "100.64.0.1:1"); e != nil {
		t.Fatal(e)
	}
	if _, e = l.Authorize(context.Background(), "100.64.0.1:2"); e != nil || calls.Load() != 1 {
		t.Fatalf("cache: %v %d", e, calls.Load())
	}
	now = now.Add(61 * time.Second)
	if _, e = l.Authorize(context.Background(), "100.64.0.1:2"); e != nil || calls.Load() != 2 {
		t.Fatal(e, calls.Load())
	}
	allowed.Store(false)
	now = now.Add(61 * time.Second)
	if _, e = l.Authorize(context.Background(), "100.64.0.2:1"); e == nil {
		t.Fatal("wanted denial")
	}
	if _, e = l.Authorize(context.Background(), "100.64.0.2:1"); e == nil || calls.Load() != 3 {
		t.Fatal("negative cache")
	}
	now = now.Add(6 * time.Second)
	_, _ = l.Authorize(context.Background(), "100.64.0.2:1")
	if calls.Load() != 4 {
		t.Fatal("negative TTL did not expire")
	}
	_ = os.Remove(sock)
}
