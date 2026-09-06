package server

import (
	"context"
	"fmt"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/internal/routing"
)

// forward2 runs the full request pipeline for an already-resolved route:
// PreRequest hooks -> (if not aborted) proxy -> PostResponse hooks. Any
// error at any stage routes into OnError hooks instead of continuing.
func forward(route *routing.ResolvedRoute, proxy ProxyFunc, c context.Context, rc *app.RequestContext) {
	if err := runStage(c, rc, route.PreRequestHooks); err != nil {
		_ = rc.Error(err)
		runOnError(c, rc, route)

		return
	}

	if rc.IsAborted() {
		return
	}

	if err := proxy(c, rc); err != nil {
		_ = rc.Error(err)
		runOnError(c, rc, route)

		return
	}

	if err := runStage(c, rc, route.PostResponseHooks); err != nil {
		_ = rc.Error(err)
		runOnError(c, rc, route)
	}
}

func runStage(c context.Context, rc *app.RequestContext, hooks []hook.Hook) error {
	for _, h := range hooks {
		if rc.IsAborted() {
			break
		}

		if err := h.Execute(c, rc); err != nil {
			return fmt.Errorf("hook %q failed: %w", h.Name(), err)
		}
	}

	return nil
}

// runOnError executes the route's OnError hooks. If they themselves
// fail (or panic, via the same recovery inside runStage), that failure
// is recorded rather than silently discarded, so it remains visible in
// rc.Errors for observability even though the response the caller
// receives is whatever status the OnError hooks (or the framework
// default) ultimately set.
func runOnError(c context.Context, rc *app.RequestContext, route *routing.ResolvedRoute) {
	if err := runStage(c, rc, route.OnErrorHooks); err != nil {
		_ = rc.Error(fmt.Errorf("onError hook also failed: %w", err))
	}
}
