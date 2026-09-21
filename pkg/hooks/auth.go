package hooks

import (
	"context"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/pkg/errors"
	"github.com/maadiii/goutils"
	"github.com/maadiii/goutils/auth"
)

type Auth struct {
	paseto goutils.LocalPaseto
}

// authHookName is the single name this hook is known by, in code and in
// gateway.yaml.
//
// The name appears twice — once as the Registry key and once as Name() — and
// neither is derived from the other: the Registry rejects a hook whose Name()
// disagrees with the key it was registered under, which is exactly what this
// hook did until both sites started reading from this constant. Keeping one
// literal means the two cannot drift apart again.
const authHookName = "auth"

func registerAuth(reg hook.Registry, cfg *config.Config) error {
	return reg.Register(authHookName, func(config map[string]any) (hook.Hook, error) {
		localPaseto, err := auth.NewLocalPaseto(auth.LocalPasetoConfig{
			Issuer:     cfg.AppName,
			AccessKey:  []byte(cfg.Auth.AccessToken.Secret),
			RefreshKey: []byte(cfg.Auth.RefreshToken.Secret),
			AccessTTL:  time.Duration(cfg.Auth.AccessToken.TTL),
			RefreshTTL: time.Duration(cfg.Auth.RefreshToken.TTL),
		})
		if err != nil {
			return nil, err
		}

		return &Auth{localPaseto}, nil
	})
}

// bearerScheme is the HTTP authentication scheme this hook accepts, per
// RFC 6750.
const bearerScheme = "Bearer"

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

// identityOf returns the identity shared across hooks for this request,
// creating it on first use.
//
// It must tolerate the state being absent. A hook is built once at startup and
// shared across every request, so it cannot assume anything ran before it on
// this particular request — and a missing value read with the single-value form
// of a type assertion panics, which the pipeline recovers and reports as a
// server fault. That would make every authenticated request a 500.
//
// TODO: this is provisional. Where shared hook state lives, what its key
// namespace is, and whether a middleware has to seed it are still open
// questions (roadmap Phase 2, item 1), and this hook should not be the thing
// that decides them by default.
func identityOf(rc *app.RequestContext) *State {
	if existing, ok := rc.Get(stateKey); ok {
		if state, ok := existing.(*State); ok {
			return state
		}
	}

	state := new(State)
	rc.Set(stateKey, state)

	return state
}

// Execute implements [hook.Hook].
func (a *Auth) Execute(c context.Context, rc *app.RequestContext) error {
	token, ok := bearerToken(rc.GetHeader("Authorization"))
	if !ok {
		return reject(rc)
	}

	claims, err := a.paseto.ValidateAccess(token)
	if err != nil {
		return reject(rc)
	}

	state := identityOf(rc)

	state.userId = claims.Subject
	rc.Request.Header.Set("X-UserID", claims.Subject)

	roles, ok := claims.CustomClaims["roles"].([]string)
	if ok {
		state.roles = roles

		for _, role := range roles {
			rc.Request.Header.Add("X-Roles", role)
		}
	}

	perms, ok := claims.CustomClaims["perms"].([]string)
	if ok {
		state.permissions = perms

		for _, perm := range perms {
			rc.Request.Header.Add("X-Perms", perm)
		}
	}

	return nil
}

// Name implements [hook.Hook].
func (a *Auth) Name() string {
	return authHookName
}

// Stage implements [hook.Hook].
func (a *Auth) Stage() hook.Stage {
	return hook.PreRequest
}
