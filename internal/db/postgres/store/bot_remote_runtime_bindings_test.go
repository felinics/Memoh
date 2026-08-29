package postgresstore

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felinics/memoh/internal/db"
)

func TestMapDeleteRemoteMountErrRecognizesOnlyWorkdirRestrictViolation(t *testing.T) {
	t.Parallel()

	// PostgreSQL 18 reports RESTRICT as 23001; older releases used 23503.
	for _, code := range []string{"23001", "23503"} {
		inUse := fmt.Errorf("delete remote mount: %w", &pgconn.PgError{
			Code:           code,
			ConstraintName: "bot_workdirs_remote_binding_fkey",
		})
		if err := mapDeleteRemoteMountErr(inUse); !errors.Is(err, db.ErrWorkspaceTargetInUse) {
			t.Fatalf("mapDeleteRemoteMountErr(%s) = %v, want ErrWorkspaceTargetInUse", code, err)
		}
	}

	for name, pgErr := range map[string]*pgconn.PgError{
		"different foreign key": {
			Code:           "23001",
			ConstraintName: "some_other_foreign_key",
		},
		"different SQLSTATE": {
			Code:           "22000",
			ConstraintName: "bot_workdirs_remote_binding_fkey",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mapDeleteRemoteMountErr(pgErr); !errors.Is(got, pgErr) {
				t.Fatalf("mapDeleteRemoteMountErr() = %v, want original database error", got)
			}
		})
	}
}
