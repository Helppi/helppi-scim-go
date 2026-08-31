package scim_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/scimtest"
)

func newClient(t *testing.T, baseURL string) *scim.Client {
	t.Helper()
	c, err := scim.New(scim.Options{
		BaseURL: baseURL,
		Token:   scimtest.Token,
		// Never actually wait in tests.
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func fixtures(t *testing.T, name string) []scim.User {
	t.Helper()
	return scimtest.Load(t, filepath.Join("..", "testdata", name))
}

func TestListUsersWalksEveryPage(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	c := newClient(t, dir.URL)

	var ids []string
	// Page size 2 over 6 records: three full pages plus the terminating short page.
	if err := c.ListUsers(context.Background(), "", 2, func(u scim.User) error {
		ids = append(ids, u.ID)
		return nil
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 6 {
		t.Fatalf("got %d records, want 6: %v", len(ids), ids)
	}
	if ids[0] != "hlp_8fK2Lm91" {
		t.Errorf("unexpected first id %q", ids[0])
	}
}

func TestListUsersHonoursFilter(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	c := newClient(t, dir.URL)

	filter := `meta.lastModified gt "2026-08-27T00:00:00Z"`
	var got []string
	if err := c.ListUsers(context.Background(), filter, 100, func(u scim.User) error {
		got = append(got, u.ID)
		return nil
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	want := map[string]bool{"hlp_8fK2Lm91": true, "hlp_5kM1Zc08": true, "hlp_7hJ4Ls21": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d records", got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("record %s should not have matched the filter", id)
		}
	}
}

func TestActiveIsNilWhenAbsent(t *testing.T) {
	// The whole reason Active is a *bool: a record without the attribute must
	// decode to nil, never to false.
	dir := scimtest.New(t, fixtures(t, "malformed.json"))
	c := newClient(t, dir.URL)

	u, err := c.GetUser(context.Background(), "hlp_6nD8Qa14")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if u.Active != nil {
		t.Fatalf("Active = %v, want nil for a record with no active attribute", *u.Active)
	}
}

func TestRetriesOn429AndHonoursRetryAfter(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))

	var slept []time.Duration
	c, err := scim.New(scim.Options{
		BaseURL: dir.URL, Token: scimtest.Token,
		Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	dir.FailNext(http.StatusTooManyRequests, "7")
	dir.FailNext(http.StatusInternalServerError, "")

	n := 0
	if err := c.ListUsers(context.Background(), "", 100, func(scim.User) error { n++; return nil }); err != nil {
		t.Fatalf("list: %v", err)
	}
	if n != 6 {
		t.Fatalf("got %d records after retries, want 6", n)
	}
	if len(slept) != 2 {
		t.Fatalf("slept %v, want two waits (429 then 5xx)", slept)
	}
	if slept[0] != 7*time.Second {
		t.Errorf("first wait = %v, want the 7s from Retry-After", slept[0])
	}
}

func TestCredentialErrorIsNotRetried(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	c, err := scim.New(scim.Options{BaseURL: dir.URL, Token: "wrong-token",
		Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}

	err = c.ListUsers(context.Background(), "", 100, func(scim.User) error { return nil })
	var se *scim.Error
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a *scim.Error", err)
	}
	if !se.Credential() {
		t.Errorf("Credential() = false for %v", se)
	}
	if got := dir.Requests(); got != 1 {
		t.Errorf("directory saw %d requests, want exactly 1 — 401 must not be retried", got)
	}
}

func TestPatchExternalIDSendsTheContractualBody(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", scim.ContentType)
		_, _ = w.Write([]byte(`{"id":"hlp_8fK2Lm91","externalId":"782195","active":true}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	if _, err := c.PatchExternalID(context.Background(), "hlp_8fK2Lm91", "782195"); err != nil {
		t.Fatalf("patch: %v", err)
	}

	var op scim.PatchOp
	if err := json.Unmarshal(body, &op); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if len(op.Schemas) != 1 || op.Schemas[0] != scim.SchemaPatchOp {
		t.Errorf("schemas = %v", op.Schemas)
	}
	if len(op.Operations) != 1 {
		t.Fatalf("operations = %v", op.Operations)
	}
	if op.Operations[0].Op != "replace" || op.Operations[0].Path != "externalId" {
		t.Errorf("operation = %+v, want replace externalId", op.Operations[0])
	}
}

func TestPatchConflictIsTyped(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	dir.Bind("999999", "hlp_3vQ7Pd42") // value already belongs to someone else
	c := newClient(t, dir.URL)

	_, err := c.PatchExternalID(context.Background(), "hlp_8fK2Lm91", "999999")
	var se *scim.Error
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a *scim.Error", err)
	}
	if !se.Conflict() {
		t.Errorf("Conflict() = false for %v", se)
	}
	if se.ScimType != "uniqueness" {
		t.Errorf("scimType = %q", se.ScimType)
	}
}

func TestEmptyExternalIDIsRefusedLocally(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	c := newClient(t, dir.URL)
	if _, err := c.PatchExternalID(context.Background(), "hlp_8fK2Lm91", ""); err == nil {
		t.Fatal("expected a local error, got nil")
	}
	if dir.Requests() != 0 {
		t.Errorf("directory saw %d requests, want 0", dir.Requests())
	}
}
