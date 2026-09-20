package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
)

// The gateway bounds a request with one deadline (defaultProxyTimeout), shared
// by every stage: PreRequest hooks, the proxy call, and PostResponse hooks all
// run under the same context.
//
// That deadline alone, however, is not enough to guarantee the request ends. It
// is a context deadline, and the hertz client only consults the context at the
// top of its retry loop - so a read that is already blocked on a backend which
// accepted the connection and then went silent never observes it. The reverse
// proxy therefore also sets a request-level timeout
// (reverseproxy.ClientDoTimeout), which is what actually reaches the socket's
// read deadline.
//
// These two tests cover the two halves: that the deadline reaches the hooks
// (fast), and that a hanging backend is really cut off (slow, ~10s).

// TestForward_HooksShareTheDownstreamDeadline locks in what the comment in
// gateway.go used to get wrong: the timeout wraps hook execution too, not just
// the proxy call. Every hook in the pipeline sees the same single deadline, so
// a hook cannot outlive the request's budget.
func TestForward_HooksShareTheDownstreamDeadline(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	var (
		preDeadline, postDeadline time.Time
		preHasDeadline            bool
		postHasDeadline           bool
	)

	preHook := &recordingHook{
		name: "pre", stage: hook.PreRequest,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			preDeadline, preHasDeadline = ctx.Deadline()

			return nil
		},
	}
	postHook := &recordingHook{
		name: "post", stage: hook.PostResponse,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			postDeadline, postHasDeadline = ctx.Deadline()

			return nil
		},
	}

	registry := hook.NewRegistry()
	registerHook(t, registry, preHook)
	registerHook(t, registry, postHook)

	gw := buildGateway(t, registry, routeSpec{
		path: "/ping", methods: []string{"GET"}, target: backend.URL,
		hooks: config.RouteHooks{
			PreRequest:   config.HookRefList{{Name: preHook.name}},
			PostResponse: config.HookRefList{{Name: postHook.name}},
		},
	})

	start := time.Now()

	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc)

	if got := rc.Response.StatusCode(); got != http.StatusOK {
		t.Fatalf("expected the request to succeed, got status %d: %v", got, rc.Errors)
	}

	if !preHasDeadline {
		t.Error("the PreRequest hook received a context with no deadline; the gateway's timeout does not reach hooks")
	}
	if !postHasDeadline {
		t.Error("the PostResponse hook received a context with no deadline")
	}
	if !preDeadline.Equal(postDeadline) {
		t.Errorf(
			"hooks saw different deadlines (%s vs %s); the pipeline is supposed to share one budget",
			preDeadline, postDeadline,
		)
	}

	// The deadline must be the configured one, not some other duration that
	// happens to be non-zero.
	const tolerance = 3 * time.Second

	earliest := start.Add(defaultProxyTimeout - tolerance)
	latest := start.Add(defaultProxyTimeout + tolerance)

	if preDeadline.Before(earliest) || preDeadline.After(latest) {
		t.Errorf(
			"expected a deadline about %s out, got %s (which is %s from the start of the request)",
			defaultProxyTimeout, preDeadline, preDeadline.Sub(start),
		)
	}
}

// TestForward_HangingBackend_TimesOutWithBadGateway is the behaviour-level
// counterpart: a backend that accepts the connection and then never answers
// must not be able to hold the request open forever. The gateway's deadline
// has to fire and the caller has to get a real error, not a hang.
//
// This test is what caught the request-level timeout being missing from
// newProxyFunc: without it, Forward never returned for this backend at all,
// because a context deadline cannot interrupt a read the hertz client is
// already blocked on.
//
// It deliberately takes about defaultProxyTimeout: the point is to observe the
// real configured deadline fire, so it cannot be shortened or skipped.
func TestForward_HangingBackend_TimesOutWithBadGateway(t *testing.T) {
	t.Parallel()

	hanging := newHangingTarget(t)

	gw := buildGateway(t, hook.NewRegistry(), routeSpec{
		path: "/slow", methods: []string{"GET"}, target: hanging,
	})

	rc := newTestContext("GET", "/slow")

	start := time.Now()

	// Run Forward on its own goroutine so that a regression shows up as a
	// failed assertion with a readable message, rather than as a hung test
	// binary that has to be killed.
	done := make(chan struct{})

	go func() {
		gw.Forward(context.Background(), rc)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(defaultProxyTimeout + 20*time.Second):
		t.Fatalf(
			"the gateway did not return within %s for a backend that accepts the connection and "+
				"never replies; such a backend is pinning the request instead of being cut off at "+
				"the %s deadline",
			defaultProxyTimeout+20*time.Second, defaultProxyTimeout,
		)
	}

	elapsed := time.Since(start)

	if got := rc.Response.StatusCode(); got != http.StatusBadGateway {
		t.Errorf("expected 502 for a backend that never responds, got %d", got)
	}

	if len(rc.Errors) == 0 {
		t.Error("expected the timeout to be recorded as an error on the context")
	}

	// The whole point is that the gateway, not something else, cut the request
	// off - and did it at roughly the configured deadline.
	if elapsed < defaultProxyTimeout {
		t.Errorf(
			"the request failed after only %s, before the %s deadline - something other than the gateway's timeout ended it",
			elapsed, defaultProxyTimeout,
		)
	}
	if elapsed > defaultProxyTimeout+10*time.Second {
		t.Errorf("the request took %s, far longer than the %s deadline", elapsed, defaultProxyTimeout)
	}
}

// newHangingTarget starts a TCP listener that accepts connections and then
// never writes a single byte back, so any client that talks to it waits for a
// response that will never come. It returns the target URL.
//
// A raw listener is used rather than an httptest.Server because
// httptest.Server.Close blocks until every in-flight request finishes - and
// the whole point here is a request that never finishes.
func newHangingTarget(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding hanging listener: %v", err)
	}

	var (
		mu       sync.Mutex
		accepted []net.Conn
	)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			// Hold the connection open, and never reply on it.
			mu.Lock()
			accepted = append(accepted, conn)
			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()

		mu.Lock()
		defer mu.Unlock()

		for _, conn := range accepted {
			_ = conn.Close()
		}
	})

	return "http://" + ln.Addr().String()
}
