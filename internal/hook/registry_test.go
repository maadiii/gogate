package hook_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/internal/hook"
)

// The Registry is the one place where a typo in gateway.yaml (or in a
// Register call) has to be caught, and it has to be caught at startup rather
// than at request time. These tests cover every way that lookup can go wrong.

type fakeHook struct {
	name  string
	stage hook.Stage
}

func (fh *fakeHook) Name() string                                            { return fh.name }
func (fh *fakeHook) Stage() hook.Stage                                       { return fh.stage }
func (fh *fakeHook) Execute(c context.Context, rc *app.RequestContext) error { return nil }

func TestRegistry(t *testing.T) {
	t.Parallel()

	r := hook.NewRegistry()

	err := r.Register("dummy", func(config map[string]any) (hook.Hook, error) {
		return &fakeHook{name: "dummy", stage: hook.PreRequest}, nil
	})
	if err != nil {
		t.Fatalf("expected no error registering, got: %v", err)
	}

	h, err := r.Build("dummy", nil)
	if err != nil {
		t.Fatalf("expected no error building, got: %v", err)
	}
	if h.Name() != "dummy" {
		t.Errorf("expected name %q, got %q", "dummy", h.Name())
	}

	if h.Stage() != hook.PreRequest {
		t.Errorf("expected stage %q, got %q", hook.PreRequest, h.Stage())
	}
}

func TestRegistry_RegisterDuplicateNameFails(t *testing.T) {
	t.Parallel()

	r := hook.NewRegistry()

	factory := func(map[string]any) (hook.Hook, error) {
		return &fakeHook{name: "dup", stage: hook.PreRequest}, nil
	}

	if err := r.Register("dup", factory); err != nil {
		t.Fatalf("first registration should succeed, got: %v", err)
	}

	// The second registration must be rejected rather than silently
	// overwriting the first, which would make which hook wins depend on
	// registration order.
	err := r.Register("dup", factory)
	if err == nil {
		t.Fatal("expected re-registering the same name to fail, got nil")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("expected the error to name the duplicate hook, got: %v", err)
	}
}

func TestRegistry_RegisterEmptyNameFails(t *testing.T) {
	t.Parallel()

	r := hook.NewRegistry()

	err := r.Register("", func(map[string]any) (hook.Hook, error) {
		return &fakeHook{name: "", stage: hook.PreRequest}, nil
	})
	if err == nil {
		t.Fatal("expected registering an empty name to fail, got nil")
	}
}

func TestRegistry_BuildUnknownNameFails(t *testing.T) {
	t.Parallel()

	r := hook.NewRegistry()

	if err := r.Register("known", func(map[string]any) (hook.Hook, error) {
		return &fakeHook{name: "known", stage: hook.PreRequest}, nil
	}); err != nil {
		t.Fatalf("registering: %v", err)
	}

	_, err := r.Build("unknown", nil)
	if err == nil {
		t.Fatal("expected building an unregistered name to fail, got nil")
	}
	// This is the error a user sees when gateway.yaml references a hook
	// nobody registered, so it has to name the offending hook.
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected the error to name the missing hook, got: %v", err)
	}
}

// A factory's own error has to survive as the wrapped cause, so callers can
// still match on it with errors.Is while the message says which hook failed.
func TestRegistry_BuildWrapsFactoryError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("signing key is not a valid RSA public key")

	r := hook.NewRegistry()
	if err := r.Register("bad_config", func(map[string]any) (hook.Hook, error) {
		return nil, sentinel
	}); err != nil {
		t.Fatalf("registering: %v", err)
	}

	_, err := r.Build("bad_config", map[string]any{"key": "nonsense"})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected the factory error to be wrapped (errors.Is), got: %v", err)
	}
	if !strings.Contains(err.Error(), "bad_config") {
		t.Errorf("expected the error to name the failing hook, got: %v", err)
	}
}

// A factory registered under one name that returns a hook answering to a
// different one would silently break stage lookup and config resolution, so
// the Registry refuses it.
func TestRegistry_BuildRejectsNameMismatch(t *testing.T) {
	t.Parallel()

	r := hook.NewRegistry()
	if err := r.Register("declared", func(map[string]any) (hook.Hook, error) {
		return &fakeHook{name: "something_else", stage: hook.PreRequest}, nil
	}); err != nil {
		t.Fatalf("registering: %v", err)
	}

	_, err := r.Build("declared", nil)
	if err == nil {
		t.Fatal("expected a factory returning a mismatched Name() to fail, got nil")
	}
	for _, want := range []string{"declared", "something_else"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}
}

// The per-route `config:` block from gateway.yaml is the factory's only
// input, so it has to arrive intact.
func TestRegistry_BuildPassesConfigToFactory(t *testing.T) {
	t.Parallel()

	cfg := map[string]any{
		"mode":   "jwt",
		"limit":  1000,
		"window": "1h",
	}

	var received map[string]any

	r := hook.NewRegistry()
	if err := r.Register("capture", func(c map[string]any) (hook.Hook, error) {
		received = c

		return &fakeHook{name: "capture", stage: hook.PreRequest}, nil
	}); err != nil {
		t.Fatalf("registering: %v", err)
	}

	if _, err := r.Build("capture", cfg); err != nil {
		t.Fatalf("building: %v", err)
	}

	for _, key := range []string{"mode", "limit", "window"} {
		if received[key] != cfg[key] {
			t.Errorf("config key %q: expected %v, got %v", key, cfg[key], received[key])
		}
	}
}

// A hook with no `config:` block in YAML must be buildable with a nil map -
// the loader has nothing to pass for it.
func TestRegistry_BuildWithNilConfig(t *testing.T) {
	t.Parallel()

	r := hook.NewRegistry()
	if err := r.Register("no_config", func(map[string]any) (hook.Hook, error) {
		return &fakeHook{name: "no_config", stage: hook.PostResponse}, nil
	}); err != nil {
		t.Fatalf("registering: %v", err)
	}

	h, err := r.Build("no_config", nil)
	if err != nil {
		t.Fatalf("expected a hook needing no config to build from a nil map, got: %v", err)
	}
	if h.Stage() != hook.PostResponse {
		t.Errorf("expected stage %q, got %q", hook.PostResponse, h.Stage())
	}
}

// Each registered name gets its own factory, and building one name never
// builds another - the map is keyed by name, not shared.
func TestRegistry_BuildIsScopedToRequestedName(t *testing.T) {
	t.Parallel()

	built := make(map[string]int)

	r := hook.NewRegistry()
	for _, name := range []string{"alpha", "beta"} {
		if err := r.Register(name, func(map[string]any) (hook.Hook, error) {
			built[name]++

			return &fakeHook{name: name, stage: hook.PreRequest}, nil
		}); err != nil {
			t.Fatalf("registering %q: %v", name, err)
		}
	}

	if _, err := r.Build("alpha", nil); err != nil {
		t.Fatalf("building alpha: %v", err)
	}

	if built["alpha"] != 1 {
		t.Errorf("expected alpha's factory to be called once, got %d", built["alpha"])
	}
	if built["beta"] != 0 {
		t.Errorf("expected beta's factory not to be called, got %d", built["beta"])
	}
}
