package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/memoh/domains/agent/chat/message"
)

func TestMapMessageErrorDoesNotLeakNoRows(t *testing.T) {
	t.Parallel()

	err := mapMessageError(pgx.ErrNoRows)
	if !errors.Is(err, message.ErrNotFound) {
		t.Fatalf("mapMessageError() error = %v, want message.ErrNotFound", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("mapMessageError() error = %v, leaked pgx.ErrNoRows", err)
	}
}
