package config_test

import (
	"strings"
	"testing"
	"time"

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

func TestLoad_AuthTTLAcceptsDurationStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ttl  string
		want time.Duration
	}{
		{name: "minutes", ttl: "15m", want: 15 * time.Minute},
		{name: "hours", ttl: "1h", want: time.Hour},
		{name: "combined", ttl: "1h30m", want: 90 * time.Minute},
		{name: "seconds", ttl: "900s", want: 900 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeTempConfig(t, configWithAuth(accessTokenBlock(tt.ttl)))

			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("expected no error for ttl %q, got: %v", tt.ttl, err)
			}

			got := time.Duration(cfg.Auth.AccessToken.TTL)
			if got != tt.want {
				t.Errorf("expected ttl %q to decode to %s, got %s", tt.ttl, tt.want, got)
			}
		})
	}
}

func TestLoad_AuthTTLRejectsBadValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ttl     string
		wantMsg string
	}{
		{name: "bare number", ttl: "900", wantMsg: "must include a unit"},
		{name: "no unit at all", ttl: "abc", wantMsg: "invalid duration"},
		{name: "zero", ttl: "0s", wantMsg: "must be greater than zero"},
		{name: "negative", ttl: "-5m", wantMsg: "must be greater than zero"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeTempConfig(t, configWithAuth(accessTokenBlock(tt.ttl)))

			_, err := config.Load(path)
			if err == nil {
				t.Fatalf("expected an error for ttl %q, got none", tt.ttl)
			}

			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("expected error to mention %q, got: %v", tt.wantMsg, err)
			}
		})
	}
}

func TestLoad_AuthTTLMissingIsAnError(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, configWithAuth(`
auth:
  accessToken:
    secret: `+testSecret+`
`))

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error when ttl is omitted, got none")
	}

	if !strings.Contains(err.Error(), "auth.accessToken.ttl") {
		t.Errorf("expected error to name auth.accessToken.ttl, got: %v", err)
	}
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

	if cfg.Auth.RefreshToken.Secret != "" {
		t.Errorf("expected no refresh token, got secret %q", cfg.Auth.RefreshToken.Secret)
	}
}

func TestLoad_AuthRefreshTokenRejectsEmptySecret(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, configWithAuth(`
auth:
  accessToken:
    secret: `+testSecret+`
    ttl: 15m
  refreshToken:
    ttl: 24h
`))

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected an error for a refresh token with no secret, got none")
	}

	if !strings.Contains(err.Error(), "auth.refreshToken.secret") {
		t.Errorf("expected error to name auth.refreshToken.secret, got: %v", err)
	}
}
