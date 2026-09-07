package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/internal/routing"
)

// forward runs the full request pipeline for an already-resolved route:
// PreRequest hooks -> (if not aborted) proxy -> PostResponse hooks. Any
// error at any stage routes into OnError hooks instead of continuing.
func forward(route *routing.ResolvedRoute, proxy ProxyFunc, c context.Context, rc *app.RequestContext) {
	if err := runStage(c, rc, route.PreRequestHooks); err != nil {
		_ = rc.Error(err)
		runOnErrorWithFallback(c, rc, route, http.StatusInternalServerError)

		return
	}

	if rc.IsAborted() {
		return
	}

	if err := proxy(c, rc); err != nil {
		_ = rc.Error(err)
		runOnErrorWithFallback(c, rc, route, http.StatusBadGateway)

		return
	}

	if err := runStage(c, rc, route.PostResponseHooks); err != nil {
		_ = rc.Error(err)
		runOnErrorWithFallback(c, rc, route, http.StatusInternalServerError)
	}
}

func runStage(c context.Context, rc *app.RequestContext, hooks []hook.Hook) (err error) {
	var currentHookName string
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("hook %q panicked: %v", currentHookName, r)
		}
	}()

	for _, h := range hooks {
		if rc.IsAborted() {
			break
		}

		currentHookName = h.Name()
		if err := h.Execute(c, rc); err != nil {
			return fmt.Errorf("hook %q failed: %w", h.Name(), err)
		}
	}

	return nil
}

// runOnErrorWithFallback runs the route's OnError hooks (if any), then
// guarantees the response reflects a real failure. If no OnError hook
// ran (or none of them explicitly set a status via Abort), the caller
// must never see a misleadingly successful-looking response — a
// sensible default status is forced instead.
func runOnErrorWithFallback(
	c context.Context, rc *app.RequestContext, route *routing.ResolvedRoute, fallbackStatus int,
) {
	if err := runStage(c, rc, route.OnErrorHooks); err != nil {
		_ = rc.Error(fmt.Errorf("onError hook also failed: %w", err))
	}

	if !rc.IsAborted() {
		rc.AbortWithStatus(fallbackStatus)
	}
}
