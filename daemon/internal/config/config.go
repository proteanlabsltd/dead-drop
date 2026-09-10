package config

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
)

type Config struct {
	Port                      int
	Roots                     []string
	AllowUsers                []string
	AllowTags                 []string
	FollowSymlinksOutsideRoot bool
}

func Default() Config {
	c := Config{Port: 7477}
	if u, err := user.Current(); err == nil {
		c.Roots = []string{u.HomeDir}
	}
	return c
}

func Load(path string) (Config, error) {
	c := Default()
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		line := strings.TrimSpace(strings.SplitN(s.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			return Config{}, fmt.Errorf("%s:%d: expected key = value", path, n)
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch key {
		case "port":
			c.Port, err = strconv.Atoi(val)
		case "roots":
			c.Roots, err = parseArray(val)
		case "allow_users":
			c.AllowUsers, err = parseArray(val)
		case "allow_tags":
			c.AllowTags, err = parseArray(val)
		case "follow_symlinks_outside_root":
			c.FollowSymlinksOutsideRoot, err = strconv.ParseBool(val)
		default:
			err = fmt.Errorf("unknown key %q", key)
		}
		if err != nil {
			return Config{}, fmt.Errorf("%s:%d: %w", path, n, err)
		}
	}
	if err := s.Err(); err != nil {
		return Config{}, err
	}
	if err := Validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func parseArray(v string) ([]string, error) {
	if len(v) < 2 || v[0] != '[' || v[len(v)-1] != ']' {
		return nil, fmt.Errorf("expected string array")
	}
	v = strings.TrimSpace(v[1 : len(v)-1])
	if v == "" {
		return []string{}, nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		q, err := strconv.Unquote(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("invalid string array: %w", err)
		}
		out = append(out, q)
	}
	return out, nil
}

func Validate(c Config) error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if len(c.Roots) == 0 {
		return fmt.Errorf("at least one root is required")
	}
	for _, r := range c.Roots {
		if !strings.HasPrefix(r, "/") {
			return fmt.Errorf("root %q must be absolute", r)
		}
		st, err := os.Stat(r)
		if err != nil {
			return fmt.Errorf("root %q: %w", r, err)
		}
		if !st.IsDir() {
			return fmt.Errorf("root %q is not a directory", r)
		}
	}
	if c.FollowSymlinksOutsideRoot {
		return fmt.Errorf("follow_symlinks_outside_root is unsupported because it defeats confinement")
	}
	return nil
}
