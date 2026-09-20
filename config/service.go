package config

import "fmt"

type Service struct {
	Target string  `yaml:"target"`
	Routes []Route `yaml:"routes"`
}

type Route struct {
	Path    string     `yaml:"path"`
	Methods []string   `yaml:"methods"`
	Hooks   RouteHooks `yaml:"hooks,omitempty"`
}

func (r Route) validate(svcName string, idx int) error {
	if r.Path == "" {
		return fmt.Errorf("services.%s.routes[%d].path: cannot be empty", svcName, idx)
	}

	if len(r.Methods) == 0 {
		return fmt.Errorf("services.%s.routes[%d].methods: must have at least one method", svcName, idx)
	}

	if err := r.Hooks.PreRequest.validate(svcName, idx, "pre_request"); err != nil {
		return err
	}

	if err := r.Hooks.PostResponse.validate(svcName, idx, "post_response"); err != nil {
		return err
	}

	if err := r.Hooks.OnError.validate(svcName, idx, "on_error"); err != nil {
		return err
	}

	return nil
}

type RouteHooks struct {
	PreRequest   HookRefList `yaml:"pre_request,omitempty"`   //nolint
	PostResponse HookRefList `yaml:"post_response,omitempty"` //nolint
	OnError      HookRefList `yaml:"on_error,omitempty"`      //nolint
}

type HookRefList []HookRef

func (h HookRefList) validate(svcName string, idx int, stage string) error {
	seen := make(map[string]bool)
	for j, ref := range h {
		if ref.Name == "" {
			return fmt.Errorf(
				"services.%s.routes[%d].hooks.%s[%d].name: cannot be empty",
				svcName, idx, stage, j,
			)
		}
		if seen[ref.Name] {
			return fmt.Errorf(
				"services.%s.routes[%d].hooks.%s[%d]: duplicate hook name %q in this stage",
				svcName, idx, stage, j, ref.Name,
			)
		}

		seen[ref.Name] = true
	}

	return nil
}

type HookRef struct {
	Name   string         `yaml:"name"`
	Config map[string]any `yaml:"config,omitempty"`
}
