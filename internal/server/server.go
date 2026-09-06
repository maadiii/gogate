package server

import (
	"context"
	"fmt"
	"log"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/hertz-contrib/reverseproxy"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/internal/routing"
)

const targetHeader = "X-Gateway-Target"

type ProxyFunc func(c context.Context, rc *app.RequestContext) error

func Forward(
	table *routing.Table,
	client *client.Client,
	c context.Context,
	rc *app.RequestContext,
) {
	route, ok := table.Resolve(string(rc.Method()), string(rc.Path()))
	if !ok {
		rc.NotFound()

		return
	}

	proxy, err := newProxyFunc(route.Target, client)
	if err != nil {
		log.Fatalf("creating proxy func: %v", err)
	}

	forward(route, proxy, c, rc)
}

func newProxyFunc(target string, client *client.Client) (ProxyFunc, error) {
	rp, err := reverseproxy.NewSingleHostReverseProxy(target)
	if err != nil {
		return nil, fmt.Errorf("creating reverse proxy: %w", err)
	}

	rp.SetClient(client)
	rp.SetDirector(func(req *protocol.Request) {
		targetHeader := string(req.Header.Peek(targetHeader))
		req.Header.Del(targetHeader)

		req.SetRequestURI(string(reverseproxy.JoinURLPath(req, targetHeader)))
		req.Header.SetHostBytes(req.URI().Host())
	})

	return func(c context.Context, rc *app.RequestContext) error {
		targetHeader := rc.Request.Header.Get(targetHeader)
		if targetHeader == "" {
			return fmt.Errorf("proxying request: %s header not set (routing hook must set it)", targetHeader)
		}

		rp.ServeHTTP(c, rc)

		if rc.Response.StatusCode() == 0 {
			return fmt.Errorf("proxying request: no response received from target")
		}

		return nil
	}, nil
}

func forward(route *routing.ResolvedRoute, proxy ProxyFunc, c context.Context, rc *app.RequestContext) {
	if err := runStage(route.PreRequestHooks, rc); err != nil {
		_ = rc.Error(err)
		runOnError(route, rc)

		return
	}

	if rc.IsAborted() {
		return
	}

	if err := proxy(c, rc); err != nil {
		_ = rc.Error(err)
		runOnError(route, rc)

		return
	}

	if err := runStage(route.PostResponseHooks, rc); err != nil {
		_ = rc.Error(err)
		runOnError(route, rc)
	}
}

func runStage(hooks []hook.Hook, rc *app.RequestContext) error {
	for _, h := range hooks {
		if rc.IsAborted() {
			break
		}

		if err := h.Execute(rc); err != nil {
			return fmt.Errorf("hook %q failed: %w", h.Name(), err)
		}
	}

	return nil
}

func runOnError(route *routing.ResolvedRoute, rc *app.RequestContext) {
	if err := runStage(route.OnErrorHooks, rc); err != nil {
		_ = rc.Error(fmt.Errorf("onError hook also failed: %w", err))
	}
}
