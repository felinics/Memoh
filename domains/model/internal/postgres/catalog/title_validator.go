// Package catalog implements Model catalog PostgreSQL persistence adapters.
package catalog

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type titleModelQueries interface {
	GetModelByID(context.Context, pgtype.UUID) (dbsqlc.ModelModel, error)
}

// TitleModelValidator validates Accounts' title-model reference without
// leaking Model-owned generated rows into Accounts.
type TitleModelValidator struct {
	queries titleModelQueries
}

func NewTitleModelValidator(pool *pgxpool.Pool) *TitleModelValidator {
	return &TitleModelValidator{queries: dbsqlc.New(pool)}
}

// NewTitleModelValidatorWithQueries creates a validator with an injected query surface (tests).
func NewTitleModelValidatorWithQueries(queries titleModelQueries) *TitleModelValidator {
	return &TitleModelValidator{queries: queries}
}

func (v *TitleModelValidator) IsValidTitleModel(ctx context.Context, modelID string) (bool, error) {
	id, err := db.ParseUUID(modelID)
	if err != nil {
		return false, nil
	}
	model, err := v.queries.GetModelByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return model.Type == "chat", nil
}
