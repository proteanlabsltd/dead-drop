package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/protean-labs/dead-drop/daemon/internal/config"
)

func TestInsecureLoopbackStartsWithoutTailscale(t *testing.T) {
	probe, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	s, e := New(Options{Config: config.Config{Port: port, Roots: []string{t.TempDir()}, AllowUsers: []string{"me"}}, Socket: t.TempDir() + "/missing", InsecureAllowLoopback: true, PollInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	url := "http://127.0.0.1:" + fmt.Sprint(port) + "/v1/info"
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, e = http.Get(url)
		if e == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	cancel()
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}
