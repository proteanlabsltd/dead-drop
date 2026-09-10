package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/proteanlabsltd/dead-drop/daemon/internal/client"
	"github.com/proteanlabsltd/dead-drop/daemon/internal/config"
	"github.com/proteanlabsltd/dead-drop/daemon/internal/install"
	"github.com/proteanlabsltd/dead-drop/daemon/internal/server"
)

var version = "0.1.1"

type rootsFlag []string

func (r *rootsFlag) String() string     { return strings.Join(*r, ",") }
func (r *rootsFlag) Set(s string) error { *r = append(*r, s); return nil }
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx, os.Args[1:]); e != nil {
		fmt.Fprintf(os.Stderr, "deaddrop: %v\n", e)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: deaddrop serve|install|status|hosts|ls|get|put|mkdir|version")
	}
	switch args[0] {
	case "version", "--version":
		fmt.Println(version)
		return nil
	case "serve":
		f := flag.NewFlagSet("serve", flag.ContinueOnError)
		cfgPath := f.String("config", "/etc/deaddrop/config.toml", "configuration file")
		port := f.Int("port", 0, "tailnet port (default 7477)")
		insecure := f.Bool("insecure-allow-loopback", false, "development only: serve loopback without whois")
		var roots rootsFlag
		f.Var(&roots, "root", "exposed root (repeatable)")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		cfg, e := config.Load(*cfgPath)
		if os.IsNotExist(e) && *cfgPath == "/etc/deaddrop/config.toml" {
			cfg = config.Default()
			e = nil
		}
		if e != nil {
			return e
		}
		if len(roots) > 0 {
			cfg.Roots = roots
		}
		if *port != 0 {
			cfg.Port = *port
		}
		s, e := server.New(server.Options{Config: cfg, Version: version, Logger: slog.Default(), InsecureAllowLoopback: *insecure})
		if e != nil {
			return e
		}
		return s.Run(ctx)
	case "install":
		if runtime.GOOS != "linux" {
			return fmt.Errorf("systemd installation is Linux-only")
		}
		f := flag.NewFlagSet("install", flag.ContinueOnError)
		u := f.String("user", "", "service user (required)")
		var roots rootsFlag
		f.Var(&roots, "root", "exposed root")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if *u == "" {
			return fmt.Errorf("--user required")
		}
		return install.Install(*u, roots)
	case "hosts":
		return hosts(ctx, false, 7477)
	case "status":
		port := 7477
		if c, e := config.Load("/etc/deaddrop/config.toml"); e == nil {
			port = c.Port
		} else if !os.IsNotExist(e) {
			return fmt.Errorf("load daemon config: %w", e)
		}
		return hosts(ctx, true, port)
	case "ls", "get", "put", "mkdir":
		return remoteCommand(ctx, args)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func remoteCommand(ctx context.Context, args []string) error {
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	overwrite := f.Bool("overwrite", false, "replace existing remote file") // Accept flags before or after operands.
	reordered := []string{}
	operands := []string{}
	for _, a := range args[1:] {
		if a == "--overwrite" {
			if args[0] != "put" {
				return fmt.Errorf("--overwrite is only valid for put")
			}
			reordered = append(reordered, a)
		} else {
			operands = append(operands, a)
		}
	}
	if e := f.Parse(append(reordered, operands...)); e != nil {
		return e
	}
	a := f.Args()
	needed := 1
	if args[0] == "get" || args[0] == "put" {
		needed = 2
	}
	if len(a) != needed {
		return fmt.Errorf("%s requires %d operands", args[0], needed)
	}
	remote := a[0]
	if args[0] == "put" {
		remote = a[1]
	}
	host, p, e := client.ParseRemote(remote)
	if e != nil {
		return e
	}
	c := client.New(host)
	if st, e := os.Stderr.Stat(); e == nil && st.Mode()&os.ModeCharDevice != 0 {
		c.Progress = func(done, total int64) {
			pct := float64(100)
			if total > 0 {
				pct = float64(done) * 100 / float64(total)
			}
			fmt.Fprintf(os.Stderr, "\r%6.1f%%  %d / %d bytes", pct, done, total)
		}
		defer fmt.Fprintln(os.Stderr)
	}
	switch args[0] {
	case "ls":
		v, e := c.List(ctx, p)
		if e != nil {
			return e
		}
		for _, ent := range v.Entries {
			fmt.Printf("%s\t%12d\t%s\t%s\n", ent.Mode, ent.Size, ent.Mtime.Format(time.RFC3339), ent.Name)
		}
		if v.Truncated {
			fmt.Fprintln(os.Stderr, "Listing truncated at 5,000 entries")
		}
		return nil
	case "mkdir":
		return c.Mkdir(ctx, p)
	case "put":
		if strings.HasSuffix(p, "/") {
			p = path.Join(p, filepath.Base(filepath.Clean(a[0])))
		}
		return c.Put(ctx, a[0], p, *overwrite)
	case "get":
		local := a[1]
		if st, e := os.Stat(local); e == nil && st.IsDir() {
			local = filepath.Join(local, path.Base(p))
		}
		if _, e := os.Stat(local); e == nil {
			return fmt.Errorf("destination exists: %s", local)
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		return c.Get(ctx, p, local)
	}
	return nil
}
func hosts(ctx context.Context, self bool, port int) error {
	out, e := exec.CommandContext(ctx, "tailscale", "status", "--json").CombinedOutput()
	if e != nil {
		return fmt.Errorf("tailscale status: %w: %s", e, strings.TrimSpace(string(out)))
	}
	type peer struct {
		HostName     string
		TailscaleIPs []string
		OS           string
		Online       bool
	}
	var status struct {
		BackendState string
		Self         peer
		Peer         map[string]peer
	}
	if e = json.Unmarshal(out, &status); e != nil {
		return e
	}
	if status.BackendState != "Running" {
		return fmt.Errorf("tailscale not running")
	}
	peers := []peer{}
	if self {
		peers = append(peers, status.Self)
	} else {
		for _, p := range status.Peer {
			if p.Online && p.OS == "linux" {
				peers = append(peers, p)
			}
		}
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	var mu sync.Mutex
	found := []string{}
	for _, p := range peers {
		wg.Add(1)
		go func(p peer) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			for _, ip := range p.TailscaleIPs {
				probe, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
				v, e := client.New(net.JoinHostPort(ip, fmt.Sprint(port))).Info(probe)
				cancel()
				if e == nil {
					mu.Lock()
					found = append(found, fmt.Sprintf("%s\t%s\t%s\t%s", v.Name, net.JoinHostPort(ip, fmt.Sprint(port)), v.Version, strings.Join(v.Roots, ", ")))
					mu.Unlock()
					if !self {
						return
					}
				}
			}
		}(p)
	}
	wg.Wait()
	if self && len(found) == 0 {
		return fmt.Errorf("local daemon did not answer on Tailscale addresses (port %d)", port)
	}
	sort.Strings(found)
	for _, line := range found {
		fmt.Println(line)
	}
	return nil
}
