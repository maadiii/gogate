package hooks

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	psto "aidanwoods.dev/go-paseto"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/goutils/auth"
)

func newPublicPasetoTestIssuer(t *testing.T, accessTTL time.Duration) (*auth.PublicPaseto, []byte) {
	t.Helper()

	accessKey := psto.NewV4AsymmetricSecretKey()
	refreshKey := psto.NewV4AsymmetricSecretKey()
	issuer, err := auth.NewPublicPaseto(auth.PublicPasetoConfig{
		Issuer:            "hooks-test",
		AccessPrivateKey:  accessKey.ExportBytes(),
		AccessPublicKey:   accessKey.Public().ExportBytes(),
		RefreshPrivateKey: refreshKey.ExportBytes(),
		RefreshPublicKey:  refreshKey.Public().ExportBytes(),
		AccessTTL:         accessTTL,
		RefreshTTL:        time.Hour,
	})
	if err != nil {
		t.Fatalf("creating public PASETO issuer: %v", err)
	}

	return issuer, accessKey.Public().ExportBytes()
}

func TestPublicPasetoVerifier_ValidatesClaimsWithPublicKey(t *testing.T) {
	t.Parallel()

	issuer, publicKey := newPublicPasetoTestIssuer(t, time.Hour)
	tokens, err := issuer.Generate("user-1", "gateway", map[string]any{
		"roles": []string{"member"}, "perms": []string{"reports:read"},
	})
	if err != nil {
		t.Fatalf("generating token: %v", err)
	}

	verifier, err := newPublicPasetoVerifier(publicKey)
	if err != nil {
		t.Fatalf("creating verifier: %v", err)
	}
	claims, err := verifier.ValidateAccess(tokens.Access)
	if err != nil {
		t.Fatalf("validating token: %v", err)
	}

	if claims.Subject != "user-1" || claims.CustomClaims["roles"] == nil || claims.CustomClaims["perms"] == nil {
		t.Fatalf("claims = %+v, want subject and custom claims", claims)
	}
}

func TestPublicPasetoVerifier_RejectsWrongKeyAndExpiredToken(t *testing.T) {
	t.Parallel()

	issuer, publicKey := newPublicPasetoTestIssuer(t, -time.Minute)
	tokens, err := issuer.Generate("user-1", "gateway", nil)
	if err != nil {
		t.Fatalf("generating expired token: %v", err)
	}
	verifier, err := newPublicPasetoVerifier(publicKey)
	if err != nil {
		t.Fatalf("creating verifier: %v", err)
	}
	if _, err := verifier.ValidateAccess(tokens.Access); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
	if _, err := verifier.ValidateAccess(tokens.Refresh); err == nil {
		t.Fatal("expected refresh token to be rejected by access validation")
	}

	otherIssuer, otherPublicKey := newPublicPasetoTestIssuer(t, time.Hour)
	otherTokens, err := otherIssuer.Generate("user-1", "gateway", nil)
	if err != nil {
		t.Fatalf("generating token with another key: %v", err)
	}
	otherVerifier, err := newPublicPasetoVerifier(otherPublicKey)
	if err != nil {
		t.Fatalf("creating other verifier: %v", err)
	}
	if _, err := verifier.ValidateAccess(otherTokens.Access); err == nil {
		t.Fatal("expected token signed by another key to be rejected")
	}
	if _, err := otherVerifier.ValidateAccess(tokens.Access); err == nil {
		t.Fatal("expected token signed by the old key to be rejected")
	}
}

func TestRegister_ValidatesAccessPublicKey(t *testing.T) {
	t.Parallel()

	validKey := psto.NewV4AsymmetricSecretKey().Public().ExportBytes()
	tests := []struct {
		name      string
		publicKey string
		wantErr   bool
	}{
		{name: "malformed base64", publicKey: "not-base64", wantErr: true},
		{name: "wrong decoded length", publicKey: base64.StdEncoding.EncodeToString([]byte("short")), wantErr: true},
		{name: "valid access-only key", publicKey: base64.StdEncoding.EncodeToString(validKey)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := hook.NewRegistry()
			cfg := &config.Config{Auth: config.Auth{
				AccessToken: config.AccessToken{PublicKey: tt.publicKey},
			}}
			if err := Register(registry, cfg); err != nil {
				t.Fatalf("registering hook: %v", err)
			}

			_, err := registry.Build(pasetoHookName, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPaseto_Execute_DoesNotForwardMalformedClaims(t *testing.T) {
	t.Parallel()

	issuer, publicKey := newPublicPasetoTestIssuer(t, time.Hour)
	tokens, err := issuer.Generate("user-1", "gateway", map[string]any{
		"roles": []any{"member", 7}, "perms": "reports:read",
	})
	if err != nil {
		t.Fatalf("generating token: %v", err)
	}
	verifier, err := newPublicPasetoVerifier(publicKey)
	if err != nil {
		t.Fatalf("creating verifier: %v", err)
	}

	rc := ut.CreateUtRequestContext("GET", "/private", &ut.Body{})
	rc.Request.Header.Set(authorizationHeader, "Bearer "+tokens.Access)
	paseto := &Paseto{paseto: verifier}
	if err := paseto.Execute(context.Background(), rc); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	identity, ok := hook.IdentityFrom(rc)
	if !ok {
		t.Fatal("expected identity to be stored")
	}
	if identity.Subject != "user-1" || len(identity.Roles) != 0 || len(identity.Permissions) != 0 {
		t.Fatalf("identity = %+v, want subject without malformed roles or permissions", identity)
	}
	if got := rc.Request.Header.Get("X-UserID"); got != "user-1" {
		t.Fatalf("X-UserID = %q, want user-1", got)
	}
	if got := rc.Request.Header.GetAll("X-Roles"); len(got) != 0 {
		t.Fatalf("X-Roles = %v, want no forwarded roles", got)
	}
	if got := rc.Request.Header.GetAll("X-Perms"); len(got) != 0 {
		t.Fatalf("X-Perms = %v, want no forwarded permissions", got)
	}
}

// TestBearerToken pins down the header parsing on its own, because this is
// where the hook's one silent-failure mode lives: a credential that never
// reaches the parser looks exactly like a bad credential from the outside, so
// a parsing regression here reads as "auth rejects everything" rather than as
// a crash.
func TestBearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   string
		wantOK bool
	}{
		{name: "well formed", header: "Bearer abc123", want: "abc123", wantOK: true},
		{name: "lowercase scheme", header: "bearer abc123", want: "abc123", wantOK: true},
		{name: "uppercase scheme", header: "BEARER abc123", want: "abc123", wantOK: true},
		{name: "mixed case scheme", header: "BeArEr abc123", want: "abc123", wantOK: true},
		{name: "extra spaces after scheme", header: "Bearer   abc123", want: "abc123", wantOK: true},
		{name: "surrounding whitespace", header: "  Bearer abc123  ", want: "abc123", wantOK: true},
		{name: "scheme only", header: "Bearer", want: "", wantOK: false},
		{name: "scheme and spaces", header: "Bearer   ", want: "", wantOK: false},
		{name: "no scheme", header: "abc123", want: "", wantOK: false},
		{name: "empty header", header: "", want: "", wantOK: false},
		{name: "whitespace header", header: "   ", want: "", wantOK: false},
		{name: "wrong scheme", header: "Basic abc123", want: "", wantOK: false},
		{name: "scheme as substring", header: "Bearerabc123", want: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := bearerToken([]byte(tt.header))
			if ok != tt.wantOK {
				t.Fatalf("bearerToken(%q) ok = %v, want %v", tt.header, ok, tt.wantOK)
			}

			if got != tt.want {
				t.Errorf("bearerToken(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestForwardIdentity_ReplacesClientSuppliedHeaders(t *testing.T) {
	t.Parallel()

	rc := ut.CreateUtRequestContext("GET", "/ping", &ut.Body{})
	rc.Request.Header.Set("X-UserID", "spoofed-user")
	rc.Request.Header.Add("X-Roles", "admin")
	rc.Request.Header.Add("X-Perms", "all:write")

	forwardIdentity(rc, hook.Identity{
		Subject:     "user-1",
		Roles:       []string{"member", "billing"},
		Permissions: []string{"reports:read"},
		AuthMethod:  "paseto",
	})

	if got := rc.Request.Header.Get("X-UserID"); got != "user-1" {
		t.Errorf("X-UserID = %q, want %q", got, "user-1")
	}
	if got := rc.Request.Header.GetAll("X-Roles"); len(got) != 2 || got[0] != "member" || got[1] != "billing" {
		t.Errorf("X-Roles = %v, want [member billing]", got)
	}
	if got := rc.Request.Header.GetAll("X-Perms"); len(got) != 1 || got[0] != "reports:read" {
		t.Errorf("X-Perms = %v, want [reports:read]", got)
	}
}

func TestStringSliceClaim(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		value any
		want  []string
	}{
		{name: "string slice", value: []string{"member"}, want: []string{"member"}},
		{name: "decoded generic slice", value: []any{"member", "billing"}, want: []string{"member", "billing"}},
		{name: "malformed generic slice", value: []any{"member", 1}},
		{name: "wrong type", value: "member"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := stringSliceClaim(map[string]any{"claim": tt.value}, "claim")
			if len(got) != len(tt.want) {
				t.Fatalf("stringSliceClaim() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("stringSliceClaim() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
