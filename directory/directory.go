// Package directory reconciles the local picker base against the Helppi directory.
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
	"time"

	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/store"
)

// Directory is the slice of the SCIM client the reconciler depends on.
type Directory interface {
	ListUsers(ctx context.Context, filter string, pageSize int, fn func(scim.User) error) error
	PatchExternalID(ctx context.Context, id, externalID string) (*scim.User, error)
}

// DefaultOverlap is re-read on every incremental cycle to absorb
// commit-visibility races on the directory side. It is safe because Apply is
// idempotent.
const DefaultOverlap = 2 * time.Minute

// Stats summarises one cycle.
type Stats struct {
	Scanned    int
	Created    int
	Enabled    int
	Disabled   int
	Renamed    int
	WroteBack  int
	Conflicts  int
	Unchanged  int
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

// Syncer runs the cycles. Construct it with New.
type Syncer struct {
	dir      Directory
	st       store.Store
	pageSize int
	overlap  time.Duration
	log      *slog.Logger
	alert    func(format string, args ...any)
	now      func() time.Time
}

// Options configures a Syncer.
type Options struct {
	PageSize int
	Overlap  time.Duration
	Logger   *slog.Logger
	// Alert is called for conditions a retry cannot fix — a write-back
	// conflict, above all. Wire it to your paging system.
	Alert func(format string, args ...any)
	Now   func() time.Time
}

// New builds a Syncer.
func New(dir Directory, st store.Store, opts Options) *Syncer {
	s := &Syncer{dir: dir, st: st, pageSize: opts.PageSize, overlap: opts.Overlap,
		log: opts.Logger, alert: opts.Alert, now: opts.Now}
	if s.pageSize <= 0 {
		s.pageSize = scim.DefaultPageSize
	}
	if s.overlap == 0 {
		s.overlap = DefaultOverlap
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

	var (
		stats Stats
		high  time.Time
	)
	err = s.dir.ListUsers(ctx, filter, s.pageSize, func(u scim.User) error {
		if err := s.apply(ctx, u, &stats); err != nil {
			return err
		}
		// The watermark comes from directory-issued timestamps, never from the
		// local clock: that makes skew between the two companies irrelevant.
		if u.Meta.LastModified.After(high) {
			high = u.Meta.LastModified
		}
		return nil
	})
	stats.Duration = s.now().Sub(started)
	if err != nil {
		return stats, err
	}

	stats.Checkpoint = cp
	if !high.IsZero() && high.After(cp) {
		if err := s.st.SetCheckpoint(ctx, high); err != nil {
			return stats, fmt.Errorf("advance checkpoint: %w", err)
		}
		stats.Checkpoint = high
	}
	return stats, nil
}

// Full walks the whole directory, applies every record and reports drift. Run
// it once a day: it is the net that catches anything the incremental path
// dropped.
func (s *Syncer) Full(ctx context.Context) (Stats, Drift, error) {
	started := s.now()
	var (
		stats  Stats
		drift  Drift
		seen   = map[string]bool{}
		high   time.Time
		errApp error
	)

	errApp = s.dir.ListUsers(ctx, "", s.pageSize, func(u scim.User) error {
		seen[u.ID] = true

		// Drift is measured *before* applying: after the upsert the two sides
		// agree by construction, and the report would always come back empty.
		if p, err := s.st.PickerByDirectoryID(ctx, u.ID); err == nil {
			if u.Active != nil && !*u.Active && p.Enabled {
				drift.ShouldBeDisabled = append(drift.ShouldBeDisabled, u.ID)
			}
			if u.ExternalID == "" {
				drift.MissingPickerID = append(drift.MissingPickerID, u.ID)
			}
		}

		if err := s.apply(ctx, u, &stats); err != nil {
			return err
		}
		if u.Meta.LastModified.After(high) {
			high = u.Meta.LastModified
		}
		return nil
	})
	stats.Duration = s.now().Sub(started)
	if errApp != nil {
		return stats, drift, errApp
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

	stats.Checkpoint, _ = s.st.Checkpoint(ctx)
	if !high.IsZero() && high.After(stats.Checkpoint) {
		if err := s.st.SetCheckpoint(ctx, high); err != nil {
			return stats, drift, fmt.Errorf("advance checkpoint: %w", err)
		}
		stats.Checkpoint = high
	}
	return stats, drift, nil
}

// apply converges one directory record into the local base. Every state
// transition described in the integration contract — created, suspended,
// reactivated, renamed, terminated — collapses into this one upsert, which is
// what makes replays free.
func (s *Syncer) apply(ctx context.Context, u scim.User, stats *Stats) error {
	stats.Scanned++

	if u.ID == "" {
		return errors.New("directory record without id")
	}
	// Fail closed. A missing active flag must never be read as "disabled":
	// that would blank the whole fleet on a malformed page.
	if u.Active == nil {
		return fmt.Errorf("directory record %s: missing active flag", u.ID)
	}
	active := *u.Active

	p, err := s.st.PickerByDirectoryID(ctx, u.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if !active {
			// Never create an account that is already disabled upstream.
			stats.Unchanged++
			return nil
		}
		p, err = s.st.CreatePicker(ctx, store.NewPicker{
			DirectoryID: u.ID,
			Login:       u.PrimaryEmail(),
			DisplayName: u.DisplayName,
		})
		if errors.Is(err, store.ErrAlreadyExists) {
			// Another worker won the race. Re-read and fall through.
			if p, err = s.st.PickerByDirectoryID(ctx, u.ID); err != nil {
				return fmt.Errorf("re-read %s after create conflict: %w", u.ID, err)
			}
			break
		}
		if err != nil {
			return fmt.Errorf("create picker for %s: %w", u.ID, err)
		}
		stats.Created++
		s.log.Info("picker created", "directory_id", u.ID, "picker_id", p.ID)
		return s.writeBack(ctx, u, p, stats)

	case err != nil:
		return fmt.Errorf("lookup %s: %w", u.ID, err)
	}

	login := u.PrimaryEmail()
	if p.Enabled != active || p.DisplayName != u.DisplayName || p.Login != login {
		if err := s.st.UpdatePicker(ctx, p.ID, active, u.DisplayName, login); err != nil {
			return fmt.Errorf("update picker %d: %w", p.ID, err)
		}
		switch {
		case p.Enabled && !active:
			stats.Disabled++
			s.log.Info("picker disabled", "directory_id", u.ID, "picker_id", p.ID)
		case !p.Enabled && active:
			stats.Enabled++
			s.log.Info("picker re-enabled", "directory_id", u.ID, "picker_id", p.ID)
		default:
			stats.Renamed++
		}
	} else {
		stats.Unchanged++
	}

	// Covers the first sync and any later repair, including a directory record
	// that lost the value.
	if u.ExternalID != p.PickerID() {
		return s.writeBack(ctx, u, p, stats)
	}
	return nil
}

func (s *Syncer) writeBack(ctx context.Context, u scim.User, p store.Picker, stats *Stats) error {
	_, err := s.dir.PatchExternalID(ctx, u.ID, p.PickerID())
	if err == nil {
		stats.WroteBack++
		return nil
	}

	var scimErr *scim.Error
	if errors.As(err, &scimErr) && scimErr.Conflict() {
		// The picker_id is already bound to a different directory identity.
		// Retrying cannot fix it and a retry loop would hide it.
		stats.Conflicts++
		s.alert("write-back conflict: picker %s already bound to another identity (directory_id=%s): %v",
			p.PickerID(), u.ID, scimErr)
		return nil
	}
	return fmt.Errorf("write back picker_id for %s: %w", u.ID, err)
}
