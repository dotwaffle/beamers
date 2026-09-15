package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPShutdownDrainsActiveRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(response, "completed")
	})
	server := httptest.NewUnstartedServer(handler)
	server.Config = newHTTPServer(handler)
	server.Start()
	defer server.Close()
	defer close(release)
	requestDone := make(chan error, 1)
	go func() {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		if err != nil {
			requestDone <- err
			return
		}
		response, err := server.Client().Do(request)
		if err == nil {
			var body []byte
			body, err = io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err == nil && string(body) != "completed" {
				err = fmt.Errorf("response body = %q, want completed", body)
			}
		}
		requestDone <- err
	}()
	<-started
	shutdownStarted := make(chan struct{})
	server.Config.RegisterOnShutdown(func() { close(shutdownStarted) })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Config.Shutdown(ctx) }()
	<-shutdownStarted
	select {
	case err := <-requestDone:
		t.Fatalf("active request ended before release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	if err := <-requestDone; err != nil {
		t.Fatalf("complete active request: %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("drain active request: %v", err)
	}
}

func TestHTTPShutdownClosesConnectionsWithoutRequests(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		name := "HTTP"
		if encrypted {
			name = "TLS"
		}
		t.Run(name, func(t *testing.T) {
			handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
			server := httptest.NewUnstartedServer(handler)
			server.Config = newHTTPServer(handler)
			accepted := make(chan struct{}, 1)
			previous := server.Config.ConnState
			server.Config.ConnState = func(connection net.Conn, state http.ConnState) {
				if previous != nil {
					previous(connection, state)
				}
				if state == http.StateNew {
					accepted <- struct{}{}
				}
			}
			if encrypted {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			var dialer net.Dialer
			connection, err := dialer.DialContext(t.Context(), "tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatalf("connect without sending a request: %v", err)
			}
			defer func() { _ = connection.Close() }()
			select {
			case <-accepted:
			case <-t.Context().Done():
				t.Fatal("connection was not accepted")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			if err := server.Config.Shutdown(ctx); err != nil {
				t.Fatalf("shutdown with an unused connection: %v", err)
			}
		})
	}
}
