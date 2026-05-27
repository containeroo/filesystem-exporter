package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/containeroo/filesystem-exporter/internal/exporter"
	"github.com/containeroo/filesystem-exporter/internal/usage"
)

type blockingScanner struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingScanner) Scan(ctx context.Context) (usage.ScanResult, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}

	select {
	case <-s.release:
		return usage.ScanResult{
			Usages: []usage.PathUsage{
				{Path: "/data", CapacityBytes: 1000, AvailableBytes: 400, UsedBytes: 600},
			},
		}, nil
	case <-ctx.Done():
		return usage.ScanResult{}, ctx.Err()
	}
}

func TestRunServesHealthAndReadinessWhileInitialCollectionIsRunning(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	scanner := &blockingScanner{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}

	originalNewPathScanner := newPathScanner
	newPathScanner = func(string, bool, int) exporter.Scanner {
		return scanner
	}
	defer func() {
		newPathScanner = originalNewPathScanner
	}()

	originalListen := listen
	listenAddrCh := make(chan string, 1)
	listen = func(network, address string) (net.Listener, error) {
		ln, err := net.Listen(network, address)
		if err != nil {
			return nil, err
		}

		listenAddrCh <- ln.Addr().String()
		return ln, nil
	}
	defer func() {
		listen = originalListen
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- Run(ctx, Config{
			ListenAddress: "127.0.0.1:0",
			MetricsPath:   "/metrics",
			Filesystem: FilesystemConfig{
				Path:            root,
				ScanConcurrency: 1,
			},
			Collector: CollectorConfig{
				Interval: time.Hour,
				Timeout:  time.Minute,
			},
		}, logger)
	}()

	listenAddr := waitForValue(t, listenAddrCh)
	<-scanner.started

	assertStatusEventually(t, "http://"+listenAddr+"/-/healthy", http.StatusOK)
	assertStatusEventually(t, "http://"+listenAddr+"/-/ready", http.StatusOK)

	cancel()

	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run() to return")
	}
}

func TestRunServesMetricsFromRealFilesystemBytes(t *testing.T) {
	root := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mustMkdirAll(t, filepath.Join(root, "archive", "nested"))
	mustMkdirAll(t, filepath.Join(root, "uploads"))
	mustWriteFileWithSize(t, filepath.Join(root, "root.bin"), 11)
	mustWriteFileWithSize(t, filepath.Join(root, "archive", "a.bin"), 13)
	mustWriteFileWithSize(t, filepath.Join(root, "archive", "nested", "b.bin"), 17)
	mustWriteFileWithSize(t, filepath.Join(root, "uploads", "c.bin"), 19)

	if err := os.Symlink(filepath.Join(root, "archive", "a.bin"), filepath.Join(root, "archive", "link.bin")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	originalListen := listen
	listenAddrCh := make(chan string, 1)
	listen = func(network, address string) (net.Listener, error) {
		ln, err := net.Listen(network, address)
		if err != nil {
			return nil, err
		}

		listenAddrCh <- ln.Addr().String()
		return ln, nil
	}
	defer func() {
		listen = originalListen
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- Run(ctx, Config{
			ListenAddress: "127.0.0.1:0",
			MetricsPath:   "/metrics",
			Filesystem: FilesystemConfig{
				Path:            root,
				ReportChildDirs: true,
				ScanConcurrency: 2,
			},
			Collector: CollectorConfig{
				Interval: time.Hour,
				Timeout:  time.Minute,
			},
		}, logger)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runErrCh:
			if err != nil {
				t.Errorf("Run() error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("timed out waiting for Run() to return")
		}
	})

	listenAddr := waitForValue(t, listenAddrCh)
	metricsURL := "http://" + listenAddr + "/metrics"
	families := waitForCollectedMetrics(t, metricsURL, map[string]string{
		"root_path": root,
	})

	assertScrapedMetricValue(t, families, "filesystem_exporter_path_used_bytes", map[string]string{
		"root_path": root,
		"path":      filepath.ToSlash(root),
	}, 60)
	assertScrapedMetricValue(t, families, "filesystem_exporter_path_used_bytes", map[string]string{
		"root_path": root,
		"path":      filepath.ToSlash(filepath.Join(root, "archive")),
	}, 30)
	assertScrapedMetricValue(t, families, "filesystem_exporter_path_used_bytes", map[string]string{
		"root_path": root,
		"path":      filepath.ToSlash(filepath.Join(root, "uploads")),
	}, 19)
}

func TestRunFailsFastWhenFilesystemPathIsUnavailable(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := Config{
		ListenAddress: "127.0.0.1:0",
		MetricsPath:   "/metrics",
		Filesystem: FilesystemConfig{
			Path:            "/path/that/does/not/exist",
			ScanConcurrency: 1,
		},
		Collector: CollectorConfig{
			Interval: time.Hour,
			Timeout:  time.Minute,
		},
	}

	err := Run(context.Background(), cfg, logger)
	if err == nil {
		t.Fatal("expected Run() to fail, got nil")
	}
	if !strings.Contains(err.Error(), "validate filesystem path") {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func waitForCollectedMetrics(t *testing.T, url string, labels map[string]string) map[string]*dto.MetricFamily {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		families, err := scrapeMetricFamilies(url)
		if err == nil {
			if value, ok := metricValue(families, "filesystem_exporter_collect_success", labels); ok && value == 1 {
				return families
			}
		} else {
			lastErr = err
		}

		time.Sleep(50 * time.Millisecond)
	}

	if lastErr != nil {
		t.Fatalf("timed out waiting for successful collection from %s: last scrape error: %v", url, lastErr)
	}
	t.Fatalf("timed out waiting for successful collection from %s", url)

	return nil
}

func scrapeMetricFamilies(url string) (map[string]*dto.MetricFamily, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain; version=0.0.4")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned status %d: %s", url, resp.StatusCode, buf.String())
	}

	parser := expfmt.NewTextParser(model.LegacyValidation)
	return parser.TextToMetricFamilies(bytes.NewReader(buf.Bytes()))
}

func assertScrapedMetricValue(t *testing.T, families map[string]*dto.MetricFamily, name string, labels map[string]string, want float64) {
	t.Helper()

	got, ok := metricValue(families, name, labels)
	if !ok {
		t.Fatalf("metric %s with labels %v not found", name, labels)
	}
	if got != want {
		t.Fatalf("metric %s with labels %v = %v, want %v", name, labels, got, want)
	}
}

func metricValue(families map[string]*dto.MetricFamily, name string, labels map[string]string) (float64, bool) {
	family, ok := families[name]
	if !ok {
		return 0, false
	}

	for _, metric := range family.GetMetric() {
		if !labelsMatch(metric, labels) {
			continue
		}
		if gauge := metric.GetGauge(); gauge != nil {
			return gauge.GetValue(), true
		}
	}

	return 0, false
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

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", dir, err)
	}
}

func mustWriteFileWithSize(t *testing.T, filePath string, size int) {
	t.Helper()

	if err := os.WriteFile(filePath, make([]byte, size), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", filePath, err)
	}
}

func waitForValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()

	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for value")
	}

	var zero T
	return zero
}

func assertStatusEventually(t *testing.T, url string, want int) {
	t.Helper()

	client := &http.Client{
		Timeout: 200 * time.Millisecond,
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s to return status %d", url, want)
}
