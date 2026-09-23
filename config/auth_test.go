package config_test

import (
	"strings"
	"testing"

	"github.com/maadiii/gogate/config"
)

const testPublicKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

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

func accessTokenBlock() string {
	return `
auth:
  accessToken:
    publicKey: "` + testPublicKey + `"
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

	path := writeTempConfig(t, configWithAuth(accessTokenBlock()))

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("expected access-token-only config to load, got: %v", err)
	}

	if cfg.Auth.RefreshToken.PublicKey != "" {
		t.Errorf("expected no refresh token, got public key %q", cfg.Auth.RefreshToken.PublicKey)
	}
	if cfg.Auth.AccessToken.PublicKey != testPublicKey {
		t.Errorf("access public key = %q, want %q", cfg.Auth.AccessToken.PublicKey, testPublicKey)
	}
}

func TestLoad_AuthRejectsRefreshTokenWithoutAccessToken(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, configWithAuth(`
auth:
  refreshToken:
    publicKey: "`+testPublicKey+`"
`))

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected refresh-only auth configuration to fail")
	}
	if !strings.Contains(err.Error(), "auth.accessToken: must be configured") {
		t.Fatalf("error = %q, want missing access token", err)
	}
}

func TestLoad_AuthRejectsEmptyAccessPublicKeyWhenRefreshIsConfigured(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, configWithAuth(`
auth:
  accessToken:
    publicKey: ""
  refreshToken:
    publicKey: "`+testPublicKey+`"
`))

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected empty access public key configuration to fail")
	}
	if !strings.Contains(err.Error(), "auth.accessToken: must be configured") {
		t.Fatalf("error = %q, want missing access token", err)
	}
}
