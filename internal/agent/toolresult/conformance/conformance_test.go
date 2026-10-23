package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/felinics/memoh/internal/runtimekind"
)

// Every external runtime has recorded samples whose translated results are
// native-format. A runtime added to the table without samples fails here, so a
// new adapter cannot skip the translation unnoticed.
func TestEveryExternalRuntimeHasNativeFormatResults(t *testing.T) {
	for _, kind := range runtimekind.ExternalKinds() {
		t.Run(string(kind), func(t *testing.T) {
			path := filepath.Join(GoldenDir(), string(kind)+".json")
			raw, err := os.ReadFile(path) //nolint:gosec // fixed test fixture path
			if err != nil {
				t.Fatalf("runtime %q has no recorded samples: %v", kind, err)
			}
			var entries []Entry
			if err := json.Unmarshal(raw, &entries); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			if len(entries) == 0 {
				t.Fatalf("runtime %q samples contain no call under a native tool name", kind)
			}
			Check(t, entries, nil, Options{Extras: true, AnyOutput: true})
		})
	}
}
