// Package client implements the streaming v1 client shared by CLI commands.
package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Entry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	LinkType string    `json:"link_type,omitempty"`
	Size     int64     `json:"size"`
	Mtime    time.Time `json:"mtime"`
	Mode     string    `json:"mode"`
}
type Listing struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}
type Info struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Protocol int      `json:"protocol"`
	Roots    []string `json:"roots"`
	User     string   `json:"user"`
}
type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s (HTTP %d)", e.Code, e.Message, e.Status) }

type Client struct {
	Base     string
	HTTP     *http.Client
	Progress func(int64, int64)
}

func New(host string) *Client {
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(strings.Trim(host, "[]"), "7477")
	}
	return &Client{Base: "http://" + host, HTTP: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: dialTailnet, ResponseHeaderTimeout: 15 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Resolve once and dial only an approved literal IP, preventing DNS rebinding.
func dialTailnet(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		if !allowedIP(ip.IP.String()) {
			continue
		}
		d := net.Dialer{Timeout: 5 * time.Second}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last != nil {
		return nil, last
	}
	return nil, fmt.Errorf("host does not resolve to a Tailscale or loopback address")
}
func allowedIP(raw string) bool {
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(ip)
}

func ParseRemote(s string) (string, string, error) {
	i := strings.Index(s, ":/")
	if i < 1 {
		return "", "", fmt.Errorf("expected host:/absolute/path (IPv6: [address]:/path)")
	}
	return s[:i], s[i+1:], nil
}
func (c *Client) request(ctx context.Context, method, endpoint string, q url.Values, body io.Reader, length int64, headers map[string]string) (*http.Response, error) {
	u := c.Base + "/v1/" + endpoint
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	r, e := http.NewRequestWithContext(ctx, method, u, body)
	if e != nil {
		return nil, e
	}
	r.ContentLength = length
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, e := c.HTTP.Do(r)
	if e != nil {
		return nil, e
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var wire struct {
			Error struct{ Code, Message string }
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wire)
		if wire.Error.Code == "" {
			wire.Error.Code = http.StatusText(resp.StatusCode)
		}
		if wire.Error.Message == "" {
			wire.Error.Message = "request failed"
		}
		return nil, &Error{resp.StatusCode, wire.Error.Code, wire.Error.Message}
	}
	return resp, nil
}
func (c *Client) json(ctx context.Context, method, endpoint, p string, body io.Reader, out any) error {
	q := url.Values{}
	if p != "" {
		q.Set("path", p)
	}
	r, e := c.request(ctx, method, endpoint, q, body, 0, nil)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}
func (c *Client) Info(ctx context.Context) (Info, error) {
	var v Info
	e := c.json(ctx, "GET", "info", "", nil, &v)
	return v, e
}
func (c *Client) List(ctx context.Context, p string) (Listing, error) {
	var v Listing
	e := c.json(ctx, "GET", "ls", p, nil, &v)
	return v, e
}
func (c *Client) Stat(ctx context.Context, p string) (Entry, error) {
	var v Entry
	e := c.json(ctx, "GET", "stat", p, nil, &v)
	return v, e
}
func (c *Client) Mkdir(ctx context.Context, p string) error {
	b, _ := json.Marshal(map[string]string{"path": p})
	r, e := c.request(ctx, "POST", "mkdir", nil, strings.NewReader(string(b)), int64(len(b)), map[string]string{"Content-Type": "application/json"})
	if e != nil {
		return e
	}
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()
	return nil
}
func (c *Client) Partial(ctx context.Context, p string) (int64, error) {
	r, e := c.request(ctx, "HEAD", "write", url.Values{"path": {p}}, nil, 0, nil)
	if e != nil {
		return 0, e
	}
	defer r.Body.Close()
	return strconv.ParseInt(r.Header.Get("X-Partial-Size"), 10, 64)
}
func (c *Client) Cancel(ctx context.Context, p string) error {
	r, e := c.request(ctx, "DELETE", "write", url.Values{"path": {p}}, nil, 0, nil)
	if e == nil {
		r.Body.Close()
	}
	return e
}

type progressReader struct {
	io.Reader
	done, total int64
	report      func(int64, int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, e := p.Reader.Read(b)
	p.done += int64(n)
	if p.report != nil {
		p.report(p.done, p.total)
	}
	return n, e
}
func retry(ctx context.Context, n int) error {
	d := time.Duration(2<<n) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
func retryable(e error) bool {
	if a, ok := e.(*Error); ok {
		return a.Status >= 500
	}
	return true
}
func (c *Client) Put(ctx context.Context, local, remote string, overwrite bool) error {
	st, e := os.Lstat(local)
	if e != nil {
		return e
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing local symlink: %s", local)
	}
	if st.IsDir() {
		if e = c.Mkdir(ctx, remote); e != nil {
			a, ok := e.(*Error)
			if !ok || a.Code != "exists" {
				return e
			}
		}
		entries, e := os.ReadDir(local)
		if e != nil {
			return e
		}
		for _, ent := range entries {
			if e = c.Put(ctx, filepath.Join(local, ent.Name()), path.Join(remote, ent.Name()), overwrite); e != nil {
				return e
			}
		}
		return nil
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", local)
	}
	hashFile, e := os.Open(local)
	if e != nil {
		return e
	}
	digest := sha256.New()
	_, e = io.Copy(digest, hashFile)
	hashFile.Close()
	if e != nil {
		return e
	}
	checksum := hex.EncodeToString(digest.Sum(nil))
	var last error
	clearedStalePartial := false
	for attempt := 0; attempt <= 5; attempt++ {
		if attempt > 0 {
			if e = retry(ctx, attempt-1); e != nil {
				return e
			}
		}
		offset, e := c.Partial(ctx, remote)
		if e != nil {
			if a, ok := e.(*Error); !ok || a.Status != 404 {
				last = e
				if retryable(e) {
					continue
				}
				return e
			}
			offset = 0
		}
		if offset < 0 || offset > st.Size() {
			return fmt.Errorf("invalid remote partial size %d", offset)
		}
		f, e := os.Open(local)
		if e != nil {
			return e
		}
		current, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		if current.Size() != st.Size() || !current.ModTime().Equal(st.ModTime()) {
			f.Close()
			return fmt.Errorf("local file changed during upload")
		}
		_, e = f.Seek(offset, io.SeekStart)
		if e != nil {
			f.Close()
			return e
		}
		q := url.Values{"path": {remote}, "offset": {strconv.FormatInt(offset, 10)}, "overwrite": {"0"}}
		if overwrite {
			q.Set("overwrite", "1")
		}
		reader := &progressReader{f, offset, st.Size(), c.Progress}
		r, e := c.request(ctx, "PUT", "write", q, reader, st.Size()-offset, map[string]string{"X-Expected-Size": strconv.FormatInt(st.Size(), 10), "X-Content-SHA256": checksum})
		f.Close()
		if e == nil {
			var result struct{ Partial bool }
			e = json.NewDecoder(r.Body).Decode(&result)
			r.Body.Close()
			if e == nil && !result.Partial {
				return nil
			}
			if e == nil {
				e = fmt.Errorf("server retained incomplete upload")
			}
		}
		if ae, ok := e.(*Error); ok && ae.Code == "bad_request" && strings.Contains(ae.Message, "sha256 mismatch") && !clearedStalePartial {
			current, statErr := os.Stat(local)
			if statErr != nil {
				return statErr
			}
			if current.Size() != st.Size() || !current.ModTime().Equal(st.ModTime()) {
				_ = c.Cancel(ctx, remote)
				return fmt.Errorf("local file changed during upload")
			}
			if cancelErr := c.Cancel(ctx, remote); cancelErr != nil {
				return fmt.Errorf("discard stale remote partial: %w", cancelErr)
			}
			clearedStalePartial = true
			last = e
			attempt = -1
			continue
		}
		last = e
		if !retryable(e) {
			return e
		}
	}
	return last
}
func (c *Client) Get(ctx context.Context, remote, local string) error {
	st, e := c.Stat(ctx, remote)
	if e != nil {
		return e
	}
	if st.Type == "dir" {
		if e = os.MkdirAll(local, 0755); e != nil {
			return e
		}
		ls, e := c.List(ctx, remote)
		if e != nil {
			return e
		}
		if ls.Truncated {
			return fmt.Errorf("directory listing truncated; refusing incomplete download")
		}
		for _, ent := range ls.Entries {
			if ent.Name == "." || ent.Name == ".." || path.Base(ent.Name) != ent.Name {
				return fmt.Errorf("unsafe entry name")
			}
			if ent.Type == "symlink" {
				return fmt.Errorf("refusing recursive symlink %s", ent.Name)
			}
			if e = c.Get(ctx, path.Join(remote, ent.Name), filepath.Join(local, ent.Name)); e != nil {
				return e
			}
		}
		return nil
	}
	if st.Type != "file" {
		return fmt.Errorf("not a regular file")
	}
	if _, err := os.Lstat(local); err == nil {
		return fmt.Errorf("destination exists: %s", local)
	} else if !os.IsNotExist(err) {
		return err
	}
	partial := local + ".deaddrop-part"
	if info, err := os.Lstat(partial); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("partial is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Bind a persisted partial to the source revision before resuming it.
	marker := partial + ".json"
	revision, _ := json.Marshal(struct {
		Remote, Base string
		Size         int64
		Mtime        time.Time
	}{remote, c.Base, st.Size, st.Mtime})
	if markerInfo, err := os.Lstat(marker); err == nil && !markerInfo.Mode().IsRegular() {
		return fmt.Errorf("partial metadata is not a regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if saved, err := os.ReadFile(marker); err == nil {
		if string(saved) != string(revision) {
			return fmt.Errorf("remote file changed; remove stale partial %s to restart", partial)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		if info, err := os.Stat(partial); err == nil && info.Size() > 0 {
			return fmt.Errorf("partial lacks source revision metadata; remove %s to restart", partial)
		}
		mf, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if _, err = mf.Write(revision); err == nil {
			err = mf.Sync()
		}
		closeErr := mf.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	f, e := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	var last error
	for attempt := 0; attempt <= 5; attempt++ {
		if attempt > 0 {
			if e = retry(ctx, attempt-1); e != nil {
				return e
			}
		}
		info, e := f.Stat()
		if e != nil {
			return e
		}
		offset := info.Size()
		if offset > st.Size {
			return fmt.Errorf("local partial exceeds remote size")
		}
		headers := map[string]string{}
		if offset > 0 {
			headers["Range"] = fmt.Sprintf("bytes=%d-", offset)
		}
		if offset == st.Size && offset > 0 {
			break
		}
		r, e := c.request(ctx, "GET", "read", url.Values{"path": {remote}}, nil, 0, headers)
		if e != nil {
			last = e
			if retryable(e) {
				continue
			}
			return e
		}
		if offset == 0 && r.StatusCode != http.StatusOK {
			r.Body.Close()
			return fmt.Errorf("invalid initial download status %d", r.StatusCode)
		}
		if offset > 0 && r.StatusCode != 206 {
			r.Body.Close()
			return fmt.Errorf("server ignored resume range")
		}
		if offset > 0 && !validContentRange(r.Header.Get("Content-Range"), offset, st.Size) {
			r.Body.Close()
			return fmt.Errorf("invalid resume range")
		}
		if r.ContentLength != st.Size-offset {
			r.Body.Close()
			return fmt.Errorf("invalid download content length")
		}
		expectedETag := fmt.Sprintf("\"%x-%x\"", st.Size, st.Mtime.UnixNano())
		if got := r.Header.Get("ETag"); got == "" || got != expectedETag {
			r.Body.Close()
			return fmt.Errorf("remote file revision changed before download")
		}
		if _, e = f.Seek(offset, io.SeekStart); e != nil {
			r.Body.Close()
			return e
		}
		_, e = io.Copy(f, &progressReader{r.Body, offset, st.Size, c.Progress})
		r.Body.Close()
		if e == nil {
			info, e = f.Stat()
			if e == nil && info.Size() == st.Size {
				last = nil
				break
			}
			e = fmt.Errorf("download size mismatch")
		}
		last = e
	}
	if last != nil {
		return last
	}
	latest, e := c.Stat(ctx, remote)
	if e != nil {
		return e
	}
	if latest.Size != st.Size || !latest.Mtime.Equal(st.Mtime) {
		return fmt.Errorf("remote file changed during download")
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	// A hard link publishes atomically without replacing an existing destination.
	if e = os.Link(partial, local); e != nil {
		return e
	}
	if e = os.Remove(partial); e != nil {
		return e
	}
	return os.Remove(marker)
}
func validContentRange(v string, start, total int64) bool {
	if !strings.HasPrefix(v, "bytes ") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(v, "bytes "), "/")
	if len(parts) != 2 {
		return false
	}
	span := strings.Split(parts[0], "-")
	if len(span) != 2 {
		return false
	}
	a, e1 := strconv.ParseInt(span[0], 10, 64)
	b, e2 := strconv.ParseInt(span[1], 10, 64)
	n, e3 := strconv.ParseInt(parts[1], 10, 64)
	return e1 == nil && e2 == nil && e3 == nil && a == start && b == total-1 && n == total
}
