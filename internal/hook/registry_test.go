package hook_test

import (
	"context"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/maadiii/gogate/internal/hook"
)

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
