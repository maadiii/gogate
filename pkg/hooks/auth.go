package hooks

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/goutils"
)

type Paseto struct {
	paseto goutils.PublicPaseto
}

// Execute implements [hook.Hook].
func (a *Paseto) Execute(c context.Context, rc *app.RequestContext) error {
	token, ok := bearerToken(rc.GetHeader(authorizationHeader))
	if !ok {
		return reject(rc)
	}

	claims, err := a.paseto.ValidateAccess(token)
	if err != nil {
		return reject(rc)
	}

	roles := stringSliceClaim(claims.CustomClaims, "roles")
	perms := stringSliceClaim(claims.CustomClaims, "perms")

	identity := hook.Identity{
		Subject:     claims.Subject,
		Roles:       roles,
		Permissions: perms,
		AuthMethod:  "paseto",
	}
	hook.SetIdentity(rc, identity)
	forwardIdentity(rc, identity)

	return nil
}

// Name implements [hook.Hook].
func (a *Paseto) Name() string {
	return pasetoHookName
}

// Stage implements [hook.Hook].
func (a *Paseto) Stage() hook.Stage {
	return hook.PreRequest
}
