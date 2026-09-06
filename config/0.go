package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validating config file %q: %w", path, err)
	}

	return &cfg, nil
}

type Config struct {
	Port     int                `yaml:"port"`
	Services map[string]Service `yaml:"services"`
}

func (c Config) validate() error {
	if len(c.Services) == 0 {
		return fmt.Errorf("services: at least one service must be defined")
	}

	for name, svc := range c.Services {
		if svc.Target == "" {
			return fmt.Errorf("services.%s.target cannot be empty", name)
		}

		if len(svc.Routes) == 0 {
			return fmt.Errorf("services.%s.routes: at least one route must be defined", name)
		}

		for i, route := range svc.Routes {
			if err := route.validate(name, i); err != nil {
				return err
			}
		}
	}

	return nil
}
