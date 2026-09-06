package routing

import "strings"

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
