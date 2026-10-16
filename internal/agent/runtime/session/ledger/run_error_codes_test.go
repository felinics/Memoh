package ledger

import (
	"testing"

	"github.com/felinics/memoh/internal/apperror"
)

// The ledger writes this code to session_runs.error_code itself; it must
// resolve in the apperror catalog like every other run error code.
func TestRunErrorCodesAreCatalogued(t *testing.T) {
	t.Parallel()
	if _, ok := apperror.Lookup(apperror.Code(runErrorHistoryReset)); !ok {
		t.Errorf("session_runs error code %q is not in the apperror catalog", runErrorHistoryReset)
	}
}
