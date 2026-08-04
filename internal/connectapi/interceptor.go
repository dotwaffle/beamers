package connectapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// ErrorInterceptor translates domain failures into stable Connect codes using
// one service-supplied mapper. Errors already carrying a Connect code pass
// through untouched, so authentication and validation decisions survive. A nil
// mapper yields no interceptor, leaving the service without translation.
func ErrorInterceptor(mapper func(error) error) connect.Interceptor {
	if mapper == nil {
		return nil
	}
	return errorInterceptor{mapper: mapper}
}

type errorInterceptor struct {
	mapper func(error) error
}

func (interceptor errorInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		response, err := next(ctx, request)
		if err == nil {
			return response, nil
		}
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return response, err
		}
		return response, interceptor.mapper(err)
	}
}

func (errorInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (errorInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// ValidationInterceptor rejects malformed protobuf requests before dispatch
// using one service-supplied validator. A nil validator yields no interceptor,
// leaving the service without request validation.
func ValidationInterceptor(validate func(request any) error) connect.Interceptor {
	if validate == nil {
		return nil
	}
	return validationInterceptor{validate: validate}
}

type validationInterceptor struct {
	validate func(request any) error
}

func (interceptor validationInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
		if err := interceptor.validate(request.Any()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return next(ctx, request)
	}
}

func (validationInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (validationInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
