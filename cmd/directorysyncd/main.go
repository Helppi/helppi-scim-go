// Command directorysyncd keeps the local helpper base converged with the Helppi
// partner directory.
//
// It runs two loops: a frequent incremental cycle and a daily full walk. Both
// are reconciliations, so the process can be killed and restarted at any point
// without losing anything.
//
// IMPORTANT: run exactly one instance. Two replicas on the same schedule will
// both try to create helppers on the first sync; the unique index on
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
	"github.com/Helppi/helppi-scim-go/store"
	"github.com/Helppi/helppi-scim-go/store/memory"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		baseURL     = flag.String("base-url", os.Getenv("DIRECTORY_BASE_URL"), "directory base URL, e.g. https://.../scim/v2")
		incremental = flag.Duration("incremental", envDuration("INCREMENTAL_INTERVAL", 5*time.Minute), "incremental cycle interval")
		full        = flag.Duration("full", envDuration("FULL_INTERVAL", 24*time.Hour), "full reconciliation interval")
		pageSize    = flag.Int("page-size", 200, "records per page")
		rps         = flag.Float64("rps", 5, "max requests per second against the directory")
		metricsAddr = flag.String("metrics-addr", ":9090", "address for /metrics and /healthz; empty disables")
		once        = flag.Bool("once", false, "run a single full reconciliation and exit")
		dryRun      = flag.Bool("dry-run", false, "report what a cycle would do; write nothing, anywhere")
		allowMemory = flag.Bool("allow-ephemeral-store", false,
			"permit the in-memory store against a real directory (DANGEROUS: see -dry-run)")
		cycleTimeout = flag.Duration("cycle-timeout", 30*time.Minute, "abort a cycle that runs longer than this")
	)
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	token := os.Getenv("DIRECTORY_TOKEN")
	if *baseURL == "" || token == "" {
		return errors.New("DIRECTORY_BASE_URL and DIRECTORY_TOKEN are required")
	}

	client, err := scim.New(scim.Options{
		BaseURL:           *baseURL,
		Token:             token,
		RequestsPerSecond: *rps,
		UserAgent:         "helppi-scim-go/1.0 (+sync worker)",
	})
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}

	// Replace this with your own store.Store. The interface is in package
	// store; nothing in the reconciler knows which implementation it talks to.
	var st store.Store = memory.New(nil)

	// An empty store makes every directory record look new, so the worker would
	// mint fresh helpper ids and write them over the real ones. Refusing here is
	// the difference between a demo and a data-loss incident.
	if store.IsEphemeral(st) && !*dryRun && !*allowMemory {
		return errors.New("refusing to run against a real directory with an in-memory store: " +
			"it would create new helppers and overwrite every externalId in the directory. " +
			"Use -dry-run to see what a cycle would do, plug in a real store.Store, " +
			"or pass -allow-ephemeral-store if this really is a throwaway directory")
	}

	m := &metrics{}
	syncer := directory.New(client, st, directory.Options{
		PageSize: *pageSize,
		DryRun:   *dryRun,
		Logger:   log,
		Alert: func(format string, args ...any) {
			m.alerts.Add(1)
			// Wire this to the on-call channel: these are the conditions a
			// retry cannot fix.
			log.Error("ALERT: " + fmt.Sprintf(format, args...))
		},
	})
	if *dryRun {
		log.Warn("dry run: no local writes, no directory writes, checkpoint frozen")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := preflight(ctx, client, log); err != nil {
		return err
	}

	if *metricsAddr != "" {
		go serveMetrics(ctx, log, *metricsAddr, m, st)
	}

	// One cycle at a time, whichever kind.
	var cycle sync.Mutex

	runFull := func() {
		cycle.Lock()
		defer cycle.Unlock()

		cycleCtx, cancel := context.WithTimeout(ctx, *cycleTimeout)
		defer cancel()

		stats, drift, err := syncer.Full(cycleCtx)
		m.record(stats, err)
		if err != nil {
			log.Error("full cycle failed", "err", err, "scanned", stats.Scanned)
			return
		}
		log.Info("full cycle",
			"scanned", stats.Scanned, "created", stats.Created,
			"enabled", stats.Enabled, "disabled", stats.Disabled,
			"updated", stats.Updated, "skipped", stats.Skipped,
			"malformed", stats.Malformed, "wrote_back", stats.WroteBack,
			"conflicts", stats.Conflicts,
			"absent_from_directory", len(drift.AbsentFromDirectory),
			"should_be_disabled", len(drift.ShouldBeDisabled),
			"missing_external_id", len(drift.MissingExternalID),
			"duration_ms", stats.Duration.Milliseconds())
	}

	if *once {
		runFull()
		return nil
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
			return nil
		case <-fullTicker.C:
			runFull()
		case <-incTicker.C:
			cycle.Lock()
			cycleCtx, cancel := context.WithTimeout(ctx, *cycleTimeout)
			stats, err := syncer.Incremental(cycleCtx)
			cancel()
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
				"updated", stats.Updated, "skipped", stats.Skipped,
				"malformed", stats.Malformed, "wrote_back", stats.WroteBack,
				"conflicts", stats.Conflicts,
				"checkpoint", stats.Checkpoint.Format(time.RFC3339),
				"duration_ms", stats.Duration.Milliseconds())
		}
	}
}

// preflight asks the directory what it supports instead of assuming. A
// directory without filter support cannot be synchronized incrementally, and
// one without PATCH cannot receive our identifier — both are better discovered
// at startup than at 03:00.
func preflight(ctx context.Context, client *scim.Client, log *slog.Logger) error {
	cfg, err := client.ServiceProviderConfig(ctx)
	if err != nil {
		var scimErr *scim.Error
		if errors.As(err, &scimErr) && scimErr.Credential() {
			return fmt.Errorf("preflight: the credential was rejected: %w", err)
		}
		// Not every deployment exposes the endpoint; that alone is not fatal.
		log.Warn("preflight: could not read ServiceProviderConfig", "err", err)
		return nil
	}
	if !cfg.Filter.Supported {
		return errors.New("preflight: the directory reports no filter support, " +
			"so incremental synchronization is impossible")
	}
	if !cfg.Patch.Supported {
		return errors.New("preflight: the directory reports no PATCH support, " +
			"so our identifier cannot be written back")
	}
	log.Info("preflight ok", "filter_max_results", cfg.Filter.MaxResults)
	return nil
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

// checkpointReader is the slice of the store the metrics endpoint needs.
type checkpointReader interface {
	Checkpoint(context.Context) (time.Time, error)
}

func serveMetrics(ctx context.Context, log *slog.Logger, addr string, m *metrics, st checkpointReader) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if m.cycles.Load() == 0 {
			http.Error(w, "no cycle completed yet", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
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
