package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Helppi/helppi-scim-go/scim"
)

type runner struct {
	ctx    context.Context
	dir    Directory
	opts   Options
	report Report

	// carried between cases so the suite stays cheap
	page    *scim.ListResponse
	users   []scim.User
	sample  scim.User
	newest  time.Time
	blocked string // set when the directory is unusable; later cases skip
}

func (r *runner) add(phase Phase, id, title, criterion string, status Status, detail string) {
	r.report.Cases = append(r.report.Cases, Case{
		ID: id, Phase: phase.Name, Title: title, Criterion: criterion,
		Status: status, Detail: detail,
	})
}

func (r *runner) pass(phase Phase, id, title, criterion, detail string) {
	r.add(phase, id, title, criterion, Pass, detail)
}

func (r *runner) fail(phase Phase, id, title, criterion, detail string) {
	r.add(phase, id, title, criterion, Fail, detail)
}

func (r *runner) skip(phase Phase, id, title, criterion, reason string) {
	r.add(phase, id, title, criterion, Skip, reason)
}

// ---------------------------------------------------------------- phase one

func (r *runner) phaseOne() {
	r.checkCredential()
	r.checkEnvelope()
	r.checkStableIDs()
	r.checkActiveFlag()
	r.checkAlias()
	r.checkMeta()
	r.checkPagination()
	r.checkFiltering()
	r.checkSingleRead()
	r.checkUnknownID()
	r.checkProviderConfig()
}

func (r *runner) checkCredential() {
	const id, title, crit = "P1.01", "Credential is accepted", "§13 configuration"

	page, err := r.dir.Page(r.ctx, "", 1, r.opts.PageSize)
	if err != nil {
		var se *scim.Error
		if errors.As(err, &se) && se.Credential() {
			r.blocked = "the credential was rejected"
			r.fail(PhaseOne, id, title, crit,
				fmt.Sprintf("the directory rejected the token: %v", err))
			return
		}
		r.blocked = "the directory could not be read"
		r.fail(PhaseOne, id, title, crit, err.Error())
		return
	}

	r.page = page
	users, err := page.Users()
	if err != nil {
		r.blocked = "the directory returned records that cannot be decoded"
		r.fail(PhaseOne, id, title, crit, err.Error())
		return
	}
	r.users = users
	if len(users) > 0 {
		r.sample = users[0]
	}
	for _, u := range users {
		if u.Meta.LastModified.After(r.newest) {
			r.newest = u.Meta.LastModified
		}
	}
	r.pass(PhaseOne, id, title, crit, fmt.Sprintf("%d records visible", page.TotalResults))
}

// blockedSkip reports whether the remaining cases should be skipped.
func (r *runner) blockedSkip(phase Phase, id, title, crit string) bool {
	if r.blocked == "" {
		return false
	}
	r.skip(phase, id, title, crit, r.blocked)
	return true
}

func (r *runner) checkEnvelope() {
	const id, title, crit = "P1.02", "Responses are SCIM ListResponses", "Appendix A — envelope"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}
	// Page already refuses anything without the ListResponse schema, so
	// reaching here proves the envelope. What is left is the paging metadata a
	// client needs in order to walk safely.
	if r.page.TotalResults < len(r.page.Resources) {
		r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
			"totalResults=%d is smaller than the %d records in this page",
			r.page.TotalResults, len(r.page.Resources)))
		return
	}
	if len(r.page.Resources) > 0 && r.page.StartIndex != 1 {
		r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
			"startIndex echoed as %d for the first page; RFC 7644 is 1-based", r.page.StartIndex))
		return
	}
	r.pass(PhaseOne, id, title, crit,
		fmt.Sprintf("totalResults=%d itemsPerPage=%d", r.page.TotalResults, r.page.ItemsPerPage))
}

func (r *runner) checkStableIDs() {
	const id, title, crit = "P1.03", "Every record carries a stable id", "§05 identity model"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}
	if len(r.users) == 0 {
		r.skip(PhaseOne, id, title, crit, "the directory returned no records")
		return
	}

	seen := map[string]bool{}
	for _, u := range r.users {
		if strings.TrimSpace(u.ID) == "" {
			r.fail(PhaseOne, id, title, crit, "a record was returned with an empty id")
			return
		}
		if seen[u.ID] {
			r.fail(PhaseOne, id, title, crit, "id "+u.ID+" appeared twice in one page")
			return
		}
		seen[u.ID] = true
	}
	r.pass(PhaseOne, id, title, crit, fmt.Sprintf("%d unique ids", len(seen)))
}

func (r *runner) checkActiveFlag() {
	const id, title, crit = "P1.04", "active is present on every record", "§06 lifecycle"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}
	if len(r.users) == 0 {
		r.skip(PhaseOne, id, title, crit, "the directory returned no records")
		return
	}

	// The single most consequential attribute in the contract. A client that
	// reads a missing flag as "disabled" blocks the whole fleet; one that reads
	// it as "enabled" leaves closed accounts able to work. Neither is acceptable,
	// so the directory must always send it.
	for _, u := range r.users {
		if u.Active == nil {
			r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
				"record %s has no active attribute; a client cannot safely guess it", u.ID))
			return
		}
	}
	r.pass(PhaseOne, id, title, crit, "present on all sampled records")
}

func (r *runner) checkAlias() {
	const id, title, crit = "P1.05", "Identities are aliases, not mailboxes", "§05 data minimization"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}
	if r.opts.AliasDomain == "" {
		r.skip(PhaseOne, id, title, crit, "pass an alias domain to check data minimization")
		return
	}
	if len(r.users) == 0 {
		r.skip(PhaseOne, id, title, crit, "the directory returned no records")
		return
	}

	suffix := "@" + strings.TrimPrefix(r.opts.AliasDomain, "@")
	for _, u := range r.users {
		if !strings.HasSuffix(strings.ToLower(u.UserName), suffix) {
			r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
				"record %s publishes userName outside %s; the directory should expose an alias, "+
					"never a real address", u.ID, suffix))
			return
		}
		for _, e := range u.Emails {
			if !strings.HasSuffix(strings.ToLower(e.Value), suffix) {
				r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
					"record %s publishes an e-mail outside %s", u.ID, suffix))
				return
			}
		}
	}
	r.pass(PhaseOne, id, title, crit, "all identities on "+suffix)
}

func (r *runner) checkMeta() {
	const id, title, crit = "P1.06", "meta.lastModified is usable as a watermark", "§08 synchronization"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}
	if len(r.users) == 0 {
		r.skip(PhaseOne, id, title, crit, "the directory returned no records")
		return
	}

	for _, u := range r.users {
		if u.Meta.LastModified.IsZero() {
			r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
				"record %s has no meta.lastModified; incremental synchronization is impossible "+
					"without it", u.ID))
			return
		}
	}
	if skew := time.Until(r.newest); skew > time.Hour {
		r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
			"the newest timestamp is %s in the future; a client that trusted it as a watermark "+
				"would skip everything in between", skew.Round(time.Minute)))
		return
	}
	r.pass(PhaseOne, id, title, crit, "newest "+r.newest.UTC().Format(time.RFC3339))
}

func (r *runner) checkPagination() {
	const id, title, crit = "P1.07", "startIndex and count are honored", "Appendix A — pagination"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}
	if r.page.TotalResults < 2 {
		r.skip(PhaseOne, id, title, crit, "needs at least two records in the directory")
		return
	}

	if len(r.page.Resources) > r.opts.PageSize {
		r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
			"asked for %d records, received %d", r.opts.PageSize, len(r.page.Resources)))
		return
	}

	second, err := r.dir.Page(r.ctx, "", 2, r.opts.PageSize)
	if err != nil {
		r.fail(PhaseOne, id, title, crit, "second page: "+err.Error())
		return
	}
	users, err := second.Users()
	if err != nil {
		r.fail(PhaseOne, id, title, crit, "second page: "+err.Error())
		return
	}
	if len(users) == 0 {
		r.fail(PhaseOne, id, title, crit, "startIndex=2 returned nothing despite more records existing")
		return
	}
	// A directory that ignores startIndex hands back page one forever, and a
	// client walking it never terminates.
	if users[0].ID == r.sample.ID {
		r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
			"startIndex=2 returned the same first record (%s) as startIndex=1; "+
				"pagination would never terminate", users[0].ID))
		return
	}
	r.pass(PhaseOne, id, title, crit, "the window moves with startIndex")
}

func (r *runner) checkFiltering() {
	const id, title, crit = "P1.08", "Filtering by meta.lastModified works", "§08 incremental sync"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}
	if r.newest.IsZero() {
		r.skip(PhaseOne, id, title, crit, "no usable timestamps to filter on")
		return
	}

	// Everything modified after the newest record we saw: should be empty or
	// nearly so. If the directory ignores the filter it returns the whole
	// directory instead, and incremental synchronization silently becomes a
	// full walk every five minutes.
	future := r.newest.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	narrowed, err := r.dir.Page(r.ctx, fmt.Sprintf("meta.lastModified gt %q", future), 1, r.opts.PageSize)
	if err != nil {
		r.fail(PhaseOne, id, title, crit, "filtered query: "+err.Error())
		return
	}
	if narrowed.TotalResults >= r.page.TotalResults && r.page.TotalResults > 0 {
		r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
			"a filter for changes after %s still returned %d of %d records; the filter appears "+
				"to be ignored", future, narrowed.TotalResults, r.page.TotalResults))
		return
	}

	// And the opposite bound: everything since the epoch is everything.
	all, err := r.dir.Page(r.ctx, `meta.lastModified gt "1970-01-01T00:00:00Z"`, 1, r.opts.PageSize)
	if err != nil {
		r.fail(PhaseOne, id, title, crit, "epoch query: "+err.Error())
		return
	}
	if all.TotalResults < r.page.TotalResults {
		r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
			"a filter since the epoch returned %d records but the directory holds %d",
			all.TotalResults, r.page.TotalResults))
		return
	}
	r.pass(PhaseOne, id, title, crit, fmt.Sprintf(
		"narrowed to %d, unfiltered %d", narrowed.TotalResults, r.page.TotalResults))
}

func (r *runner) checkSingleRead() {
	const id, title, crit = "P1.09", "A record can be read by id", "Appendix A — endpoints"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}
	if r.sample.ID == "" {
		r.skip(PhaseOne, id, title, crit, "the directory returned no records")
		return
	}

	got, err := r.dir.GetUser(r.ctx, r.sample.ID)
	if err != nil {
		r.fail(PhaseOne, id, title, crit, err.Error())
		return
	}
	if got.ID != r.sample.ID {
		r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
			"asked for %s, received %s", r.sample.ID, got.ID))
		return
	}
	if got.Active == nil {
		r.fail(PhaseOne, id, title, crit, "the single-record read omits active")
		return
	}
	r.pass(PhaseOne, id, title, crit, "matches the record from the listing")
}

func (r *runner) checkUnknownID() {
	const id, title, crit = "P1.10", "An unknown id is a typed 404", "Appendix A — error matrix"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}

	_, err := r.dir.GetUser(r.ctx, "hlp_conformance_probe_does_not_exist")
	if err == nil {
		r.fail(PhaseOne, id, title, crit, "an identifier that cannot exist returned a record")
		return
	}
	var se *scim.Error
	if !errors.As(err, &se) {
		r.fail(PhaseOne, id, title, crit, "the error is not a SCIM error payload: "+err.Error())
		return
	}
	if !se.NotFound() {
		r.fail(PhaseOne, id, title, crit, fmt.Sprintf(
			"expected 404, received %d: %v", se.Code(), se))
		return
	}
	r.pass(PhaseOne, id, title, crit, "404 with a SCIM error envelope")
}

func (r *runner) checkProviderConfig() {
	const id, title, crit = "P1.11", "Capabilities are advertised", "§13 configuration"
	if r.blockedSkip(PhaseOne, id, title, crit) {
		return
	}

	cfg, err := r.dir.ServiceProviderConfig(r.ctx)
	if err != nil {
		r.skip(PhaseOne, id, title, crit,
			"ServiceProviderConfig is not exposed; not every deployment does: "+err.Error())
		return
	}
	var missing []string
	if !cfg.Filter.Supported {
		missing = append(missing, "filter (incremental synchronization needs it)")
	}
	if !cfg.Patch.Supported {
		missing = append(missing, "patch (the write-back needs it)")
	}
	if len(missing) > 0 {
		r.fail(PhaseOne, id, title, crit, "not advertised: "+strings.Join(missing, "; "))
		return
	}
	r.pass(PhaseOne, id, title, crit, fmt.Sprintf("filter and patch, maxResults=%d", cfg.Filter.MaxResults))
}

// ---------------------------------------------------------------- phase two

func (r *runner) phaseTwo() {
	r.checkWriteBack()
	r.checkWriteBackIdempotent()
	r.checkForbiddenAttribute()
}

const noWriteTarget = "pass the id of a record reserved for write probing"

func (r *runner) writeTarget() (scim.User, bool) {
	if r.blocked != "" || r.opts.WriteID == "" {
		return scim.User{}, false
	}
	u, err := r.dir.GetUser(r.ctx, r.opts.WriteID)
	if err != nil {
		return scim.User{}, false
	}
	return *u, true
}

func (r *runner) checkWriteBack() {
	const id, title, crit = "P2.01", "externalId accepts our identifier", "§07 write-back"
	if r.blocked != "" {
		r.skip(PhaseTwo, id, title, crit, r.blocked)
		return
	}
	if r.opts.WriteID == "" {
		r.skip(PhaseTwo, id, title, crit, noWriteTarget)
		return
	}

	u, ok := r.writeTarget()
	if !ok {
		r.fail(PhaseTwo, id, title, crit, "could not read the record named for write probing: "+r.opts.WriteID)
		return
	}

	// Prefer rewriting the value the record already carries: proves the write
	// path without changing anything at all.
	value := u.ExternalID
	if value == "" {
		if r.opts.ProbeExternalID == "" {
			r.skip(PhaseTwo, id, title, crit,
				"the record has no externalId yet; supply a probe value to write one")
			return
		}
		value = r.opts.ProbeExternalID
	}

	got, err := r.dir.Patch(r.ctx, u.ID, scim.NewExternalIDPatch(value))
	if err != nil {
		r.fail(PhaseTwo, id, title, crit, err.Error())
		return
	}
	if got.ExternalID != value {
		r.fail(PhaseTwo, id, title, crit, fmt.Sprintf(
			"wrote %q, the record came back with %q", value, got.ExternalID))
		return
	}
	r.pass(PhaseTwo, id, title, crit, "accepted and reflected on read")
}

func (r *runner) checkWriteBackIdempotent() {
	const id, title, crit = "P2.02", "Writing the same value twice changes nothing", "§08 idempotency"
	if r.blocked != "" {
		r.skip(PhaseTwo, id, title, crit, r.blocked)
		return
	}
	if r.opts.WriteID == "" {
		r.skip(PhaseTwo, id, title, crit, noWriteTarget)
		return
	}

	u, ok := r.writeTarget()
	if !ok || u.ExternalID == "" {
		r.skip(PhaseTwo, id, title, crit, "needs a record that already carries an externalId")
		return
	}

	// Our own write bumps meta.lastModified, so the record returns on the next
	// incremental cycle and is applied again. That must be harmless.
	for i := 0; i < 2; i++ {
		got, err := r.dir.Patch(r.ctx, u.ID, scim.NewExternalIDPatch(u.ExternalID))
		if err != nil {
			r.fail(PhaseTwo, id, title, crit, fmt.Sprintf("write %d: %v", i+1, err))
			return
		}
		if got.ExternalID != u.ExternalID {
			r.fail(PhaseTwo, id, title, crit, fmt.Sprintf(
				"write %d changed the value to %q", i+1, got.ExternalID))
			return
		}
	}
	r.pass(PhaseTwo, id, title, crit, "repeated writes are a no-op")
}

func (r *runner) checkForbiddenAttribute() {
	const id, title, crit = "P2.03", "Authoritative attributes are read-only", "§12 responsibilities"
	if r.blocked != "" {
		r.skip(PhaseTwo, id, title, crit, r.blocked)
		return
	}
	if r.opts.WriteID == "" {
		r.skip(PhaseTwo, id, title, crit, noWriteTarget)
		return
	}

	u, ok := r.writeTarget()
	if !ok || u.Active == nil {
		r.skip(PhaseTwo, id, title, crit, "could not read the record named for write probing")
		return
	}

	// Deliberately patch active to the value it already holds. If the
	// directory correctly refuses the write, we learn that. If it wrongly
	// accepts it, nobody's access changed — which is the only responsible way
	// to probe this.
	_, err := r.dir.Patch(r.ctx, u.ID, scim.PatchOp{
		Schemas:    []string{scim.SchemaPatchOp},
		Operations: []scim.Operation{{Op: "replace", Path: "active", Value: *u.Active}},
	})
	if err == nil {
		r.fail(PhaseTwo, id, title, crit,
			"the directory accepted a write to active; only externalId may be written by this "+
				"credential, or a partner can silently re-enable someone")
		return
	}

	var se *scim.Error
	if !errors.As(err, &se) {
		r.fail(PhaseTwo, id, title, crit, "the refusal is not a SCIM error payload: "+err.Error())
		return
	}
	if se.Code() != 403 && se.Code() != 400 {
		r.fail(PhaseTwo, id, title, crit, fmt.Sprintf(
			"expected 403, received %d: %v", se.Code(), se))
		return
	}
	r.pass(PhaseTwo, id, title, crit, fmt.Sprintf("refused with %d", se.Code()))
}
