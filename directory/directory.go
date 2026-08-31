// Package directory reconciles the local picker base against the Helppi
// partner directory.
//
// The model is a reconciler, not a message consumer: every cycle re-derives the
// desired state from the directory and converges. There is no ordering to
// preserve and no event to lose, so the worker can be killed and restarted at
// any point.
package directory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/store"
)

// Directory is the slice of the SCIM client the reconciler depends on.
type Directory interface {
	ListUsers(ctx context.Context, filter string, pageSize int, fn func(scim.User) error) error
	PatchExternalID(ctx context.Context, id, externalID string) (*scim.User, error)
}

const (
	// DefaultOverlap is re-read on every incremental cycle to absorb
	// commit-visibility races on the directory side. It is safe because
	// applying a record is idempotent.
	DefaultOverlap = 2 * time.Minute

	// DefaultMaxMalformed is how many unusable records a single cycle tolerates
	// before giving up. Skipping a few is better than halting the fleet;
	// skipping hundreds means something is systematically wrong.
	DefaultMaxMalformed = 25

	// DefaultFutureSkew is how far ahead of our clock a directory timestamp may
	// be before we refuse to trust it as a watermark.
	DefaultFutureSkew = 5 * time.Minute
)

// Stats summarises one cycle.
type Stats struct {
	Scanned   int // records read from the directory
	Created   int // pickers created locally
	Enabled   int // pickers re-enabled
	Disabled  int // pickers disabled
	Updated   int // display name or login changed
	Unchanged int // already in the desired state
	Skipped   int // inactive and unknown: deliberately not created
	Malformed int // unusable records, skipped and alerted
	WroteBack int // picker_id written back to the directory
	Conflicts int // write-backs refused with 409

	Duration   time.Duration
	Checkpoint time.Time
}

// Drift is what the daily full walk reports. None of it is acted on
// automatically: per the integration contract, absence from the directory never
// means "deprovision", so a non-empty report means the incremental path missed
// something and a human should look.
type Drift struct {
	AbsentFromDirectory []string // enabled locally, not returned by a full walk
	ShouldBeDisabled    []string // directory says inactive, still enabled locally
	MissingPickerID     []string // we have a picker, directory has no externalId
}

// Empty reports whether the full walk found the two sides in agreement.
func (d Drift) Empty() bool {
	return len(d.AbsentFromDirectory) == 0 && len(d.ShouldBeDisabled) == 0 && len(d.MissingPickerID) == 0
}

// Options configures a Syncer.
type Options struct {
	PageSize int
	Overlap  time.Duration

	// MaxMalformed caps how many unusable records one cycle tolerates before
	// failing. Zero uses DefaultMaxMalformed; a negative value tolerates any
	// number of them.
	MaxMalformed int

	// FutureSkew bounds how far ahead of the local clock a directory timestamp
	// may be and still be accepted as the checkpoint. Zero uses
	// DefaultFutureSkew.
	FutureSkew time.Duration

	// DryRun reports what a cycle would do and writes nothing — neither to the
	// local store nor to the directory, and not even the checkpoint.
	DryRun bool

	Logger *slog.Logger
	// Alert is called for conditions a retry cannot fix — a write-back conflict
	// above all. Wire it to your paging system. It is called at most once per
	// directory id per process, so a permanent problem does not become a
	// five-minute alarm forever.
	Alert func(format string, args ...any)
	Now   func() time.Time
}

// Syncer runs the cycles. Construct it with New.
type Syncer struct {
	dir          Directory
	st           store.Store
	pageSize     int
	overlap      time.Duration
	maxMalformed int
	futureSkew   time.Duration
	dryRun       bool
	log          *slog.Logger
	alert        func(format string, args ...any)
	now          func() time.Time

	mu      sync.Mutex
	alerted map[string]bool
}

// New builds a Syncer.
func New(dir Directory, st store.Store, opts Options) *Syncer {
	s := &Syncer{
		dir: dir, st: st,
		pageSize:     opts.PageSize,
		overlap:      opts.Overlap,
		maxMalformed: opts.MaxMalformed,
		futureSkew:   opts.FutureSkew,
		dryRun:       opts.DryRun,
		log:          opts.Logger,
		alert:        opts.Alert,
		now:          opts.Now,
		alerted:      map[string]bool{},
	}
	if s.pageSize <= 0 {
		s.pageSize = scim.DefaultPageSize
	}
	if s.overlap == 0 {
		s.overlap = DefaultOverlap
	}
	if s.maxMalformed == 0 {
		s.maxMalformed = DefaultMaxMalformed
	}
	if s.futureSkew == 0 {
		s.futureSkew = DefaultFutureSkew
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.alert == nil {
		s.alert = func(format string, args ...any) { s.log.Error(fmt.Sprintf(format, args...)) }
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// DryRun reports whether this Syncer writes anything.
func (s *Syncer) DryRun() bool { return s.dryRun }

// Incremental fetches everything modified since the checkpoint and applies it.
//
// The checkpoint advances only when the whole cycle succeeded. A partial cycle
// that advanced the watermark would lose records permanently, and the failure
// would be silent — it surfaces weeks later as one picker nobody blocked.
func (s *Syncer) Incremental(ctx context.Context) (Stats, error) {
	started := s.now()
	cp, err := s.st.Checkpoint(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("read checkpoint: %w", err)
	}

	filter := ""
	if !cp.IsZero() {
		filter = fmt.Sprintf("meta.lastModified gt %q",
			cp.Add(-s.overlap).UTC().Format(time.RFC3339))
	}

	stats, watermark, err := s.walk(ctx, filter)
	stats.Duration = s.now().Sub(started)
	if err != nil {
		return stats, err
	}
	stats.Checkpoint, err = s.advance(ctx, cp, watermark)
	return stats, err
}

// Full walks the whole directory, applies every record and reports drift. Run
// it once a day: it is the net that catches anything the incremental path
// dropped.
func (s *Syncer) Full(ctx context.Context) (Stats, Drift, error) {
	started := s.now()
	cp, err := s.st.Checkpoint(ctx)
	if err != nil {
		return Stats{}, Drift{}, fmt.Errorf("read checkpoint: %w", err)
	}

	var (
		drift Drift
		seen  = map[string]bool{}
	)
	stats, watermark, err := s.walkWith(ctx, "", func(u scim.User, r record) {
		seen[u.ID] = true
		if !r.existed {
			return
		}
		// Drift is derived from the state *before* the upsert: afterwards the
		// two sides agree by construction and the report is always empty.
		if r.enabledBefore && !r.activeNow {
			drift.ShouldBeDisabled = append(drift.ShouldBeDisabled, u.ID)
		}
		if r.externalIDBefore == "" {
			drift.MissingPickerID = append(drift.MissingPickerID, u.ID)
		}
	})
	stats.Duration = s.now().Sub(started)
	if err != nil {
		return stats, drift, err
	}

	local, err := s.st.EnabledPickers(ctx)
	if err != nil {
		return stats, drift, fmt.Errorf("list enabled pickers: %w", err)
	}
	for _, p := range local {
		if !seen[p.DirectoryID] {
			// Do NOT disable here. Absence is not a deprovisioning signal: the
			// directory keeps terminated people visible as inactive for the
			// agreed retention window, so a disable would already have arrived.
			drift.AbsentFromDirectory = append(drift.AbsentFromDirectory, p.DirectoryID)
		}
	}
	if !drift.Empty() {
		s.alert("directory drift detected: absent=%d should_be_disabled=%d missing_picker_id=%d",
			len(drift.AbsentFromDirectory), len(drift.ShouldBeDisabled), len(drift.MissingPickerID))
	}

	stats.Checkpoint, err = s.advance(ctx, cp, watermark)
	return stats, drift, err
}

func (s *Syncer) walk(ctx context.Context, filter string) (Stats, time.Time, error) {
	return s.walkWith(ctx, filter, nil)
}

// walkWith applies every matching record, calling observe (when non-nil) with
// the pre-upsert state, which is what drift reporting needs.
func (s *Syncer) walkWith(ctx context.Context, filter string, observe func(scim.User, record)) (Stats, time.Time, error) {
	var (
		stats             Stats
		high              time.Time
		earliestMalformed time.Time
	)

	err := s.dir.ListUsers(ctx, filter, s.pageSize, func(u scim.User) error {
		r, err := s.apply(ctx, u, &stats)
		if err != nil {
			return err
		}
		if observe != nil {
			observe(u, r)
		}

		if r.malformed {
			// Hold the watermark at (or before) the oldest unusable record.
			// Skipping it keeps the fleet syncing; refusing to move the
			// watermark past it means the record is re-read every cycle, so
			// nothing is lost once the directory repairs it.
			if earliestMalformed.IsZero() || u.Meta.LastModified.Before(earliestMalformed) {
				if !u.Meta.LastModified.IsZero() {
					earliestMalformed = u.Meta.LastModified
				}
			}
			if s.maxMalformed >= 0 && stats.Malformed > s.maxMalformed {
				return fmt.Errorf("%d unusable records in one cycle (limit %d): the directory feed looks broken",
					stats.Malformed, s.maxMalformed)
			}
			return nil
		}

		// The watermark comes from directory-issued timestamps, never from the
		// local clock: that makes skew between the two companies irrelevant.
		if u.Meta.LastModified.After(high) {
			high = u.Meta.LastModified
		}
		return nil
	})
	if err != nil {
		return stats, time.Time{}, err
	}

	if !earliestMalformed.IsZero() && high.After(earliestMalformed) {
		high = earliestMalformed
	}
	return stats, high, nil
}

// advance moves the checkpoint, refusing timestamps implausibly far in the
// future: a directory clock running fast would otherwise skip every record in
// between.
func (s *Syncer) advance(ctx context.Context, current, watermark time.Time) (time.Time, error) {
	if watermark.IsZero() || !watermark.After(current) {
		return current, nil
	}
	if limit := s.now().Add(s.futureSkew); watermark.After(limit) {
		s.alertOnce("future-watermark",
			"directory timestamp %s is more than %s ahead of our clock; holding the checkpoint at %s",
			watermark.Format(time.RFC3339), s.futureSkew, current.Format(time.RFC3339))
		return current, nil
	}
	if s.dryRun {
		return current, nil
	}
	if err := s.st.SetCheckpoint(ctx, watermark); err != nil {
		return current, fmt.Errorf("advance checkpoint: %w", err)
	}
	return watermark, nil
}

// dryRunPickerID stands in for the id a real store would have assigned.
const dryRunPickerID = "(assigned on a real run)"

// record is the state of one identity as it was *before* this cycle touched it.
type record struct {
	existed          bool
	enabledBefore    bool
	activeNow        bool
	externalIDBefore string
	malformed        bool
}

// apply converges one directory record into the local base. Every state
// transition in the integration contract — created, suspended, reactivated,
// renamed, terminated — collapses into this one upsert, which is what makes
// replays free.
func (s *Syncer) apply(ctx context.Context, u scim.User, stats *Stats) (record, error) {
	stats.Scanned++

	if reason := validate(u); reason != "" {
		stats.Malformed++
		s.alertOnce("malformed:"+u.ID, "unusable directory record (id=%q): %s; skipped", u.ID, reason)
		return record{malformed: true}, nil
	}
	r := record{activeNow: *u.Active}

	p, err := s.st.PickerByDirectoryID(ctx, u.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if !r.activeNow {
			// Never create an account that is already disabled upstream.
			stats.Skipped++
			return r, nil
		}
		p, err = s.ensurePicker(ctx, u, stats)
		if err != nil {
			return r, err
		}
		return r, s.writeBack(ctx, u.ID, p.ID, stats)

	case err != nil:
		return r, fmt.Errorf("lookup %s: %w", u.ID, err)
	}

	r.existed = true
	r.enabledBefore = p.Enabled
	r.externalIDBefore = u.ExternalID

	if err := s.syncState(ctx, u, p, stats); err != nil {
		return r, err
	}

	// Covers the first sync and any later repair, including a directory record
	// that lost the value.
	if u.ExternalID != p.ID {
		return r, s.writeBack(ctx, u.ID, p.ID, stats)
	}
	return r, nil
}

// validate returns the reason a record cannot be used, or "" when it is usable.
//
// A missing active flag is the important one: read as "disabled" it would blank
// the whole fleet, so it is refused rather than guessed.
func validate(u scim.User) string {
	switch {
	case u.ID == "":
		return "no id"
	case u.Active == nil:
		return "no active flag"
	case u.UserName == "" && u.PrimaryEmail() == "":
		return "no userName and no email"
	}
	return ""
}

func (s *Syncer) ensurePicker(ctx context.Context, u scim.User, stats *Stats) (store.Picker, error) {
	if s.dryRun {
		stats.Created++
		s.log.Info("would create picker", "directory_id", u.ID)
		// The id a real store would assign is unknowable here; the placeholder
		// keeps the write-back accounted for without inventing a value that
		// could be mistaken for one.
		return store.Picker{DirectoryID: u.ID, ID: dryRunPickerID}, nil
	}

	p, err := s.st.CreatePicker(ctx, store.NewPicker{
		DirectoryID: u.ID,
		Login:       u.PrimaryEmail(),
		DisplayName: u.DisplayName,
	})
	if errors.Is(err, store.ErrAlreadyExists) {
		// Another worker won the race. Re-read and use theirs.
		if p, err = s.st.PickerByDirectoryID(ctx, u.ID); err != nil {
			return store.Picker{}, fmt.Errorf("re-read %s after create conflict: %w", u.ID, err)
		}
		return p, nil
	}
	if err != nil {
		return store.Picker{}, fmt.Errorf("create picker for %s: %w", u.ID, err)
	}
	stats.Created++
	s.log.Info("picker created", "directory_id", u.ID, "picker_id", p.ID)
	return p, nil
}

func (s *Syncer) syncState(ctx context.Context, u scim.User, p store.Picker, stats *Stats) error {
	active := *u.Active
	login := u.PrimaryEmail()

	if p.Enabled == active && p.DisplayName == u.DisplayName && p.Login == login {
		stats.Unchanged++
		return nil
	}

	if !s.dryRun {
		if err := s.st.UpdatePicker(ctx, p.ID, store.PickerUpdate{
			Enabled:     active,
			DisplayName: u.DisplayName,
			Login:       login,
		}); err != nil {
			return fmt.Errorf("update picker %s: %w", p.ID, err)
		}
	}

	verb := "would "
	if !s.dryRun {
		verb = ""
	}
	switch {
	case p.Enabled && !active:
		stats.Disabled++
		s.log.Info(verb+"disable picker", "directory_id", u.ID, "picker_id", p.ID)
	case !p.Enabled && active:
		stats.Enabled++
		s.log.Info(verb+"re-enable picker", "directory_id", u.ID, "picker_id", p.ID)
	default:
		stats.Updated++
	}
	return nil
}

func (s *Syncer) writeBack(ctx context.Context, directoryID, pickerID string, stats *Stats) error {
	if pickerID == "" {
		return fmt.Errorf("refusing to write back an empty picker id for %s", directoryID)
	}
	if s.dryRun {
		stats.WroteBack++
		s.log.Info("would write back picker_id", "directory_id", directoryID, "picker_id", pickerID)
		return nil
	}

	_, err := s.dir.PatchExternalID(ctx, directoryID, pickerID)
	if err == nil {
		stats.WroteBack++
		return nil
	}

	var scimErr *scim.Error
	if errors.As(err, &scimErr) && scimErr.Conflict() {
		// The picker_id is already bound to a different directory identity.
		// Retrying cannot fix it, and this record is re-read every cycle, so
		// the alert is raised once per identity rather than every five minutes.
		stats.Conflicts++
		s.alertOnce("conflict:"+directoryID,
			"write-back conflict: picker %s is already bound to another identity (directory_id=%s): %v",
			pickerID, directoryID, scimErr)
		return nil
	}
	return fmt.Errorf("write back picker_id for %s: %w", directoryID, err)
}

// alertOnce raises an alert the first time a given key occurs in this process.
// A permanent condition is re-read on every cycle; without this it would page
// someone every five minutes forever. A restart deliberately re-alerts once, so
// the problem resurfaces after a deploy.
func (s *Syncer) alertOnce(key, format string, args ...any) {
	s.mu.Lock()
	if s.alerted[key] {
		s.mu.Unlock()
		return
	}
	s.alerted[key] = true
	s.mu.Unlock()

	s.alert(format, args...)
}
