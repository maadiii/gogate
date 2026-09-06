package main

import (
	"context"
	"log"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/client"
	hertz "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/internal/routing"
	"github.com/maadiii/gogate/internal/server"
)

func main() {
	cfg, err := config.Load("gateway.yaml")
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	registry := hook.NewRegistry()

	table, err := routing.Build(cfg, registry)
	if err != nil {
		log.Fatalf("building routing table: %v", err)
	}

	client, err := client.NewClient()
	if err != nil {
		log.Fatalf("creating hertz client: %v", err)
	}

	s := hertz.New()
	s.Any("/*path", func(c context.Context, rc *app.RequestContext) {
		server.Forward(table, client, c, rc)
	})

	s.Spin()
}
