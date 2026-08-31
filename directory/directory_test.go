package directory_test

import (
	"context"
	"testing"
	"time"

	"github.com/Helppi/helppi-scim-go/directory"
	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/store"
)

func TestFirstCycleCreatesActivePickersAndWritesBack(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})

	stats, err := h.syncer.Incremental(context.Background())
	if err != nil {
		t.Fatalf("incremental: %v", err)
	}
	if stats.Scanned != 6 {
		t.Errorf("scanned = %d, want 6", stats.Scanned)
	}
	// Four records are active; the two inactive ones must NOT produce a picker.
	if stats.Created != 4 {
		t.Errorf("created = %d, want 4", stats.Created)
	}
	if stats.Skipped != 2 {
		t.Errorf("skipped = %d, want 2 (inactive and unknown)", stats.Skipped)
	}
	if got := len(h.store.All()); got != 4 {
		t.Fatalf("local pickers = %d, want 4", got)
	}
	for _, p := range h.store.All() {
		if p.DirectoryID == "hlp_9xB2Rt77" || p.DirectoryID == "hlp_2wT6Yn53" {
			t.Errorf("created a picker for inactive identity %s", p.DirectoryID)
		}
	}
	if stats.WroteBack != 4 {
		t.Errorf("wrote back = %d, want 4", stats.WroteBack)
	}
	for _, patch := range h.dir.Patches() {
		u, _ := h.dir.User(patch.ID)
		p, err := h.store.PickerByDirectoryID(context.Background(), patch.ID)
		if err != nil {
			t.Fatalf("missing local picker for %s", patch.ID)
		}
		if u.ExternalID != p.ID {
			t.Errorf("%s: externalId = %q, want %q", patch.ID, u.ExternalID, p.ID)
		}
	}
}

func TestSecondCycleIsANoOp(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()

	if _, err := h.syncer.Incremental(ctx); err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	patchesAfterFirst := len(h.dir.Patches())

	stats, _, err := h.syncer.Full(ctx)
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if stats.Created != 0 || stats.Enabled != 0 || stats.Disabled != 0 {
		t.Errorf("second cycle mutated state: %+v", stats)
	}
	if stats.WroteBack != 0 {
		t.Errorf("wrote back = %d on a converged base, want 0", stats.WroteBack)
	}
	if got := len(h.dir.Patches()); got != patchesAfterFirst {
		t.Errorf("PATCHes = %d, want unchanged at %d", got, patchesAfterFirst)
	}
}

func TestSuspensionThenReactivation(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()
	if _, err := h.syncer.Incremental(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h.dir.Touch("hlp_5kM1Zc08", newestFixture.Add(time.Minute), func(u *scim.User) { u.Active = active(false) })
	stats, err := h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("suspend cycle: %v", err)
	}
	if stats.Disabled != 1 {
		t.Errorf("disabled = %d, want 1", stats.Disabled)
	}
	p, _ := h.store.PickerByDirectoryID(ctx, "hlp_5kM1Zc08")
	if p.Enabled {
		t.Error("picker still enabled after the directory reported active:false")
	}

	h.dir.Touch("hlp_5kM1Zc08", newestFixture.Add(2*time.Minute), func(u *scim.User) { u.Active = active(true) })
	stats, err = h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("reactivate cycle: %v", err)
	}
	if stats.Enabled != 1 {
		t.Errorf("enabled = %d, want 1", stats.Enabled)
	}
	if p, _ = h.store.PickerByDirectoryID(ctx, "hlp_5kM1Zc08"); !p.Enabled {
		t.Error("picker not re-enabled")
	}
	if stats.Created != 0 {
		t.Error("reactivation created a second picker; the directory id must be reused")
	}
}

func TestCheckpointComesFromDirectoryTimestamps(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()

	stats, err := h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("incremental: %v", err)
	}
	cp, _ := h.store.Checkpoint(ctx)
	if !cp.Equal(newestFixture) {
		t.Fatalf("checkpoint = %s, want the newest meta.lastModified %s", cp, newestFixture)
	}
	if !stats.Checkpoint.Equal(newestFixture) {
		t.Errorf("stats.Checkpoint = %s", stats.Checkpoint)
	}
	if cp.After(time.Now()) {
		t.Error("checkpoint should be a directory timestamp, not a local clock read")
	}
}

func TestFullAdvancesTheCheckpoint(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()

	stats, _, err := h.syncer.Full(ctx)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if !stats.Checkpoint.Equal(newestFixture) {
		t.Errorf("stats.Checkpoint = %s, want %s", stats.Checkpoint, newestFixture)
	}
	if cp, _ := h.store.Checkpoint(ctx); !cp.Equal(newestFixture) {
		t.Errorf("stored checkpoint = %s, want %s", cp, newestFixture)
	}
}

func TestEmptyDirectoryLeavesTheCheckpointAlone(t *testing.T) {
	h := newHarnessWith(t, nil, directory.Options{})
	ctx := context.Background()

	stats, err := h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("incremental: %v", err)
	}
	if stats.Scanned != 0 {
		t.Errorf("scanned = %d, want 0", stats.Scanned)
	}
	if cp, _ := h.store.Checkpoint(ctx); !cp.IsZero() {
		t.Errorf("checkpoint moved to %s on an empty directory", cp)
	}
}

// --- malformed records -------------------------------------------------------

func TestMalformedRecordIsSkippedNotFatal(t *testing.T) {
	t0 := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	h := newHarnessWith(t, []scim.User{
		user("hlp_good1", active(true), t0.Add(3*time.Hour)),
		user("hlp_bad", nil, t0.Add(time.Hour)), // no active flag
		user("hlp_good2", active(true), t0.Add(2*time.Hour)),
	}, directory.Options{})

	stats, err := h.syncer.Incremental(context.Background())
	if err != nil {
		t.Fatalf("one unusable record must not fail the cycle: %v", err)
	}
	if stats.Malformed != 1 {
		t.Errorf("malformed = %d, want 1", stats.Malformed)
	}
	if stats.Created != 2 {
		t.Errorf("created = %d, want 2 — the usable records still converge", stats.Created)
	}
	if h.alertCount() != 1 {
		t.Errorf("alerts = %d, want 1", h.alertCount())
	}
}

func TestMalformedRecordHoldsTheCheckpointBehindIt(t *testing.T) {
	t0 := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	bad := t0.Add(time.Hour)
	h := newHarnessWith(t, []scim.User{
		user("hlp_good1", active(true), t0.Add(3*time.Hour)),
		user("hlp_bad", nil, bad),
		user("hlp_good2", active(true), t0.Add(2*time.Hour)),
	}, directory.Options{})
	ctx := context.Background()

	if _, err := h.syncer.Incremental(ctx); err != nil {
		t.Fatalf("incremental: %v", err)
	}
	cp, _ := h.store.Checkpoint(ctx)
	if cp.After(bad) {
		t.Fatalf("checkpoint = %s advanced past the unusable record at %s; "+
			"its eventual fix would never be seen", cp, bad)
	}

	// Once the directory repairs the record, the next cycle picks it up.
	h.dir.Touch("hlp_bad", bad, func(u *scim.User) { u.Active = active(true) })
	stats, err := h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("recovery cycle: %v", err)
	}
	if stats.Created != 1 {
		t.Errorf("created = %d, want 1 — the repaired record must be picked up", stats.Created)
	}
	// Not an exact equality: our own write-back bumps meta.lastModified on the
	// directory side, so the released watermark is at or just after the newest
	// record we read.
	if cp, _ = h.store.Checkpoint(ctx); cp.Before(t0.Add(3 * time.Hour)) {
		t.Errorf("checkpoint = %s, want it released to at least %s", cp, t0.Add(3*time.Hour))
	}
}

func TestTooManyMalformedRecordsFailTheCycle(t *testing.T) {
	t0 := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	h := newHarnessWith(t, []scim.User{
		user("hlp_bad1", nil, t0),
		user("hlp_bad2", nil, t0.Add(time.Minute)),
	}, directory.Options{MaxMalformed: 1})

	if _, err := h.syncer.Incremental(context.Background()); err == nil {
		t.Fatal("a feed that is mostly unusable must fail the cycle, not be skipped silently")
	}
	if cp, _ := h.store.Checkpoint(context.Background()); !cp.IsZero() {
		t.Errorf("checkpoint moved to %s on a failed cycle", cp)
	}
}

// --- write-back conflicts ----------------------------------------------------

// conflictDirectory returns a 409 from every write-back.
type conflictDirectory struct{ directory.Directory }

func (c conflictDirectory) PatchExternalID(context.Context, string, string) (*scim.User, error) {
	return nil, &scim.Error{HTTPStatus: 409, Status: "409", ScimType: "uniqueness",
		Detail: "externalId already assigned to another identity"}
}

func TestWriteBackConflictAlertsAndDoesNotFailTheCycle(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	h.syncer = wrapConflict(h)

	stats, err := h.syncer.Incremental(context.Background())
	if err != nil {
		t.Fatalf("a write-back conflict must not fail the cycle: %v", err)
	}
	if stats.Conflicts != 4 {
		t.Errorf("conflicts = %d, want 4", stats.Conflicts)
	}
	if cp, _ := h.store.Checkpoint(context.Background()); cp.IsZero() {
		t.Error("checkpoint should still advance: the cycle itself succeeded")
	}
}

func TestConflictAlertsOnlyOncePerIdentity(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	h.syncer = wrapConflict(h)
	ctx := context.Background()

	if _, err := h.syncer.Incremental(ctx); err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	afterFirst := h.conflictAlerts()
	if afterFirst != 4 {
		t.Fatalf("conflict alerts after first cycle = %d, want 4 (one per identity)", afterFirst)
	}

	stats, _, err := h.syncer.Full(ctx)
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if stats.Conflicts != 4 {
		t.Errorf("conflicts = %d, want 4 — the condition is still counted", stats.Conflicts)
	}
	if got := h.conflictAlerts(); got != afterFirst {
		t.Errorf("conflict alerts = %d after a second cycle, want %d: a permanent "+
			"conflict must not page someone every five minutes", got, afterFirst)
	}
}

// --- dry run -----------------------------------------------------------------

func TestDryRunWritesNothingAnywhere(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{DryRun: true})
	ctx := context.Background()

	stats, err := h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if stats.Created != 4 || stats.WroteBack != 4 {
		t.Errorf("dry run should still report what it would do: %+v", stats)
	}
	if got := len(h.store.All()); got != 0 {
		t.Errorf("dry run created %d local pickers, want 0", got)
	}
	if got := len(h.dir.Patches()); got != 0 {
		t.Errorf("dry run sent %d PATCHes, want 0", got)
	}
	if cp, _ := h.store.Checkpoint(ctx); !cp.IsZero() {
		t.Errorf("dry run advanced the checkpoint to %s", cp)
	}
}

// --- clock skew --------------------------------------------------------------

func TestFutureTimestampDoesNotAdvanceTheCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	h := newHarnessWith(t, []scim.User{
		user("hlp_future", active(true), now.Add(48*time.Hour)),
	}, directory.Options{Now: func() time.Time { return now }})
	ctx := context.Background()

	stats, err := h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("incremental: %v", err)
	}
	if stats.Created != 1 {
		t.Errorf("the record itself should still be applied: created = %d", stats.Created)
	}
	if cp, _ := h.store.Checkpoint(ctx); !cp.IsZero() {
		t.Errorf("checkpoint jumped to %s from a clock running fast; everything "+
			"modified in between would be skipped", cp)
	}
	if h.alertCount() == 0 {
		t.Error("a future watermark should alert")
	}
}

// --- resilience --------------------------------------------------------------

func TestFailedCycleIsRetriedFromTheSameCheckpoint(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()

	for i := 0; i < 8; i++ {
		h.dir.FailNext(503, "")
	}
	if _, err := h.syncer.Incremental(ctx); err == nil {
		t.Fatal("expected the cycle to fail")
	}
	if cp, _ := h.store.Checkpoint(ctx); !cp.IsZero() {
		t.Fatalf("checkpoint moved to %s after a failed cycle", cp)
	}

	stats, err := h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("recovery cycle: %v", err)
	}
	if stats.Scanned != 6 {
		t.Errorf("recovery scanned %d records, want the full 6", stats.Scanned)
	}
}

func TestCancelledContextDoesNotAdvanceTheCheckpoint(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := h.syncer.Incremental(ctx); err == nil {
		t.Fatal("a cancelled context must fail the cycle")
	}
	if cp, _ := h.store.Checkpoint(context.Background()); !cp.IsZero() {
		t.Errorf("checkpoint moved to %s despite cancellation", cp)
	}
}

func TestPartnerWriteBackEchoIsHarmless(t *testing.T) {
	// Our own PATCH bumps meta.lastModified, so the record comes back on the
	// next incremental cycle. It must be a no-op, not a second write.
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()
	if _, err := h.syncer.Incremental(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := len(h.dir.Patches())

	stats, err := h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("echo cycle: %v", err)
	}
	if stats.WroteBack != 0 || len(h.dir.Patches()) != before {
		t.Errorf("echoed records triggered %d new writes", len(h.dir.Patches())-before)
	}
	if stats.Created != 0 || stats.Disabled != 0 || stats.Enabled != 0 {
		t.Errorf("echoed records mutated state: %+v", stats)
	}
}

// --- drift -------------------------------------------------------------------

func TestFullReportsDriftWithoutDeprovisioning(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()

	ghost, err := h.store.CreatePicker(ctx, store.NewPicker{
		DirectoryID: "hlp_0000Gone", Login: "gone@separador.app", DisplayName: "Ghost G.",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, drift, err := h.syncer.Full(ctx)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if len(drift.AbsentFromDirectory) != 1 || drift.AbsentFromDirectory[0] != "hlp_0000Gone" {
		t.Fatalf("AbsentFromDirectory = %v, want [hlp_0000Gone]", drift.AbsentFromDirectory)
	}
	still, _ := h.store.PickerByDirectoryID(ctx, ghost.DirectoryID)
	if !still.Enabled {
		t.Error("full walk disabled a picker missing from the directory; absence is not a deprovisioning signal")
	}
	if h.alertCount() == 0 {
		t.Error("drift was not alerted")
	}
}

func TestFullDetectsAPickerTheIncrementalPathMissed(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()

	if _, err := h.store.CreatePicker(ctx, store.NewPicker{
		DirectoryID: "hlp_9xB2Rt77", Login: "9xb2rt77@separador.app", DisplayName: "Bruno S.",
	}); err != nil {
		t.Fatal(err)
	}

	_, drift, err := h.syncer.Full(ctx)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if len(drift.ShouldBeDisabled) != 1 || drift.ShouldBeDisabled[0] != "hlp_9xB2Rt77" {
		t.Fatalf("ShouldBeDisabled = %v, want [hlp_9xB2Rt77]", drift.ShouldBeDisabled)
	}
	if p, _ := h.store.PickerByDirectoryID(ctx, "hlp_9xB2Rt77"); p.Enabled {
		t.Error("full walk reported the drift but did not converge it")
	}
}

func TestFullReportsAMissingPickerID(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()

	// hlp_8fK2Lm91 carries no externalId in the fixture.
	if _, err := h.store.CreatePicker(ctx, store.NewPicker{
		DirectoryID: "hlp_8fK2Lm91", Login: "8fk2lm91@separador.app", DisplayName: "Marcio C.",
	}); err != nil {
		t.Fatal(err)
	}

	_, drift, err := h.syncer.Full(ctx)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if len(drift.MissingPickerID) != 1 || drift.MissingPickerID[0] != "hlp_8fK2Lm91" {
		t.Fatalf("MissingPickerID = %v, want [hlp_8fK2Lm91]", drift.MissingPickerID)
	}
}

func TestCreateRaceFallsBackToTheExistingPicker(t *testing.T) {
	h := newHarness(t, "directory.json", directory.Options{})
	ctx := context.Background()

	existing, err := h.store.CreatePicker(ctx, store.NewPicker{
		DirectoryID: "hlp_8fK2Lm91", Login: "8fk2lm91@separador.app", DisplayName: "Marcio C.",
	})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := h.syncer.Incremental(ctx)
	if err != nil {
		t.Fatalf("incremental: %v", err)
	}
	if stats.Created != 3 {
		t.Errorf("created = %d, want 3 (the fourth already existed)", stats.Created)
	}
	got, _ := h.store.PickerByDirectoryID(ctx, "hlp_8fK2Lm91")
	if got.ID != existing.ID {
		t.Errorf("picker id = %s, want the pre-existing %s — no duplicate may be created", got.ID, existing.ID)
	}
}
