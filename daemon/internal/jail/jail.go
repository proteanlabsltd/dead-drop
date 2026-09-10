package jail

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrInvalidPath = errors.New("path is outside configured roots")

type Root struct {
	Path     string
	realPath string
	dir      *os.Root
}
type Jail struct{ roots []Root }

func Open(paths []string) (*Jail, error) {
	j := &Jail{}
	seen := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) || seen[p] {
			if !filepath.IsAbs(p) {
				j.Close()
				return nil, fmt.Errorf("%w: %q", ErrInvalidPath, p)
			}
			continue
		}
		d, err := os.OpenRoot(p)
		if err != nil {
			j.Close()
			return nil, err
		}
		realPath, err := filepath.EvalSymlinks(p)
		if err != nil {
			d.Close()
			j.Close()
			return nil, err
		}
		realPath = filepath.Clean(realPath)
		seen[p] = true
		j.roots = append(j.roots, Root{Path: p, realPath: realPath, dir: d})
	}
	sort.Slice(j.roots, func(a, b int) bool { return len(j.roots[a].Path) > len(j.roots[b].Path) })
	if len(j.roots) == 0 {
		return nil, fmt.Errorf("no roots")
	}
	return j, nil
}
func (j *Jail) Close() error {
	var first error
	for _, r := range j.roots {
		if err := r.dir.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
func (j *Jail) Roots() []string {
	out := make([]string, len(j.roots))
	for i := range j.roots {
		out[i] = j.roots[i].Path
	}
	sort.Strings(out)
	return out
}
func (j *Jail) Resolve(p string) (*os.Root, string, string, error) {
	return j.resolve(p, true)
}
func (j *Jail) lexical(p string) (*os.Root, string, string, error) {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return nil, "", "", ErrInvalidPath
	}
	for _, r := range j.roots {
		for _, base := range []string{r.Path, r.realPath} {
			rel, err := filepath.Rel(base, p)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				if rel == "." {
					rel = "."
				}
				return r.dir, filepath.ToSlash(rel), base, nil
			}
		}
	}
	return nil, "", "", ErrInvalidPath
}
func (j *Jail) resolve(p string, followFinal bool) (*os.Root, string, string, error) {
	r, name, rootPath, err := j.lexical(p)
	if err != nil {
		return nil, "", "", err
	}
	links := 0
	for {
		parts := strings.Split(filepath.ToSlash(name), "/")
		if name == "." {
			parts = nil
		}
		restarted := false
		for i := range parts {
			if !followFinal && i == len(parts)-1 {
				break
			}
			candidate := strings.Join(parts[:i+1], "/")
			info, e := r.Lstat(candidate)
			if e != nil {
				if errors.Is(e, fs.ErrNotExist) {
					return r, name, rootPath, nil
				}
				return nil, "", "", e
			}
			if info.Mode()&fs.ModeSymlink == 0 {
				continue
			}
			links++
			if links > 40 {
				return nil, "", "", fmt.Errorf("too many symlinks: %w", ErrInvalidPath)
			}
			target, e := r.Readlink(candidate)
			if e != nil {
				return nil, "", "", e
			}
			var targetAbs string
			if filepath.IsAbs(target) {
				targetAbs = filepath.Clean(target)
				// Normalize platform-level aliases such as macOS /var -> /private/var.
				// The eventual operation still uses the already-open root descriptor.
				if physical, e := filepath.EvalSymlinks(targetAbs); e == nil {
					targetAbs = physical
				}
			} else {
				parent := filepath.Join(append([]string{rootPath}, parts[:i]...)...)
				targetAbs = filepath.Clean(filepath.Join(parent, target))
			}
			if i+1 < len(parts) {
				targetAbs = filepath.Join(append([]string{targetAbs}, parts[i+1:]...)...)
			}
			r, name, rootPath, e = j.lexical(targetAbs)
			if e != nil {
				return nil, "", "", e
			}
			restarted = true
			break
		}
		if !restarted {
			return r, name, rootPath, nil
		}
	}
}
func (j *Jail) Stat(p string) (fs.FileInfo, error) {
	r, n, _, e := j.resolve(p, true)
	if e != nil {
		return nil, e
	}
	return r.Stat(n)
}
func (j *Jail) Lstat(p string) (fs.FileInfo, error) {
	r, n, _, e := j.resolve(p, false)
	if e != nil {
		return nil, e
	}
	return r.Lstat(n)
}
func (j *Jail) Open(p string) (*os.File, error) {
	r, n, _, e := j.resolve(p, true)
	if e != nil {
		return nil, e
	}
	return r.Open(n)
}
func (j *Jail) OpenFile(p string, flag int, mode fs.FileMode) (*os.File, error) {
	r, n, _, e := j.resolve(p, false)
	if e != nil {
		return nil, e
	}
	return r.OpenFile(n, flag, mode)
}
func (j *Jail) Mkdir(p string, mode fs.FileMode) error {
	r, n, _, e := j.resolve(p, false)
	if e != nil {
		return e
	}
	return r.Mkdir(n, mode)
}
func (j *Jail) Remove(p string) error {
	r, n, _, e := j.resolve(p, false)
	if e != nil {
		return e
	}
	return r.Remove(n)
}
func (j *Jail) Link(a, b string) error {
	ra, na, _, e := j.resolve(a, false)
	if e != nil {
		return e
	}
	rb, nb, _, e := j.resolve(b, false)
	if e != nil {
		return e
	}
	if ra != rb {
		return fmt.Errorf("cross-root link: %w", ErrInvalidPath)
	}
	return ra.Link(na, nb)
}
func (j *Jail) Rename(a, b string) error {
	ra, na, _, e := j.resolve(a, false)
	if e != nil {
		return e
	}
	rb, nb, _, e := j.resolve(b, false)
	if e != nil {
		return e
	}
	if ra != rb {
		return fmt.Errorf("cross-root rename: %w", ErrInvalidPath)
	}
	return ra.Rename(na, nb)
}
