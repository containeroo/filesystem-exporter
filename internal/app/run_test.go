package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/containeroo/filesystem-exporter/internal/exporter"
	"github.com/containeroo/filesystem-exporter/internal/usage"
)

type blockingScanner struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingScanner) Scan(ctx context.Context) ([]usage.PathUsage, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}

	select {
	case <-s.release:
		return []usage.PathUsage{
			{Path: "/data", CapacityBytes: 1000, AvailableBytes: 400, UsedBytes: 600},
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
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
