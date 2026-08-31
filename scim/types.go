// Package scim implements the subset of SCIM 2.0 (RFC 7643 / RFC 7644) needed
// to consume a partner directory: list, read and a single-attribute PATCH.
//
// It carries no business logic. Nothing in this package knows what a picker is.
package scim

import (
	"encoding/json"
	"time"
)

// Schema URNs used on the wire.
const (
	SchemaUser    = "urn:ietf:params:scim:schemas:core:2.0:User"
	SchemaList    = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	SchemaPatchOp = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	SchemaError   = "urn:ietf:params:scim:api:messages:2.0:Error"

	// ContentType is required on every request and response body.
	ContentType = "application/scim+json"
)

// Name holds the minimized name published by the directory. The directory
// deliberately publishes an abbreviated family name (e.g. "C."), so never treat
// these fields as a legal name.
type Name struct {
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

// Email is the alias address published by the directory. It is an identifier in
// e-mail shape; it is not the person's real address.
type Email struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
	Type    string `json:"type,omitempty"`
}

// Meta is server-controlled. Never send it back.
type Meta struct {
	ResourceType string    `json:"resourceType,omitempty"`
	Created      time.Time `json:"created,omitempty"`
	LastModified time.Time `json:"lastModified,omitempty"`
}

// User is the directory projection of one professional.
//
// Active is a pointer on purpose. With a plain bool, a truncated response or a
// missing attribute decodes to false, and a reconciler would happily disable
// every account it sees. A nil Active must be treated as an error, never as
// "disabled" — see sync.Syncer.Apply.
type User struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id"`
	ExternalID  string   `json:"externalId,omitempty"`
	UserName    string   `json:"userName"`
	DisplayName string   `json:"displayName,omitempty"`
	Name        Name     `json:"name"`
	Emails      []Email  `json:"emails,omitempty"`
	Active      *bool    `json:"active"`
	Meta        Meta     `json:"meta"`
}

// PrimaryEmail returns the alias marked primary, falling back to userName.
func (u User) PrimaryEmail() string {
	for _, e := range u.Emails {
		if e.Primary {
			return e.Value
		}
	}
	if len(u.Emails) > 0 {
		return u.Emails[0].Value
	}
	return u.UserName
}

// ProviderConfig is the subset of ServiceProviderConfig this client cares
// about. A directory without filter support cannot be synchronized
// incrementally; one without PATCH support cannot receive the picker_id.
type ProviderConfig struct {
	Schemas []string `json:"schemas"`
	Patch   struct {
		Supported bool `json:"supported"`
	} `json:"patch"`
	Filter struct {
		Supported  bool `json:"supported"`
		MaxResults int  `json:"maxResults"`
	} `json:"filter"`
}

// ListResponse is the envelope of every query. Resources stays raw so a single
// malformed record cannot destroy the whole page, and so unknown attributes
// survive decoding untouched.
type ListResponse struct {
	Schemas      []string          `json:"schemas"`
	TotalResults int               `json:"totalResults"`
	StartIndex   int               `json:"startIndex"`
	ItemsPerPage int               `json:"itemsPerPage"`
	Resources    []json.RawMessage `json:"Resources"` // capital R, per RFC 7644
}

// PatchOp is the request body of a PATCH. Only externalId may be written by
// this client; see Client.PatchExternalID.
type PatchOp struct {
	Schemas    []string    `json:"schemas"`
	Operations []Operation `json:"Operations"`
}

// Operation is a single PATCH operation.
type Operation struct {
	Op    string `json:"op"`
	Path  string `json:"path,omitempty"`
	Value any    `json:"value,omitempty"`
}

// NewExternalIDPatch builds the only write this client ever performs.
func NewExternalIDPatch(externalID string) PatchOp {
	return PatchOp{
		Schemas: []string{SchemaPatchOp},
		Operations: []Operation{
			{Op: "replace", Path: "externalId", Value: externalID},
		},
	}
}
