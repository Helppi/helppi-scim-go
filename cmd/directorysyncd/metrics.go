package main

import (
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Helppi/helppi-scim-go/directory"
)

// metrics is a deliberately tiny Prometheus exposition, so the reference client
// has no third-party dependencies at all. Replace it with your own registry.
//
// sync_lag_seconds is the one that matters: it is the SLI behind "an account closure
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

	var b strings.Builder
	gauge := func(name, help string, value string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, value)
	}
	counter := func(name, help string, value int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
	}

	gauge("directory_sync_lag_seconds", "Age of the sync checkpoint. The SLI behind \"an account closure reaches the partner within N minutes\".", fmt.Sprintf("%.0f", lag))
	gauge("directory_last_cycle_duration_ms", "Duration of the most recent cycle.", fmt.Sprintf("%d", m.lastMs.Load()))
	counter("directory_sync_cycles_total", "Cycles attempted.", m.cycles.Load())
	counter("directory_sync_failures_total", "Cycles that failed.", m.failures.Load())
	counter("directory_helppers_created_total", "Helppers created locally.", m.created.Load())
	counter("directory_helppers_enabled_total", "Helppers re-enabled.", m.enabled.Load())
	counter("directory_helppers_disabled_total", "Helppers disabled.", m.disabled.Load())
	counter("directory_write_backs_total", "Identifiers written back to the directory as externalId.", m.wroteBack.Load())
	counter("directory_write_back_conflicts_total", "Write-backs refused with 409.", m.conflicts.Load())
	counter("directory_malformed_records_total", "Records the directory served that could not be used.", m.malformed.Load())
	counter("directory_alerts_total", "Conditions no retry can fix.", m.alerts.Load())

	_, _ = io.WriteString(w, b.String())
}
