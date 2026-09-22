package config_test

import (
	"testing"

	"github.com/maadiii/gogate/config"
)

const testSecret = "test-secret"

// configWithAuth appends a top-level `auth:` block to an otherwise valid
// config, so these tests go through the real Load path — YAML text, through the
// yaml decoder, through validate — rather than constructing an Auth struct.
func configWithAuth(authBlock string) string {
	return `
port: 8000
services:
  users:
    target: "localhost:3000"
    routes:
      - path: /api/v1/users/*
        methods: [GET]
` + authBlock
}

func accessTokenBlock(ttl string) string {
	return `
auth:
  accessToken:
    secret: ` + testSecret + `
    ttl: ` + ttl + `
`
}

func TestLoad_AuthBlockIsOptional(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, validYAML())

	if _, err := config.Load(path); err != nil {
		t.Fatalf("expected a config with no auth block to load, got: %v", err)
	}
}

func TestLoad_AuthRefreshTokenIsOptional(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, configWithAuth(accessTokenBlock("15m")))

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("expected access-token-only config to load, got: %v", err)
	}

	if cfg.Auth.RefreshToken.PublicKey != "" {
		t.Errorf("expected no refresh token, got secret %q", cfg.Auth.RefreshToken.PublicKey)
	}
}
