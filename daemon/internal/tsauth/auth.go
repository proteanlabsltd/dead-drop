package tsauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var ErrForbidden = errors.New("forbidden")

type Identity struct {
	LoginName string   `json:"login_name"`
	Tags      []string `json:"tags,omitempty"`
}
type Authorizer interface {
	Authorize(context.Context, string) (Identity, error)
}
type cacheEntry struct {
	id    Identity
	err   error
	until time.Time
}
type Local struct {
	socket      string
	users, tags map[string]bool
	client      *http.Client
	mu          sync.Mutex
	cache       map[string]cacheEntry
	now         func() time.Time
}

func (l *Local) AllowUser(login string) {
	if login == "" {
		return
	}
	l.mu.Lock()
	l.users[login] = true
	l.cache = map[string]cacheEntry{}
	l.mu.Unlock()
}

func NewLocal(socket string, allowUsers, allowTags []string) *Local {
	if socket == "" {
		socket = "/var/run/tailscale/tailscaled.sock"
	}
	l := &Local{socket: socket, users: map[string]bool{}, tags: map[string]bool{}, cache: map[string]cacheEntry{}, now: time.Now}
	for _, v := range allowUsers {
		l.users[v] = true
	}
	for _, v := range allowTags {
		l.tags[v] = true
	}
	l.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	return l
}
func (l *Local) Authorize(ctx context.Context, remote string) (Identity, error) {
	host, port, err := net.SplitHostPort(remote)
	if err != nil {
		return Identity{}, ErrForbidden
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return Identity{}, ErrForbidden
	}
	key := ip.String()
	l.mu.Lock()
	if c, ok := l.cache[key]; ok && l.now().Before(c.until) {
		l.mu.Unlock()
		return c.id, c.err
	}
	l.mu.Unlock()
	q := url.QueryEscape(net.JoinHostPort(key, port))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock/localapi/v0/whois?addr="+q, nil)
	resp, err := l.client.Do(req)
	if err == nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	var raw struct {
		UserProfile struct{ LoginName string }
		Node        struct{ Tags []string }
	}
	if err == nil && resp.StatusCode == http.StatusOK {
		err = json.NewDecoder(resp.Body).Decode(&raw)
	} else if err == nil {
		err = fmt.Errorf("whois status %d", resp.StatusCode)
	}
	id := Identity{LoginName: raw.UserProfile.LoginName, Tags: raw.Node.Tags}
	l.mu.Lock()
	allowed := l.users[id.LoginName]
	l.mu.Unlock()
	for _, t := range id.Tags {
		allowed = allowed || l.tags[t]
	}
	if err != nil || !allowed {
		err = ErrForbidden
	}
	ttl := 60 * time.Second
	if err != nil {
		ttl = 5 * time.Second
	}
	l.mu.Lock()
	l.cache[key] = cacheEntry{id, err, l.now().Add(ttl)}
	l.mu.Unlock()
	return id, err
}
func IsTailscaleIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	_, v4, n4 := net.ParseCIDR("100.64.0.0/10")
	_, v6, n6 := net.ParseCIDR("fd7a:115c:a1e0::/48")
	return n4 == nil && v4.Contains(ip) || n6 == nil && v6.Contains(ip)
}
