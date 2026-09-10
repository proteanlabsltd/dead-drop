package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/protean-labs/dead-drop/daemon/internal/api"
	"github.com/protean-labs/dead-drop/daemon/internal/config"
	"github.com/protean-labs/dead-drop/daemon/internal/tsauth"
)

type Options struct {
	Config                config.Config
	Version, Socket       string
	Logger                *slog.Logger
	PollInterval          time.Duration
	InsecureAllowLoopback bool
}
type Server struct {
	o         Options
	handler   *api.Handler
	mu        sync.RWMutex
	listeners map[string]net.Listener
	servers   map[string]*http.Server
	localAuth *tsauth.Local
}

func New(o Options) (*Server, error) {
	if err := config.Validate(o.Config); err != nil {
		return nil, err
	}
	if o.Socket == "" {
		o.Socket = "/var/run/tailscale/tailscaled.sock"
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.PollInterval == 0 {
		o.PollInterval = 30 * time.Second
	}
	status, err := localStatus(context.Background(), o.Socket)
	if err != nil {
		o.Logger.Warn("tailscale LocalAPI unavailable at startup", "error", err)
	}
	if status.HostName == "" {
		status.HostName, _ = os.Hostname()
	}
	if len(o.Config.AllowUsers) == 0 && status.LoginName != "" {
		o.Config.AllowUsers = []string{status.LoginName}
	}
	localAuth := tsauth.NewLocal(o.Socket, o.Config.AllowUsers, o.Config.AllowTags)
	var auth tsauth.Authorizer = localAuth
	if o.InsecureAllowLoopback {
		auth = loopbackAuth{auth}
	}
	h, err := api.New(api.Options{Name: status.HostName, Version: o.Version, Roots: o.Config.Roots, Authorizer: auth})
	if err != nil {
		return nil, err
	}
	return &Server{o: o, handler: h, listeners: map[string]net.Listener{}, servers: map[string]*http.Server{}, localAuth: localAuth}, nil
}

type loopbackAuth struct{ next tsauth.Authorizer }

func (a loopbackAuth) Authorize(ctx context.Context, remote string) (tsauth.Identity, error) {
	host, _, err := net.SplitHostPort(remote)
	if err == nil && net.ParseIP(host).IsLoopback() {
		return tsauth.Identity{LoginName: "loopback-dev"}, nil
	}
	return a.next.Authorize(ctx, remote)
}
func (s *Server) Addrs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.listeners))
	for a := range s.listeners {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
func (s *Server) Run(ctx context.Context) error {
	if err := s.reconcile(ctx); err != nil {
		s.o.Logger.Warn("tailscale bind unavailable", "error", err)
	}
	t := time.NewTicker(s.o.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.close()
		case <-t.C:
			if err := s.reconcile(ctx); err != nil {
				s.o.Logger.Warn("tailscale rebind failed", "error", err)
			}
		}
	}
}
func (s *Server) reconcile(ctx context.Context) error {
	st, err := localStatus(ctx, s.o.Socket)
	if err != nil && !s.o.InsecureAllowLoopback {
		return err
	}
	if err == nil && len(s.o.Config.AllowUsers) == 0 {
		s.localAuth.AllowUser(st.LoginName)
	}
	wanted := map[string]bool{}
	for _, ip := range st.IPs {
		p := net.ParseIP(ip)
		if !tsauth.IsTailscaleIP(p) {
			continue
		}
		wanted[net.JoinHostPort(ip, strconv.Itoa(s.o.Config.Port))] = true
	}
	if s.o.InsecureAllowLoopback {
		wanted[net.JoinHostPort("127.0.0.1", strconv.Itoa(s.o.Config.Port))] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for addr, ln := range s.listeners {
		if !wanted[addr] {
			_ = ln.Close()
			delete(s.listeners, addr)
			delete(s.servers, addr)
			s.o.Logger.Info("stopped listener", "address", addr)
		}
	}
	var errs []error
	for addr := range wanted {
		if s.listeners[addr] != nil {
			continue
		}
		ln, e := net.Listen("tcp", addr)
		if e != nil {
			errs = append(errs, fmt.Errorf("listen %s: %w", addr, e))
			continue
		}
		srv := &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		s.listeners[addr] = ln
		s.servers[addr] = srv
		s.o.Logger.Info("listening", "address", addr)
		go func() {
			e := srv.Serve(ln)
			if e != nil && !errors.Is(e, http.ErrServerClosed) && !errors.Is(e, net.ErrClosed) {
				s.o.Logger.Error("listener failed", "address", addr, "error", e)
			}
		}()
	}
	if len(wanted) == 0 {
		errs = append(errs, fmt.Errorf("LocalAPI reported no Tailscale addresses"))
	}
	return errors.Join(errs...)
}
func (s *Server) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var es []error
	for a, sv := range s.servers {
		es = append(es, sv.Close())
		delete(s.servers, a)
		delete(s.listeners, a)
	}
	es = append(es, s.handler.Close())
	return errors.Join(es...)
}

type status struct {
	IPs                 []string
	LoginName, HostName string
}

func localStatus(ctx context.Context, socket string) (status, error) {
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://local-tailscaled.sock/localapi/v0/status", nil)
	resp, err := c.Do(req)
	if err != nil {
		return status{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return status{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var v struct {
		Self struct {
			TailscaleIPs []string
			HostName     string
			UserID       int64
		}
		User map[string]struct{ LoginName string }
	}
	if err = json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return status{}, err
	}
	u := v.User[strconv.FormatInt(v.Self.UserID, 10)].LoginName
	return status{v.Self.TailscaleIPs, u, v.Self.HostName}, nil
}
