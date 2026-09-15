package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

func newHTTPServer(handler http.Handler) *http.Server {
	var mu sync.Mutex
	pending := make(map[net.Conn]struct{})
	stopping := false
	server := &http.Server{
		Handler:           handler,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ConnState: func(connection net.Conn, state http.ConnState) {
			mu.Lock()
			defer mu.Unlock()
			if state != http.StateNew {
				delete(pending, connection)
				return
			}
			if stopping {
				_ = connection.Close()
				return
			}
			pending[connection] = struct{}{}
		},
	}
	server.RegisterOnShutdown(func() {
		mu.Lock()
		defer mu.Unlock()
		stopping = true
		// net/http keeps new connections open for over five seconds. They
		// have no request to drain and can exhaust the HTTP shutdown budget.
		for connection := range pending {
			_ = connection.Close()
		}
		clear(pending)
	})
	return server
}
