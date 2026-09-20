package hook

import "fmt"

type registry struct {
	factories map[string]HookFactory
}

func NewRegistry() Registry {
	return &registry{factories: make(map[string]HookFactory)}
}

func (r *registry) Register(name string, factory HookFactory) error {
	if name == "" {
		return fmt.Errorf("registering hook factory: name cannot be empty")
	}

	if _, exists := r.factories[name]; exists {
		return fmt.Errorf("registering hook factory %q: name already registered", name)
	}

	r.factories[name] = factory

	return nil
}

func (r *registry) Build(name string, config map[string]any) (Hook, error) {
	factory, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("hook %q is not registered", name)
	}

	h, err := factory(config)
	if err != nil {
		return nil, fmt.Errorf("building hook %q: %w", name, err)
	}
	if h.Name() != name {
		return nil, fmt.Errorf(
			"hook factory %q returned a hook with mismatched Name() %q",
			name, h.Name(),
		)
	}

	return h, nil
}
