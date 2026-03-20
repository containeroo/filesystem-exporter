package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/containeroo/filesystem-exporter/internal/exporter"
	"github.com/containeroo/filesystem-exporter/internal/scanner"
)

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	scan := scanner.NewPathScanner(cfg.Filesystem.Path)
	monitor := exporter.NewMonitor(scan, cfg.Collector.Interval, cfg.Collector.Timeout, logger)

	if err := monitor.Refresh(ctx); err != nil {
		return fmt.Errorf("initial collection failed for %s: %w", cfg.Filesystem.Path, err)
	}

	go monitor.Run(ctx)

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

	serverErrCh := make(chan error, 1)
	go func() {
		logger.Info(
			"starting filesystem exporter",
			"listen_address", cfg.ListenAddress,
			"metrics_path", cfg.MetricsPath,
			"filesystem_path", cfg.Filesystem.Path,
		)
		serverErrCh <- server.ListenAndServe()
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
