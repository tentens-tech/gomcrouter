package config

import (
	"context"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"time"
)

type App struct {
	LogLevel            string        `mapstructure:"log-level"`
	RouterConfigPath    string        `mapstructure:"router-config"`
	UpstreamConnections int           `mapstructure:"upstream-connections"`
	TimeoutMillis       int64         `mapstructure:"timeout-ms"`
	HealthCheckInterval time.Duration `mapstructure:"healthcheck-interval"`
	Listen              string        `mapstructure:"listen"`
	ListenMetricsAddr   string        `mapstructure:"metrics-listen-addr"`
	ServerEventLoops    int           `mapstructure:"server-event-loops"`
	DnsCacheTTL         time.Duration `mapstructure:"dns-cache-ttl"`
	BufferSize          int           `mapstructure:"buffer-size"`

	TimeoutNs int64
}

type AppContext struct {
	context.Context
	Logger *zap.SugaredLogger
	Config *App
}

func PrepareAppContext(ctx context.Context, flagSet *pflag.FlagSet) (*AppContext, error) {
	appConfig, err := parseAppConfig(flagSet)
	if err != nil {
		return nil, err
	}

	logger, err := createLogger(appConfig)
	if err != nil {
		return nil, err
	}

	appCtx := AppContext{
		Context: ctx,
		Logger:  logger,
		Config:  appConfig,
	}

	return &appCtx, nil
}

func parseAppConfig(flagSet *pflag.FlagSet) (*App, error) {
	err := viper.BindPFlags(flagSet)
	if err != nil {
		return nil, err
	}

	var appConfig App
	if err = viper.Unmarshal(&appConfig); err != nil {
		return nil, err
	}

	appConfig.TimeoutNs = appConfig.TimeoutMillis * 1_000_000

	return &appConfig, nil
}

func createLogger(appConfig *App) (*zap.SugaredLogger, error) {
	level, err := zapcore.ParseLevel(appConfig.LogLevel)
	if err != nil {
		return nil, err
	}

	logger, err := zap.NewProduction(zap.WithCaller(false), zap.IncreaseLevel(level))
	if err != nil {
		return nil, err
	}

	sugarLogger := logger.Sugar()

	return sugarLogger, nil
}
