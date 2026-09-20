package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maadiii/gogate/config"
)

func validYAML() string {
	return `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /api/v1/auth/me
        methods: [GET]
        hooks:
          pre_request:
            - name: jwt_auth
              config:
                mode: jwt
          post_response:
            - name: audit_log
      - path: /api/v1/users/*
        methods: [GET, POST]
        hooks:
          pre_request:
            - name: jwt_auth
            - name: quota_check
              config:
                limit: 1000
                window: 1h
`
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}

	return path
}

func TestLoad(t *testing.T) { //nolint
	t.Run("valid config", func(t *testing.T) {
		t.Parallel()

		path := writeTempConfig(t, validYAML())

		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}

		if cfg.Port != 8000 {
			t.Errorf("expected port 8000, got: %d", cfg.Port)
		}

		svc, ok := cfg.Services["users"]
		if !ok {
			t.Fatalf("expected service %q to exist", "users")
		}

		if svc.Target != "localhost:3000" {
			t.Errorf("expected target %q, got %q", "localhost:3000", svc.Target)
		}

		if len(svc.Routes) != 2 {
			t.Fatalf("expected 2 routes, got %d", len(svc.Routes))
		}
	})

	t.Run("file not found", func(t *testing.T) {
		t.Parallel()

		_, err := config.Load("/nonexistent/path/gateway.yaml")
		if err == nil {
			t.Fatal("expected error for nonexistent file, got nil")
		}
	})

	t.Run("missing port", func(t *testing.T) {
		t.Parallel()

		content := `
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /health
        methods: [GET]
`
		path := writeTempConfig(t, content)

		_, err := config.Load(path)
		if err == nil {
			t.Fatal("expected error for missing port, got nil")
		}
	})

	t.Run("missing target", func(t *testing.T) {
		t.Parallel()

		content := `
port: 8000
services:
  users:
    routes:
      - path: /health
        methods: [GET]
`
		path := writeTempConfig(t, content)

		_, err := config.Load(path)
		if err == nil {
			t.Fatal("expected error for missing target, got nil")
		}
	})

	t.Run("empty path", func(t *testing.T) {
		t.Parallel()

		content := `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: ""
        methods: [GET]
`
		path := writeTempConfig(t, content)

		_, err := config.Load(path)
		if err == nil {
			t.Fatal("expected error for empty path, got nil")
		}
	})

	t.Run("duplicate route across services", func(t *testing.T) {
		t.Parallel()

		content := `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /api/v1/shared
        methods: [GET]
  reports:
    target: "localhost:3001"
    routes:
      - path: /api/v1/shared
        methods: [GET]
`
		path := writeTempConfig(t, content)

		_, err := config.Load(path)
		if err == nil {
			t.Fatal("expected error for duplicate route across services, got nil")
		}
	})

	t.Run("duplicate hook name in same stage", func(t *testing.T) {
		t.Parallel()

		content := `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /health
        methods: [GET]
        hooks:
          pre_request:
            - name: jwt_auth
            - name: jwt_auth
`
		path := writeTempConfig(t, content)

		_, err := config.Load(path)
		if err == nil {
			t.Fatal("expected error for duplicate hook name in same stage, got nil")
		}
	})

	t.Run("empty hook name", func(t *testing.T) {
		t.Parallel()

		content := `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /health
        methods: [GET]
        hooks:
          pre_request:
            - name: ""
`
		path := writeTempConfig(t, content)

		_, err := config.Load(path)
		if err == nil {
			t.Fatal("expected error for empty hook name, got nil")
		}
	})

	t.Run("route with no hooks is valid", func(t *testing.T) {
		t.Parallel()

		content := `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /health
        methods: [GET]
`
		path := writeTempConfig(t, content)

		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("expected no error for route without hooks, got: %v", err)
		}
		route := cfg.Services["users"].Routes[0]
		if len(route.Hooks.PreRequest) != 0 {
			t.Errorf("expected no pre_request hooks, got %d", len(route.Hooks.PreRequest))
		}
	})
}

// The `config:` block under a hook is the only input a hook factory gets, so
// the test above proving a config loads is not enough - the values have to
// arrive with the right names, the right order and, crucially, the right Go
// types. A factory that type-asserts `limit` as an int breaks the day YAML
// hands it a string, and nothing else in the suite would catch that.
func TestLoad_DecodesHookConfigValues(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(writeTempConfig(t, validYAML()))
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	routes := cfg.Services["users"].Routes

	// Route 1: jwt_auth with params, audit_log without.
	meHooks := routes[0].Hooks
	if len(meHooks.PreRequest) != 1 || meHooks.PreRequest[0].Name != "jwt_auth" {
		t.Fatalf("expected a single pre_request hook %q, got %+v", "jwt_auth", meHooks.PreRequest)
	}
	if got := meHooks.PreRequest[0].Config["mode"]; got != "jwt" {
		t.Errorf("expected jwt_auth config mode %q, got %v (%T)", "jwt", got, got)
	}

	if len(meHooks.PostResponse) != 1 || meHooks.PostResponse[0].Name != "audit_log" {
		t.Fatalf("expected a single post_response hook %q, got %+v", "audit_log", meHooks.PostResponse)
	}
	// audit_log takes no parameters, so it must have no config at all rather
	// than an empty-but-present map the factory would have to special-case.
	if meHooks.PostResponse[0].Config != nil {
		t.Errorf("expected no config for audit_log, got %v", meHooks.PostResponse[0].Config)
	}

	// Route 2: hook order must be the order written in the YAML, since that
	// order is the execution order.
	userHooks := routes[1].Hooks.PreRequest
	if len(userHooks) != 2 {
		t.Fatalf("expected 2 pre_request hooks, got %d", len(userHooks))
	}
	if userHooks[0].Name != "jwt_auth" || userHooks[1].Name != "quota_check" {
		t.Errorf(
			"expected hook order [jwt_auth quota_check], got [%s %s]",
			userHooks[0].Name, userHooks[1].Name,
		)
	}

	quota := userHooks[1].Config
	if got, ok := quota["limit"].(int); !ok || got != 1000 {
		t.Errorf("expected quota_check limit to decode as the int 1000, got %v (%T)", quota["limit"], quota["limit"])
	}
	if got := quota["window"]; got != "1h" {
		t.Errorf("expected quota_check window %q, got %v (%T)", "1h", got, got)
	}

	// Methods are a plain list and both entries must survive.
	if got := strings.Join(routes[1].Methods, ","); got != "GET,POST" {
		t.Errorf("expected methods %q, got %q", "GET,POST", got)
	}
}

// Every validation failure has to name the offending service, route and field
// - the whole point of failing at startup is telling the operator which line
// of gateway.yaml to fix.
func TestLoad_ValidationFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "port above range",
			yaml: `
port: 70000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /health
        methods: [GET]
`,
			want: "port",
		},
		{
			name: "negative port",
			yaml: `
port: -1
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /health
        methods: [GET]
`,
			want: "port",
		},
		{
			name: "no services",
			yaml: `
port: 8000
services: {}
`,
			want: "services",
		},
		{
			name: "service with no routes",
			yaml: `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes: []
`,
			want: "routes",
		},
		{
			name: "route with no methods",
			yaml: `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /health
`,
			want: "method",
		},
		{
			name: "route with empty methods list",
			yaml: `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /health
        methods: []
`,
			want: "method",
		},
		{
			name: "duplicate route within one service",
			yaml: `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /api/v1/users
        methods: [GET]
      - path: /api/v1/users
        methods: [GET]
`,
			want: "duplicate",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(writeTempConfig(t, tc.yaml))
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected the error to mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// Duplicate detection keys on "METHOD path", not on the path alone: the same
// path serving two different methods is a normal REST route, not a conflict.
func TestLoad_SamePathDifferentMethodsIsValid(t *testing.T) {
	t.Parallel()

	content := `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /api/v1/users
        methods: [GET]
      - path: /api/v1/users
        methods: [POST]
`

	cfg, err := config.Load(writeTempConfig(t, content))
	if err != nil {
		t.Fatalf("expected the same path on different methods to be allowed, got: %v", err)
	}
	if got := len(cfg.Services["users"].Routes); got != 2 {
		t.Errorf("expected 2 routes, got %d", got)
	}
}

