package connectapi_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/dotwaffle/beamers/internal/connectapi"
)

var errDomain = errors.New("domain failure")

func TestErrorInterceptorTranslatesDomainFailures(t *testing.T) {
	t.Parallel()
	mapper := func(err error) error {
		if errors.Is(err, errDomain) {
			return connect.NewError(connect.CodeNotFound, err)
		}
		return connect.NewError(connect.CodeInternal, errors.New("service failed"))
	}
	tests := []struct {
		name         string
		returned     error
		expectedCode connect.Code
		expectedText string
	}{
		{name: "success", returned: nil},
		{
			name:         "known domain failure",
			returned:     errDomain,
			expectedCode: connect.CodeNotFound,
			expectedText: "domain failure",
		},
		{
			name:         "unknown failure",
			returned:     errors.New("boom"),
			expectedCode: connect.CodeInternal,
			expectedText: "service failed",
		},
		{
			name:         "existing Connect error passes through unmapped",
			returned:     connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required")),
			expectedCode: connect.CodeUnauthenticated,
			expectedText: "authentication required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
				return nil, test.returned
			})
			_, err := connectapi.ErrorInterceptor(mapper).WrapUnary(next)(t.Context(), nil)
			if test.returned == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var connectErr *connect.Error
			if !errors.As(err, &connectErr) {
				t.Fatalf("expected a Connect error, got %v", err)
			}
			if connectErr.Code() != test.expectedCode {
				t.Fatalf("expected code %v, got %v", test.expectedCode, connectErr.Code())
			}
			if connectErr.Message() != test.expectedText {
				t.Fatalf("expected message %q, got %q", test.expectedText, connectErr.Message())
			}
		})
	}
}

func TestErrorInterceptorWithoutMapperIsInert(t *testing.T) {
	t.Parallel()
	if connectapi.ErrorInterceptor(nil) != nil {
		t.Fatal("expected no interceptor without a mapper")
	}
}

func TestValidationInterceptorRejectsMalformedRequests(t *testing.T) {
	t.Parallel()
	validate := func(request any) error {
		if request == nil {
			return errors.New("request is required")
		}
		return nil
	}
	dispatched := false
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		dispatched = true
		return nil, nil
	})
	_, err := connectapi.ValidationInterceptor(validate).WrapUnary(next)(t.Context(), stubRequest{})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected a Connect error, got %v", err)
	}
	if connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument, got %v", connectErr.Code())
	}
	if dispatched {
		t.Fatal("expected no dispatch for a malformed request")
	}
}

func TestValidationInterceptorDispatchesValidRequests(t *testing.T) {
	t.Parallel()
	dispatched := false
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		dispatched = true
		return nil, nil
	})
	validate := func(any) error { return nil }
	if _, err := connectapi.ValidationInterceptor(validate).WrapUnary(next)(t.Context(), stubRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dispatched {
		t.Fatal("expected dispatch for a valid request")
	}
}

func TestValidationInterceptorWithoutValidatorIsInert(t *testing.T) {
	t.Parallel()
	if connectapi.ValidationInterceptor(nil) != nil {
		t.Fatal("expected no interceptor without a validator")
	}
}

type stubRequest struct {
	connect.AnyRequest
}

func (stubRequest) Any() any { return nil }
