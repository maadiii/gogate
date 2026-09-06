package config_test

import (
	"os"
	"path/filepath"
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
	path := filepath.Join(dir, "gatetway.yaml")

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
			t.Errorf("expected port 8000, got: %v", err)
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

	t.Run("not methods", func(t *testing.T) {
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
