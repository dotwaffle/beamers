// Package connectapi provides shared authenticated Connect request infrastructure.
package connectapi

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"github.com/dotwaffle/beamers/internal/auth"
)

const sessionCookieName = "beamers_session"

type actorContextKey struct{}

// AuthenticationInterceptor authenticates the shared browser session cookie once per RPC.
func AuthenticationInterceptor(authentication *auth.Service) (connect.Interceptor, error) {
	if authentication == nil {
		return nil, errors.New("authentication service is required")
	}
	return &authenticationInterceptor{authentication: authentication}, nil
}

type authenticationInterceptor struct {
	authentication *auth.Service
}

func (interceptor *authenticationInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		cookie, err := (&http.Request{Header: request.Header()}).Cookie(sessionCookieName)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
		}
		actor, err := interceptor.authentication.Authenticate(ctx, cookie.Value)
		if errors.Is(err, auth.ErrInvalidSession) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("authentication unavailable"))
		}
		return next(context.WithValue(ctx, actorContextKey{}, actor), request)
	}
}

func (interceptor *authenticationInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (interceptor *authenticationInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// ActorFromContext returns the Account authenticated by AuthenticationInterceptor.
func ActorFromContext(ctx context.Context) (auth.Account, error) {
	actor, ok := ctx.Value(actorContextKey{}).(auth.Account)
	if !ok {
		return auth.Account{}, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	return actor, nil
}
