package application

import (
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

// testRuntimes registers test drivers the way the server does, so a stub
// that declares what it does not implement fails the test that uses it.
func testRuntimes(t *testing.T, drivers ...external.Driver) *external.Registry {
	t.Helper()
	registry, err := external.NewRegistry(drivers...)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

// stubCaps is the declaration of a test driver that implements no optional
// capability.
var stubCaps = external.Caps{}
