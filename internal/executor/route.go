package executor

import (
	"fmt"
	"strings"

	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
)

func Build(cfg *config.Config, registry hook.Registry) (*Table, error) {
	t := &Table{exact: make(map[string]ResolvedRoute)}
}

type Table struct {
	exact    map[string]ResolvedRoute
	wildcard []wildcardEntry
}

func (t *Table) Resolve(method, path string) (*ResolvedRoute, bool) {
	if resolved, ok := t.exact[method+" "+path]; ok {
		return &resolved, true
	}

	var best *wildcardEntry
	for i := range t.wildcard {
		entry := &t.wildcard[i]
		if entry.method != method {
			continue
		}

		if !strings.HasPrefix(path, entry.prefix) {
			continue
		}

		if best == nil || len(entry.prefix) > len(best.prefix) {
			best = entry
		}
	}

	if best != nil {
		return &best.route, true
	}

	return nil, false
}

type ResolvedRoute struct {
	ServiceName string
	Target      string

	PreRequestHooks   []hook.Hook
	PostResponseHooks []hook.Hook
	OnErrorHooks      []hook.Hook
}

type wildcardEntry struct {
	method string
	prefix string
	route  ResolvedRoute
}

func wildcardPrefix(path string) (string, bool) {
	if len(path) > 0 && path[len(path)-1] == '*' {
		return path[:len(path)-1], true
	}

	return "", false
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
