package server

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/internal/routing"
)

type recordingHook struct {
	name    string
	stage   hook.Stage
	execute func(context.Context, *app.RequestContext) error
}

func (h *recordingHook) Name() string      { return h.name }
func (h *recordingHook) Stage() hook.Stage { return h.stage }
func (h *recordingHook) Execute(c context.Context, rc *app.RequestContext) error {
	return h.execute(c, rc)
}

func newTestClient(t *testing.T) *client.Client {
	t.Helper()

	cli, err := client.NewClient()
	if err != nil {
		t.Fatalf("creating hertz client: %v", err)
	}

	return cli
}

func newTestContext(method, path string) *app.RequestContext {
	return ut.CreateUtRequestContext(method, path, &ut.Body{})
}

func registerHook(t *testing.T, registry hook.Registry, h *recordingHook) {
	t.Helper()

	if err := registry.Register(h.name, func(config map[string]any) (hook.Hook, error) {
		return h, nil
	}); err != nil {
		t.Fatalf("registering hook %q: %v", h.name, err)
	}
}

// routeSpec is a minimal description of one route used to build a
// *routing.Table (and, from it, a *Gateway) without repeating the same
// config/registry boilerplate in every test.
type routeSpec struct {
	path    string
	methods []string
	target  string
	hooks   config.RouteHooks
}

// buildGateway builds a fully wired *Gateway (table + hooks resolved +
// a real client) from a set of route specs. Each spec becomes its own
// service so routes never accidentally collide.
func buildGateway(t *testing.T, registry hook.Registry, specs ...routeSpec) *Gateway {
	t.Helper()

	services := map[string]config.Service{}
	for i, spec := range specs {
		services[fmt.Sprintf("svc-%d", i)] = config.Service{
			Target: spec.target,
			Routes: []config.Route{
				{Path: spec.path, Methods: spec.methods, Hooks: spec.hooks},
			},
		}
	}

	cfg := &config.Config{Port: 8000, Services: services}
	table, err := routing.Build(cfg, registry)
	if err != nil {
		t.Fatalf("building routing table: %v", err)
	}

	return NewGateway(table, newTestClient(t))
}

// mustRun executes fn and fails the test if it does not complete within
// timeout. This is essential for any test that could, in the presence of
// a regression, deadlock forever — without a watchdog like this, a reintroduced deadlock would
// hang `go test` indefinitely instead of failing loudly.
func mustRun(t *testing.T, timeout time.Duration, fn func()) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("test did not complete within %s - likely deadlock", timeout)
	}
}

// runConcurrently invokes fn(i) for every i in [0, n) with at most
// maxInFlight calls running at any instant.
//
// The bound is not a performance tweak, it is what makes concurrency tests
// meaningful. Backends in these tests are httptest servers, whose listeners
// have a finite accept backlog. Firing thousands of simultaneous connection
// attempts at one makes the OS start refusing connections, and the gateway
// then correctly answers 502 for reasons that have nothing to do with the
// property under test. Bounding in-flight requests keeps every failure a real
// one, so a red test always means a genuine bug.
func runConcurrently(n, maxInFlight int, fn func(i int)) {
	sem := make(chan struct{}, maxInFlight)

	var wg sync.WaitGroup

	for i := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			fn(i)
		}()
	}

	wg.Wait()
}
