package handlers

import (
	"os"
	"strings"
	"testing"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

// TestGeneratedContractsCarryEveryMutationKind pins the generated OpenAPI
// document to the Go mutation vocabulary, so adding a MutationKind without
// regenerating the contracts fails here instead of shipping a stale SDK enum.
func TestGeneratedContractsCarryEveryMutationKind(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../spec/swagger.json")
	if err != nil {
		t.Fatalf("read generated swagger document: %v", err)
	}
	spec := string(raw)
	for _, kind := range []contextfrag.MutationKind{
		contextfrag.MutationBeforeModelCallHook,
		contextfrag.MutationBackgroundSummary,
		contextfrag.MutationMidTaskPrune,
		contextfrag.MutationInjectedMessage,
		contextfrag.MutationContextViewFallback,
		contextfrag.MutationContextBudgetFailure,
		contextfrag.MutationContextBudgetDisabled,
		contextfrag.MutationCapabilityGate,
		contextfrag.MutationReadMedia,
		contextfrag.MutationRendererPrune,
	} {
		if !strings.Contains(spec, string(kind)) {
			t.Fatalf("generated swagger document is missing mutation kind %q; run mise run swagger-generate and generate-sdk", kind)
		}
	}
}
