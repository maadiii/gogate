package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	hertz "github.com/cloudwego/hertz/pkg/app/server"
	"github.com/maadiii/gogate/config"
	"github.com/maadiii/gogate/internal/hook"
	"github.com/maadiii/gogate/internal/routing"
	"github.com/maadiii/gogate/internal/server"
	"github.com/maadiii/gogate/pkg/hooks"
)

const (
	maxConnWaitTime = 2 * time.Second
	maxReadTimeout  = 10 * time.Second
	maxWriteTimeout = 10 * time.Second
	maxIdleTimeout  = 60 * time.Second

	maxRequestBodyBytes = 10 << 20
	maxHeaderBytes      = 1 << 20
	maxKeepBodyBytes    = 4 << 20
)

func main() {
	cfg := getConfig()
	reg := getRegistry(cfg)
	tbl := buildRoutingTable(cfg, reg)
	cli := getHttpClient()
	s := makeServer(tbl, cli, cfg)

	log.Printf("gateway running on :%d", cfg.Port)
	s.Spin()
}

func getConfig() *config.Config {
	var configPath string

	flag.StringVar(&configPath, "config", "", "path to the gateway configuration file (required)")
	flag.Parse()

	if configPath == "" {
		log.Fatal("the -config flag is required: no configuration file was specified")
	}

	if _, err := os.Stat(configPath); errors.Is(err, fs.ErrNotExist) {
		log.Fatalf("config file %q does not exist", configPath)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	return cfg
}

func getRegistry(cfg *config.Config) hook.Registry {
	reg := hook.NewRegistry()
	if err := hooks.Register(reg, cfg); err != nil {
		log.Fatalf("registering hooks: %v", err)
	}

	return reg
}

func buildRoutingTable(cfg *config.Config, reg hook.Registry) *routing.Table {
	table, err := routing.Build(cfg, reg)
	if err != nil {
		log.Fatalf("building routing table: %v", err)
	}

	return table
}

func getHttpClient() *client.Client {
	client, err := client.NewClient(
		client.WithMaxConnWaitTimeout(maxConnWaitTime),
	)
	if err != nil {
		log.Fatalf("creating hertz client: %v", err)
	}

	return client
}

func makeServer(tbl *routing.Table, client *client.Client, cfg *config.Config) *hertz.Hertz {
	gw := server.NewGateway(tbl, client)

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

	return s
}
