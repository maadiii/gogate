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

func registerAuth(reg hook.Registry, cfg *config.Config) error {
	return reg.Register("auth", func(config map[string]any) (hook.Hook, error) {
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

// Execute implements [hook.Hook].
func (a *Auth) Execute(c context.Context, rc *app.RequestContext) error {
	h := rc.GetHeader("Authorization")
	if len(h) == 0 {
		return errors.Unauthorized()
	}

	token, ok := strings.CutPrefix(string(h), "Bearer")
	if !ok {
		return errors.Unauthorized()
	}

	claims, err := a.paseto.ValidateAccess(token)
	if err != nil {
		return errors.Unauthorized()
	}

	state, _ := rc.Get(stateKey)

	state.(*State).userId = claims.Subject
	rc.Request.Header.Set("X-UserID", claims.Subject)

	roles, ok := claims.CustomClaims["roles"].([]string)
	if ok {
		state.(*State).roles = roles

		for _, role := range roles {
			rc.Request.Header.Add("X-Roles", role)
		}
	}

	perms, ok := claims.CustomClaims["perms"].([]string)
	if ok {
		state.(*State).permissions = perms

		for _, perm := range perms {
			rc.Request.Header.Add("X-Perms", perm)
		}
	}

	return nil
}

// Name implements [hook.Hook].
func (a *Auth) Name() string {
	return "authZ"
}

// Stage implements [hook.Hook].
func (a *Auth) Stage() hook.Stage {
	return hook.PreRequest
}
