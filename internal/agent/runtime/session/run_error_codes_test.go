package sessionruntime

import (
	"testing"

	"github.com/felinics/memoh/internal/apperror"
)

// Every code this package writes to session_runs.error_code is shown to users
// by code, so each must resolve in the apperror catalog. The package keeps its
// own constants rather than importing apperror; this test is what ties the two.
func TestRunErrorCodesAreCatalogued(t *testing.T) {
	t.Parallel()
	for _, code := range []string{
		runErrorRunFailed,
		runErrorOwnerLeaseExpired,
		runErrorBackendLost,
		runErrorAdmissionOrphaned,
		runErrorFenceActivationFailed,
		runErrorReservationFailed,
		runErrorReservationDeclined,
		RunErrorInterrupted,
	} {
		if _, ok := apperror.Lookup(apperror.Code(code)); !ok {
			t.Errorf("session_runs error code %q is not in the apperror catalog", code)
		}
	}
}
