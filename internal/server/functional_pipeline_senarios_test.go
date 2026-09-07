package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
)

func TestForward_Success_NoHooks(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	}))
	defer backend.Close()

	gw := buildGateway(t, hook.NewRegistry(), routeSpec{
		path: "/ping", methods: []string{"GET"}, target: backend.URL, //nolint
	})

	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc)

	if got := rc.Response.StatusCode(); got != http.StatusOK {
		t.Errorf("expected status 200, got %d", got)
	}
	if got := string(rc.Response.Body()); got != "pong" {
		t.Errorf("expected body %q, got %q", "pong", got)
	}
}

func TestForward_UnmatchedRoute_Returns404(t *testing.T) {
	t.Parallel()

	gw := buildGateway(t, hook.NewRegistry(), routeSpec{
		path:    "/ping",
		methods: []string{"GET"},
		target:  "http://unused.invalid",
	})

	rc := newTestContext("GET", "/does-not-exists")
	gw.Forward(context.Background(), rc)

	if got := rc.Response.StatusCode(); got != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", got)
	}
}

func TestForward_MalformedTarget_Returns502(t *testing.T) {
	t.Parallel()

	gw := buildGateway(t, hook.NewRegistry(), routeSpec{
		path: "/broken", methods: []string{"GET"}, target: "mallformed",
	})

	rc := newTestContext("GET", "/broken")
	gw.Forward(context.Background(), rc)

	if got := rc.Response.StatusCode(); got != http.StatusBadGateway {
		t.Errorf("expected status 502 for a malformed target, got %d", got)
	}
}

func TestForward_UnreachableBackend_NoOnErrorHook_StillReturnsFailureStatus(t *testing.T) {
	t.Parallel()

	const unreachableTarget = "http://127.0.0.1:1"

	gw := buildGateway(t, hook.NewRegistry(), routeSpec{
		path: "/ping", methods: []string{"GET"}, target: unreachableTarget,
	})

	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc)

	if got := rc.Response.StatusCode(); got != http.StatusBadGateway {
		t.Errorf("expected status 502 (forced fallback, no OnError hook configured), got %d", got)
	}
	if len(rc.Errors) == 0 {
		t.Error("expected the proxy failure to be recorded via rc.Error(), but rc.Errors is empty")
	}
}

func TestForward_PreRequestAbort_RunOnErrorAndSkipsProxy(t *testing.T) {
	t.Parallel()

	var backendHit atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit.Store(true)
	}))
	defer backend.Close()

	failingHook := &recordingHook{
		name: "always_fails", stage: hook.PreRequest,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			return errors.New("boom: simulated internal failure")
		},
	}

	var onErrorRan atomic.Bool
	onErrorHook := &recordingHook{
		name: "fallback_response", stage: hook.OnError,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			onErrorRan.Store(true)
			rc.AbortWithStatus(http.StatusInternalServerError)

			return nil
		},
	}

	registry := hook.NewRegistry()
	registerHook(t, registry, failingHook)
	registerHook(t, registry, onErrorHook)

	gw := buildGateway(t, registry, routeSpec{
		path: "/ping", methods: []string{"GET"}, target: backend.URL,
		hooks: config.RouteHooks{
			PreRequest: config.HookRefList{{Name: failingHook.name}},
			OnError:    config.HookRefList{{Name: onErrorHook.name}},
		},
	})

	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc)

	if backendHit.Load() {
		t.Error("expected backend to never be called after a PreRequest hook error, but it was")
	}
	if !onErrorRan.Load() {
		t.Error("expected the OnError hook to run after a PreRequest failure, but it did not")
	}
	if len(rc.Errors) == 0 {
		t.Error("expected the original hook error to be recorded via rc.Error(), but rc.Errors is empty")
	}
	if got := rc.Response.StatusCode(); got != http.StatusInternalServerError {
		t.Errorf("expected status 500 from the OnError fallback, got %d", got)
	}
}

func TestForward_PreRequestAbort_SkipsProxyAndPostResponse(t *testing.T) {
	t.Parallel()

	var backendHit atomic.Bool

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendHit.Store(true)

		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	var postResponseRan atomic.Bool
	postHook := &recordingHook{
		name: "post_should_not_run", stage: hook.PostResponse,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			postResponseRan.Store(true)

			return nil
		},
	}
	authHook := &recordingHook{
		name: "fake_auth_reject", stage: hook.PreRequest,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			rc.AbortWithStatus(http.StatusUnauthorized)

			return nil
		},
	}

	registry := hook.NewRegistry()
	registerHook(t, registry, authHook)
	registerHook(t, registry, postHook)

	gw := buildGateway(t, registry, routeSpec{
		path: "/ping", methods: []string{"GET"}, target: backend.URL,
		hooks: config.RouteHooks{
			PreRequest:   config.HookRefList{{Name: authHook.name}},
			PostResponse: config.HookRefList{{Name: postHook.name}},
		},
	})

	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc)

	if backendHit.Load() {
		t.Error("expected backend to never be called after PreRequest abort, but it was")
	}
	if postResponseRan.Load() {
		t.Error("expected PostResponse hooks to be skipped after an abort, but one ran")
	}
	if got := rc.Response.StatusCode(); got != http.StatusUnauthorized {
		t.Errorf("expected status 401 from the aborting hook, got %d", got)
	}
}

func TestForward_PostResponseError_RunsOnErrorAfterSuccessfulProxy(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("original-backend-body"))
	}))
	defer backend.Close()

	failingPostHook := &recordingHook{
		name: "post_fails", stage: hook.PostResponse,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			return errors.New("boom: simulate post-processing failure")
		},
	}

	var onErrorRan atomic.Bool
	onErrorHook := &recordingHook{
		name: "fallback_after_post_failure", stage: hook.OnError,
		execute: func(ctx context.Context, rc *app.RequestContext) error {
			onErrorRan.Store(true)
			rc.AbortWithStatus(http.StatusBadGateway)

			return nil
		},
	}

	registry := hook.NewRegistry()
	registerHook(t, registry, failingPostHook)
	registerHook(t, registry, onErrorHook)

	gw := buildGateway(t, registry, routeSpec{
		path: "/ping", methods: []string{"GET"}, target: backend.URL,
		hooks: config.RouteHooks{
			PostResponse: config.HookRefList{{Name: failingPostHook.name}},
			OnError:      config.HookRefList{{Name: onErrorHook.name}},
		},
	})

	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc)

	if !onErrorRan.Load() {
		t.Error("expected OnError to run after a PostResponse hook failure, but it did not")
	}
	if got := rc.Response.StatusCode(); got != http.StatusBadGateway {
		t.Errorf("expected status 502 from OnError fallback, got %d", got)
	}
}

func TestForward_HooksRunInConfiguredOrder(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	var mu sync.Mutex
	var executionOrder []string
	record := func(name string) {
		mu.Lock()
		defer mu.Unlock()

		executionOrder = append(executionOrder, name)
	}

	makeOrderedHook := func(name string) *recordingHook {
		return &recordingHook{
			name: name, stage: hook.PreRequest,
			execute: func(ctx context.Context, rc *app.RequestContext) error {
				record(name)

				return nil
			},
		}
	}

	registry := hook.NewRegistry()
	for _, name := range []string{"first", "second", "third"} { //nolint
		registerHook(t, registry, makeOrderedHook(name))
	}

	gw := buildGateway(t, registry, routeSpec{
		path: "/ping", methods: []string{"GET"}, target: backend.URL,
		hooks: config.RouteHooks{
			PreRequest: config.HookRefList{{Name: "first"}, {Name: "second"}, {Name: "third"}},
		},
	})

	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc)

	want := []string{"first", "second", "third"}
	if len(executionOrder) != len(want) {
		t.Fatalf("expected %d hooks to run, got %d (%v)", len(want), len(executionOrder), executionOrder)
	}
	for i, name := range want {
		if executionOrder[i] != name {
			t.Errorf("position %d: expected hook %q, got %q (full order: %v)", i, name, executionOrder[i], executionOrder)
		}
	}
}

func TestForward_AbortMidStage_SkipsRemainingHooksInSameStage(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	var secondHookRan atomic.Bool
	abortingFirst := &recordingHook{
		name: "aborts_immediately", stage: hook.PreRequest,
		execute: func(c context.Context, rc *app.RequestContext) error {
			rc.AbortWithStatus(http.StatusForbidden)

			return nil
		},
	}
	shouldNotRunSecond := &recordingHook{
		name: "should_not_run", stage: hook.PreRequest,
		execute: func(c context.Context, rc *app.RequestContext) error {
			secondHookRan.Store(true)

			return nil
		},
	}

	registry := hook.NewRegistry()
	registerHook(t, registry, abortingFirst)
	registerHook(t, registry, shouldNotRunSecond)

	gw := buildGateway(t, registry, routeSpec{
		path: "/ping", methods: []string{"GET"}, target: backend.URL,
		hooks: config.RouteHooks{
			PreRequest: config.HookRefList{
				{Name: "aborts_immediately"}, {Name: "should_not_run"},
			},
		},
	})

	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc)

	if secondHookRan.Load() {
		t.Error("expected the second PreRequest hook to be skipped after an abort, but it ran")
	}
	if got := rc.Response.StatusCode(); got != http.StatusForbidden {
		t.Errorf("expected status 403 set by the aborting hook, got %d", got)
	}
}

func TestForward_QueryStringIsForwardedToBackend(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex

	var receivedQuery string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedQuery = r.URL.RawQuery
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	gw := buildGateway(t, hook.NewRegistry(), routeSpec{
		path: "/search", methods: []string{"GET"}, target: backend.URL,
	})

	rc := newTestContext("GET", "/search?q=hello&page=2")
	gw.Forward(context.Background(), rc)

	mu.Lock()
	if receivedQuery != "q=hello&page=2" {
		t.Errorf("expected backend to receive query string %q, got %q", "q=hello&page=2", receivedQuery)
	}
	mu.Unlock()
}
