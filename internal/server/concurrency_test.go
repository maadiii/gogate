package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
)

// These tests pin down the properties the gateway must hold under
// concurrency: no response ever reaches the wrong requester (cross-talk), and
// no per-request state survives into another request (context bleed).
//
// Two conventions apply throughout, both deliberate:
//
//   - Concurrency is always bounded via runConcurrently. Unbounded versions
//     make the httptest listeners refuse connections, and those transport
//     failures get attributed to whatever the test is actually asserting.
//
//   - A transport failure and a wrong answer are always separate failures.
//     If the gateway cannot reach a backend, that is a loud test failure in
//     its own right - never quietly folded into a body comparison, because an
//     empty body is exactly what real cross-talk looks like too.

// TestForward_ConcurrentDistinctTargets_NoCrossTalk is the direct
// cross-talk regression test: many backends, hit concurrently, each
// answering with a marker unique to itself. Every response must carry its own
// backend's marker.
func TestForward_ConcurrentDistinctTargets_NoCrossTalk(t *testing.T) {
	t.Parallel()

	const (
		backendCount = 6
		iterations   = 1200
		maxInFlight  = 32
	)

	bodies := make([]string, backendCount)
	specs := make([]routeSpec, backendCount)
	targets := make(map[string]string, backendCount)

	for i := range backendCount {
		body := fmt.Sprintf("backend-%d-payload", i)
		bodies[i] = body

		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(backend.Close)

		specs[i] = routeSpec{
			path:    fmt.Sprintf("/svc-%d", i),
			methods: []string{"GET"},
			target:  backend.URL,
		}
		targets[fmt.Sprintf("/svc-%d", i)] = body
	}

	gw := buildGateway(t, hook.NewRegistry(), specs...)

	var wrongAnswer, transportFailure atomic.Int64

	runConcurrently(iterations, maxInFlight, func(i int) {
		idx := i % backendCount
		path := fmt.Sprintf("/svc-%d", idx)

		rc := newTestContext("GET", path)
		gw.Forward(context.Background(), rc)

		if code := rc.Response.StatusCode(); code != http.StatusOK {
			transportFailure.Add(1)

			t.Errorf("GET %s: transport failure (status %d), not cross-talk: %v", path, code, rc.Errors)

			return
		}

		if got := string(rc.Response.Body()); got != targets[path] {
			wrongAnswer.Add(1)

			t.Errorf("GET %s: expected %q, got %q (CROSS-TALK)", path, targets[path], got)
		}
	})

	if n := transportFailure.Load(); n > 0 {
		t.Errorf("%d requests failed to reach their backend; cross-talk assertions for those requests were void", n)
	}
	if n := wrongAnswer.Load(); n > 0 {
		t.Errorf("%d responses were served by the wrong backend", n)
	}
}

// TestForward_SameTarget_ConcurrentRequests_ResponsesIsolated hits one
// single target concurrently. The backend echoes a caller-supplied
// correlation header back in the body, so each request can prove the body it
// received belongs to the request that asked for it - even though every
// request shares one ProxyFunc, one ReverseProxy and one client.
func TestForward_SameTarget_ConcurrentRequests_ResponsesIsolated(t *testing.T) {
	t.Parallel()

	const (
		iterations  = 1200
		maxInFlight = 32
	)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("echo:" + r.Header.Get("X-Correlation-ID")))
	}))
	t.Cleanup(backend.Close)

	gw := buildGateway(t, hook.NewRegistry(), routeSpec{
		path: "/echo", methods: []string{"GET"}, target: backend.URL,
	})

	runConcurrently(iterations, maxInFlight, func(i int) {
		correlationID := fmt.Sprintf("req-%d", i)

		rc := newTestContext("GET", "/echo")
		rc.Request.Header.Set("X-Correlation-ID", correlationID)

		gw.Forward(context.Background(), rc)

		if code := rc.Response.StatusCode(); code != http.StatusOK {
			t.Errorf("request %s: transport failure (status %d): %v", correlationID, code, rc.Errors)

			return
		}

		want := "echo:" + correlationID
		if got := string(rc.Response.Body()); got != want {
			t.Errorf("request %s: expected body %q, got %q (cross-talk between requests)", correlationID, want, got)
		}
	})
}

// TestForward_ProxyError_DoesNotTaintNextRequest_SameContext is the
// context-bleed regression test.
//
// The gateway signals "the backend could not be reached" by stashing the
// underlying error on the RequestContext under proxyErrorKey, then reading it
// back. In production Hertz recycles RequestContexts through a sync.Pool, so
// the same context object serves many unrelated requests in sequence. If that
// key ever survived a request - or if a later request read a stale value -
// a perfectly healthy request would be turned into a spurious 502.
//
// This test reproduces the pooling behaviour explicitly: it drives a single
// RequestContext through a failing request followed by healthy ones, calling
// Reset() between them exactly as Hertz does before returning a context to
// the pool, and requires the healthy requests to stay healthy.
func TestForward_ProxyError_DoesNotTaintNextRequest_SameContext(t *testing.T) {
	t.Parallel()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("healthy"))
	}))
	t.Cleanup(healthy.Close)

	// A target that nothing is listening on: connections are refused
	// immediately, so this exercises the proxy error path deterministically.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	gw := buildGateway(
		t, hook.NewRegistry(),
		routeSpec{path: "/healthy", methods: []string{"GET"}, target: healthy.URL},
		routeSpec{path: "/dead", methods: []string{"GET"}, target: deadURL},
	)

	rc := newTestContext("GET", "/dead")

	// 1. A request that must fail: the backend is unreachable.
	gw.Forward(context.Background(), rc)

	if got := rc.Response.StatusCode(); got != http.StatusBadGateway {
		t.Fatalf("expected the unreachable backend to produce 502, got %d", got)
	}
	if _, exists := rc.Get(proxyErrorKey); !exists {
		t.Fatalf("expected the proxy error to be recorded under %q", proxyErrorKey)
	}

	// 2. Recycle the very same context, as Hertz's pool does, and drive a
	//    healthy request through it. It must not inherit the failure above.
	for i := range 5 {
		rc.Reset()

		// Repopulate the request on the recycled context exactly the way both
		// Hertz's server and the `ut` helper do.
		protocol.NewRequest("GET", "/healthy", nil).CopyTo(&rc.Request)

		gw.Forward(context.Background(), rc)

		if code := rc.Response.StatusCode(); code != http.StatusOK {
			t.Errorf(
				"reuse %d: healthy request returned %d after a context that previously carried a proxy error (context bleed)",
				i, code,
			)
		}
		if got := string(rc.Response.Body()); got != "healthy" {
			t.Errorf("reuse %d: expected body %q, got %q", i, "healthy", got)
		}
	}
}

// TestForward_ConcurrentMixOfFailingAndHealthyTargets proves a broken route
// cannot poison traffic on healthy routes. Requests to an unreachable backend
// and requests to a healthy one run at the same time through the same Gateway;
// every healthy request must still succeed.
func TestForward_ConcurrentMixOfFailingAndHealthyTargets(t *testing.T) {
	t.Parallel()

	const (
		iterations  = 1200
		maxInFlight = 32
	)

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("healthy-payload"))
	}))
	t.Cleanup(healthy.Close)

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	gw := buildGateway(
		t, hook.NewRegistry(),
		routeSpec{path: "/healthy", methods: []string{"GET"}, target: healthy.URL},
		routeSpec{path: "/dead", methods: []string{"GET"}, target: deadURL},
	)

	runConcurrently(iterations, maxInFlight, func(i int) {
		if i%2 == 0 {
			rc := newTestContext("GET", "/healthy")
			gw.Forward(context.Background(), rc)

			if code := rc.Response.StatusCode(); code != http.StatusOK {
				t.Errorf("healthy request returned %d while other requests targeted a dead backend", code)

				return
			}
			if got := string(rc.Response.Body()); got != "healthy-payload" {
				t.Errorf("healthy request got body %q", got)
			}

			return
		}

		rc := newTestContext("GET", "/dead")
		gw.Forward(context.Background(), rc)

		if code := rc.Response.StatusCode(); code != http.StatusBadGateway {
			t.Errorf("request to the dead backend returned %d, expected 502", code)
		}
	})
}

// TestForward_HookSeesOnlyItsOwnRequestState verifies that data a hook
// stores on the per-request context never leaks across concurrent requests.
// Hooks are built once at startup and shared by every request to their route,
// so anything request-scoped must live on the context, never on the hook.
func TestForward_HookSeesOnlyItsOwnRequestState(t *testing.T) {
	t.Parallel()

	const (
		iterations  = 1200
		maxInFlight = 32
	)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	const stateKey = "test_shared_state"

	stashingHook := &recordingHook{
		name: "stash_and_verify", stage: hook.PreRequest,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			// Whatever a previous request left behind must not be visible.
			if leaked, exists := rc.Get(stateKey); exists {
				return fmt.Errorf("found state %v left over from another request", leaked)
			}

			rc.Set(stateKey, rc.Request.Header.Get("X-Request-ID"))

			return nil
		},
	}
	verifyingHook := &recordingHook{
		name: "verify_still_mine", stage: hook.PreRequest,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			stashed, exists := rc.Get(stateKey)
			if !exists {
				return fmt.Errorf("state set by the previous hook in this same request is missing")
			}

			want := rc.Request.Header.Get("X-Request-ID")
			if stashed != want {
				return fmt.Errorf("state belongs to another request: got %v, want %q", stashed, want)
			}

			return nil
		},
	}

	registry := hook.NewRegistry()
	registerHook(t, registry, stashingHook)
	registerHook(t, registry, verifyingHook)

	gw := buildGateway(t, registry, routeSpec{
		path: "/state", methods: []string{"GET"}, target: backend.URL,
		hooks: config.RouteHooks{
			PreRequest: config.HookRefList{
				{Name: stashingHook.name},
				{Name: verifyingHook.name},
			},
		},
	})

	runConcurrently(iterations, maxInFlight, func(i int) {
		rc := newTestContext("GET", "/state")
		rc.Request.Header.Set("X-Request-ID", fmt.Sprintf("req-%d", i))

		gw.Forward(context.Background(), rc)

		if code := rc.Response.StatusCode(); code != http.StatusOK {
			t.Errorf("request %d returned %d: %v", i, code, rc.Errors)
		}
	})
}

// TestGetOrCreateProxy_ConcurrentLoad_OneEntryPerTarget verifies the proxy
// cache converges on exactly one entry per distinct target under concurrent
// first access - no duplicate builds, no missing entries, no entries keyed by
// the wrong target.
func TestGetOrCreateProxy_ConcurrentLoad_OneEntryPerTarget(t *testing.T) {
	t.Parallel()

	const (
		targetCount = 5
		goroutines  = 400
		maxInFlight = 32
	)

	urls := make([]string, targetCount)
	specs := make([]routeSpec, targetCount)

	for i := range targetCount {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		t.Cleanup(backend.Close)

		urls[i] = backend.URL
		specs[i] = routeSpec{
			path:    fmt.Sprintf("/t-%d", i),
			methods: []string{"GET"},
			target:  backend.URL,
		}
	}

	gw := buildGateway(t, hook.NewRegistry(), specs...)

	errCh := make(chan error, goroutines)

	mustRun(t, 15*time.Second, func() {
		runConcurrently(goroutines, maxInFlight, func(i int) {
			if _, err := gw.getOrCreateProxy(urls[i%targetCount]); err != nil {
				errCh <- fmt.Errorf("building proxy for %s: %w", urls[i%targetCount], err)
			}
		})
	})

	close(errCh)

	for err := range errCh {
		t.Error(err)
	}

	gw.proxyMu.RLock()
	cached := len(gw.proxies)
	gw.proxyMu.RUnlock()

	if cached != targetCount {
		t.Errorf("expected exactly %d cached proxies (one per target), got %d", targetCount, cached)
	}
}
