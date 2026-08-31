package directory_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helppi/helppi-scim-go/directory"
	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/scimtest"
	"github.com/Helppi/helppi-scim-go/store/memory"
)

// The newest meta.lastModified in testdata/directory.json.
var newestFixture = time.Date(2026, 8, 27, 20, 15, 32, 0, time.UTC)

// harness wires a fake directory, an in-memory store and a syncer, and collects
// whatever the syncer alerts about.
type harness struct {
	dir    *scimtest.Directory
	store  *memory.Store
	syncer *directory.Syncer

	mu     sync.Mutex
	alerts []string
}

func (h *harness) alertCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.alerts)
}

// conflictAlerts counts only write-back conflicts, ignoring the drift alert a
// full walk raises for its own reasons.
func (h *harness) conflictAlerts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, a := range h.alerts {
		if strings.HasPrefix(a, "write-back conflict:") {
			n++
		}
	}
	return n
}

func newHarness(t *testing.T, fixture string, opts directory.Options) *harness {
	t.Helper()
	return newHarnessWith(t, scimtest.Load(t, filepath.Join("..", "testdata", fixture)), opts)
}

func newHarnessWith(t *testing.T, users []scim.User, opts directory.Options) *harness {
	t.Helper()

	h := &harness{
		dir:   scimtest.New(t, users),
		store: memory.New(nil),
	}
	client, err := scim.New(scim.Options{
		BaseURL: h.dir.URL, Token: scimtest.Token,
		Sleep: func(context.Context, time.Duration) error { return nil }, // never wait in tests
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	if opts.PageSize == 0 {
		opts.PageSize = 2 // exercise pagination everywhere
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	opts.Alert = func(format string, args ...any) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.alerts = append(h.alerts, format)
	}
	h.syncer = directory.New(client, h.store, opts)
	return h
}

func active(v bool) *bool { return &v }

// user builds a directory record; pass nil for active to make it malformed.
func user(id string, act *bool, lastModified time.Time) scim.User {
	return scim.User{
		ID:          id,
		UserName:    id + "@separador.app",
		DisplayName: id,
		Active:      act,
		Meta:        scim.Meta{ResourceType: "User", LastModified: lastModified},
	}
}

// wrapConflict rebuilds the harness syncer so every write-back returns 409.
func wrapConflict(h *harness) *directory.Syncer {
	client, _ := scim.New(scim.Options{
		BaseURL: h.dir.URL, Token: scimtest.Token,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	return directory.New(conflictDirectory{client}, h.store, directory.Options{
		PageSize: 2,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Alert: func(format string, args ...any) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.alerts = append(h.alerts, format)
		},
	})
}
