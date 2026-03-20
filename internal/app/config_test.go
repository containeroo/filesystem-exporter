package app

import (
	"strings"
	"testing"
	"time"
)

func TestParseConfigDefaultsAndNormalization(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig([]string{"--filesystem.path=/srv/data", "--web.metrics-path=custom"})
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}

	if cfg.Filesystem.Path != "/srv/data" {
		t.Fatalf("expected filesystem path /srv/data, got %q", cfg.Filesystem.Path)
	}

	if cfg.MetricsPath != "/custom" {
		t.Fatalf("expected normalized metrics path /custom, got %q", cfg.MetricsPath)
	}

	if cfg.Collector.Interval != 5*time.Minute {
		t.Fatalf("expected default interval 5m, got %s", cfg.Collector.Interval)
	}

	if cfg.Collector.Timeout != 2*time.Minute {
		t.Fatalf("expected default timeout 2m, got %s", cfg.Collector.Timeout)
	}
}

func TestParseConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "empty filesystem path",
			args:    []string{"--filesystem.path="},
			wantErr: "-filesystem.path is required",
		},
		{
			name:    "relative filesystem path",
			args:    []string{"--filesystem.path=data"},
			wantErr: "-filesystem.path must be an absolute path",
		},
		{
			name:    "invalid interval",
			args:    []string{"--filesystem.path=/data", "--collector.interval=0s"},
			wantErr: "-collector.interval must be greater than 0",
		},
		{
			name:    "invalid timeout",
			args:    []string{"--filesystem.path=/data", "--collector.timeout=0s"},
			wantErr: "-collector.timeout must be greater than 0",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseConfig(tc.args)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}
