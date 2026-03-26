package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/containeroo/filesystem-exporter/internal/app"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{}))

	cfg, err := app.ParseConfig(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}

		logger.Error("failed to parse flags", "err", err)
		os.Exit(2)
	}

	logLevel, err := app.ParseLogLevel(cfg.LogLevel)
	if err != nil {
		logger.Error("invalid log level", "log_level", cfg.LogLevel, "err", err)
		os.Exit(2)
	}

	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg, logger); err != nil {
		logger.Error("exporter exited with error", "err", err)
		os.Exit(1)
	}
}
