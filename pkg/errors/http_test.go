package errors_test

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	hertz "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/maadiii/gogate/pkg/errors"
)

func TestStatusOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code errors.Code
		want int
	}{
		{name: "internal", code: errors.CodeInternal, want: http.StatusInternalServerError},
		{name: "not found", code: errors.CodeNotFound, want: http.StatusNotFound},
		{name: "already exists", code: errors.CodeAlreadyExists, want: http.StatusConflict},
		{name: "unauthorized", code: errors.CodeUnauthorized, want: http.StatusUnauthorized},
		{name: "forbidden", code: errors.CodeForbidden, want: http.StatusForbidden},
		{name: "bad request", code: errors.CodeBadRequest, want: http.StatusBadRequest},
		{name: "bad gateway", code: errors.CodeBadGateway, want: http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := errors.StatusOf(errors.WrapCK(nil, tt.code, "x")); got != tt.want {
				t.Errorf("StatusOf(%s) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}

	t.Run("uncoded error", func(t *testing.T) {
		t.Parallel()

		if got := errors.StatusOf(stderrors.New("plain")); got != http.StatusInternalServerError {
			t.Errorf("expected an uncoded error to map to 500, got %d", got)
		}
	})

	// The status has to survive being carried through the pipeline's own
	// wrapping, since that is the route every hook error takes to the client.
	t.Run("through a wrapped chain", func(t *testing.T) {
		t.Parallel()

		if got := errors.StatusOf(wrapTwice(errors.Unauthorized())); got != http.StatusUnauthorized {
			t.Errorf("expected 401 through the wrapper chain, got %d", got)
		}
	})
}

// wrapTwice reproduces what the pipeline does to a hook's error: runStage wraps
// it to name the hook, then forward wraps that to classify it. Like the real
// code, the cause survives both hops — if it did not, the coded error would be
// lost and every hook failure would be reported as a 500.
func wrapTwice(err error) error {
	return errors.Wrap(wrapOnce(err))
}

func wrapOnce(err error) error {
	return fmt.Errorf("hook %q failed: %w", "some_hook", err)
}

// errorResponder records and aborts the way the pipeline does, leaving the body
// to ErrorHandler so these tests exercise the same path a real failing request
// takes rather than calling the middleware in isolation.
func errorResponder(appErr error) app.HandlerFunc {
	return func(_ context.Context, rc *app.RequestContext) {
		_ = rc.AbortWithError(errors.StatusOf(appErr), appErr)
	}
}

// served boots a server with the middleware installed exactly as main.go
// installs it, and returns the status and the decoded body of one GET.
func served(t *testing.T, isProd bool, handler app.HandlerFunc) (int, map[string]any) {
	t.Helper()

	h := hertz.New()
	h.Use(errors.ErrorHandler(isProd))
	h.GET("/boom", handler)

	resp := ut.PerformRequest(h.Engine, http.MethodGet, "/boom", nil)

	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected a JSON error body, got %q: %v", resp.Body.String(), err)
	}

	return resp.Code, body
}

// TestErrorHandler_BodyIsTheKeyAndStack covers the shape of every error
// response: one key identifying what went wrong, a stack alongside it while
// running in development, and nothing else.
func TestErrorHandler_BodyIsTheKeyAndStack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		appErr   error
		wantKey  string
		wantCode int
	}{
		{
			name:     "unauthorized",
			appErr:   errors.Unauthorized(),
			wantKey:  "UNAUTHORIZED",
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "forbidden",
			appErr:   errors.Forbidden(),
			wantKey:  "FORBIDDEN",
			wantCode: http.StatusForbidden,
		},
		{
			name:     "not found",
			appErr:   errors.NotFound("route"),
			wantKey:  "ROUTE_NOT_FOUND",
			wantCode: http.StatusNotFound,
		},
		{
			name:     "bad gateway",
			appErr:   errors.BadGateway(stderrors.New("connection refused")),
			wantKey:  "BAD_GATEWAY",
			wantCode: http.StatusBadGateway,
		},
		{
			name:     "wrapped plain error",
			appErr:   errors.Wrap(stderrors.New("something broke")),
			wantKey:  "INTERNAL",
			wantCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, body := served(t, false, errorResponder(tt.appErr))

			if code != tt.wantCode {
				t.Errorf("status = %d, want %d", code, tt.wantCode)
			}

			if got := body["key"]; got != tt.wantKey {
				t.Errorf("key = %v, want %q", got, tt.wantKey)
			}

			if _, ok := body["stack"]; !ok {
				t.Error("expected a stack field outside production")
			}

			if len(body) != 2 {
				t.Errorf("expected the body to carry only key and stack, got %v", body)
			}
		})
	}
}

// TestErrorHandler_ProdOmitsStack is the security-relevant half: the stack is
// the one field that must not survive into production.
func TestErrorHandler_ProdOmitsStack(t *testing.T) {
	t.Parallel()

	code, body := served(t, true, errorResponder(errors.Unauthorized()))

	if code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", code)
	}

	if _, ok := body["stack"]; ok {
		t.Error("expected no stack field in production")
	}

	if len(body) != 1 {
		t.Errorf("expected the body to carry only the key in production, got %v", body)
	}
}

// TestErrorHandler_LastCodedErrorWins lets an OnError hook have the final say
// about how a failure is reported: the pipeline records why the request failed,
// and a hook that wants a different answer records its own error afterwards.
func TestErrorHandler_LastCodedErrorWins(t *testing.T) {
	t.Parallel()

	handler := func(_ context.Context, rc *app.RequestContext) {
		rootCause := errors.Wrap(stderrors.New("hook exploded"))
		_ = rc.Error(rootCause)

		laterOpinion := errors.NotFound("fallback")
		_ = rc.AbortWithError(errors.StatusOf(laterOpinion), laterOpinion)
	}

	code, body := served(t, false, handler)

	if code != http.StatusNotFound {
		t.Errorf("status = %d, want the last coded error's 404", code)
	}

	if got := body["key"]; got != "FALLBACK_NOT_FOUND" {
		t.Errorf("key = %v, want FALLBACK_NOT_FOUND", got)
	}
}

// TestErrorHandler_IgnoresUncodedErrors keeps bookkeeping out of the decision.
// A failure recorded while handling a failure is not a statement about what the
// client should be told, so it must not displace the reason the request failed.
func TestErrorHandler_IgnoresUncodedErrors(t *testing.T) {
	t.Parallel()

	handler := func(_ context.Context, rc *app.RequestContext) {
		appErr := errors.NotFound("route")
		_ = rc.Error(appErr)

		_ = rc.Error(stderrors.New("onError hook also failed"))
		rc.AbortWithStatus(errors.StatusOf(appErr))
	}

	code, body := served(t, false, handler)

	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 from the coded error", code)
	}

	if got := body["key"]; got != "ROUTE_NOT_FOUND" {
		t.Errorf("key = %v, want ROUTE_NOT_FOUND", got)
	}
}

// TestErrorHandler_LeavesCleanRequestsAlone guards against the middleware
// turning every successful request into an error response.
func TestErrorHandler_LeavesCleanRequestsAlone(t *testing.T) {
	t.Parallel()

	handler := func(_ context.Context, rc *app.RequestContext) {
		rc.String(http.StatusOK, "hello")
	}

	h := hertz.New()
	h.Use(errors.ErrorHandler(false))
	h.GET("/boom", handler)

	resp := ut.PerformRequest(h.Engine, http.MethodGet, "/boom", nil)

	if resp.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.Code)
	}

	if got := resp.Body.String(); got != "hello" {
		t.Errorf("body = %q, want %q", got, "hello")
	}
}
