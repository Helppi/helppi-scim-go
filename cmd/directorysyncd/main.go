// Command directorysyncd keeps the local picker base converged with the Helppi
// partner directory.
//
// It runs two loops: a frequent incremental cycle and a daily full walk. Both
// are reconciliations, so the process can be killed and restarted at any point
// without losing anything.
//
// IMPORTANT: run exactly one instance. Two replicas on the same schedule will
// both try to create pickers on the first sync; the unique index on
// directory_id is what saves you, but the resulting 409 storm is avoidable.
// Use a lease, a Postgres advisory lock, or a CronJob with
// concurrencyPolicy: Forbid.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Helppi/helppi-scim-go/directory"
	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/store/memory"
)

func main() {
	var (
		baseURL     = flag.String("base-url", os.Getenv("DIRECTORY_BASE_URL"), "directory base URL, e.g. https://.../scim/v2")
		incremental = flag.Duration("incremental", envDuration("INCREMENTAL_INTERVAL", 5*time.Minute), "incremental cycle interval")
		full        = flag.Duration("full", envDuration("FULL_INTERVAL", 24*time.Hour), "full reconciliation interval")
		pageSize    = flag.Int("page-size", 200, "records per page")
		rps         = flag.Float64("rps", 5, "max requests per second against the directory")
		metricsAddr = flag.String("metrics-addr", ":9090", "address for /metrics and /healthz; empty disables")
		once        = flag.Bool("once", false, "run a single full reconciliation and exit")
	)
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	token := os.Getenv("DIRECTORY_TOKEN")
	if *baseURL == "" || token == "" {
		log.Error("DIRECTORY_BASE_URL and DIRECTORY_TOKEN are required")
		os.Exit(2)
	}

	client, err := scim.New(scim.Options{
		BaseURL:           *baseURL,
		Token:             token,
		RequestsPerSecond: *rps,
		UserAgent:         "helppi-scim-go/1.0 (+sync worker)",
	})
	if err != nil {
		log.Error("build client", "err", err)
		os.Exit(1)
	}

	// Swap this for the real store. The interface is store.Store; nothing in
	// the reconciler knows which implementation it is talking to.
	st := memory.New(nil)
	m := &metrics{}

	syncer := directory.New(client, st, directory.Options{
		PageSize: *pageSize,
		Logger:   log,
		Alert: func(format string, args ...any) {
			m.alerts.Add(1)
			// Wire this to the on-call channel: these are the conditions a
			// retry cannot fix.
			log.Error("ALERT: " + fmt.Sprintf(format, args...))
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *metricsAddr != "" {
		go serveMetrics(ctx, log, *metricsAddr, m, st)
	}

	// One cycle at a time, whichever kind.
	var cycle sync.Mutex

	runFull := func() {
		cycle.Lock()
		defer cycle.Unlock()
		stats, drift, err := syncer.Full(ctx)
		m.record(stats, err)
		if err != nil {
			log.Error("full cycle failed", "err", err, "scanned", stats.Scanned)
			return
		}
		log.Info("full cycle",
			"scanned", stats.Scanned, "created", stats.Created,
			"enabled", stats.Enabled, "disabled", stats.Disabled,
			"wrote_back", stats.WroteBack, "conflicts", stats.Conflicts,
			"absent_from_directory", len(drift.AbsentFromDirectory),
			"should_be_disabled", len(drift.ShouldBeDisabled),
			"missing_picker_id", len(drift.MissingPickerID),
			"duration_ms", stats.Duration.Milliseconds())
	}

	if *once {
		runFull()
		return
	}

	// Seed from a full walk so a cold start never relies on a checkpoint that
	// does not exist yet.
	runFull()

	incTicker := time.NewTicker(*incremental)
	defer incTicker.Stop()
	fullTicker := time.NewTicker(*full)
	defer fullTicker.Stop()

	log.Info("worker started", "incremental", incremental.String(), "full", full.String())
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			return
		case <-fullTicker.C:
			runFull()
		case <-incTicker.C:
			cycle.Lock()
			stats, err := syncer.Incremental(ctx)
			cycle.Unlock()
			m.record(stats, err)
			if err != nil {
				// The checkpoint did not move: the next cycle redoes this work.
				log.Error("incremental cycle failed", "err", err, "scanned", stats.Scanned)
				continue
			}
			log.Info("incremental cycle",
				"scanned", stats.Scanned, "created", stats.Created,
				"enabled", stats.Enabled, "disabled", stats.Disabled,
				"wrote_back", stats.WroteBack, "conflicts", stats.Conflicts,
				"checkpoint", stats.Checkpoint.Format(time.RFC3339),
				"duration_ms", stats.Duration.Milliseconds())
		}
	}
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func serveMetrics(ctx context.Context, log *slog.Logger, addr string, m *metrics, st interface {
	Checkpoint(context.Context) (time.Time, error)
}) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		cp, _ := st.Checkpoint(r.Context())
		m.write(w, cp)
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("metrics server", "err", err)
	}
}
