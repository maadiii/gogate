package hooks

import (
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
)

func Register(reg hook.Registry, cfg *config.Config) error {
	if err := registerPaseto(reg, cfg); err != nil {
		return err
	}

	return nil
}
