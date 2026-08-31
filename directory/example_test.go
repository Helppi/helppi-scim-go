package directory_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/Helppi/helppi-scim-go/directory"
	"github.com/Helppi/helppi-scim-go/scim"
	"github.com/Helppi/helppi-scim-go/scimtest"
	"github.com/Helppi/helppi-scim-go/store/memory"
)

// Everything a partner has to write is an implementation of store.Store. The
// rest is three lines.
func Example() {
	active, suspended := true, false

	// A stand-in for the Helppi directory, so this example runs offline.
	dir := scimtest.Start([]scim.User{
		{
			ID: "hlp_8fK2Lm91", UserName: "8fk2lm91@separador.app",
			DisplayName: "Marcio C.", Active: &active,
		},
		{
			ID: "hlp_9xB2Rt77", UserName: "9xb2rt77@separador.app",
			DisplayName: "Bruno S.", Active: &suspended,
		},
	})
	defer dir.Close()

	client, err := scim.New(scim.Options{BaseURL: dir.URL, Token: scimtest.Token})
	if err != nil {
		panic(err)
	}

	syncer := directory.New(client, memory.New(nil), directory.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	stats, err := syncer.Incremental(context.Background())
	if err != nil {
		panic(err)
	}

	// The suspended identity produces no account: never create something the
	// directory already says is disabled.
	fmt.Printf("scanned=%d created=%d wrote_back=%d\n",
		stats.Scanned, stats.Created, stats.WroteBack)
	// Output: scanned=2 created=1 wrote_back=1
}
