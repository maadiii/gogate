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
		clearResponse(rc)
		runOnErrorWithFallback(c, rc, route, http.StatusBadGateway)

		return
	}

	if err := runStage(c, rc, route.PostResponseHooks); err != nil {
		_ = rc.Error(err)
		discardProxiedResponse(rc)
		runOnErrorWithFallback(c, rc, route, http.StatusInternalServerError)
	}
}

// discardProxiedResponse keeps a copy of the downstream response for the
// OnError stage, then throws the response itself away so that no part of a
// downstream payload can survive into an error response.
//
// This is the fix for a real leak. A hook in the PostResponse stage runs after
// the backend has already answered, so by then rc.Response holds that
// backend's complete status, headers and body. Hooks in that stage exist
// precisely to reshape that payload before the client sees it — a future
// TransformHook strips fields, an AggregationHook merges responses. If one of
// them fails, the payload in hand is the *unshaped* one, and forwarding any
// part of it under an error status hands the client exactly the data the hook
// was there to remove, with a matching Content-Length to go with it.
//
// The rule this establishes: a hook that wants to answer the client directly
// must call AbortWithStatus/AbortWithMsg (which short-circuits and keeps its
// own response), while a hook that returns an error hands the response to the
// OnError stage, which starts from a clean slate.
//
// Throwing the payload away must not also blind the OnError stage, so it is
// copied into the context first. An OnError hook reads it back with
// hook.FailedResponseFrom — which is how an audit or error-formatting hook
// still reports *what the backend said* without that payload being what the
// client receives. The copy is a snapshot, not a reference: the response being
// reset here has its buffers pooled, so holding onto it would mean reading
// recycled memory.
//
// The PreRequest error path deliberately does NOT call this: nothing
// downstream has run, so the response cannot contain a proxied payload, and
// anything in it was put there on purpose.
func discardProxiedResponse(rc *app.RequestContext) {
	hook.CaptureFailedResponse(rc)
	rc.Response.Reset()
}

// clearResponse throws the response away without keeping a copy of it, for the
// proxy-failure path where the reverse proxy returned without writing a
// response at all. There is no downstream payload to preserve there, and
// capturing anyway would be worse than useless: Hertz reports 200 for a
// response whose status was never set, so the OnError stage would be told the
// backend said 200 when in fact it never answered.
func clearResponse(rc *app.RequestContext) {
	rc.Response.Reset()
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
