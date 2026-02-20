package config

import (
	"gopkg.in/yaml.v3"
	"io"
	"os"
)

const (
	OperationPolicySelectorRoute Policy = "OperationSelectorRoute"
	AllFastestRoute              Policy = "AllFastestRoute"
	MissFailoverRoute            Policy = "MissFailoverRoute"
	LocalRoute                   Policy = "LocalRoute"
	DefaultRoute                 Policy = "DefaultRoute"
)

type Router struct {
	OrderedPoolConfig OrderedPoolConfig `yaml:"orderedPool"`
	RouteConfig       RouteConfig       `yaml:"route"`
}

type OrderedPoolConfig []string

type Policy string

type RouteConfig struct {
	Type              string            `yaml:"type"`
	OperationPolicies map[string]Policy `yaml:"operationPolicies,omitempty"`
}

func ParseRouterConfig(path string) (*Router, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	routerConfig := &Router{}
	err = yaml.Unmarshal(data, routerConfig)
	if err != nil {
		return nil, err
	}

	return routerConfig, nil
}
