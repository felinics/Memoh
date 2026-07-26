package email

import (
	"errors"

	"github.com/jackc/pgx/v5"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

func classifyError(err error) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return emailport.ErrNotFound
}
