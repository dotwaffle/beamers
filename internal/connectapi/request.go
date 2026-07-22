package connectapi

import (
	"context"
	"crypto/rand"
	"errors"

	"connectrpc.com/connect"

	"github.com/dotwaffle/beamers/internal/command"
)

const requestIDHeader = "X-Request-ID"

// RequestIDInterceptor validates or creates one request correlation identifier.
func RequestIDInterceptor() connect.Interceptor {
	return requestIDInterceptor{}
}

type requestIDInterceptor struct{}

func (requestIDInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		requestID := request.Header().Get(requestIDHeader)
		if requestID == "" {
			requestID = rand.Text()
		} else if err := command.ValidateID(requestID); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid request ID"))
		}
		response, err := next(ctx, request)
		if response != nil {
			response.Header().Set(requestIDHeader, requestID)
		}
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			connectErr.Meta().Set(requestIDHeader, requestID)
		}
		return response, err
	}
}

func (requestIDInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (requestIDInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
