package main

import (
	"fmt"
	"log"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	hertz "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/internal/routing"
	"github.com/maadiii/gogate/internal/server"
)

const (
	maxConnWiatTime = 2 * time.Second
	maxReadTimeout  = 10 * time.Second
	maxWriteTimeout = 10 * time.Second
	maxIdleTimeout  = 60 * time.Second

	maxRequestBodyBytes = 10 << 20
	maxHeaderBytes      = 1 << 20
	maxKeepBodyBytes    = 4 << 20
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

	client, err := client.NewClient(
		client.WithMaxConnWaitTimeout(maxConnWiatTime),
	)
	if err != nil {
		log.Fatalf("creating hertz client: %v", err)
	}

	gw := server.NewGateway(table, client)

	s := hertz.New(
		hertz.WithHostPorts(fmt.Sprintf(":%d", cfg.Port)),
		hertz.WithMaxRequestBodySize(maxRequestBodyBytes),
		hertz.WithMaxKeepBodySize(maxKeepBodyBytes),
		hertz.WithMaxHeaderBytes(maxHeaderBytes),
		hertz.WithReadTimeout(maxReadTimeout),
		hertz.WithWriteTimeout(maxWriteTimeout),
		hertz.WithIdleTimeout(maxIdleTimeout),
	)
	s.Any("/*path", gw.Forward)

	log.Printf("gateway running on :%d", cfg.Port)
	s.Spin()
}
