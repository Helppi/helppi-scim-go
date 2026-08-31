package memory_test

import (
	"testing"

	"github.com/Helppi/helppi-scim-go/store"
	"github.com/Helppi/helppi-scim-go/store/memory"
	"github.com/Helppi/helppi-scim-go/store/storetest"
)

// The reference implementation has to pass the same contract suite a partner
// runs against theirs.
func TestMemoryStoreSatisfiesTheContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		return memory.New(nil)
	})
}

func TestMemoryStoreIsMarkedEphemeral(t *testing.T) {
	// The worker refuses to point an ephemeral store at a real directory: an
	// empty store makes every record look new, so it would mint fresh picker
	// ids and overwrite the real ones.
	if !store.IsEphemeral(memory.New(nil)) {
		t.Fatal("the in-memory store must declare itself ephemeral")
	}
}
