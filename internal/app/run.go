package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/containeroo/filesystem-exporter/internal/exporter"
	"github.com/containeroo/filesystem-exporter/internal/scanner"
)

var newPathScanner = func(rootPath string, reportChildDirs bool, scanConcurrency int) exporter.Scanner {
	return scanner.NewPathScanner(rootPath, reportChildDirs, scanConcurrency)
}

var listen = net.Listen

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	if err := scanner.ValidatePath(cfg.Filesystem.Path); err != nil {
		return fmt.Errorf("validate filesystem path %s: %w", cfg.Filesystem.Path, err)
	}

	scan := newPathScanner(cfg.Filesystem.Path, cfg.Filesystem.ReportChildDirs, cfg.Filesystem.ScanConcurrency)
	monitor := exporter.NewMonitor(scan, cfg.Collector.Interval, cfg.Collector.Timeout, logger)

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		exporter.NewCollector(monitor, prometheus.Labels{
			"root_path": cfg.Filesystem.Path,
		}),
	)

	mux := http.NewServeMux()
	mux.Handle(cfg.MetricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))
	mux.HandleFunc("/-/healthy", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := listen("tcp", cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddress, err)
	}

	serverErrCh := make(chan error, 1)
	go func() {
		logger.Info(
			"starting filesystem exporter",
			"listen_address", cfg.ListenAddress,
			"metrics_path", cfg.MetricsPath,
			"filesystem_path", cfg.Filesystem.Path,
			"filesystem_report_child_dirs", cfg.Filesystem.ReportChildDirs,
			"filesystem_scan_concurrency", cfg.Filesystem.ScanConcurrency,
		)
		serverErrCh <- server.Serve(listener)
	}()

	go func() {
		if err := monitor.Refresh(ctx); err != nil {
			logger.Error("initial collection failed", "filesystem_path", cfg.Filesystem.Path, "err", err)
		}
		monitor.Run(ctx)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()

		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	case err := <-serverErrCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	}
}
