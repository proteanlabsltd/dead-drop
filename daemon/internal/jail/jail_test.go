package jail

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConfinement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("no"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	j, err := Open([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, p := range []string{"", root + "/../x", outside} {
		if _, err := j.Open(p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Open(%q) error=%v", p, err)
		}
	}
	if _, err := j.Open(filepath.Join(root, "escape")); err == nil {
		t.Fatal("outside symlink was followed")
	}
}
func TestSymlinkInsideAndLoop(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "file"), []byte("ok"), 0600)
	os.Symlink("file", filepath.Join(root, "link"))
	os.Symlink("loop", filepath.Join(root, "loop"))
	j, _ := Open([]string{root})
	defer j.Close()
	f, err := j.Open(filepath.Join(root, "link"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err = j.Open(filepath.Join(root, "loop")); err == nil {
		t.Fatal("symlink loop succeeded")
	}
}
func TestAbsoluteSymlinksInsideRoot(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "real"), 0700)
	os.WriteFile(filepath.Join(root, "real", "file"), []byte("ok"), 0600)
	os.Symlink(filepath.Join(root, "real", "file"), filepath.Join(root, "file-link"))
	os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "dir-link"))
	j, _ := Open([]string{root})
	defer j.Close()
	for _, p := range []string{filepath.Join(root, "file-link"), filepath.Join(root, "dir-link", "file")} {
		f, e := j.Open(p)
		if e != nil {
			t.Fatalf("Open(%s): %v", p, e)
		}
		b, e := io.ReadAll(f)
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		if string(b) != "ok" {
			t.Fatalf("got %q", b)
		}
	}
}
func TestAbsoluteSymlinkAcrossConfiguredRoots(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(b, "file"), []byte("ok"), 0600)
	os.Symlink(filepath.Join(b, "file"), filepath.Join(a, "link"))
	j, _ := Open([]string{a, b})
	defer j.Close()
	f, e := j.Open(filepath.Join(a, "link"))
	if e != nil {
		t.Fatal(e)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "ok" {
		t.Fatal(string(data))
	}
}
func TestOperationsThroughAbsoluteParentSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	os.Mkdir(real, 0700)
	os.Symlink(real, filepath.Join(root, "alias"))
	j, _ := Open([]string{root})
	defer j.Close()
	base := filepath.Join(root, "alias")
	if e := j.Mkdir(filepath.Join(base, "dir"), 0700); e != nil {
		t.Fatal(e)
	}
	f, e := j.OpenFile(filepath.Join(base, "a"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString("ok")
	f.Close()
	if e = j.Rename(filepath.Join(base, "a"), filepath.Join(base, "b")); e != nil {
		t.Fatal(e)
	}
	if e = j.Link(filepath.Join(base, "b"), filepath.Join(base, "c")); e != nil {
		t.Fatal(e)
	}
	if e = j.Remove(filepath.Join(base, "c")); e != nil {
		t.Fatal(e)
	}
	if _, e = j.Stat(filepath.Join(real, "b")); e != nil {
		t.Fatal(e)
	}
}
func TestConfiguredSymlinkRootAcceptsPhysicalAbsoluteTarget(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	os.Mkdir(real, 0700)
	configured := filepath.Join(base, "configured")
	os.Symlink(real, configured)
	os.WriteFile(filepath.Join(real, "file"), []byte("ok"), 0600)
	os.Symlink(filepath.Join(real, "file"), filepath.Join(real, "absolute"))
	j, e := Open([]string{configured})
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if got := j.Roots(); len(got) != 1 || got[0] != configured {
		t.Fatalf("roots=%v", got)
	}
	f, e := j.Open(filepath.Join(configured, "absolute"))
	if e != nil {
		t.Fatal(e)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "ok" {
		t.Fatal(string(data))
	}
}
func TestSymlinkSwapNeverEscapes(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(root, "safe"), 0700)
	os.WriteFile(filepath.Join(root, "safe", "value"), []byte("inside"), 0600)
	os.WriteFile(filepath.Join(outside, "value"), []byte("outside"), 0600)
	link := filepath.Join(root, "switch")
	os.Symlink("safe", link)
	j, _ := Open([]string{root})
	defer j.Close()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.Remove(link)
			os.Symlink(outside, link)
			os.Remove(link)
			os.Symlink("safe", link)
		}
	}()
	for i := 0; i < 3000; i++ {
		f, e := j.Open(filepath.Join(link, "value"))
		if e != nil {
			continue
		}
		data, e := io.ReadAll(f)
		f.Close()
		if e == nil && string(data) != "inside" {
			close(stop)
			wg.Wait()
			t.Fatalf("escaped root: %q", data)
		}
	}
	close(stop)
	wg.Wait()
}
func TestPrefixIsNotRoot(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	ab := filepath.Join(base, "ab")
	os.Mkdir(a, 0700)
	os.Mkdir(ab, 0700)
	j, _ := Open([]string{a})
	defer j.Close()
	if _, err := j.Stat(ab); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("error=%v", err)
	}
}

func TestLinkDoesNotReplace(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	os.WriteFile(a, []byte("new"), 0600)
	os.WriteFile(b, []byte("old"), 0600)
	j, _ := Open([]string{root})
	defer j.Close()
	if err := j.Link(a, b); !errors.Is(err, os.ErrExist) {
		t.Fatalf("error=%v", err)
	}
	got, _ := os.ReadFile(b)
	if string(got) != "old" {
		t.Fatalf("destination replaced: %q", got)
	}
}
