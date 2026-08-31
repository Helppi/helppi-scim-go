// Package scimtest is a fake Helppi directory: enough of the protocol to test a
// client end to end, plus fault injection for the failures the contract names.
//
// The fixtures under testdata are the conformance set: five lifecycle states
// plus the write-back cases. Both sides of the integration can test against the
// same bytes.
package scimtest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Helppi/helppi-scim-go/scim"
)

// Token is the bearer credential the fake directory expects.
const Token = "scim_test_0000000000"

// PatchRecord is one accepted write-back.
type PatchRecord struct {
	ID         string
	ExternalID string
}

type fault struct {
	status     int
	retryAfter string
}

// Directory is a fake SCIM server.
type Directory struct {
	*httptest.Server

	mu       sync.Mutex
	users    []scim.User
	bound    map[string]string // externalId -> directory id, to produce 409
	faults   []fault
	requests int
	patches  []PatchRecord
}

// New starts a fake directory serving the given records and closes it when the
// test finishes. It takes testing.TB so it works from tests and benchmarks.
func New(tb testing.TB, users []scim.User) *Directory {
	tb.Helper()
	d := Start(users)
	tb.Cleanup(d.Close)
	return d
}

// Start is New without a testing.TB, for examples and local experiments. The
// caller is responsible for calling Close.
func Start(users []scim.User) *Directory {
	d := &Directory{users: append([]scim.User(nil), users...), bound: map[string]string{}}
	for _, u := range d.users {
		if u.ExternalID != "" {
			d.bound[u.ExternalID] = u.ID
		}
	}
	d.Server = httptest.NewServer(http.HandlerFunc(d.handle))
	return d
}

// Load reads a fixture file of SCIM users.
func Load(tb testing.TB, path string) []scim.User {
	tb.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read fixture %s: %v", path, err)
	}
	var users []scim.User
	if err := json.Unmarshal(raw, &users); err != nil {
		tb.Fatalf("decode fixture %s: %v", path, err)
	}
	return users
}

// FailNext queues one synthetic failure, consumed by the next request.
func (d *Directory) FailNext(status int, retryAfter string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.faults = append(d.faults, fault{status: status, retryAfter: retryAfter})
}

// Requests returns how many HTTP requests the directory has served.
func (d *Directory) Requests() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests
}

// Patches returns the accepted write-backs, in order.
func (d *Directory) Patches() []PatchRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]PatchRecord(nil), d.patches...)
}

// User returns a record by directory id.
func (d *Directory) User(id string) (scim.User, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, u := range d.users {
		if u.ID == id {
			return u, true
		}
	}
	return scim.User{}, false
}

// Touch mutates a record and bumps meta.lastModified, the way the real
// directory does when a professional changes state.
func (d *Directory) Touch(id string, at time.Time, mutate func(*scim.User)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.users {
		if d.users[i].ID != id {
			continue
		}
		mutate(&d.users[i])
		d.users[i].Meta.LastModified = at
		return
	}
}

// Bind pre-associates an externalId with a directory id so a later write-back
// of the same value from a different identity produces a 409.
func (d *Directory) Bind(externalID, directoryID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bound[externalID] = directoryID
}

var filterRE = regexp.MustCompile(`meta\.lastModified\s+gt\s+"([^"]+)"`)

func (d *Directory) handle(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.requests++
	var f *fault
	if len(d.faults) > 0 {
		f, d.faults = &d.faults[0], d.faults[1:]
	}
	d.mu.Unlock()

	if f != nil {
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		d.writeError(w, f.status, "", "synthetic failure")
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+Token {
		d.writeError(w, http.StatusUnauthorized, "", "invalid credential")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && path == "Users":
		d.list(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "Users/"):
		d.get(w, strings.TrimPrefix(path, "Users/"))
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "Users/"):
		d.patch(w, r, strings.TrimPrefix(path, "Users/"))
	case r.Method == http.MethodGet && path == "ServiceProviderConfig":
		d.writeJSON(w, http.StatusOK, map[string]any{
			"patch":  map[string]any{"supported": true},
			"filter": map[string]any{"supported": true, "maxResults": 500},
		})
	default:
		// The directory refuses everything else, including POST/PUT/DELETE.
		d.writeError(w, http.StatusForbidden, "mutability",
			"this partner may only read; the sole writable attribute is externalId")
	}
}

func (d *Directory) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	startIndex, _ := strconv.Atoi(q.Get("startIndex"))
	if startIndex < 1 {
		startIndex = 1
	}
	count, _ := strconv.Atoi(q.Get("count"))
	if count <= 0 {
		count = 100
	}

	var since time.Time
	if m := filterRE.FindStringSubmatch(q.Get("filter")); m != nil {
		if t, err := time.Parse(time.RFC3339, m[1]); err == nil {
			since = t
		}
	}

	d.mu.Lock()
	matched := make([]scim.User, 0, len(d.users))
	for _, u := range d.users {
		if since.IsZero() || u.Meta.LastModified.After(since) {
			matched = append(matched, u)
		}
	}
	d.mu.Unlock()

	page := matched
	if startIndex-1 < len(page) {
		page = page[startIndex-1:]
	} else {
		page = nil
	}
	if len(page) > count {
		page = page[:count]
	}

	resources := make([]json.RawMessage, 0, len(page))
	for _, u := range page {
		u.Schemas = []string{scim.SchemaUser}
		raw, _ := json.Marshal(u)
		resources = append(resources, raw)
	}
	d.writeJSON(w, http.StatusOK, scim.ListResponse{
		Schemas:      []string{scim.SchemaList},
		TotalResults: len(matched),
		StartIndex:   startIndex,
		ItemsPerPage: len(resources),
		Resources:    resources,
	})
}

func (d *Directory) get(w http.ResponseWriter, id string) {
	u, ok := d.User(id)
	if !ok {
		d.writeError(w, http.StatusNotFound, "", "unknown identifier for this partner")
		return
	}
	u.Schemas = []string{scim.SchemaUser}
	d.writeJSON(w, http.StatusOK, u)
}

func (d *Directory) patch(w http.ResponseWriter, r *http.Request, id string) {
	var op scim.PatchOp
	if err := json.NewDecoder(r.Body).Decode(&op); err != nil {
		d.writeError(w, http.StatusBadRequest, "invalidSyntax", "malformed PatchOp")
		return
	}
	if _, ok := d.User(id); !ok {
		d.writeError(w, http.StatusNotFound, "", "unknown identifier for this partner")
		return
	}
	for _, o := range op.Operations {
		if o.Path != "externalId" {
			d.writeError(w, http.StatusForbidden, "mutability",
				"attribute is read-only for this partner; writable attributes: externalId")
			return
		}
		value, _ := o.Value.(string)

		d.mu.Lock()
		if owner, taken := d.bound[value]; taken && owner != id {
			d.mu.Unlock()
			d.writeError(w, http.StatusConflict, "uniqueness",
				"externalId already assigned to another identity")
			return
		}
		d.bound[value] = id
		for i := range d.users {
			if d.users[i].ID == id {
				d.users[i].ExternalID = value
				// The partner's own write bumps lastModified — the record comes
				// back on the next incremental cycle. Expected and harmless.
				d.users[i].Meta.LastModified = d.users[i].Meta.LastModified.Add(time.Second)
			}
		}
		d.patches = append(d.patches, PatchRecord{ID: id, ExternalID: value})
		d.mu.Unlock()
	}
	u, _ := d.User(id)
	u.Schemas = []string{scim.SchemaUser}
	d.writeJSON(w, http.StatusOK, u)
}

func (d *Directory) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", scim.ContentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (d *Directory) writeError(w http.ResponseWriter, status int, scimType, detail string) {
	d.writeJSON(w, status, scim.Error{
		Schemas:  []string{scim.SchemaError},
		Status:   strconv.Itoa(status),
		ScimType: scimType,
		Detail:   detail,
	})
}
