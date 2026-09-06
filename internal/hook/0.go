package hook

import "github.com/cloudwego/hertz/pkg/app"

type Stage string

const (
	PreRequest   Stage = "pre_request"
	PostResponse Stage = "post_response"
	OnError      Stage = "on_error"
)

type Hook interface {
	Name() string
	Stage() Stage
	Execute(rc *app.RequestContext) error
}

type Registry interface {
	Register(name string, factory HookFactory) error
	Build(name string, config map[string]any) (Hook, error)
}

type HookFactory func(config map[string]any) (Hook, error)
