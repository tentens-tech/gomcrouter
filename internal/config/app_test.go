package config

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

type ctxKey struct{}

// newFlagSet mirrors the flags declared in cmd/root.go.
func newFlagSet(t *testing.T) *pflag.FlagSet {
	t.Helper()
	// PrepareAppContext binds into the global viper instance; reset it so
	// tests do not leak configuration into each other.
	viper.Reset()
	t.Cleanup(viper.Reset)

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("log-level", "info", "")
	fs.String("router-config", "router.yaml", "")
	fs.String("listen", "tcp://:8080", "")
	fs.String("metrics-listen-addr", ":9090", "")
	fs.Duration("healthcheck-interval", 10*time.Second, "")
	fs.Int("upstream-connections", 2, "")
	fs.Int64("timeout-ms", 1000, "")
	fs.Int("server-event-loops", 1, "")
	fs.Duration("dns-cache-ttl", 30*time.Second, "")
	fs.Int("buffer-size", 1024*1024, "")
	return fs
}

func TestPrepareAppContext_Defaults(t *testing.T) {
	fs := newFlagSet(t)
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")

	appCtx, err := PrepareAppContext(ctx, fs)
	require.NoError(t, err)
	require.NotNil(t, appCtx)

	assert.Equal(t, "v", appCtx.Value(ctxKey{}), "parent context must be embedded")
	require.NotNil(t, appCtx.Logger)
	assert.True(t, appCtx.Logger.Desugar().Core().Enabled(zapcore.InfoLevel))
	assert.False(t, appCtx.Logger.Desugar().Core().Enabled(zapcore.DebugLevel))

	assert.Equal(t, &App{
		LogLevel:            "info",
		RouterConfigPath:    "router.yaml",
		UpstreamConnections: 2,
		TimeoutMillis:       1000,
		HealthCheckInterval: 10 * time.Second,
		Listen:              "tcp://:8080",
		ListenMetricsAddr:   ":9090",
		ServerEventLoops:    1,
		DnsCacheTTL:         30 * time.Second,
		BufferSize:          1024 * 1024,
		TimeoutNs:           1000 * int64(time.Millisecond),
	}, appCtx.Config)
}

func TestPrepareAppContext_ParsedFlags(t *testing.T) {
	fs := newFlagSet(t)
	require.NoError(t, fs.Parse([]string{
		"--log-level=error",
		"--router-config=/etc/gomcrouter/router.yaml",
		"--listen=tcp://127.0.0.1:11211",
		"--metrics-listen-addr=127.0.0.1:9100",
		"--healthcheck-interval=250ms",
		"--upstream-connections=8",
		"--timeout-ms=15",
		"--server-event-loops=4",
		"--dns-cache-ttl=1m",
		"--buffer-size=4096",
	}))

	appCtx, err := PrepareAppContext(context.Background(), fs)
	require.NoError(t, err)

	cfg := appCtx.Config
	assert.Equal(t, "error", cfg.LogLevel)
	assert.Equal(t, "/etc/gomcrouter/router.yaml", cfg.RouterConfigPath)
	assert.Equal(t, "tcp://127.0.0.1:11211", cfg.Listen)
	assert.Equal(t, "127.0.0.1:9100", cfg.ListenMetricsAddr)
	assert.Equal(t, 250*time.Millisecond, cfg.HealthCheckInterval)
	assert.Equal(t, 8, cfg.UpstreamConnections)
	assert.Equal(t, int64(15), cfg.TimeoutMillis)
	assert.Equal(t, int64(15*time.Millisecond), cfg.TimeoutNs)
	assert.Equal(t, 4, cfg.ServerEventLoops)
	assert.Equal(t, time.Minute, cfg.DnsCacheTTL)
	assert.Equal(t, 4096, cfg.BufferSize)

	core := appCtx.Logger.Desugar().Core()
	assert.True(t, core.Enabled(zapcore.ErrorLevel))
	assert.False(t, core.Enabled(zapcore.WarnLevel))
}

func TestPrepareAppContext_DebugLevel(t *testing.T) {
	fs := newFlagSet(t)
	require.NoError(t, fs.Parse([]string{"--log-level=debug"}))

	appCtx, err := PrepareAppContext(context.Background(), fs)
	require.NoError(t, err)
	assert.Equal(t, "debug", appCtx.Config.LogLevel)
	assert.True(t, appCtx.Logger.Desugar().Core().Enabled(zapcore.DebugLevel))
}

func TestPrepareAppContext_InvalidLogLevel(t *testing.T) {
	fs := newFlagSet(t)
	require.NoError(t, fs.Parse([]string{"--log-level=loud"}))

	appCtx, err := PrepareAppContext(context.Background(), fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loud")
	assert.Nil(t, appCtx)
}

func TestPrepareAppContext_UnmarshalError(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	// a flag whose value cannot be decoded into App.TimeoutMillis (int64)
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("log-level", "info", "")
	fs.String("timeout-ms", "not-a-number", "")

	appCtx, err := PrepareAppContext(context.Background(), fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout-ms")
	assert.Nil(t, appCtx)
}
