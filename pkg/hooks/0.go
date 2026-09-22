package hooks

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/pkg/errors"
	"github.com/maadiii/goutils/auth"
)

func Register(reg hook.Registry, cfg *config.Config) error {
	if err := registerAuth(reg, cfg); err != nil {
		return err
	}

	return nil
}

const (
	ed25519PublicKeySize = 32

	pasetoHookName = "paseto"

	authorizationHeader = "Authorization"
	bearerScheme        = "Bearer"
)

func registerAuth(reg hook.Registry, cfg *config.Config) error {
	return reg.Register(pasetoHookName, func(config map[string]any) (hook.Hook, error) {
		accessPubKey, err := decodeBase64Key(cfg.Auth.AccessToken.PublicKey, ed25519PublicKeySize)
		if err != nil {
			return nil, err
		}

		refreshPubkey, err := decodeBase64Key(cfg.Auth.RefreshToken.PublicKey, ed25519PublicKeySize)
		if err != nil {
			return nil, err
		}

		publicPaseto, err := auth.NewPublicPaseto(
			auth.PublicPasetoConfig{
				AccessPublicKey:  accessPubKey,
				RefreshPublicKey: refreshPubkey,
			},
		)

		return &Paseto{paseto: publicPaseto}, err
	})
}

// bearerToken extracts the credentials from an Authorization header value of
// the form "Bearer <token>".
//
// The space belongs to the grammar rather than being a separator that happens
// to sit there: RFC 6750 defines credentials as `"Bearer" 1*SP b64token`.
// Trimming "Bearer" off the front with strings.CutPrefix therefore leaves the
// space behind and hands the parser " v4.local.…", which every PASETO parser
// rejects — so every well-formed request would be refused. Cutting on the
// space keeps the two parts honest.
//
// RFC 7235 makes the scheme name case-insensitive, so "bearer" and "BEARER"
// are accepted as well; rejecting them would fail clients that are behaving
// correctly.
func bearerToken(header []byte) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(string(header)), " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return "", false
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}

	return token, true
}

// reject answers the client with 401 instead of returning an error.
//
// An auth rejection is this hook's own response, not a failure of the pipeline:
// returning an error would route it into the OnError stage, where the status
// follows from the error's code and a missing token would be reported as a
// server fault. Aborting also short-circuits the stage, which is what keeps an
// unauthenticated request from reaching a backend the caller may not touch.
//
// The error is recorded along with the status, so ErrorHandler can shape the
// body; because the status is derived from the recorded error, the two cannot
// disagree about what went wrong.
func reject(rc *app.RequestContext) error {
	appErr := errors.Unauthorized()
	_ = rc.AbortWithError(errors.StatusOf(appErr), appErr)

	return nil
}

// forwardIdentity is the explicit boundary between gateway-owned request
// state and the headers trusted by the downstream service. Client-provided
// values are removed first, then rebuilt exclusively from validated identity.
func forwardIdentity(rc *app.RequestContext, identity hook.Identity) {
	rc.Request.Header.Del("X-UserID")
	rc.Request.Header.Del("X-Roles")
	rc.Request.Header.Del("X-Perms")

	rc.Request.Header.Set("X-UserID", identity.Subject)
	for _, role := range identity.Roles {
		rc.Request.Header.Add("X-Roles", role)
	}
	for _, permission := range identity.Permissions {
		rc.Request.Header.Add("X-Perms", permission)
	}
}

// stringSliceClaim accepts both the concrete slice used when claims are made
// in Go and the []any form produced by generic PASETO decoding. A malformed
// claim is deliberately treated as absent: it cannot become a trusted header.
func stringSliceClaim(claims map[string]any, key string) []string {
	value, ok := claims[key]
	if !ok {
		return nil
	}

	if values, ok := value.([]string); ok {
		return values
	}

	values, ok := value.([]any)
	if !ok {
		return nil
	}

	result := make([]string, len(values))
	for i, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil
		}
		result[i] = text
	}

	return result
}

// decodeBase64Key base64-decodes a key and checks its length matches what
// PASETO v4 public expects.
func decodeBase64Key(encoded string, wantLen int) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}

	if len(key) != wantLen {
		return nil, fmt.Errorf("expected %d bytes, got %d", wantLen, len(key))
	}

	return key, nil
}
