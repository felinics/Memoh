package checkpoint

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// A summary copied to a fork names the fork's copies of the rows it cites,
// however the summarizer cased their ids, and leaves every other id alone.
func TestRenameRowsNamesTheCopies(t *testing.T) {
	t.Parallel()
	source, copied, other := uuid.New(), uuid.New(), uuid.NewString()
	copies := map[string]pgtype.UUID{source.String(): {Bytes: copied, Valid: true}}
	summary := "Read the build log (ref=" + source.String() + ":0), then its tail at " + strings.ToUpper(source.String()) + ":1; deploy " + other + " is unrelated."
	want := "Read the build log (ref=" + copied.String() + ":0), then its tail at " + copied.String() + ":1; deploy " + other + " is unrelated."
	if got := renameRows(summary, copies); got != want {
		t.Fatalf("renameRows =\n%s\nwant\n%s", got, want)
	}
}
