package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
