package sessionruntime

import (
	"os"
	"regexp"
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

// The resume worker and its partial index select interrupted runs by the
// literal error code in SQL, so RunErrorInterrupted must be spelled exactly as
// the queries and the index spell it. A rename on either side would silently
// stop interrupted sessions from resuming.
func TestRunErrorInterruptedMatchesSQL(t *testing.T) {
	t.Parallel()
	literal := regexp.MustCompile(`state = 'lost' AND (?:\w+\.)?error_code = '([^']*)'`)
	for _, path := range []string{
		"../../../../db/postgres/queries/session_runs.sql",
		"../../../../db/postgres/migrations/0159_session_runs_resume_pending_index.up.sql",
		"../../../../db/postgres/migrations/0001_init.up.sql",
	} {
		source, err := os.ReadFile(path) //nolint:gosec // repo-local SQL file
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		matches := literal.FindAllSubmatch(source, -1)
		if len(matches) == 0 {
			t.Errorf("%s selects no lost run by error code", path)
		}
		for _, match := range matches {
			if got := string(match[1]); got != RunErrorInterrupted {
				t.Errorf("%s selects lost runs by error_code %q, want %q", path, got, RunErrorInterrupted)
			}
		}
	}
}
