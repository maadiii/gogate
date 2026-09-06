package routing

import (
	"fmt"

	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
)

func Build(cfg *config.Config, registry hook.Registry) (*Table, error) {
	t := &Table{exact: make(map[string]ResolvedRoute)}

	for svcName, svc := range cfg.Services {
		for i, route := range svc.Routes {
			preHooks, err := resolveHooks(registry, route.Hooks.PreRequest, hook.PreRequest, svcName, i, "pre_request")
			if err != nil {
				return nil, err
			}

			postResponse, err := resolveHooks(registry, route.Hooks.PostResponse, hook.PostResponse, svcName, i, "post_response")
			if err != nil {
				return nil, err
			}

			errHooks, err := resolveHooks(registry, route.Hooks.OnError, hook.OnError, svcName, i, "on_error")
			if err != nil {
				return nil, err
			}

			resolved := ResolvedRoute{
				ServiceName:       svcName,
				Target:            svc.Target,
				PreRequestHooks:   preHooks,
				PostResponseHooks: postResponse,
				OnErrorHooks:      errHooks,
			}

			for _, method := range route.Methods {
				if prefix, isWildcard := wildcardPrefix(route.Path); isWildcard {
					t.wildcard = append(t.wildcard, wildcardEntry{
						method: method,
						prefix: prefix,
						route:  resolved,
					})
				} else {
					key := method + " " + route.Path
					t.exact[key] = resolved
				}
			}
		}
	}

	return t, nil
}

type ResolvedRoute struct {
	ServiceName string
	Target      string

	PreRequestHooks   []hook.Hook
	PostResponseHooks []hook.Hook
	OnErrorHooks      []hook.Hook
}

func resolveHooks(
	registry hook.Registry,
	refs config.HookRefList,
	expectedStage hook.Stage,
	svcName string,
	routeIdx int,
	stageName string,
) ([]hook.Hook, error) {
	if len(refs) == 0 {
		return nil, nil
	}

	hooks := make([]hook.Hook, 0, len(refs))
	for j, ref := range refs {
		h, err := registry.Build(ref.Name, ref.Config)
		if err != nil {
			return nil, fmt.Errorf(
				"services.%s.routes[%d].hooks.%s[%d]: %w",
				svcName, routeIdx, stageName, j, err,
			)
		}
		if h.Stage() != expectedStage {
			return nil, fmt.Errorf(
				"services.%s.routes[%d].hooks.%s[%d]: hook %q has Stage() %q, cannot be used in %q",
				svcName, routeIdx, stageName, j, ref.Name, h.Stage(), stageName,
			)
		}

		hooks = append(hooks, h)
	}

	return hooks, nil
}
