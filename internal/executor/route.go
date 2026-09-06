package executor

import (
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
