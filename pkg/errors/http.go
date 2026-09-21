package errors

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

// TODO: Add observability
func ErrorHandler(isProd bool) app.HandlerFunc {
	return func(c context.Context, rc *app.RequestContext) {
		rc.Next(c)

		if len(rc.Errors) == 0 {
			return
		}

		var appErr *Error
		as := As(rc.Errors[0], &appErr)
		if !as {
			return
		}

		var status int
		switch appErr.code {
		case forbidden:
			status = http.StatusForbidden
		default:
			status = http.StatusInternalServerError
		}

		res := map[string]any{"key": appErr.key}
		if isProd {
			rc.AbortWithStatusJSON(status, res)
		}

		res["stack"] = fmt.Sprintf("%+v", appErr.error)
		rc.AbortWithStatusJSON(status, res)
	}
}

func NotFound(err error, what string) error {
	msg := fmt.Sprintf("%s not found", what)

	return &Error{
		error: New(err.Error()),
		key:   makeErrorKey(msg),
		code:  notFound,
	}
}

func Unauthorized() error {
	msg := "unauthorized"

	return &Error{
		error: New(msg),
		key:   makeErrorKey(msg),
		code:  unauthorized,
	}
}

func Forbidden() error {
	msg := "forbidden"

	return &Error{
		error: New(msg),
		key:   makeErrorKey(msg),
		code:  forbidden,
	}
}
