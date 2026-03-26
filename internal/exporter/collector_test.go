package exporter

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/containeroo/filesystem-exporter/internal/usage"
)

type fakeScanner struct {
	result usage.ScanResult
	err    error
}

func (f *fakeScanner) Scan(context.Context) (usage.ScanResult, error) {
	if f.err != nil {
		return usage.ScanResult{}, f.err
	}
	return f.result, nil
}

func TestCollectorExportsMetricsFromLastSuccessfulSnapshot(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := &fakeScanner{
		result: usage.ScanResult{
			Usages: []usage.PathUsage{
				{Path: "/data", CapacityBytes: 1000, AvailableBytes: 400, UsedBytes: 600},
				{Path: "/data/archive", CapacityBytes: 1000, AvailableBytes: 400, UsedBytes: 250},
			},
		},
	}

	monitor := NewMonitor(scanner, time.Minute, time.Second, logger)
	if err := monitor.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewCollector(monitor, prometheus.Labels{
		"root_path": "/data",
	}))

	assertMetricValue(t, registry, "filesystem_exporter_collect_success", map[string]string{
		"root_path": "/data",
	}, 1)
	assertMetricValue(t, registry, "filesystem_exporter_path_used_bytes", map[string]string{
		"root_path": "/data",
		"path":      "/data/archive",
	}, 250)
	assertMetricValue(t, registry, "filesystem_exporter_path_capacity_bytes", map[string]string{
		"root_path": "/data",
		"path":      "/data",
	}, 1000)
}

func TestMonitorKeepsLastSuccessfulSnapshotOnFailure(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := &fakeScanner{
		result: usage.ScanResult{
			Usages: []usage.PathUsage{
				{Path: "/data", CapacityBytes: 1000, AvailableBytes: 500, UsedBytes: 500},
			},
		},
	}

	monitor := NewMonitor(scanner, time.Minute, time.Second, logger)
	if err := monitor.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	scanner.err = context.DeadlineExceeded
	if err := monitor.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error, got nil")
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewCollector(monitor, prometheus.Labels{
		"root_path": "/data",
	}))

	assertMetricValue(t, registry, "filesystem_exporter_collect_success", map[string]string{
		"root_path": "/data",
	}, 0)
	assertMetricValue(t, registry, "filesystem_exporter_path_used_bytes", map[string]string{
		"root_path": "/data",
		"path":      "/data",
	}, 500)
}

func TestMonitorLogsInitialSuccessAndDebugLifecycle(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	scanner := &fakeScanner{
		result: usage.ScanResult{
			Usages: []usage.PathUsage{
				{Path: "/data", CapacityBytes: 1000, AvailableBytes: 500, UsedBytes: 500},
			},
			Stats: usage.ScanStats{
				DirectoriesSeen:     3,
				FilesStatted:        9,
				IgnoredMissingPaths: 1,
			},
		},
	}

	monitor := NewMonitor(scanner, time.Minute, time.Second, logger)
	if err := monitor.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	logOutput := logBuf.String()
	for _, want := range []string{
		"level=DEBUG msg=\"starting collection\"",
		"level=DEBUG msg=\"collection completed\"",
		"level=INFO msg=\"initial collection succeeded\"",
		"reported_paths=1",
		"directories_seen=3",
		"files_statted=9",
		"ignored_missing_paths=1",
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("expected log output to contain %q, got %q", want, logOutput)
		}
	}
}

func TestMonitorLogsTimeoutsExplicitly(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	scanner := &fakeScanner{
		err: context.DeadlineExceeded,
	}

	monitor := NewMonitor(scanner, time.Minute, time.Second, logger)
	if err := monitor.Refresh(context.Background()); err == nil {
		t.Fatal("expected Refresh() error, got nil")
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "level=ERROR msg=\"collection timed out\"") {
		t.Fatalf("expected timeout log, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "timeout=1s") {
		t.Fatalf("expected timeout field in log output, got %q", logOutput)
	}
}

func assertMetricValue(t *testing.T, registry *prometheus.Registry, name string, labels map[string]string, want float64) {
	t.Helper()

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}

	for _, family := range families {
		if family.GetName() != name {
			continue
		}

		for _, metric := range family.GetMetric() {
			if labelsMatch(metric, labels) {
				if got := metric.GetGauge().GetValue(); got != want {
					t.Fatalf("metric %s = %v, want %v", name, got, want)
				}
				return
			}
		}
	}

	t.Fatalf("metric %s with labels %v not found", name, labels)
}

func labelsMatch(metric *dto.Metric, want map[string]string) bool {
	if len(want) == 0 {
		return len(metric.GetLabel()) == 0
	}

	if len(metric.GetLabel()) != len(want) {
		return false
	}

	for _, pair := range metric.GetLabel() {
		value, ok := want[pair.GetName()]
		if !ok || value != pair.GetValue() {
			return false
		}
	}

	return true
}
