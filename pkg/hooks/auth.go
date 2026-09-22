package hooks

import (
	"context"
	stderrors "errors"
	"time"

	psto "aidanwoods.dev/go-paseto"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/goutils/auth"
)

type Paseto struct {
	paseto publicPaseto
}

type publicPaseto interface {
	ValidateAccess(token string) (auth.PublicClaims, error)
}

type publicPasetoVerifier struct {
	key psto.V4AsymmetricPublicKey
}

func newPublicPasetoVerifier(key []byte) (publicPasetoVerifier, error) {
	publicKey, err := psto.NewV4AsymmetricPublicKeyFromBytes(key)
	if err != nil {
		return publicPasetoVerifier{}, err
	}

	return publicPasetoVerifier{key: publicKey}, nil
}

func (p publicPasetoVerifier) ValidateAccess(token string) (auth.PublicClaims, error) {
	value, err := psto.NewParser().ParseV4Public(p.key, token, nil)
	if err != nil {
		return auth.PublicClaims{}, err
	}

	typ := ""
	_ = value.Get("typ", &typ)
	if typ != string(auth.PublicAccessToken) {
		return auth.PublicClaims{}, stderrors.New("paseto: unexpected token type")
	}

	expiresAt, _ := value.GetExpiration()
	if time.Now().UTC().After(expiresAt) {
		return auth.PublicClaims{}, stderrors.New("paseto: token expired")
	}

	subject, _ := value.GetSubject()
	claims := value.Claims()
	customClaims := make(map[string]any)
	for key, claim := range claims {
		switch key {
		case "sub", "aud", "iss", "jti", "exp", "iat", "nbf", "typ":
		default:
			customClaims[key] = claim
		}
	}

	return auth.PublicClaims{Subject: subject, ExpiresAt: expiresAt, CustomClaims: customClaims}, nil
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
