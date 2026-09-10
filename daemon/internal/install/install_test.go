package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderConfinement(t *testing.T) {
	cfg, unit, e := Render("alice", []string{"/home/alice", "/srv/my files"})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(cfg, `"/srv/my files"`) || !strings.Contains(unit, `ReadWritePaths="/home/alice" "/srv/my files"`) || !strings.Contains(unit, "User=alice") {
		t.Fatal("incorrect rendering")
	}
	for _, roots := range [][]string{{"/"}, {"relative"}, {"/srv\nExecStart=evil"}, {"/srv/%h"}} {
		if _, _, e := Render("alice", roots); e == nil {
			t.Fatalf("accepted %q", roots)
		}
	}
	if _, _, e := Render("alice\nUser=root", []string{"/srv"}); e == nil {
		t.Fatal("accepted user injection")
	}
}
func TestAtomicFileHelpers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file")
	if e := createExclusive(p, []byte("one"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := createExclusive(p, []byte("two"), 0600); e == nil {
		t.Fatal("exclusive create replaced file")
	}
	if got, _ := os.ReadFile(p); string(got) != "one" {
		t.Fatal(string(got))
	}
	if e := writeAtomic(p, []byte("two"), 0640); e != nil {
		t.Fatal(e)
	}
	if got, _ := os.ReadFile(p); string(got) != "two" {
		t.Fatal(string(got))
	}
	src := filepath.Join(dir, "src")
	os.WriteFile(src, []byte("binary"), 0600)
	if e := copyAtomic(src, p, 0755); e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(p)
	if e != nil || st.Mode().Perm() != 0755 {
		t.Fatal(e, st.Mode())
	}
}
