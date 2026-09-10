// Package install generates the least-privilege systemd installation.
package install

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/protean-labs/dead-drop/daemon/internal/config"
)

func safe(s string) bool { return s != "" && !strings.ContainsAny(s, "\n\r\x00\"\\%$") }
func Render(username string, roots []string) (string, string, error) {
	if !safe(username) || strings.ContainsAny(username, " /\t") {
		return "", "", fmt.Errorf("invalid service user")
	}
	if len(roots) == 0 {
		return "", "", fmt.Errorf("at least one root required")
	}
	quoted := []string{}
	unitRoots := []string{}
	for _, r := range roots {
		if !filepath.IsAbs(r) || filepath.Clean(r) == "/" || !safe(r) {
			return "", "", fmt.Errorf("invalid root %q", r)
		}
		quoted = append(quoted, strconv.Quote(filepath.Clean(r)))
		unitRoots = append(unitRoots, "\""+filepath.Clean(r)+"\"")
	}
	cfg := "port = 7477\nroots = [" + strings.Join(quoted, ", ") + "]\nallow_tags = []\nfollow_symlinks_outside_root = false\n"
	unit := fmt.Sprintf("[Unit]\nDescription=Dead Drop file daemon\nAfter=network-online.target tailscaled.service\nWants=tailscaled.service\n\n[Service]\nUser=%s\nExecStart=/usr/local/bin/deaddrop serve --config /etc/deaddrop/config.toml\nRestart=on-failure\nRestartSec=3\nNoNewPrivileges=true\nProtectSystem=strict\nReadWritePaths=%s\n\n[Install]\nWantedBy=multi-user.target\n", username, strings.Join(unitRoots, " "))
	return cfg, unit, nil
}
func Install(username string, roots []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("install needs root; run sudo deaddrop install --user YOUR_USER")
	}
	u, e := user.Lookup(username)
	if e != nil {
		return e
	}
	const configPath = "/etc/deaddrop/config.toml"
	configExists := false
	if _, statErr := os.Stat(configPath); statErr == nil {
		existing, loadErr := config.Load(configPath)
		if loadErr != nil {
			return fmt.Errorf("preserved config is invalid: %w", loadErr)
		}
		roots = existing.Roots
		configExists = true
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if len(roots) == 0 {
		roots = []string{u.HomeDir}
	}
	for _, r := range roots {
		st, e := os.Stat(r)
		if e != nil {
			return e
		}
		if !st.IsDir() {
			return fmt.Errorf("root is not a directory: %s", r)
		}
	}
	cfg, unit, e := Render(username, roots)
	if e != nil {
		return e
	}
	// Validate socket access as the service user before writing an installation.
	check := exec.Command("runuser", "-u", username, "--", "sh", "-c", "test -r /var/run/tailscale/tailscaled.sock && test -w /var/run/tailscale/tailscaled.sock")
	if e = check.Run(); e != nil {
		st, statErr := os.Stat("/var/run/tailscale/tailscaled.sock")
		if statErr != nil {
			return fmt.Errorf("tailscaled socket unavailable: %w", statErr)
		}
		raw, ok := st.Sys().(*syscall.Stat_t)
		if !ok || st.Mode().Perm()&0060 != 0060 {
			return fmt.Errorf("service user cannot access tailscaled socket; configure a persistent tailscale group with read/write socket permissions, then retry")
		}
		group, lookupErr := user.LookupGroupId(strconv.FormatUint(uint64(raw.Gid), 10))
		if lookupErr != nil || group.Name == "root" || !safe(group.Name) || strings.ContainsAny(group.Name, " /\t") {
			return fmt.Errorf("socket access would require privileged or invalid group; configure a dedicated tailscale group on the socket, then retry")
		}
		unit = strings.Replace(unit, "NoNewPrivileges=true", "SupplementaryGroups="+group.Name+"\nNoNewPrivileges=true", 1)
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	if e = os.MkdirAll("/usr/local/bin", 0755); e != nil {
		return e
	}
	if filepath.Clean(exe) != "/usr/local/bin/deaddrop" {
		if e = copyAtomic(exe, "/usr/local/bin/deaddrop", 0755); e != nil {
			return e
		}
	}
	if e = os.MkdirAll("/etc/deaddrop", 0755); e != nil {
		return e
	}
	if !configExists {
		if e = createExclusive(configPath, []byte(cfg), 0644); e != nil {
			return e
		}
	}
	if e = writeAtomic("/etc/systemd/system/deaddrop.service", []byte(unit), 0644); e != nil {
		return e
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", "deaddrop.service"}} {
		cmd := exec.Command("systemctl", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if e = cmd.Run(); e != nil {
			return e
		}
	}
	return nil
}

func createExclusive(path string, data []byte, mode os.FileMode) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, e = f.Write(data); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	ok = true
	return nil
}
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, e := os.CreateTemp(dir, ".deaddrop-*")
	if e != nil {
		return e
	}
	name := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if e = f.Chmod(mode); e != nil {
		return e
	}
	if _, e = f.Write(data); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	ok = true
	return nil
}
func copyAtomic(source, dest string, mode os.FileMode) error {
	in, e := os.Open(source)
	if e != nil {
		return e
	}
	defer in.Close()
	dir := filepath.Dir(dest)
	out, e := os.CreateTemp(dir, ".deaddrop-*")
	if e != nil {
		return e
	}
	name := out.Name()
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if e = out.Chmod(mode); e != nil {
		return e
	}
	if _, e = io.Copy(out, in); e != nil {
		return e
	}
	if e = out.Sync(); e != nil {
		return e
	}
	if e = out.Close(); e != nil {
		return e
	}
	if e = os.Rename(name, dest); e != nil {
		return e
	}
	ok = true
	return nil
}
