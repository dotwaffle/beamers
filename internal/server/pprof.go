package server

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
)

// pprofRequestTimeout accommodates the longest default profile capture
// (a 30-second CPU profile) plus headroom, rather than the 10-second
// default applied to ordinary Crew routes.
const pprofRequestTimeout = 60 * time.Second

func pprofRoute() routeContract {
	return routeContract{kind: crewInterface, timeout: pprofRequestTimeout}
}

// pprofGuardInput names the shared dependencies guardPprofHandler enforces
// loopback-or-Administrator access with, factored out of registerPprofRoutes
// so each per-pattern guard call only needs to add the handler itself.
type pprofGuardInput struct {
	Authentication  *auth.Service
	Logger          *slog.Logger
	ListenerAddress net.Addr
}

// registerPprofRoutes exposes net/http/pprof profiling unauthenticated only
// on a loopback listener, and behind Administrator session authentication
// otherwise, following the same loopback-or-authenticated pattern as
// /diagnostics and /admin/diagnostics. pprof output can reveal stack
// traces, goroutine state, and heap contents, so it is never exposed
// unauthenticated on a non-loopback listener.
func registerPprofRoutes(mux *routeMux, guard pprofGuardInput) {
	handlers := map[string]http.HandlerFunc{
		"/debug/pprof/":        pprof.Index,
		"/debug/pprof/cmdline": pprof.Cmdline,
		"/debug/pprof/profile": pprof.Profile,
		"/debug/pprof/symbol":  pprof.Symbol,
		"/debug/pprof/trace":   pprof.Trace,
	}
	for pattern, handler := range handlers {
		mux.HandleFunc(pattern, pprofRoute(), guardPprofHandler(handler, guard))
	}
}

func guardPprofHandler(handler http.HandlerFunc, guard pprofGuardInput) http.HandlerFunc {
	loopback := listenerIsLoopback(guard.ListenerAddress)
	return func(response http.ResponseWriter, request *http.Request) {
		if !requestAllowed(response, request, http.MethodGet, loopback) {
			return
		}
		if loopback {
			handler(response, request)
			return
		}
		if _, ok := authenticateAdministrator(
			response,
			request,
			guard.Authentication,
			guard.Logger,
			"pprof",
		); !ok {
			return
		}
		handler(response, request)
	}
}
