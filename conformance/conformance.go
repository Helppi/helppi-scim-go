// Package conformance checks a live directory against the integration
// contract, so "we finished Phase 1" is the output of a command rather than an
// opinion.
//
// Two ways in. A binary, for pointing at a sandbox and pasting the result into
// a ticket:
//
//	go run ./cmd/conformance -base-url https://…/scim/v2
//
// And a test helper, so the same cases run inside your CI:
//
//	func TestDirectoryConformance(t *testing.T) {
//	    conformance.Run(t, client, conformance.Options{AliasDomain: "separador.app"})
//	}
//
// Every check is read-only unless you name a record with WriteID. Even then,
// the write cases are built to leave the directory as they found it: the
// idempotency probe rewrites the value the record already has, and the
// refusal probe patches an attribute to the value it already holds, so a
// directory that wrongly accepts the write still changes nothing.
package conformance

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Helppi/helppi-scim-go/scim"
)

// Status is the outcome of one case.
type Status string

const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Skip Status = "SKIP"
)

// Phase groups cases by the rollout phase whose acceptance criteria they cover.
type Phase struct {
	Name  string
	Title string
}

var (
	PhaseOne = Phase{Name: "1", Title: "Directory synchronization"}
	PhaseTwo = Phase{Name: "2", Title: "Write-back of your identifier"}
)

// Case is one criterion, checked.
type Case struct {
	ID        string `json:"id"`
	Phase     string `json:"phase"`
	Title     string `json:"title"`
	Criterion string `json:"criterion"` // the section of the proposal it defends
	Status    Status `json:"status"`
	Detail    string `json:"detail,omitempty"`
}

// Report is the whole run.
type Report struct {
	BaseURL    string    `json:"base_url"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Cases      []Case    `json:"cases"`
}

// Counts returns passed, failed and skipped.
func (r Report) Counts() (passed, failed, skipped int) {
	for _, c := range r.Cases {
		switch c.Status {
		case Pass:
			passed++
		case Fail:
			failed++
		case Skip:
			skipped++
		}
	}
	return
}

// OK reports whether nothing failed. Skipped cases do not fail a run: they are
// checks the operator did not enable.
func (r Report) OK() bool {
	_, failed, _ := r.Counts()
	return failed == 0
}

// Options configures a run.
type Options struct {
	// AliasDomain, when set, checks that published identities are aliases on
	// that domain rather than real mailboxes (for example "separador.app").
	AliasDomain string

	// WriteID names a record reserved for write-back probing. Without it the
	// Phase 2 cases are skipped, because writing to an arbitrary record in
	// someone else's directory is not ours to do.
	WriteID string

	// ProbeExternalID is written when the reserved record has no externalId
	// yet. Leave it empty to keep the run strictly non-mutating.
	ProbeExternalID string

	// PageSize for the pagination probe. Default 2, deliberately small.
	PageSize int
}

// Directory is the slice of the SCIM client the suite exercises.
type Directory interface {
	Page(ctx context.Context, filter string, startIndex, count int) (*scim.ListResponse, error)
	GetUser(ctx context.Context, id string) (*scim.User, error)
	Patch(ctx context.Context, id string, op scim.PatchOp) (*scim.User, error)
	ServiceProviderConfig(ctx context.Context) (*scim.ProviderConfig, error)
}

// Check runs every case and returns the report. It never panics and never
// returns an error: a directory that cannot be reached is a failed case, which
// is exactly what the caller wants to see.
func Check(ctx context.Context, dir Directory, opts Options, baseURL string) Report {
	if opts.PageSize <= 0 {
		opts.PageSize = 2
	}
	r := &runner{ctx: ctx, dir: dir, opts: opts}
	r.report.BaseURL = baseURL
	r.report.StartedAt = time.Now()

	r.phaseOne()
	r.phaseTwo()

	r.report.FinishedAt = time.Now()
	return r.report
}

// Run executes the suite as a Go test, one subtest per case.
func Run(t *testing.T, dir Directory, opts Options) {
	t.Helper()
	report := Check(context.Background(), dir, opts, "")
	for _, c := range report.Cases {
		t.Run(c.ID+"_"+slug(c.Title), func(t *testing.T) {
			switch c.Status {
			case Fail:
				t.Errorf("%s (%s): %s", c.Title, c.Criterion, c.Detail)
			case Skip:
				t.Skip(c.Detail)
			}
		})
	}
}

func slug(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	return strings.Trim(s, "_")
}

// String renders the human-readable report. Report satisfies fmt.Stringer, so
// the binary is just fmt.Print(report).
func (r Report) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "helppi-scim-go conformance\n")
	if r.BaseURL != "" {
		fmt.Fprintf(&b, "%s\n", r.BaseURL)
	}
	b.WriteString("\n")

	byPhase := map[string][]Case{}
	var order []string
	for _, c := range r.Cases {
		if _, seen := byPhase[c.Phase]; !seen {
			order = append(order, c.Phase)
		}
		byPhase[c.Phase] = append(byPhase[c.Phase], c)
	}
	sort.Strings(order)

	for _, phase := range order {
		fmt.Fprintf(&b, "PHASE %s — %s\n", phase, phaseTitle(phase))
		for _, c := range byPhase[phase] {
			fmt.Fprintf(&b, "  %-4s  %-6s  %-46s  %s\n", c.Status, c.ID, c.Title, c.Criterion)
			if c.Detail != "" && c.Status != Pass {
				fmt.Fprintf(&b, "        %s\n", c.Detail)
			}
		}
		b.WriteString("\n")
	}

	passed, failed, skipped := r.Counts()
	fmt.Fprintf(&b, "%d passed, %d failed, %d skipped  (%s)\n",
		passed, failed, skipped, r.FinishedAt.Sub(r.StartedAt).Round(time.Millisecond))
	if failed == 0 && skipped == 0 {
		b.WriteString("\nEvery acceptance criterion for phases 1 and 2 is met.\n")
	}
	return b.String()
}

func phaseTitle(name string) string {
	switch name {
	case PhaseOne.Name:
		return PhaseOne.Title
	case PhaseTwo.Name:
		return PhaseTwo.Title
	}
	return ""
}
