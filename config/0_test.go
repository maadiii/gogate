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
						- name: auth
							config:
								mode: jwt
					post_response:
						- name: audit_log
			- path: /api/v1/users/*
				methods: [GET, POST]
				hooks:
					pre_request:
						- name: auth
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

func TestLoad(t *testing.T) {
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
}
