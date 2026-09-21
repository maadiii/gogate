package errors

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	hertzErrors "github.com/cloudwego/hertz/pkg/common/errors"
)

// StatusOf maps a coded error to the HTTP status it is reported with.
//
// One function decides this so that the pipeline's fallback abort and the
// ErrorHandler middleware cannot disagree. Two independent mappings would drift
// apart, and the symptom would be a status that changes depending on whether a
// middleware happened to be installed.
func StatusOf(err error) int {
	appErr := coded(err)
	if appErr == nil {
		return http.StatusInternalServerError
	}

	switch appErr.code {
	case CodeNotFound:
		return http.StatusNotFound
	case CodeAlreadyExists:
		return http.StatusConflict
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeBadGateway:
		return http.StatusBadGateway
	case CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// ErrorHandler turns whatever errors a request recorded into the response the
// client sees: a body carrying the error's key, plus a stack trace when not
// running in production, under the status that key's code maps to.
//
// This is the single place an error response is shaped, so every failure
// reaches the client in the same form — a hook rejecting a request, an
// unmatched route, a backend that refused the connection. It has to be
// installed before the routes are registered, since it depends on running ahead
// of the handler and continuing after it.
//
// TODO: Add observability
func ErrorHandler(isProd bool) app.HandlerFunc {
	return func(c context.Context, rc *app.RequestContext) {
		rc.Next(c)

		// The last coded error is the client's answer. Hooks earlier in the
		// chain record why the request failed, and an OnError hook with an
		// opinion about how to report it records one afterwards, so taking the
		// most recent lets an error-formatting or fallback hook have the final
		// say. Errors carrying no code of ours take no part in the decision:
		// they are bookkeeping about the error handling itself, and letting
		// them win would hide the failure that actually brought us here.
		appErr := lastCodedError(rc.Errors)
		if appErr == nil {
			return
		}

		res := map[string]any{"key": appErr.key}
		if !isProd {
			res["stack"] = fmt.Sprintf("%+v", appErr.error)
		}

		rc.AbortWithStatusJSON(StatusOf(appErr), res)
	}
}

// lastCodedError returns the most recently recorded coded error, or nil when
// the request recorded none.
func lastCodedError(errs []*hertzErrors.Error) *Error {
	for i := len(errs) - 1; i >= 0; i-- {
		if appErr := coded(errs[i]); appErr != nil {
			return appErr
		}
	}

	return nil
}

// NotFound reports a resource that does not exist.
func NotFound(what string) error {
	return WrapCK(nil, CodeNotFound, fmt.Sprintf("%s not found", what))
}

// Unauthorized reports a request that carried no usable credentials.
func Unauthorized() error {
	return WrapCK(nil, CodeUnauthorized, "unauthorized")
}

// Forbidden reports a caller that is authenticated but not allowed.
func Forbidden() error {
	return WrapCK(nil, CodeForbidden, "forbidden")
}

// BadGateway reports a failure to reach the downstream service, or to complete
// the call once it was reached.
func BadGateway(err error) error {
	return WrapCK(err, CodeBadGateway, "bad gateway")
}
