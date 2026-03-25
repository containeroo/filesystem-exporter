package app

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	ListenAddress string
	MetricsPath   string
	Filesystem    FilesystemConfig
	Collector     CollectorConfig
}

type FilesystemConfig struct {
	Path            string
	ReportChildDirs bool
}

type CollectorConfig struct {
	Interval time.Duration
	Timeout  time.Duration
}

func ParseConfig(args []string) (Config, error) {
	cfg := Config{
		ListenAddress: ":9799",
		MetricsPath:   "/metrics",
		Filesystem: FilesystemConfig{
			Path: "/data",
		},
		Collector: CollectorConfig{
			Interval: 5 * time.Minute,
			Timeout:  2 * time.Minute,
		},
	}

	fs := flag.NewFlagSet("filesystem-exporter", flag.ContinueOnError)

	fs.StringVar(&cfg.ListenAddress, "web.listen-address", cfg.ListenAddress, "Address to listen on for web interface and telemetry.")
	fs.StringVar(&cfg.MetricsPath, "web.metrics-path", cfg.MetricsPath, "Path under which to expose Prometheus metrics.")
	fs.StringVar(&cfg.Filesystem.Path, "filesystem.path", cfg.Filesystem.Path, "Mounted filesystem path to scan.")
	fs.BoolVar(&cfg.Filesystem.ReportChildDirs, "filesystem.report-child-dirs", cfg.Filesystem.ReportChildDirs, "Whether to report metrics for immediate child directories under the mounted filesystem path.")
	fs.DurationVar(&cfg.Collector.Interval, "collector.interval", cfg.Collector.Interval, "How often the exporter refreshes usage data.")
	fs.DurationVar(&cfg.Collector.Timeout, "collector.timeout", cfg.Collector.Timeout, "Maximum time allowed for a single collection run.")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg.Filesystem.Path = normalizeFilesystemPath(cfg.Filesystem.Path)
	cfg.MetricsPath = normalizeMetricsPath(cfg.MetricsPath)

	if cfg.Filesystem.Path == "" {
		return Config{}, fmt.Errorf("flag -filesystem.path is required")
	}

	if !filepath.IsAbs(cfg.Filesystem.Path) {
		return Config{}, fmt.Errorf("flag -filesystem.path must be an absolute path")
	}

	if cfg.Collector.Interval <= 0 {
		return Config{}, fmt.Errorf("flag -collector.interval must be greater than 0")
	}

	if cfg.Collector.Timeout <= 0 {
		return Config{}, fmt.Errorf("flag -collector.timeout must be greater than 0")
	}

	return cfg, nil
}

func normalizeFilesystemPath(value string) string {
	cleaned := filepath.Clean(strings.TrimSpace(value))
	if cleaned == "." || cleaned == "" {
		return ""
	}
	return cleaned
}

func normalizeMetricsPath(value string) string {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" || cleaned == "/" {
		return "/metrics"
	}
	if !strings.HasPrefix(cleaned, "/") {
		return "/" + cleaned
	}
	return cleaned
}
