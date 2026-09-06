package server

import (
	"context"
	"fmt"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/hertz-contrib/reverseproxy"
)

// ProxyFunc forwards a request to a fixed downstream target and writes
// the result into rc. It returns an error only when the request could
// not be completed against the target at all (e.g. connection refused,
// timeout) — a normal HTTP error response from the backend (4xx/5xx) is
// NOT an error here; it is a valid response that gets forwarded as-is.
type ProxyFunc func(c context.Context, rc *app.RequestContext) error

// proxyErrorKey is the RequestContext key used to smuggle the real
// underlying error out of the reverse proxy's error handler, since
// ReverseProxy.ServeHTTP itself has no return value.
const proxyErrorKey = "internal_proxy_error"

// newProxyFunc builds a ProxyFunc bound to a single, fixed target. This
// is called once per distinct target (see Gateway.getOrCreateProxy) and
// the returned ProxyFunc is safe to call concurrently for as many
// requests as needed, since nothing about a single target's identity
// changes between calls.
func newProxyFunc(target string, cli *client.Client) (ProxyFunc, error) {
	rp, err := reverseproxy.NewSingleHostReverseProxy(target)
	if err != nil {
		return nil, fmt.Errorf("creating reverse proxy for target %q: %w", target, err)
	}
	rp.SetClient(cli)

	// Capture the real error (connection refused, timeout, DNS failure,
	// etc.) instead of relying on an inferred zero status code, which is
	// unreliable if the library's default error handling already writes
	// some non-zero status before we get a chance to inspect it.
	rp.SetErrorHandler(func(c *app.RequestContext, proxyErr error) {
		c.Set(proxyErrorKey, proxyErr)
	})

	return func(c context.Context, rc *app.RequestContext) error {
		rp.ServeHTTP(c, rc)

		if proxyErr, exists := rc.Get(proxyErrorKey); exists {
			if err, ok := proxyErr.(error); ok {
				return fmt.Errorf("proxying requset to %q: %w", target, err)
			}
		}

		return nil
	}, nil
}
