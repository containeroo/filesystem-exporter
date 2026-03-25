package exporter

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/containeroo/filesystem-exporter/internal/usage"
)

type fakeScanner struct {
	results []usage.PathUsage
	err     error
}

func (f *fakeScanner) Scan(context.Context) ([]usage.PathUsage, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func TestCollectorExportsMetricsFromLastSuccessfulSnapshot(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := &fakeScanner{
		results: []usage.PathUsage{
			{Path: "/data", CapacityBytes: 1000, AvailableBytes: 400, UsedBytes: 600},
			{Path: "/data/archive", CapacityBytes: 1000, AvailableBytes: 400, UsedBytes: 250},
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
		results: []usage.PathUsage{
			{Path: "/data", CapacityBytes: 1000, AvailableBytes: 500, UsedBytes: 500},
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
