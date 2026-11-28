package db

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUniqueViolationConstraint(t *testing.T) {
	t.Parallel()

	matching := fmt.Errorf("insert: %w", &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "idx_example_unique",
	})
	otherConstraint := &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "idx_other_unique",
	}
	otherCode := &pgconn.PgError{
		Code:           "23503",
		ConstraintName: "idx_example_unique",
	}

	if !IsUniqueViolationConstraint(matching, "idx_a", "idx_example_unique") {
		t.Fatal("expected matching 23505 constraint to return true")
	}
	if IsUniqueViolationConstraint(otherConstraint, "idx_example_unique") {
		t.Fatal("expected non-matching 23505 constraint to return false")
	}
	if IsUniqueViolationConstraint(otherCode, "idx_example_unique") {
		t.Fatal("expected non-23505 pg error to return false")
	}
	if IsUniqueViolationConstraint(matching) {
		t.Fatal("expected empty constraint list to return false")
	}
	if IsUniqueViolationConstraint(errors.New("plain error"), "idx_example_unique") {
		t.Fatal("expected plain error to return false")
	}
}
