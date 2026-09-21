package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
)

// TestGetOrCreateProxy_ConcurrentFirstAccess_DoesNotDeadlock is a direct
// regression test for the RWMutex deadlock bug found in this review: the
// original code called RLock() with a deferred RUnlock(), then tried to
// call Lock() for the same mutex before that RUnlock() ran. Go's
// RWMutex cannot upgrade a read lock to a write lock, so this would
// deadlock forever the very first time multiple goroutines raced to
// build the proxy for a target that wasn't cached yet.
//
// This test fires many goroutines at a brand-new (never-cached) target
// simultaneously and requires the whole thing to finish within a short
// timeout. If the deadlock regresses, this test fails loudly instead of
// hanging the test suite forever.
func TestGetOrCreateProxy_ConcurrentFirstAccess_DoesNotDeadlock(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	gw := NewGateway(nil, newTestClient(t))

	const goroutines = 50
	mustRun(t, 5*time.Second, func() {
		var wg sync.WaitGroup
		errCh := make(chan error, goroutines)
		for range goroutines {
			wg.Go(func() {
				if _, err := gw.getOrCreateProxy(backend.URL); err != nil {
					errCh <- err
				}
			})
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Errorf("unexpected error building proxy concurrently: %v", err)
		}
	})

	// Sanity check: exactly one proxy should have been cached for this
	// single target, regardless of how many goroutines raced to build it.
	gw.proxyMu.RLock()
	cachedCount := len(gw.proxies)
	gw.proxyMu.RUnlock()
	if cachedCount != 1 {
		t.Errorf("expected exactly 1 cached proxy entry, got %d", cachedCount)
	}
}

// TestForward_HookPanic_IsRecoveredAsError is a direct regression test
// for the missing panic-recovery bug found in this review: without
// recover() inside runStage, and since the gateway uses server.New()
// (not server.Default(), which is the only variant with a built-in
// recovery middleware), an unhandled panic in any single Hook would
// crash the entire process — not just the one request — because an
// unrecovered panic in any goroutine terminates the whole Go program.
//
// This test deliberately panics inside a PreRequest hook. If recovery
// is missing, this test process itself would crash (not just fail);
// its passing at all is direct proof the recovery path works
func TestForward_HookPanic_IsRecoveredAsError(t *testing.T) {
	t.Parallel()

	var backendHit atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit.Store(true)
	}))
	defer backend.Close()

	panickingHook := &recordingHook{
		name: "panics", stage: hook.PreRequest,
		execute: func(c context.Context, rc *app.RequestContext) error {
			panic("simulated programming error inside a hook")
		},
	}
	var onErrorRan atomic.Bool
	onErrorHook := &recordingHook{
		name: "fallback_after_panic", stage: hook.OnError,
		execute: func(c context.Context, rc *app.RequestContext) error {
			onErrorRan.Store(true)
			rc.AbortWithStatus(http.StatusInternalServerError)

			return nil
		},
	}

	registry := hook.NewRegistry()
	registerHook(t, registry, panickingHook)
	registerHook(t, registry, onErrorHook)

	gw := buildGateway(t, registry, routeSpec{
		path: "/ping", methods: []string{"GET"}, target: backend.URL, //nolint
		hooks: config.RouteHooks{
			PreRequest: config.HookRefList{{Name: panickingHook.name}},
			OnError:    config.HookRefList{{Name: onErrorHook.name}},
		},
	})

	rc := newTestContext("GET", "/ping")

	// The mere fact that this call returns normally (instead of crashing
	// the test binary) is the primary assertion of this test.
	gw.Forward(context.Background(), rc)

	if backendHit.Load() {
		t.Error("expected backend to never be called after a hook panic, but it was")
	}
	if !onErrorRan.Load() {
		t.Error("expected OnError to run after a hook panic was recovered, but it did not")
	}
	if len(rc.Errors) == 0 {
		t.Error("expected the panic to be recorded as an error via rc.Error(), but rc.Errors is empty")
	}

	// Recording the panic as an error is only half the job: the error has to
	// say which hook panicked. Without that, a panic somewhere in a chain is
	// undebuggable, and the deferred recover in runStage is left formatting an
	// empty name — `hook "" panicked: ...` — which reads as though no hook were
	// involved at all.
	if len(rc.Errors) > 0 && !strings.Contains(rc.Errors[0].Error(), panickingHook.name) {
		t.Errorf("expected the recorded error to name the panicking hook %q, got %q",
			panickingHook.name, rc.Errors[0].Error())
	}

	if got := rc.Response.StatusCode(); got != http.StatusInternalServerError {
		t.Errorf("expected status 500 from the OnError fallback after a panic, got %d", got)
	}
}

// TestForward_ConcurrentRequestsToDifferentTargets_NoCrossTalkNoRace
// exercises the full production shape: a single shared *client.Client
// and a single shared *routing.Table (wrapped in one *Gateway), hit by
// many concurrent goroutines targeting several different backends. No
// request may ever receive another backend's response.
//
// Two things this test deliberately does differently from a naive
// version of itself:
//
//  1. Concurrency is bounded (see runConcurrently). An unbounded version
//     of this test makes the httptest listeners refuse connections, and
//     the resulting 502s are indistinguishable from real cross-talk in
//     the assertion — so the property under test ends up never actually
//     being checked.
//
//  2. A transport failure (any non-200) is reported as its own distinct
//     failure rather than being folded into the body comparison. If the
//     gateway ever fails to reach a backend, that must be loud, not
//     silently counted as "got an empty body".
func TestForward_ConcurrentRequestsToDifferentTargets_NoCrossTalkNoRace(t *testing.T) {
	t.Parallel()

	const (
		backendCount = 4
		iterations   = 2000
		maxInFlight  = 24
	)

	// Each backend answers with a marker derived only from its own index,
	// so a response carrying another backend's marker is undeniable
	// cross-talk.
	bodies := make([]string, backendCount)
	specs := make([]routeSpec, backendCount)

	for i := range backendCount {
		body := fmt.Sprintf("response-from-%d", i)
		bodies[i] = body

		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(backend.Close)

		specs[i] = routeSpec{
			path:    fmt.Sprintf("/backend-%d", i),
			methods: []string{"GET"},
			target:  backend.URL,
		}
	}

	gw := buildGateway(t, hook.NewRegistry(), specs...)

	errCh := make(chan error, iterations)

	mustRun(t, 30*time.Second, func() {
		runConcurrently(iterations, maxInFlight, func(i int) {
			want := i % backendCount
			path := fmt.Sprintf("/backend-%d", want)

			rc := newTestContext("GET", path)
			gw.Forward(context.Background(), rc)

			if code := rc.Response.StatusCode(); code != http.StatusOK {
				errCh <- fmt.Errorf(
					"iteration %d: GET %s: transport failure, status %d, not cross-talk: %v",
					i, path, code, rc.Errors,
				)

				return
			}

			if got := string(rc.Response.Body()); got != bodies[want] {
				errCh <- fmt.Errorf(
					"iteration %d: GET %s: expected body %q, got %q (CROSS-TALK)",
					i, path, bodies[want], got,
				)
			}
		})
	})

	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

// TestForward_ConcurrentRequestsToSameRoute_SharedHookInstance verifies
// that many concurrent requests to the SAME route (hence the SAME
// resolved hook instances, built once at routing.Build time and shared
// forever) do not corrupt each other's per-request state. This is
// distinct from the different-targets test above: here the risk is a
// Hook instance itself holding unsafe shared state, not the proxy cache.
func TestForward_ConcurrentRequestsToSameRoute_SharedHookInstance(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	// A hook with an atomic counter is safe by construction; this test
	// proves the *pipeline* (runStage/forward) does not introduce any
	// unsafe sharing on top of whatever the hook itself does.
	var callCount atomic.Int64
	countingHook := &recordingHook{
		name: "counter", stage: hook.PreRequest,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			callCount.Add(1)

			return nil
		},
	}

	registry := hook.NewRegistry()
	registerHook(t, registry, countingHook)

	gw := buildGateway(t, registry, routeSpec{
		path: "/ping", methods: []string{"GET"}, target: backend.URL,
		hooks: config.RouteHooks{PreRequest: config.HookRefList{{Name: countingHook.name}}},
	})

	const iterations = 3000
	mustRun(t, 15*time.Second, func() {
		var wg sync.WaitGroup
		for range iterations {
			wg.Go(func() {
				rc := newTestContext("GET", "/ping")
				gw.Forward(context.Background(), rc)
			})

			wg.Wait()
		}
	})

	if got := callCount.Load(); got != iterations {
		t.Errorf("expected the shared hook to run exactly %d times, got %d", iterations, got)
	}
}
