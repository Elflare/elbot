package testutil

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
)

// NewVirtualHTTPServer serves real HTTP over net.Pipe. Call it inside synctest.Test
// and pass server.Client() to the code under test.
func NewVirtualHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return newVirtualHTTPServer(t, handler, false)
}

// NewVirtualDefaultHTTPServer also replaces http.DefaultTransport until cleanup.
// The test and its parents must remain serial, and all clients must stop before
// cleanup. Parallel tests must not share this process-wide transport override.
func NewVirtualDefaultHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	return newVirtualHTTPServer(t, handler, true)
}

func newVirtualHTTPServer(t *testing.T, handler http.Handler, useDefault bool) *httptest.Server {
	t.Helper()
	listener := &virtualHTTPListener{peers: make(chan net.Conn), done: make(chan struct{})}
	var mu sync.Mutex
	var connections []net.Conn
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, peer := net.Pipe()
		mu.Lock()
		connections = append(connections, client, peer)
		mu.Unlock()
		select {
		case listener.peers <- peer:
			return client, nil
		case <-ctx.Done():
			_ = client.Close()
			_ = peer.Close()
			return nil, ctx.Err()
		case <-listener.done:
			_ = client.Close()
			_ = peer.Close()
			return nil, net.ErrClosed
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	server := &httptest.Server{Listener: listener, Config: &http.Server{
		BaseContext: func(net.Listener) context.Context { return ctx },
		Handler:     handler,
	}}
	server.Start()
	server.Client().Transport = transport
	original := http.DefaultTransport
	if useDefault {
		http.DefaultTransport = transport
	}
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		mu.Lock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		transport.CloseIdleConnections()
		server.Close()
		synctest.Wait()
		if useDefault {
			http.DefaultTransport = original
		}
	})
	return server
}

type virtualHTTPListener struct {
	peers chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *virtualHTTPListener) Accept() (net.Conn, error) {
	select {
	case peer := <-l.peers:
		return peer, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *virtualHTTPListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (*virtualHTTPListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80}
}
