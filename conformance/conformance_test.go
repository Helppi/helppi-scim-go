package conformance_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Helppi/helppi-scim-go/conformance"
	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/scimtest"
)

func client(t *testing.T, url, token string) *scim.Client {
	t.Helper()
	c, err := scim.New(scim.Options{
		BaseURL: url, Token: token,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func fixtures(t *testing.T, name string) []scim.User {
	t.Helper()
	return scimtest.Load(t, filepath.Join("..", "testdata", name))
}

func find(t *testing.T, r conformance.Report, id string) conformance.Case {
	t.Helper()
	for _, c := range r.Cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("case %s not present in the report", id)
	return conformance.Case{}
}

// The fake directory implements the contract, so it must pass the suite that
// checks the contract. If this ever fails, either the fake drifted or the
// contract changed.
func TestTheFakeDirectoryPassesEveryCase(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))

	report := conformance.Check(context.Background(), client(t, dir.URL, scimtest.Token),
		conformance.Options{
			AliasDomain: "separador.app",
			WriteID:     "hlp_3vQ7Pd42", // already carries an externalId
		}, dir.URL)

	passed, failed, skipped := report.Counts()
	if failed != 0 || skipped != 0 {
		for _, c := range report.Cases {
			if c.Status != conformance.Pass {
				t.Errorf("%s %s %s: %s", c.Status, c.ID, c.Title, c.Detail)
			}
		}
		t.Fatalf("%d passed, %d failed, %d skipped", passed, failed, skipped)
	}
	if !report.OK() {
		t.Error("OK() disagrees with the counts")
	}
	if passed < 14 {
		t.Errorf("only %d cases ran; the suite should cover both phases", passed)
	}
}

func TestAMissingActiveFlagFailsTheSuite(t *testing.T) {
	// The one attribute a client must never guess.
	dir := scimtest.New(t, fixtures(t, "malformed.json"))

	report := conformance.Check(context.Background(), client(t, dir.URL, scimtest.Token),
		conformance.Options{}, dir.URL)

	c := find(t, report, "P1.04")
	if c.Status != conformance.Fail {
		t.Fatalf("P1.04 = %s, want FAIL for a directory omitting active", c.Status)
	}
	if !strings.Contains(c.Detail, "active") {
		t.Errorf("the failure should name the attribute, got: %s", c.Detail)
	}
	if report.OK() {
		t.Error("a report containing a failure must not be OK")
	}
}

func TestARejectedCredentialBlocksTheRunClearly(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))

	report := conformance.Check(context.Background(), client(t, dir.URL, "wrong-token"),
		conformance.Options{}, dir.URL)

	if c := find(t, report, "P1.01"); c.Status != conformance.Fail {
		t.Fatalf("P1.01 = %s, want FAIL", c.Status)
	}
	// Everything downstream should say why it did not run, rather than
	// inventing failures that are really one failure repeated.
	_, failed, skipped := report.Counts()
	if failed != 1 {
		t.Errorf("failed = %d, want exactly 1: a bad credential is one problem", failed)
	}
	if skipped == 0 {
		t.Error("the remaining cases should be skipped with a reason")
	}
	if c := find(t, report, "P1.07"); !strings.Contains(c.Detail, "credential") {
		t.Errorf("skip reason should point at the credential, got: %s", c.Detail)
	}
}

func TestPhaseTwoIsSkippedWithoutAWriteTarget(t *testing.T) {
	// Writing to an arbitrary record in someone else's directory is not ours
	// to do, so the write cases are opt-in.
	dir := scimtest.New(t, fixtures(t, "directory.json"))

	report := conformance.Check(context.Background(), client(t, dir.URL, scimtest.Token),
		conformance.Options{AliasDomain: "separador.app"}, dir.URL)

	for _, id := range []string{"P2.01", "P2.02", "P2.03"} {
		if c := find(t, report, id); c.Status != conformance.Skip {
			t.Errorf("%s = %s, want SKIP without -write-id", id, c.Status)
		}
	}
	if !report.OK() {
		t.Error("skipped cases must not fail the run")
	}
	if len(dir.Patches()) != 0 {
		t.Errorf("a read-only run sent %d writes", len(dir.Patches()))
	}
}

func TestTheReportRendersReadably(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	report := conformance.Check(context.Background(), client(t, dir.URL, scimtest.Token),
		conformance.Options{AliasDomain: "separador.app"}, dir.URL)

	out := report.String()

	for _, want := range []string{"PHASE 1", "PHASE 2", "P1.01", "passed", "skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

// Run is what a partner calls from their own CI.
func TestRunAsAGoTest(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	conformance.Run(t, client(t, dir.URL, scimtest.Token), conformance.Options{
		AliasDomain: "separador.app",
		WriteID:     "hlp_3vQ7Pd42",
	})
}
