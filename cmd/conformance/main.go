// Command conformance checks a live directory against the integration
// contract and prints one line per acceptance criterion.
//
//	DIRECTORY_TOKEN=… go run ./cmd/conformance \
//	    -base-url https://…/scim/v2 -alias-domain separador.app
//
// It exits non-zero if any case fails, so it works as a pipeline gate. Every
// check is read-only unless -write-id names a record reserved for write
// probing, and even then the write cases are built to leave the directory as
// they found it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Helppi/helppi-scim-go/conformance"
	"github.com/Helppi/helppi-scim-go/scim"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "conformance:", err)
		os.Exit(2) // 2: could not run. 1 is reserved for "ran, something failed".
	}
}

func run() error {
	var (
		baseURL     = flag.String("base-url", os.Getenv("DIRECTORY_BASE_URL"), "directory base URL, e.g. https://.../scim/v2")
		aliasDomain = flag.String("alias-domain", "", "check that published identities are aliases on this domain, e.g. separador.app")
		writeID     = flag.String("write-id", "", "id of a record reserved for write probing; without it the phase 2 cases are skipped")
		probeValue  = flag.String("probe-external-id", "", "value to write when the reserved record has no externalId yet")
		pageSize    = flag.Int("page-size", 2, "page size for the pagination probe")
		asJSON      = flag.Bool("json", false, "emit the report as JSON, for pasting into a ticket")
		rps         = flag.Float64("rps", 5, "max requests per second")
		timeout     = flag.Duration("timeout", 2*time.Minute, "overall deadline")
	)
	flag.Parse()

	token := os.Getenv("DIRECTORY_TOKEN")
	if *baseURL == "" || token == "" {
		return fmt.Errorf("set -base-url (or DIRECTORY_BASE_URL) and DIRECTORY_TOKEN")
	}

	client, err := scim.New(scim.Options{
		BaseURL:           *baseURL,
		Token:             token,
		RequestsPerSecond: *rps,
		UserAgent:         "helppi-scim-go/1.0 (+conformance)",
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	report := conformance.Check(ctx, client, conformance.Options{
		AliasDomain:     *aliasDomain,
		WriteID:         *writeID,
		ProbeExternalID: *probeValue,
		PageSize:        *pageSize,
	}, *baseURL)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Print(report)
	}

	if !report.OK() {
		os.Exit(1)
	}
	return nil
}
