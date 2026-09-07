package server

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/maadiii/gogate/internal/routing"
)

const defaultProxyTimeout = 10 * time.Second

type Gateway struct {
	table *routing.Table
	cli   *client.Client

	proxyMu sync.RWMutex
	proxies map[string]ProxyFunc
}

func NewGateway(table *routing.Table, cli *client.Client) *Gateway {
	return &Gateway{
		table:   table,
		cli:     cli,
		proxies: map[string]ProxyFunc{},
	}
}

// Forward is the single request-handling entrypoint: it resolves the
// route, obtains (or builds and caches) the ProxyFunc for that route's
// target, and runs the full PreRequest -> proxy -> PostResponse / OnError
// pipeline. Its signature matches Hertz's handler signature directly, so
// it can be registered as-is (e.g. `s.Any("/*path", gw.Forward)`).
func (g *Gateway) Forward(c context.Context, rc *app.RequestContext) {
	route, ok := g.table.Resolve(string(rc.Method()), string(rc.Path()))
	if !ok {
		rc.NotFound()

		return
	}

	proxy, err := g.getOrCreateProxy(route.Target)
	if err != nil {
		// A malformed target must never take down the whole gateway process.
		// A single bad route configuration degrades to a 502 for requests on that route only.
		log.Printf("creating proxy for target %q: %v", route.Target, err)
		rc.AbortWithStatus(http.StatusBadGateway)

		return
	}

	// Bound the backend call with a timeout so a hanging downstream
	// service cannot block this request forever. This only wraps the
	// proxy call, not hook execution — hooks are expected to manage any
	// of their own I/O timeouts internally.
	ctx, cancel := context.WithTimeout(c, defaultProxyTimeout)
	defer cancel()

	forward(route, proxy, ctx, rc)
}

// getOrCreateProxy returns the cached ProxyFunc for target, building and
// caching it on first use. Uses the standard double-checked locking
// pattern: an RLock fast path for the common case (already cached), and
// a Lock + re-check for the rare first-time build, so concurrent
// requests for a target that hasn't been built yet cannot race to build
// duplicate (and independently stateful) proxies for the same target.
func (g *Gateway) getOrCreateProxy(target string) (ProxyFunc, error) {
	g.proxyMu.RLock()
	p, ok := g.proxies[target]
	g.proxyMu.RUnlock()

	if ok {
		return p, nil
	}

	g.proxyMu.Lock()
	defer g.proxyMu.Unlock()

	// re-check: maybe another goroutine has made it in time
	if p, ok := g.proxies[target]; ok {
		return p, nil
	}

	p, err := newProxyFunc(target, g.cli)
	if err != nil {
		return nil, err
	}

	g.proxies[target] = p

	return p, nil
}
