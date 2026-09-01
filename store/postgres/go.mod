module github.com/Helppi/helppi-scim-go/store/postgres

go 1.24.0

// Until the parent module is tagged, resolve it from the working tree. This
// directive is ignored by anyone who imports this module, so it does not leak
// into a consumer's build — but it does mean this submodule is not consumable
// until v0.1.0 exists upstream.
replace github.com/Helppi/helppi-scim-go => ../..

require (
	github.com/Helppi/helppi-scim-go v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.7.2
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.31.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)
