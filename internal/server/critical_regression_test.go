package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
)

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
	if got := rc.Response.StatusCode(); got != http.StatusInternalServerError {
		t.Errorf("expected status 500 from the OnError fallback after a panic, got %d", got)
	}
}

func TestForward_ConcurrentRequestsToDifferentTargets_NoCrossTalkNoRace(t *testing.T) {
	t.Parallel()

	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("response-from-A"))
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("response-from-B"))
	}))
	defer backendB.Close()

	gw := buildGateway(
		t, hook.NewRegistry(),
		routeSpec{path: "/a", methods: []string{"GET"}, target: backendA.URL},
		routeSpec{path: "/b", methods: []string{"GET"}, target: backendB.URL},
	)

	const iterations = 3000
	mustRun(t, 15*time.Second, func() {
		var wg sync.WaitGroup
		errCh := make(chan error, iterations)

		for i := range iterations {
			wg.Go(func() {
				var path, expected string
				if i%2 == 0 {
					path, expected = "/a", "response-from-A"
				} else {
					path, expected = "/b", "response-from-B"
				}

				rc := ut.CreateUtRequestContext("GET", path, &ut.Body{})
				gw.Forward(context.Background(), rc)

				got := string(rc.Response.Body())
				if got != expected {
					errCh <- fmt.Errorf(
						"iteration %d: requested %q, expected body %q, got %q (possible cross-talk)",
						i, path, expected, got,
					)
				}
			})
		}

		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Error(err)
		}
	})
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
