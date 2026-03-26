package exporter

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/containeroo/filesystem-exporter/internal/usage"
)

type Scanner interface {
	Scan(ctx context.Context) (usage.ScanResult, error)
}

type Snapshot struct {
	Usages           []usage.PathUsage
	CollectSuccess   float64
	CollectDuration  float64
	CollectTimestamp float64
}

type Monitor struct {
	logger   *slog.Logger
	scanner  Scanner
	interval time.Duration
	timeout  time.Duration

	mu       sync.RWMutex
	snapshot Snapshot
}

func NewMonitor(scanner Scanner, interval, timeout time.Duration, logger *slog.Logger) *Monitor {
	return &Monitor{
		logger:   logger,
		scanner:  scanner,
		interval: interval,
		timeout:  timeout,
	}
}

func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.Refresh(ctx); err != nil && errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return
			}
		}
	}
}

func (m *Monitor) Refresh(ctx context.Context) error {
	start := time.Now()
	m.logger.Debug("starting collection")

	scanCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	result, err := m.scanner.Scan(scanCtx)
	now := time.Now()
	duration := now.Sub(start)

	if duration >= m.timeout*8/10 {
		m.logger.Warn("collection is slow", "duration", duration, "timeout", m.timeout)
	}

	m.mu.Lock()
	previousCollected := m.snapshot.CollectTimestamp > 0
	previousSuccess := m.snapshot.CollectSuccess == 1

	m.snapshot.CollectDuration = duration.Seconds()
	m.snapshot.CollectTimestamp = float64(now.Unix())

	if err != nil {
		m.snapshot.CollectSuccess = 0
		m.mu.Unlock()

		switch {
		case errors.Is(err, context.DeadlineExceeded):
			m.logger.Error("collection timed out", "duration", duration, "timeout", m.timeout, "err", err)
		case errors.Is(err, context.Canceled):
		default:
			m.logger.Error("collection failed", "duration", duration, "err", err)
		}

		return err
	}

	m.snapshot.CollectSuccess = 1
	m.snapshot.Usages = cloneUsages(result.Usages)
	m.mu.Unlock()

	successAttrs := []any{
		"duration", duration,
		"reported_paths", len(result.Usages),
		"directories_seen", result.Stats.DirectoriesSeen,
		"files_statted", result.Stats.FilesStatted,
		"ignored_missing_paths", result.Stats.IgnoredMissingPaths,
	}

	m.logger.Debug("collection completed", successAttrs...)
	if !previousCollected {
		m.logger.Info("initial collection succeeded", successAttrs...)
	} else if !previousSuccess {
		m.logger.Info("collection recovered", successAttrs...)
	}

	return nil
}

func (m *Monitor) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return Snapshot{
		Usages:           cloneUsages(m.snapshot.Usages),
		CollectSuccess:   m.snapshot.CollectSuccess,
		CollectDuration:  m.snapshot.CollectDuration,
		CollectTimestamp: m.snapshot.CollectTimestamp,
	}
}

type Collector struct {
	monitor *Monitor

	capacityDesc         *prometheus.Desc
	availableDesc        *prometheus.Desc
	usedDesc             *prometheus.Desc
	collectSuccessDesc   *prometheus.Desc
	collectDurationDesc  *prometheus.Desc
	collectTimestampDesc *prometheus.Desc
}

func NewCollector(monitor *Monitor, constLabels prometheus.Labels) *Collector {
	return &Collector{
		monitor: monitor,
		capacityDesc: prometheus.NewDesc(
			"filesystem_exporter_path_capacity_bytes",
			"Total capacity in bytes for the scanned path or underlying filesystem.",
			[]string{"path"},
			constLabels,
		),
		availableDesc: prometheus.NewDesc(
			"filesystem_exporter_path_available_bytes",
			"Available capacity in bytes for the scanned path or underlying filesystem.",
			[]string{"path"},
			constLabels,
		),
		usedDesc: prometheus.NewDesc(
			"filesystem_exporter_path_used_bytes",
			"Used bytes contained in the scanned path subtree.",
			[]string{"path"},
			constLabels,
		),
		collectSuccessDesc: prometheus.NewDesc(
			"filesystem_exporter_collect_success",
			"Whether the last collection run succeeded.",
			nil,
			constLabels,
		),
		collectDurationDesc: prometheus.NewDesc(
			"filesystem_exporter_collect_duration_seconds",
			"Duration of the last collection run in seconds.",
			nil,
			constLabels,
		),
		collectTimestampDesc: prometheus.NewDesc(
			"filesystem_exporter_collect_timestamp_seconds",
			"Unix timestamp of the last collection attempt.",
			nil,
			constLabels,
		),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.capacityDesc
	ch <- c.availableDesc
	ch <- c.usedDesc
	ch <- c.collectSuccessDesc
	ch <- c.collectDurationDesc
	ch <- c.collectTimestampDesc
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.monitor.Snapshot()

	ch <- prometheus.MustNewConstMetric(c.collectSuccessDesc, prometheus.GaugeValue, snapshot.CollectSuccess)
	ch <- prometheus.MustNewConstMetric(c.collectDurationDesc, prometheus.GaugeValue, snapshot.CollectDuration)
	ch <- prometheus.MustNewConstMetric(c.collectTimestampDesc, prometheus.GaugeValue, snapshot.CollectTimestamp)

	for _, pathUsage := range snapshot.Usages {
		ch <- prometheus.MustNewConstMetric(c.capacityDesc, prometheus.GaugeValue, pathUsage.CapacityBytes, pathUsage.Path)
		ch <- prometheus.MustNewConstMetric(c.availableDesc, prometheus.GaugeValue, pathUsage.AvailableBytes, pathUsage.Path)
		ch <- prometheus.MustNewConstMetric(c.usedDesc, prometheus.GaugeValue, pathUsage.UsedBytes, pathUsage.Path)
	}
}

func cloneUsages(values []usage.PathUsage) []usage.PathUsage {
	cloned := make([]usage.PathUsage, len(values))
	copy(cloned, values)
	return cloned
}
