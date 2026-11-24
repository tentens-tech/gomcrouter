package cmd

import (
	"context"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/tentens-tech/gomcrouter/internal/config"
	"github.com/tentens-tech/gomcrouter/internal/observability/metric"
	"github.com/tentens-tech/gomcrouter/internal/router"
	"github.com/tentens-tech/gomcrouter/internal/server"
	"github.com/tentens-tech/gomcrouter/internal/upstream/io"
	"github.com/tentens-tech/gomcrouter/version"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	RootCmdName = "gomcrouter"
)

func NewRootCmd() *cobra.Command {
	rootCmd := cobra.Command{
		Use:     RootCmdName,
		Short:   "High-performance memcached router",
		Version: version.Version,
		RunE:    rootCmdDef,
	}
	rootCmd.PersistentFlags().StringP("log-level", "", "info", "log level")
	rootCmd.PersistentFlags().StringP("router-config", "f", "router.yaml", "router config file path")
	rootCmd.PersistentFlags().StringP("listen", "l", "tcp://:8080", "router listen")
	rootCmd.PersistentFlags().StringP("metrics-listen-addr", "L", ":9090", "metrics listen address")
	rootCmd.PersistentFlags().DurationP("healthcheck-interval", "H", time.Second*10, "healthcheck interval")
	rootCmd.PersistentFlags().IntP("upstream-connections", "C", 2, "active upstream connections")
	rootCmd.PersistentFlags().Int64P("timeout-ms", "r", 1000, "timeout milliseconds")
	rootCmd.PersistentFlags().IntP("server-event-loops", "n", 1, "number of server event loops")
	rootCmd.PersistentFlags().DurationP("dns-cache-ttl", "t", time.Second*30, "dns cache ttl")
	rootCmd.PersistentFlags().IntP("buffer-size", "b", 1024*1024, "upstream socket read/write buffer size in bytes")
	return &rootCmd
}

func rootCmdDef(cmd *cobra.Command, _ []string) error {
	// The command context is cancelled on SIGINT, on SIGTERM (docker stop,
	// Kubernetes) or by the caller of ExecuteContext, which triggers a
	// graceful shutdown.
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	appCtx, err := config.PrepareAppContext(ctx, cmd.Flags())
	if err != nil {
		return err
	}

	appCtx.Logger.Infof("gomcrouter %s", version.Version)
	appCtx.Logger.Info("log level: ", appCtx.Config.LogLevel)
	appCtx.Logger.Infof("listen: %s", appCtx.Config.Listen)
	appCtx.Logger.Infof("metrics listen: http://%s", appCtx.Config.ListenMetricsAddr)
	appCtx.Logger.Infof("router config: %s", appCtx.Config.RouterConfigPath)
	appCtx.Logger.Infof("dns cache ttl: %s", appCtx.Config.DnsCacheTTL)
	appCtx.Logger.Infof("upstream connections: %d", appCtx.Config.UpstreamConnections)
	appCtx.Logger.Infof("request timeout milliseconds: %d", appCtx.Config.TimeoutMillis)
	appCtx.Logger.Infof("upstream healthcheck interval: %s", appCtx.Config.HealthCheckInterval.String())
	appCtx.Logger.Infof("buffer size: %d", appCtx.Config.BufferSize)
	appCtx.Logger.Infof("server threads: %d", appCtx.Config.ServerEventLoops)
	appCtx.Logger.Info("starting")

	// Fail on configuration errors before anything is started, so that an
	// error return does not leave the metrics server running.
	routerConfig, err := config.ParseRouterConfig(appCtx.Config.RouterConfigPath)
	if err != nil {
		return err
	}

	eng, err := io.NewEngine(appCtx)
	if err != nil {
		return err
	}

	metricsServer := metric.NewServer(ctx, appCtx.Config.ListenMetricsAddr)
	go metricsServer.Listen()

	r := router.New(appCtx, *routerConfig, eng)
	srv := server.NewServer(appCtx, r)

	metric.Collector.Serve()
	go srv.Serve()

	go func() {
		<-ctx.Done()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second*5)
		defer shutdownCancel()

		appCtx.Logger.Info("shutting down")

		metricsServer.Stop(shutdownCtx)
		srv.Stop(shutdownCtx)

		eng.Stop()
	}()

	eng.Start()

	return nil
}

func Execute() {
	rootCmd := NewRootCmd()
	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
