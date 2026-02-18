package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"github.com/things-go/go-socks5"
	"github.com/xlab/closer"

	"github.com/entwico/podproxy/internal/config"
	"github.com/entwico/podproxy/internal/kube"
	"github.com/entwico/podproxy/internal/proxy"
	"github.com/entwico/podproxy/internal/version"
)

func main() {
	showVersion := pflag.Bool("version", false, "print version information and exit")
	configPath := pflag.String("config", "", "path to YAML config file (default: config.yaml in working directory)")

	pflag.Parse()

	if *showVersion {
		version.Print()
		return
	}

	if *configPath == "" {
		*configPath = "config.yaml"
	}

	cfg, clusters, err := config.LoadConfig(*configPath)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	logger := config.Logger

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	defer closer.Close()

	forwarders := make(map[string]*kube.PortForwarder, len(clusters))

	for _, rc := range clusters {
		restCfg, clientset, err := kube.NewKubeClient(rc.Kubeconfig, rc.Context)
		if err != nil {
			logger.Warn("skipping cluster due to client error", "cluster", rc.Name, "error", err)
			continue
		}

		forwarders[rc.Name] = &kube.PortForwarder{
			Config:           restCfg,
			Clientset:        clientset,
			DefaultNamespace: rc.Namespace,
			Logger:           logger.With("cluster", rc.Name),
		}
	}

	if len(forwarders) == 0 {
		logger.Error("no usable clusters found")
		os.Exit(1)
	}

	dialer := &kube.ClusterDialer{Forwarders: forwarders}

	server := socks5.NewServer(
		socks5.WithDial(dialer.DialContext),
		socks5.WithResolver(kube.Resolver{}),
		socks5.WithLogger(&slogErrorLogger{logger: logger.With("component", "socks5")}),
	)

	logger.Info("starting socks5 proxy server", "addr", cfg.ListenAddress)

	go func() {
		if err := server.ListenAndServe("tcp", cfg.ListenAddress); err != nil {
			logger.Error("socks5 server failed", "error", err)
			stop()
		}
	}()

	if cfg.HTTPListenAddress != "" {
		httpProxy := &proxy.HTTPProxy{
			DialContext: dialer.DialContext,
			Logger:      logger.With("component", "http-proxy"),
		}
		defer httpProxy.Close()

		httpServer := &http.Server{
			Addr:              cfg.HTTPListenAddress,
			Handler:           httpProxy,
			ReadHeaderTimeout: 10 * time.Second,
		}

		logger.Info("starting http proxy server", "addr", cfg.HTTPListenAddress)
		gracefulShutdown(ctx, httpServer, logger, "http server")

		go func() {
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("http connect server failed", "error", err)
				stop()
			}
		}()
	}

	if cfg.PACListenAddress != "" {
		pacServer := &proxy.PACServer{
			ClusterNames:     clusterNames(clusters),
			SOCKSAddress:     cfg.ListenAddress,
			HTTPProxyAddress: cfg.HTTPListenAddress,
		}

		pacHTTPServer := &http.Server{
			Addr:              cfg.PACListenAddress,
			Handler:           pacServer,
			ReadHeaderTimeout: 10 * time.Second,
		}

		logger.Info("starting proxy auto-configuration server", "addr", cfg.PACListenAddress, "clusters", clusterNames(clusters))
		gracefulShutdown(ctx, pacHTTPServer, logger, "pac server")

		go func() {
			if err := pacHTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("pac server failed", "error", err)
				stop()
			}
		}()
	}

	<-ctx.Done()
	logger.Info("shutting down")
}

// slogErrorLogger adapts *slog.Logger to the socks5.Logger interface.
type slogErrorLogger struct {
	logger *slog.Logger
}

func (l *slogErrorLogger) Errorf(format string, args ...any) {
	l.logger.Error(fmt.Sprintf(format, args...))
}

// gracefulShutdown starts a background goroutine that shuts down the server
// when the context is cancelled.
func gracefulShutdown(ctx context.Context, server *http.Server, logger *slog.Logger, name string) {
	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error(name+" shutdown error", "error", err)
		}
	}()
}

func clusterNames(clusters []config.ResolvedCluster) []string {
	names := make([]string, len(clusters))
	for i, rc := range clusters {
		names[i] = rc.Name
	}

	return names
}
