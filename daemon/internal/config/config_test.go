package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(t.TempDir(), "config.toml")
	body := "port = 7478\nroots = [\"" + root + "\"]\nallow_users = [\"a@example\"]\nallow_tags = [\"tag:dev\"]\nfollow_symlinks_outside_root = false\n"
	if e := os.WriteFile(p, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Port != 7478 || len(c.Roots) != 1 || c.AllowUsers[0] != "a@example" || c.AllowTags[0] != "tag:dev" {
		t.Fatalf("%+v", c)
	}
}
func TestValidation(t *testing.T) {
	for _, c := range []Config{{Port: 0, Roots: []string{t.TempDir()}}, {Port: 7477}, {Port: 7477, Roots: []string{"relative"}}, {Port: 7477, Roots: []string{t.TempDir()}, FollowSymlinksOutsideRoot: true}} {
		if e := Validate(c); e == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}
