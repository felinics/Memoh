package botworkspace

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// ObservedWrite is one observation written by a lease holder.
type ObservedWrite struct {
	BotID              string
	Owner              string
	ExpectedVersion    int64
	Observed           string
	ObservedGeneration int64
	MarkReady          bool
	LastError          string
	LastErrorPhase     string
	Attempts           int32
	NextAttemptAt      time.Time
	ReleaseLease       bool
}

// Repository is the persistence port. The postgres implementation is thin;
// the interface exists so the reconciler can be tested against an in-memory
// fake.
type Repository interface {
	Upsert(ctx context.Context, botID, desired, image string, preserveData bool) (Workspace, error)
	Get(ctx context.Context, botID string) (Workspace, error)
	Claim(ctx context.Context, owner string, lease time.Duration, limit int32) ([]Workspace, error)
	ClaimOne(ctx context.Context, botID, owner string, lease time.Duration) (Workspace, error)
	Renew(ctx context.Context, botID, owner string, lease time.Duration) error
	WriteObserved(ctx context.Context, w ObservedWrite) (Workspace, error)
	Release(ctx context.Context, botID, owner string) error
	ListByObserved(ctx context.Context, observed string, limit int32) ([]Workspace, error)
}

type postgresRepository struct {
	queries dbstore.Queries
}

// NewRepository returns the postgres-backed Repository.
func NewRepository(queries dbstore.Queries) Repository {
	return &postgresRepository{queries: queries}
}

func (r *postgresRepository) Upsert(ctx context.Context, botID, desired, image string, preserveData bool) (Workspace, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return Workspace{}, err
	}
	row, err := r.queries.UpsertBotWorkspaceIntent(ctx, dbsqlc.UpsertBotWorkspaceIntentParams{
		BotID:        id,
		DesiredState: desired,
		Image:        image,
		PreserveData: preserveData,
	})
	if err != nil {
		return Workspace{}, err
	}
	return fromRow(row), nil
}

func (r *postgresRepository) Get(ctx context.Context, botID string) (Workspace, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return Workspace{}, err
	}
	row, err := r.queries.GetBotWorkspace(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Workspace{}, ErrNotFound
		}
		return Workspace{}, err
	}
	return fromRow(row), nil
}

func (r *postgresRepository) Claim(ctx context.Context, owner string, lease time.Duration, limit int32) ([]Workspace, error) {
	rows, err := r.queries.ClaimBotWorkspaces(ctx, dbsqlc.ClaimBotWorkspacesParams{
		LeaseOwner:   owner,
		LeaseSeconds: lease.Seconds(),
		Lim:          limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRow(row))
	}
	return out, nil
}

func (r *postgresRepository) ClaimOne(ctx context.Context, botID, owner string, lease time.Duration) (Workspace, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return Workspace{}, err
	}
	row, err := r.queries.ClaimBotWorkspace(ctx, dbsqlc.ClaimBotWorkspaceParams{
		LeaseOwner:   owner,
		LeaseSeconds: lease.Seconds(),
		BotID:        id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Workspace{}, ErrNotFound
		}
		return Workspace{}, err
	}
	return fromRow(row), nil
}

func (r *postgresRepository) Renew(ctx context.Context, botID, owner string, lease time.Duration) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	n, err := r.queries.RenewBotWorkspaceLease(ctx, dbsqlc.RenewBotWorkspaceLeaseParams{
		LeaseSeconds: lease.Seconds(),
		BotID:        id,
		LeaseOwner:   owner,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrVersionConflict
	}
	return nil
}

func (r *postgresRepository) WriteObserved(ctx context.Context, w ObservedWrite) (Workspace, error) {
	id, err := db.ParseUUID(w.BotID)
	if err != nil {
		return Workspace{}, err
	}
	row, err := r.queries.UpdateBotWorkspaceObserved(ctx, dbsqlc.UpdateBotWorkspaceObservedParams{
		ObservedState:      w.Observed,
		ObservedGeneration: w.ObservedGeneration,
		MarkReady:          w.MarkReady,
		LastError:          w.LastError,
		LastErrorPhase:     w.LastErrorPhase,
		Attempts:           w.Attempts,
		NextAttemptAt:      pgtype.Timestamptz{Time: w.NextAttemptAt, Valid: true},
		ReleaseLease:       w.ReleaseLease,
		BotID:              id,
		LeaseOwner:         w.Owner,
		ExpectedVersion:    w.ExpectedVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Workspace{}, ErrVersionConflict
		}
		return Workspace{}, err
	}
	return fromRow(row), nil
}

func (r *postgresRepository) Release(ctx context.Context, botID, owner string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	_, err = r.queries.ReleaseBotWorkspaceLease(ctx, dbsqlc.ReleaseBotWorkspaceLeaseParams{
		BotID:      id,
		LeaseOwner: owner,
	})
	return err
}

func (r *postgresRepository) ListByObserved(ctx context.Context, observed string, limit int32) ([]Workspace, error) {
	rows, err := r.queries.ListBotWorkspacesByObservedState(ctx, dbsqlc.ListBotWorkspacesByObservedStateParams{
		ObservedState: observed,
		Lim:           limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Workspace, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRow(row))
	}
	return out, nil
}

func fromRow(row dbsqlc.BotWorkspace) Workspace {
	w := Workspace{
		BotID:              uuidString(row.BotID),
		TeamID:             uuidString(row.TeamID),
		Desired:            row.DesiredState,
		DesiredGeneration:  row.DesiredGeneration,
		Image:              row.Image,
		PreserveData:       row.PreserveData,
		Observed:           row.ObservedState,
		ObservedGeneration: row.ObservedGeneration,
		EverReady:          row.EverReady,
		LastError:          row.LastError,
		LastErrorPhase:     row.LastErrorPhase,
		Attempts:           row.Attempts,
		LeaseOwner:         row.LeaseOwner,
		Version:            row.Version,
	}
	if row.NextAttemptAt.Valid {
		w.NextAttemptAt = row.NextAttemptAt.Time
	}
	if row.LeaseUntil.Valid {
		w.LeaseUntil = row.LeaseUntil.Time
	}
	if row.UpdatedAt.Valid {
		w.UpdatedAt = row.UpdatedAt.Time
	}
	return w
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}
