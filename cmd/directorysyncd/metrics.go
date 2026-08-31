package main

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/Helppi/helppi-scim-go/directory"
)

// metrics is a deliberately tiny Prometheus exposition, so the reference client
// has no third-party dependencies at all. Replace it with your own registry.
//
// sync_lag_seconds is the one that matters: it is the SLI behind "a termination
// reaches the partner within N minutes", and it alerts on a stuck worker even
// when nothing is erroring.
type metrics struct {
	cycles    atomic.Int64
	failures  atomic.Int64
	created   atomic.Int64
	enabled   atomic.Int64
	disabled  atomic.Int64
	wroteBack atomic.Int64
	conflicts atomic.Int64
	malformed atomic.Int64
	alerts    atomic.Int64
	lastMs    atomic.Int64
}

func (m *metrics) record(s directory.Stats, err error) {
	m.cycles.Add(1)
	if err != nil {
		m.failures.Add(1)
	}
	m.created.Add(int64(s.Created))
	m.enabled.Add(int64(s.Enabled))
	m.disabled.Add(int64(s.Disabled))
	m.wroteBack.Add(int64(s.WroteBack))
	m.conflicts.Add(int64(s.Conflicts))
	m.malformed.Add(int64(s.Malformed))
	m.lastMs.Store(s.Duration.Milliseconds())
}

func (m *metrics) write(w io.Writer, checkpoint time.Time) {
	lag := 0.0
	if !checkpoint.IsZero() {
		lag = time.Since(checkpoint).Seconds()
	}
	fmt.Fprintf(w, "# HELP directory_sync_lag_seconds Age of the sync checkpoint.\n")
	fmt.Fprintf(w, "# TYPE directory_sync_lag_seconds gauge\ndirectory_sync_lag_seconds %.0f\n", lag)
	fmt.Fprintf(w, "# TYPE directory_sync_cycles_total counter\ndirectory_sync_cycles_total %d\n", m.cycles.Load())
	fmt.Fprintf(w, "# TYPE directory_sync_failures_total counter\ndirectory_sync_failures_total %d\n", m.failures.Load())
	fmt.Fprintf(w, "# TYPE directory_pickers_created_total counter\ndirectory_pickers_created_total %d\n", m.created.Load())
	fmt.Fprintf(w, "# TYPE directory_pickers_enabled_total counter\ndirectory_pickers_enabled_total %d\n", m.enabled.Load())
	fmt.Fprintf(w, "# TYPE directory_pickers_disabled_total counter\ndirectory_pickers_disabled_total %d\n", m.disabled.Load())
	fmt.Fprintf(w, "# TYPE directory_write_backs_total counter\ndirectory_write_backs_total %d\n", m.wroteBack.Load())
	fmt.Fprintf(w, "# TYPE directory_write_back_conflicts_total counter\ndirectory_write_back_conflicts_total %d\n", m.conflicts.Load())
	fmt.Fprintf(w, "# TYPE directory_malformed_records_total counter\ndirectory_malformed_records_total %d\n", m.malformed.Load())
	fmt.Fprintf(w, "# TYPE directory_alerts_total counter\ndirectory_alerts_total %d\n", m.alerts.Load())
	fmt.Fprintf(w, "# TYPE directory_last_cycle_duration_ms gauge\ndirectory_last_cycle_duration_ms %d\n", m.lastMs.Load())
}
