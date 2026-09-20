package routing_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/internal/routing"
)

type testHook struct {
	name  string
	stage hook.Stage
}

func (h testHook) Name() string      { return h.name }
func (h testHook) Stage() hook.Stage { return h.stage }
func (h testHook) Execute(c context.Context, rc *app.RequestContext) error {
	return nil
}

func registryWith(t *testing.T, hooks ...testHook) hook.Registry {
	t.Helper()

	r := hook.NewRegistry()
	for _, h := range hooks {
		if err := r.Register(h.name, func(map[string]any) (hook.Hook, error) {
			return h, nil
		}); err != nil {
			t.Fatalf("registering hook %q: %v", h.name, err)
		}
	}

	return r
}

// routeIn is a compact description of one route. Each route is placed in its
// own service, so the resolved Target identifies which route matched without
// any ambiguity.
type routeIn struct {
	path    string
	methods []string
	target  string
	hooks   config.RouteHooks
}

func buildTable(t *testing.T, registry hook.Registry, routes ...routeIn) *routing.Table {
	t.Helper()

	table, err := routing.Build(configFrom(routes...), registry)
	if err != nil {
		t.Fatalf("building routing table: %v", err)
	}

	return table
}

func configFrom(routes ...routeIn) *config.Config {
	services := make(map[string]config.Service, len(routes))

	for i, r := range routes {
		services[fmt.Sprintf("svc-%d", i)] = config.Service{
			Target: r.target,
			Routes: []config.Route{{Path: r.path, Methods: r.methods, Hooks: r.hooks}},
		}
	}

	return &config.Config{Port: 8000, Services: services}
}

func TestResolve_ExactMatch(t *testing.T) {
	t.Parallel()

	table := buildTable(t, hook.NewRegistry(), routeIn{
		path: "/api/users", methods: []string{"GET"}, target: "http://users.local",
	})

	resolved, ok := table.Resolve("GET", "/api/users")
	if !ok {
		t.Fatalf("expected GET /api/users to resolve")
	}
	if resolved.Target != "http://users.local" {
		t.Errorf("expected target %q, got %q", "http://users.local", resolved.Target)
	}
}

func TestResolve_UnmatchedPath(t *testing.T) {
	t.Parallel()

	table := buildTable(t, hook.NewRegistry(), routeIn{
		path: "/api/users", methods: []string{"GET"}, target: "http://users.local",
	})

	if _, ok := table.Resolve("GET", "/api/other"); ok {
		t.Error("expected /api/other not to resolve")
	}
}

func TestResolve_MethodMismatch(t *testing.T) {
	t.Parallel()

	table := buildTable(t, hook.NewRegistry(), routeIn{
		path: "/api/users", methods: []string{"GET"}, target: "http://users.local",
	})

	if _, ok := table.Resolve("POST", "/api/users"); ok {
		t.Error("expected POST on a GET-only route not to resolve")
	}
}

func TestResolve_EveryConfiguredMethodResolves(t *testing.T) {
	t.Parallel()

	table := buildTable(t, hook.NewRegistry(), routeIn{
		path: "/api/users", methods: []string{"GET", "POST", "DELETE"}, target: "http://users.local",
	})

	for _, method := range []string{"GET", "POST", "DELETE"} {
		if _, ok := table.Resolve(method, "/api/users"); !ok {
			t.Errorf("expected %s /api/users to resolve", method)
		}
	}

	if _, ok := table.Resolve("PUT", "/api/users"); ok {
		t.Error("expected PUT (not configured) not to resolve")
	}
}

func TestResolve_WildcardMatchesSubpaths(t *testing.T) {
	t.Parallel()

	table := buildTable(t, hook.NewRegistry(), routeIn{
		path: "/api/users/*", methods: []string{"GET"}, target: "http://users.local",
	})

	for _, path := range []string{"/api/users/42", "/api/users/42/posts", "/api/users/" } {
		resolved, ok := table.Resolve("GET", path)
		if !ok {
			t.Errorf("expected %s to resolve via the wildcard route", path)

			continue
		}
		if resolved.Target != "http://users.local" {
			t.Errorf("%s: expected target %q, got %q", path, "http://users.local", resolved.Target)
		}
	}
}

// A wildcard route is a prefix match, and the trailing "*" is not part of
// that prefix - so "/api/users/*" matches "/api/users/" but must not match
// the bare "/api/users".
func TestResolve_WildcardDoesNotMatchBarePrefix(t *testing.T) {
	t.Parallel()

	table := buildTable(t, hook.NewRegistry(), routeIn{
		path: "/api/users/*", methods: []string{"GET"}, target: "http://users.local",
	})

	if _, ok := table.Resolve("GET", "/api/users"); ok {
		t.Error("expected /api/users NOT to match the wildcard route /api/users/*")
	}
}

func TestResolve_ExactWinsOverWildcard(t *testing.T) {
	t.Parallel()

	table := buildTable(
		t, hook.NewRegistry(),
		routeIn{path: "/api/users/special", methods: []string{"GET"}, target: "http://exact.local"},
		routeIn{path: "/api/users/*", methods: []string{"GET"}, target: "http://wild.local"},
	)

	resolved, ok := table.Resolve("GET", "/api/users/special")
	if !ok {
		t.Fatalf("expected /api/users/special to resolve")
	}
	if resolved.Target != "http://exact.local" {
		t.Errorf("expected the exact route to win, got target %q", resolved.Target)
	}
}

func TestResolve_LongestWildcardPrefixWins(t *testing.T) {
	t.Parallel()

	table := buildTable(
		t, hook.NewRegistry(),
		routeIn{path: "/api/*", methods: []string{"GET"}, target: "http://api.local"},
		routeIn{path: "/api/users/*", methods: []string{"GET"}, target: "http://users.local"},
		routeIn{path: "/api/users/admin/*", methods: []string{"GET"}, target: "http://admin.local"},
	)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/api/health", "http://api.local"},
		{"/api/users/42", "http://users.local"},
		{"/api/users/admin/settings", "http://admin.local"},
	} {
		resolved, ok := table.Resolve("GET", tc.path)
		if !ok {
			t.Errorf("expected %s to resolve", tc.path)

			continue
		}
		if resolved.Target != tc.want {
			t.Errorf("%s: expected the longest matching prefix %q, got %q", tc.path, tc.want, resolved.Target)
		}
	}
}

// Wildcard routes are indexed per method, exactly like exact routes.
func TestResolve_WildcardRespectsMethod(t *testing.T) {
	t.Parallel()

	table := buildTable(t, hook.NewRegistry(), routeIn{
		path: "/api/users/*", methods: []string{"GET"}, target: "http://users.local",
	})

	if _, ok := table.Resolve("POST", "/api/users/42"); ok {
		t.Error("expected POST not to match a GET-only wildcard route")
	}
}

func TestResolve_CarriesServiceName(t *testing.T) {
	t.Parallel()

	table := buildTable(t, hook.NewRegistry(), routeIn{
		path: "/api/users", methods: []string{"GET"}, target: "http://users.local",
	})

	resolved, ok := table.Resolve("GET", "/api/users")
	if !ok {
		t.Fatalf("expected /api/users to resolve")
	}
	if resolved.ServiceName != "svc-0" {
		t.Errorf("expected service name %q, got %q", "svc-0", resolved.ServiceName)
	}
}

func TestBuild_UnknownHookNameFails(t *testing.T) {
	t.Parallel()

	_, err := routing.Build(configFrom(routeIn{
		path:    "/api/users",
		methods: []string{"GET"},
		target:  "http://users.local",
		hooks:   config.RouteHooks{PreRequest: config.HookRefList{{Name: "not_registered"}}},
	}), hook.NewRegistry())

	if err == nil {
		t.Fatal("expected an unregistered hook name to fail startup, got nil")
	}

	// The error must name the offending hook and its location, per the
	// project's config-error standard.
	for _, want := range []string{"not_registered", "svc-0", "pre_request"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}
}

func TestBuild_HookStageMismatchFails(t *testing.T) {
	t.Parallel()

	registry := registryWith(t, testHook{name: "wrong_stage", stage: hook.PostResponse})

	_, err := routing.Build(configFrom(routeIn{
		path:    "/api/users",
		methods: []string{"GET"},
		target:  "http://users.local",
		hooks:   config.RouteHooks{PreRequest: config.HookRefList{{Name: "wrong_stage"}}},
	}), registry)

	if err == nil {
		t.Fatal("expected a hook configured under the wrong stage to fail startup, got nil")
	}
	if !strings.Contains(err.Error(), "wrong_stage") {
		t.Errorf("expected the error to name the offending hook, got: %v", err)
	}
}

func TestBuild_PreservesConfiguredHookOrder(t *testing.T) {
	t.Parallel()

	registry := registryWith(
		t,
		testHook{name: "first", stage: hook.PreRequest},
		testHook{name: "second", stage: hook.PreRequest},
		testHook{name: "third", stage: hook.PreRequest},
		testHook{name: "after", stage: hook.PostResponse},
	)

	table := buildTable(t, registry, routeIn{
		path:    "/api/users",
		methods: []string{"GET"},
		target:  "http://users.local",
		hooks: config.RouteHooks{
			PreRequest: config.HookRefList{
				{Name: "second"}, {Name: "first"}, {Name: "third"},
			},
			PostResponse: config.HookRefList{{Name: "after"}},
		},
	})

	resolved, ok := table.Resolve("GET", "/api/users")
	if !ok {
		t.Fatalf("expected /api/users to resolve")
	}

	// Execution order is the order the hooks appear in the config, not the
	// order they were registered in the Registry.
	var got []string
	for _, h := range resolved.PreRequestHooks {
		got = append(got, h.Name())
	}

	want := []string{"second", "first", "third"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("expected pre-request hook order %v, got %v", want, got)
	}

	if len(resolved.PostResponseHooks) != 1 || resolved.PostResponseHooks[0].Name() != "after" {
		t.Errorf("expected the post-response hook to be in the post-response stage")
	}
}

func TestBuild_RouteWithNoHooksHasEmptyStages(t *testing.T) {
	t.Parallel()

	table := buildTable(t, hook.NewRegistry(), routeIn{
		path: "/health", methods: []string{"GET"}, target: "http://health.local",
	})

	resolved, ok := table.Resolve("GET", "/health")
	if !ok {
		t.Fatalf("expected /health to resolve")
	}

	if len(resolved.PreRequestHooks) != 0 ||
		len(resolved.PostResponseHooks) != 0 ||
		len(resolved.OnErrorHooks) != 0 {
		t.Error("expected all hook stages to be empty for a route with no hooks configured")
	}
}
