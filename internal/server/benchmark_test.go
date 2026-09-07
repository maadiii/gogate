package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/internal/routing"
)

// BenchmarkForward_CachedTarget_NoHooks measures steady-state throughput
// once the target's proxy is already cached (the realistic hot path for
// almost every request after the first one to a given route) and no
// hooks are configured.
func BenchmarkForward_CachedTarget_NoHooks(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cli, err := client.NewClient()
	if err != nil {
		b.Fatalf("creating hertz client: %v", err)
	}

	cfg := &config.Config{
		Port: 8000,
		Services: map[string]config.Service{
			"svc": {
				Target: backend.URL,
				Routes: []config.Route{{Path: "/ping", Methods: []string{"GET"}}}, //nolint
			},
		},
	}
	table, err := routing.Build(cfg, hook.NewRegistry())
	if err != nil {
		b.Fatalf("building routing table: %v", err)
	}
	gw := NewGateway(table, cli)

	// Warm the proxy cache before timing starts, so the benchmark
	// measures the steady-state path, not the one-time build cost.
	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rc := newTestContext("GET", "/ping")
			gw.Forward(context.Background(), rc)
		}
	})
}

// BenchmarkForward_WithThreeHooks measures the added overhead of running
// three no-op PreRequest hooks per request, isolating the cost of the
// hook-chain machinery (including the panic-recovery defer) from the
// proxy call itself.
func BenchmarkForward_WithThreeHooks(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cli, err := client.NewClient()
	if err != nil {
		b.Fatalf("creating hertz client: %v", err)
	}

	noop := func(c context.Context, rc *app.RequestContext) error { return nil }
	registry := hook.NewRegistry()
	for _, name := range []string{"h1", "h2", "h3"} {
		n := name
		_ = registry.Register(name, func(cfg map[string]any) (hook.Hook, error) {
			return &recordingHook{name: n, stage: hook.PreRequest, execute: noop}, nil
		})
	}

	cfg := &config.Config{
		Port: 8000,
		Services: map[string]config.Service{
			"svc": {
				Target: backend.URL,
				Routes: []config.Route{{
					Path: "/ping", Methods: []string{"GET"},
					Hooks: config.RouteHooks{
						PreRequest: config.HookRefList{{Name: "h1"}, {Name: "h2"}, {Name: "h3"}},
					},
				}},
			},
		},
	}
	table, err := routing.Build(cfg, registry)
	if err != nil {
		b.Fatalf("building routing table: %v", err)
	}
	gw := NewGateway(table, cli)

	rc := newTestContext("GET", "/ping")
	gw.Forward(context.Background(), rc) // warm the proxy cache

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rc := newTestContext("GET", "/ping")
			gw.Forward(context.Background(), rc)
		}
	})
}

// BenchmarkGetOrCreateProxy_CacheHit isolates the cost of the cache

// lookup fast path itself (RLock/read/RUnlock) under real concurrent
// load, to see how much RWMutex contention costs once a target is
// already cached — this is the path every single request takes after
// the very first one to any given target.
func BenchmarkGetOrCreateProxy_CacheHit(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cli, err := client.NewClient()
	if err != nil {
		b.Fatalf("creating hertz client: %v", err)
	}
	gw := NewGateway(nil, cli)

	// Pre-warm the cache for this target.
	if _, err := gw.getOrCreateProxy(backend.URL); err != nil {
		b.Fatalf("warming proxy cache: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := gw.getOrCreateProxy(backend.URL); err != nil {
				b.Fatalf("unexpected error: %v", err)
			}
		}
	})
}
