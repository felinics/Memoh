package postgresstore

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felinics/memoh/internal/db"
)

func TestMapQueryErrRecognizesLastActiveAdminConstraint(t *testing.T) {
	err := mapQueryErr(&pgconn.PgError{
		Code:           "23514",
		ConstraintName: "team_members_last_active_admin",
	})
	if !errors.Is(err, db.ErrLastActiveAdmin) {
		t.Fatalf("mapQueryErr() = %v, want ErrLastActiveAdmin", err)
	}
}
