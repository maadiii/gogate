package hooks

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
)

func Register(reg hook.Registry, cfg *config.Config) error {
	if err := registerAuth(reg, cfg); err != nil {
		return err
	}

	return nil
}

type State struct {
	// Identity
	userId      string
	roles       []string
	permissions []string
}

const stateKey = "state"

func SetState() app.HandlerFunc {
	return func(c context.Context, rc *app.RequestContext) {
		rc.Set(stateKey, new(State))

		rc.Next(c)
	}
}
