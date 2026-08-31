package scim_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/scimtest"
)

func TestRejectsAnHTMLResponseInsteadOfReadingItAsAnEmptyDirectory(t *testing.T) {
	// A captive portal, a WAF block page or a misrouted proxy answers 200 with
	// HTML. Decoded loosely that becomes "the directory has nobody in it",
	// which a full walk would then report as everyone having disappeared.
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	dir.RespondNextRaw(http.StatusOK, "text/html; charset=utf-8",
		"<html><body>Access denied by network policy</body></html>")
	c := newClient(t, dir.URL)

	n := 0
	err := c.ListUsers(context.Background(), "", 100, func(scim.User) error { n++; return nil })
	if err == nil {
		t.Fatalf("expected an error, got a successful walk over %d records", n)
	}
	if !strings.Contains(err.Error(), "Content-Type") {
		t.Errorf("error should name the unexpected Content-Type, got: %v", err)
	}
}

func TestRejectsJSONThatIsNotAListResponse(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	dir.RespondNextRaw(http.StatusOK, "application/json", `{"data":[],"page":1}`)
	c := newClient(t, dir.URL)

	err := c.ListUsers(context.Background(), "", 100, func(scim.User) error { return nil })
	if err == nil {
		t.Fatal("a JSON body that is not a SCIM ListResponse must not read as an empty directory")
	}
}

func TestNonSCIMErrorBodyIsPreserved(t *testing.T) {
	// A gateway's own JSON is valid JSON, so a naive decode produces an empty
	// SCIM error and throws away the only text that explains the failure.
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	dir.RespondNextRaw(http.StatusForbidden, "application/json",
		`{"message":"blocked by web application firewall","ref":"WAF-8812"}`)
	c := newClient(t, dir.URL)

	err := c.ListUsers(context.Background(), "", 100, func(scim.User) error { return nil })
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "web application firewall") {
		t.Errorf("the gateway's own message must survive; got: %v", err)
	}

	var se *scim.Error
	if !errors.As(err, &se) || se.Code() != http.StatusForbidden {
		t.Errorf("want a typed 403, got %v", err)
	}
}

func TestRetryAfterAcceptsAnHTTPDate(t *testing.T) {
	var slept []time.Duration
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", time.Now().Add(4*time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", scim.ContentType)
		_, _ = w.Write([]byte(`{"schemas":["` + scim.SchemaList + `"],"totalResults":0,"Resources":[]}`))
	}))
	defer srv.Close()

	c, err := scim.New(scim.Options{BaseURL: srv.URL, Token: scimtest.Token,
		Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ListUsers(context.Background(), "", 100, func(scim.User) error { return nil }); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(slept) != 1 {
		t.Fatalf("slept %v, want exactly one wait", slept)
	}
	if slept[0] < 2*time.Second || slept[0] > 5*time.Second {
		t.Errorf("wait = %v, want roughly the 4s named by the HTTP-date", slept[0])
	}
}

func TestPaginationStopsWhenTheDirectoryIgnoresStartIndex(t *testing.T) {
	// A directory that ignores startIndex — or a proxy caching page one — would
	// otherwise keep this client requesting the same page forever.
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	dir.IgnoreStartIndex(true)
	c := newClient(t, dir.URL)

	seen := 0
	err := c.ListUsers(context.Background(), "", 2, func(scim.User) error { seen++; return nil })
	if err == nil {
		t.Fatal("expected the walk to abort instead of looping")
	}
	if !strings.Contains(err.Error(), "startIndex") {
		t.Errorf("error should explain the pagination fault, got: %v", err)
	}
	if seen > 20 {
		t.Errorf("read %d records before giving up; the guard should trip almost immediately", seen)
	}
}

func TestRequestsAreThrottled(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	var slept []time.Duration
	c, err := scim.New(scim.Options{
		BaseURL: dir.URL, Token: scimtest.Token,
		RequestsPerSecond: 2, // one request every 500ms
		Sleep:             func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Page size 2 over 6 records is four requests.
	if err := c.ListUsers(context.Background(), "", 2, func(scim.User) error { return nil }); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(slept) < 2 {
		t.Fatalf("throttle waited %d times over four requests, want at least 2: %v", len(slept), slept)
	}
	if slept[0] < 400*time.Millisecond {
		t.Errorf("first throttle wait = %v, want about 500ms at 2 rps", slept[0])
	}
}

func TestServiceProviderConfigReportsCapabilities(t *testing.T) {
	dir := scimtest.New(t, fixtures(t, "directory.json"))
	c := newClient(t, dir.URL)

	cfg, err := c.ServiceProviderConfig(context.Background())
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if !cfg.Filter.Supported {
		t.Error("filter support should be reported: without it there is no incremental sync")
	}
	if !cfg.Patch.Supported {
		t.Error("patch support should be reported: without it the write-back cannot happen")
	}
	if cfg.Filter.MaxResults == 0 {
		t.Error("maxResults should be reported so a client can size its pages")
	}
}

func TestDefaultUserAgentNamesThisLibrary(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", scim.ContentType)
		_, _ = w.Write([]byte(`{"id":"hlp_1","active":true}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	if _, err := c.GetUser(context.Background(), "hlp_1"); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.HasPrefix(ua, "helppi-scim-go/") {
		t.Errorf("User-Agent = %q, want it to name this library", ua)
	}
}
