package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "router.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestParseRouterConfig_Valid(t *testing.T) {
	path := writeConfig(t, `
orderedPool:
  - 10.0.0.1:11211
  - 10.0.0.2:11211
route:
  type: OperationSelectorRoute
  operationPolicies:
    get: MissFailoverRoute
    set: AllFastestRoute
    version: LocalRoute
`)

	cfg, err := ParseRouterConfig(path)
	require.NoError(t, err)

	assert.Equal(t, &Router{
		OrderedPoolConfig: OrderedPoolConfig{"10.0.0.1:11211", "10.0.0.2:11211"},
		RouteConfig: RouteConfig{
			Type: string(OperationPolicySelectorRoute),
			OperationPolicies: map[string]Policy{
				"get":     MissFailoverRoute,
				"set":     AllFastestRoute,
				"version": LocalRoute,
			},
		},
	}, cfg)
}

func TestParseRouterConfig_WithoutOperationPolicies(t *testing.T) {
	path := writeConfig(t, `
orderedPool: [a:11211]
route:
  type: DefaultRoute
`)

	cfg, err := ParseRouterConfig(path)
	require.NoError(t, err)
	assert.Equal(t, OrderedPoolConfig{"a:11211"}, cfg.OrderedPoolConfig)
	assert.Equal(t, string(DefaultRoute), cfg.RouteConfig.Type)
	assert.Nil(t, cfg.RouteConfig.OperationPolicies)
}

func TestParseRouterConfig_EmptyFile(t *testing.T) {
	cfg, err := ParseRouterConfig(writeConfig(t, ""))
	require.NoError(t, err)
	assert.Equal(t, &Router{}, cfg)
}

func TestParseRouterConfig_UnknownKeysIgnored(t *testing.T) {
	cfg, err := ParseRouterConfig(writeConfig(t, "orderedPool: [a]\nsomethingElse: 1\n"))
	require.NoError(t, err)
	assert.Equal(t, OrderedPoolConfig{"a"}, cfg.OrderedPoolConfig)
}

// TestParseRouterConfig_ShippedExample keeps the example config in sync with
// the config schema.
func TestParseRouterConfig_ShippedExample(t *testing.T) {
	cfg, err := ParseRouterConfig(filepath.Join("..", "..", "examples", "docker", "gomcrouter.yaml"))
	require.NoError(t, err)

	assert.NotEmpty(t, cfg.OrderedPoolConfig)
	assert.Equal(t, string(OperationPolicySelectorRoute), cfg.RouteConfig.Type)
	known := map[Policy]bool{
		OperationPolicySelectorRoute: true, AllFastestRoute: true, MissFailoverRoute: true,
		LocalRoute: true, DefaultRoute: true,
	}
	for op, policy := range cfg.RouteConfig.OperationPolicies {
		assert.True(t, known[policy], "operation %q has unknown policy %q", op, policy)
	}
}

func TestParseRouterConfig_Errors(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
		is   error
	}{
		{
			name: "missing_file",
			path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope.yaml") },
			is:   os.ErrNotExist,
		},
		{
			name: "directory",
			path: func(t *testing.T) string { return t.TempDir() },
		},
		{
			name: "invalid_yaml",
			path: func(t *testing.T) string { return writeConfig(t, "orderedPool: [a, b\nroute: {") },
		},
		{
			name: "wrong_type_ordered_pool",
			path: func(t *testing.T) string { return writeConfig(t, "orderedPool:\n  a: b\n") },
		},
		{
			name: "wrong_type_route",
			path: func(t *testing.T) string { return writeConfig(t, "route: [1, 2]\n") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseRouterConfig(tt.path(t))
			require.Error(t, err)
			if tt.is != nil {
				require.ErrorIs(t, err, tt.is)
			}
			assert.Nil(t, cfg)
		})
	}
}
