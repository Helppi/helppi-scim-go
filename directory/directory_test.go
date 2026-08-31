package directory_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Helppi/helppi-scim-go/directory"
	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/scimtest"
	"github.com/Helppi/helppi-scim-go/store"
	"github.com/Helppi/helppi-scim-go/store/memory"
)

// The newest meta.lastModified in testdata/directory.json.
var newestFixture = time.Date(2026, 8, 27, 20, 15, 32, 0, time.UTC)

func harness(t *testing.T, fixture string) (*scimtest.Directory, *memory.Store, *directory.Syncer, *[]string) {
	t.Helper()
	dir := scimtest.New(t, scimtest.Load(t, filepath.Join("..", "testdata", fixture)))
	client, err := scim.New(scim.Options{
		BaseURL: dir.URL, Token: scimtest.Token,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	st := memory.New(nil)
	alerts := &[]string{}
	s := directory.New(client, st, directory.Options{
		PageSize: 2, // exercise pagination in every test
		Alert:    func(format string, args ...any) { *alerts = append(*alerts, format) },
	})
	return dir, st, s, alerts
}

func TestFirstCycleCreatesActivePickersAndWritesBack(t *testing.T) {
	dir, st, s, _ := harness(t, "directory.json")

	stats, err := s.Incremental(context.Background())
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
	if got := len(st.All()); got != 4 {
		t.Fatalf("local pickers = %d, want 4", got)
	}
	for _, p := range st.All() {
		if p.DirectoryID == "hlp_9xB2Rt77" || p.DirectoryID == "hlp_2wT6Yn53" {
			t.Errorf("created a picker for inactive identity %s", p.DirectoryID)
		}
	}
	// Every created picker gets its id written back, immediately.
	if stats.WroteBack != 4 {
		t.Errorf("wrote back = %d, want 4", stats.WroteBack)
	}
	if got := len(dir.Patches()); got != 4 {
		t.Errorf("directory saw %d PATCHes, want 4", got)
	}
	for _, patch := range dir.Patches() {
		u, _ := dir.User(patch.ID)
		p, err := st.PickerByDirectoryID(context.Background(), patch.ID)
		if err != nil {
			t.Fatalf("missing local picker for %s", patch.ID)
		}
		if u.ExternalID != p.PickerID() {
			t.Errorf("%s: externalId = %q, want %q", patch.ID, u.ExternalID, p.PickerID())
		}
	}
}

func TestSecondCycleIsANoOp(t *testing.T) {
	dir, _, s, _ := harness(t, "directory.json")
	ctx := context.Background()

	if _, err := s.Incremental(ctx); err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	patchesAfterFirst := len(dir.Patches())

	// Re-run without a checkpoint advance being required: a full walk must
	// converge to the same state and write nothing new.
	stats, _, err := s.Full(ctx)
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if stats.Created != 0 || stats.Enabled != 0 || stats.Disabled != 0 {
		t.Errorf("second cycle mutated state: %+v", stats)
	}
	if stats.WroteBack != 0 {
		t.Errorf("wrote back = %d on a converged base, want 0", stats.WroteBack)
	}
	if got := len(dir.Patches()); got != patchesAfterFirst {
		t.Errorf("PATCHes = %d, want unchanged at %d", got, patchesAfterFirst)
	}
}

func TestSuspensionThenReactivation(t *testing.T) {
	dir, st, s, _ := harness(t, "directory.json")
	ctx := context.Background()
	if _, err := s.Incremental(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	no, yes := false, true
	dir.Touch("hlp_5kM1Zc08", newestFixture.Add(time.Minute), func(u *scim.User) { u.Active = &no })

	stats, err := s.Incremental(ctx)
	if err != nil {
		t.Fatalf("suspend cycle: %v", err)
	}
	if stats.Disabled != 1 {
		t.Errorf("disabled = %d, want 1", stats.Disabled)
	}
	p, _ := st.PickerByDirectoryID(ctx, "hlp_5kM1Zc08")
	if p.Enabled {
		t.Error("picker still enabled after the directory reported active:false")
	}

	dir.Touch("hlp_5kM1Zc08", newestFixture.Add(2*time.Minute), func(u *scim.User) { u.Active = &yes })
	stats, err = s.Incremental(ctx)
	if err != nil {
		t.Fatalf("reactivate cycle: %v", err)
	}
	if stats.Enabled != 1 {
		t.Errorf("enabled = %d, want 1", stats.Enabled)
	}
	p, _ = st.PickerByDirectoryID(ctx, "hlp_5kM1Zc08")
	if !p.Enabled {
		t.Error("picker not re-enabled")
	}
	if stats.Created != 0 {
		t.Error("reactivation created a second picker; the directory id must be reused")
	}
}

func TestCheckpointComesFromDirectoryTimestamps(t *testing.T) {
	_, st, s, _ := harness(t, "directory.json")
	ctx := context.Background()

	stats, err := s.Incremental(ctx)
	if err != nil {
		t.Fatalf("incremental: %v", err)
	}
	cp, _ := st.Checkpoint(ctx)
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

func TestMissingActiveFlagAbortsCycleAndHoldsCheckpoint(t *testing.T) {
	_, st, s, _ := harness(t, "malformed.json")
	ctx := context.Background()

	_, err := s.Incremental(ctx)
	if err == nil {
		t.Fatal("expected an error for a record without an active flag")
	}
	if got := len(st.All()); got != 0 {
		t.Errorf("created %d pickers from a malformed page, want 0", got)
	}
	cp, _ := st.Checkpoint(ctx)
	if !cp.IsZero() {
		t.Errorf("checkpoint advanced to %s on a failed cycle; it must not move", cp)
	}
}

func TestFailedCycleIsRetriedFromTheSameCheckpoint(t *testing.T) {
	dir, st, s, _ := harness(t, "directory.json")
	ctx := context.Background()

	// Fail every attempt of the first request so the cycle aborts.
	for i := 0; i < 8; i++ {
		dir.FailNext(503, "")
	}
	if _, err := s.Incremental(ctx); err == nil {
		t.Fatal("expected the cycle to fail")
	}
	if cp, _ := st.Checkpoint(ctx); !cp.IsZero() {
		t.Fatalf("checkpoint moved to %s after a failed cycle", cp)
	}

	stats, err := s.Incremental(ctx)
	if err != nil {
		t.Fatalf("recovery cycle: %v", err)
	}
	if stats.Scanned != 6 {
		t.Errorf("recovery scanned %d records, want the full 6", stats.Scanned)
	}
}

func TestPartnerWriteBackEchoIsHarmless(t *testing.T) {
	// Our own PATCH bumps meta.lastModified, so the record comes back on the
	// next incremental cycle. It must be a no-op, not a second write.
	dir, _, s, _ := harness(t, "directory.json")
	ctx := context.Background()
	if _, err := s.Incremental(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := len(dir.Patches())

	stats, err := s.Incremental(ctx)
	if err != nil {
		t.Fatalf("echo cycle: %v", err)
	}
	if stats.Scanned == 0 {
		t.Skip("directory returned nothing to echo")
	}
	if stats.WroteBack != 0 || len(dir.Patches()) != before {
		t.Errorf("echoed records triggered %d new writes", len(dir.Patches())-before)
	}
	if stats.Created != 0 || stats.Disabled != 0 || stats.Enabled != 0 {
		t.Errorf("echoed records mutated state: %+v", stats)
	}
}

func TestFullReportsDriftWithoutDeprovisioning(t *testing.T) {
	_, st, s, alerts := harness(t, "directory.json")
	ctx := context.Background()

	// A local picker whose identity the directory no longer publishes: past the
	// retention window. It must be reported, never disabled automatically.
	ghost, err := st.CreatePicker(ctx, store.NewPicker{
		DirectoryID: "hlp_0000Gone", Login: "gone@separador.app", DisplayName: "Ghost G.",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, drift, err := s.Full(ctx)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if len(drift.AbsentFromDirectory) != 1 || drift.AbsentFromDirectory[0] != "hlp_0000Gone" {
		t.Fatalf("AbsentFromDirectory = %v, want [hlp_0000Gone]", drift.AbsentFromDirectory)
	}
	still, _ := st.PickerByDirectoryID(ctx, ghost.DirectoryID)
	if !still.Enabled {
		t.Error("full walk disabled a picker missing from the directory; absence is not a deprovisioning signal")
	}
	if len(*alerts) == 0 {
		t.Error("drift was not alerted")
	}
}

func TestFullDetectsAPickerTheIncrementalPathMissed(t *testing.T) {
	_, st, s, _ := harness(t, "directory.json")
	ctx := context.Background()

	// Simulate a missed disable: the directory says inactive, we still have the
	// picker enabled.
	if _, err := st.CreatePicker(ctx, store.NewPicker{
		DirectoryID: "hlp_9xB2Rt77", Login: "9xb2rt77@separador.app", DisplayName: "Bruno S.",
	}); err != nil {
		t.Fatal(err)
	}

	_, drift, err := s.Full(ctx)
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if len(drift.ShouldBeDisabled) != 1 || drift.ShouldBeDisabled[0] != "hlp_9xB2Rt77" {
		t.Fatalf("ShouldBeDisabled = %v, want [hlp_9xB2Rt77]", drift.ShouldBeDisabled)
	}
	// ...and the same walk converges it.
	p, _ := st.PickerByDirectoryID(ctx, "hlp_9xB2Rt77")
	if p.Enabled {
		t.Error("full walk reported the drift but did not converge it")
	}
}

// conflictDirectory returns a 409 from every write-back.
type conflictDirectory struct{ directory.Directory }

func (c conflictDirectory) PatchExternalID(context.Context, string, string) (*scim.User, error) {
	return nil, &scim.Error{HTTPStatus: 409, Status: "409", ScimType: "uniqueness",
		Detail: "externalId already assigned to another identity"}
}

func TestWriteBackConflictAlertsAndDoesNotFailTheCycle(t *testing.T) {
	dir := scimtest.New(t, scimtest.Load(t, filepath.Join("..", "testdata", "directory.json")))
	client, err := scim.New(scim.Options{BaseURL: dir.URL, Token: scimtest.Token,
		Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	var alerts []string
	st := memory.New(nil)
	s := directory.New(conflictDirectory{client}, st, directory.Options{
		Alert: func(format string, args ...any) { alerts = append(alerts, format) },
	})

	stats, err := s.Incremental(context.Background())
	if err != nil {
		t.Fatalf("a write-back conflict must not fail the cycle: %v", err)
	}
	if stats.Conflicts != 4 {
		t.Errorf("conflicts = %d, want 4", stats.Conflicts)
	}
	if len(alerts) != 4 {
		t.Errorf("alerts = %d, want one per conflict", len(alerts))
	}
	if cp, _ := st.Checkpoint(context.Background()); cp.IsZero() {
		t.Error("checkpoint should still advance: the cycle itself succeeded")
	}
}

func TestCreateRaceFallsBackToTheExistingPicker(t *testing.T) {
	_, st, s, _ := harness(t, "directory.json")
	ctx := context.Background()

	// Another worker created it a millisecond earlier.
	existing, err := st.CreatePicker(ctx, store.NewPicker{
		DirectoryID: "hlp_8fK2Lm91", Login: "8fk2lm91@separador.app", DisplayName: "Marcio C.",
	})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := s.Incremental(ctx)
	if err != nil {
		t.Fatalf("incremental: %v", err)
	}
	if stats.Created != 3 {
		t.Errorf("created = %d, want 3 (the fourth already existed)", stats.Created)
	}
	got, _ := st.PickerByDirectoryID(ctx, "hlp_8fK2Lm91")
	if got.ID != existing.ID {
		t.Errorf("picker id = %d, want the pre-existing %d — no duplicate may be created", got.ID, existing.ID)
	}
}

func TestUnknownIdentityIsNotFoundNotAnEmptyRecord(t *testing.T) {
	dir, _, _, _ := harness(t, "directory.json")
	client, _ := scim.New(scim.Options{BaseURL: dir.URL, Token: scimtest.Token,
		Sleep: func(context.Context, time.Duration) error { return nil }})

	_, err := client.GetUser(context.Background(), "hlp_does_not_exist")
	var se *scim.Error
	if !errors.As(err, &se) || !se.NotFound() {
		t.Fatalf("err = %v, want a typed 404", err)
	}
}
