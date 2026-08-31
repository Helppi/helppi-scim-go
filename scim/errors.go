package scim

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Error is the SCIM error payload (RFC 7644 §3.12).
//
// Status is a string on the wire — "404", not 404. Decoding it into an int is a
// common and silent bug, so the raw value is kept and Code() parses it.
type Error struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	ScimType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail,omitempty"`

	// HTTPStatus is the transport status, filled in by the client. It is the
	// authority when a server sends an empty or malformed body.
	HTTPStatus int `json:"-"`
}

func (e *Error) Error() string {
	detail := e.Detail
	if detail == "" {
		detail = http.StatusText(e.Code())
	}
	if e.ScimType != "" {
		return fmt.Sprintf("scim: %d %s: %s", e.Code(), e.ScimType, detail)
	}
	return fmt.Sprintf("scim: %d: %s", e.Code(), detail)
}

// Code returns the numeric status, preferring the transport status.
func (e *Error) Code() int {
	if e.HTTPStatus != 0 {
		return e.HTTPStatus
	}
	n, err := strconv.Atoi(e.Status)
	if err != nil {
		return 0
	}
	return n
}

// Conflict reports a 409: the picker_id is already bound to a different
// directory identity. Retrying cannot fix it — a human has to.
func (e *Error) Conflict() bool { return e.Code() == http.StatusConflict }

// Credential reports 401/403: the token is missing, revoked, or the operation
// is outside what this partner may do. Page someone; do not back off silently.
func (e *Error) Credential() bool {
	return e.Code() == http.StatusUnauthorized || e.Code() == http.StatusForbidden
}

// NotFound reports a 404: unknown id, or an id outside this partner's scope.
func (e *Error) NotFound() bool { return e.Code() == http.StatusNotFound }

func decodeError(resp *http.Response) *Error {
	e := &Error{HTTPStatus: resp.StatusCode}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(body) == 0 {
		return e
	}
	// A non-SCIM error page (proxy, gateway) must not mask the status code.
	var parsed Error
	if json.Unmarshal(body, &parsed) == nil {
		parsed.HTTPStatus = resp.StatusCode
		return &parsed
	}
	e.Detail = string(body)
	return e
}
