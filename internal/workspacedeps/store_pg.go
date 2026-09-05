package workspacedeps

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// Team context for the PostgreSQL store.
//
// Row level security on bot_dependency_installations resolves the team with
// public.memoh_current_team_id(), which reads the memoh.team_id GUC. Memoh
// binds that GUC per connection, not per request: db.OpenPostgres (and
// db.OpenPostgresDSN for tests) installs db.SetDefaultTeamOnConnect as the
// pgxpool AfterConnect hook, so every pooled connection carries the
// singleton team id at the session level. Nothing is read from the Go
// context.Context, and no HTTP middleware switches teams per request.
//
// A background worker (stale reaper, update checker) therefore needs no
// team-aware context: any context — including context.Background() with a
// deadline — behaves exactly like a request handler, provided the
// dbstore.Queries it holds is the FX-injected one backed by the shared pool
// (postgresstore.Queries over db.Open). Never build a private pool without
// the AfterConnect hook: memoh_current_team_id() then raises and every query
// fails closed.

type postgresStore struct {
	q dbstore.Queries
}

// NewPostgresStore returns a Store backed by the sqlc queries in q. The
// caller's context only carries cancellation; team scoping comes from the
// connection (see the package note above).
func NewPostgresStore(q dbstore.Queries) Store {
	return &postgresStore{q: q}
}

func (s *postgresStore) Get(ctx context.Context, key InstallationKey) (Installation, error) {
	botID, err := parseBotID(key.BotID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.GetBotDependencyInstallation(ctx, dbsqlc.GetBotDependencyInstallationParams{
		BotID:             botID,
		WorkspaceTargetID: key.WorkspaceTargetID,
		DependencyID:      key.DependencyID,
	})
	return installationResult(row, err)
}

func (s *postgresStore) ListForTarget(ctx context.Context, botID, workspaceTargetID string) ([]Installation, error) {
	botUUID, err := parseBotID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListBotDependencyInstallationsForTarget(ctx, dbsqlc.ListBotDependencyInstallationsForTargetParams{
		BotID:             botUUID,
		WorkspaceTargetID: workspaceTargetID,
	})
	return installationsResult(rows, err)
}

func (s *postgresStore) ListForBot(ctx context.Context, botID string) ([]Installation, error) {
	botUUID, err := parseBotID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListBotDependencyInstallations(ctx, botUUID)
	return installationsResult(rows, err)
}

func (s *postgresStore) ListByStatus(ctx context.Context, status Status) ([]Installation, error) {
	rows, err := s.q.ListBotDependencyInstallationsByStatus(ctx, string(status))
	return installationsResult(rows, err)
}

func (s *postgresStore) ListStaleOperations(ctx context.Context, olderThan time.Duration) ([]Installation, error) {
	rows, err := s.q.ListStaleBotDependencyOperations(ctx, staleSeconds(olderThan))
	return installationsResult(rows, err)
}

func (s *postgresStore) Upsert(ctx context.Context, in UpsertInstallation) (Installation, error) {
	botID, err := parseBotID(in.BotID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpsertBotDependencyInstallationIntent(ctx, dbsqlc.UpsertBotDependencyInstallationIntentParams{
		BotID:             botID,
		WorkspaceTargetID: in.WorkspaceTargetID,
		DependencyID:      in.DependencyID,
		Source:            in.Source,
		Status:            string(in.Status),
		InstalledVersion:  in.InstalledVersion,
		ManifestDigest:    in.ManifestDigest,
	})
	return installationResult(row, err)
}

func (s *postgresStore) SetStatus(ctx context.Context, key InstallationKey, status Status, lastError string) (Installation, error) {
	botID, err := parseBotID(key.BotID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpdateBotDependencyInstallationStatus(ctx, dbsqlc.UpdateBotDependencyInstallationStatusParams{
		Status:            string(status),
		LastError:         lastError,
		BotID:             botID,
		WorkspaceTargetID: key.WorkspaceTargetID,
		DependencyID:      key.DependencyID,
	})
	return installationResult(row, err)
}

func (s *postgresStore) UpdateObserved(ctx context.Context, key InstallationKey, upd ObservedUpdate) (Installation, error) {
	botID, err := parseBotID(key.BotID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpdateBotDependencyInstallationObserved(ctx, observedParams(botID, key, upd))
	return installationResult(row, err)
}

func (s *postgresStore) Delete(ctx context.Context, key InstallationKey) error {
	botID, err := parseBotID(key.BotID)
	if err != nil {
		return err
	}
	affected, err := s.q.DeleteBotDependencyInstallation(ctx, dbsqlc.DeleteBotDependencyInstallationParams{
		BotID:             botID,
		WorkspaceTargetID: key.WorkspaceTargetID,
		DependencyID:      key.DependencyID,
	})
	if err != nil {
		return fmt.Errorf("workspace dependency store: %w", err)
	}
	if affected == 0 {
		return ErrInstallationNotFound
	}
	return nil
}

// observedParams maps an ObservedUpdate onto the COALESCE-guarded update: a
// nil field becomes an invalid (NULL) parameter, which leaves the column as
// it is; a non-nil pointer — including a pointer to "" — writes the value.
func observedParams(botID pgtype.UUID, key InstallationKey, upd ObservedUpdate) dbsqlc.UpdateBotDependencyInstallationObservedParams {
	return dbsqlc.UpdateBotDependencyInstallationObservedParams{
		Source:            nullableText(upd.Source),
		InstalledVersion:  nullableText(upd.InstalledVersion),
		LatestVersion:     nullableText(upd.LatestVersion),
		LastCheckedAt:     nullableTimestamptz(upd.LastCheckedAt),
		LastError:         nullableText(upd.LastError),
		ManifestDigest:    nullableText(upd.ManifestDigest),
		BotID:             botID,
		WorkspaceTargetID: key.WorkspaceTargetID,
		DependencyID:      key.DependencyID,
	}
}

// staleSeconds converts the reaper threshold to the query's seconds argument;
// a non-positive duration selects every in-progress record.
func staleSeconds(olderThan time.Duration) float64 {
	if olderThan <= 0 {
		return 0
	}
	return olderThan.Seconds()
}

func parseBotID(botID string) (pgtype.UUID, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("workspace dependency store: bot id: %w", err)
	}
	return id, nil
}

func installationResult(row dbsqlc.BotDependencyInstallation, err error) (Installation, error) {
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Installation{}, ErrInstallationNotFound
		}
		return Installation{}, fmt.Errorf("workspace dependency store: %w", err)
	}
	return installationFromRow(row), nil
}

func installationsResult(rows []dbsqlc.BotDependencyInstallation, err error) ([]Installation, error) {
	if err != nil {
		return nil, fmt.Errorf("workspace dependency store: %w", err)
	}
	out := make([]Installation, 0, len(rows))
	for _, row := range rows {
		out = append(out, installationFromRow(row))
	}
	return out, nil
}

func installationFromRow(row dbsqlc.BotDependencyInstallation) Installation {
	inst := Installation{
		ID:                uuidString(row.ID),
		BotID:             uuidString(row.BotID),
		WorkspaceTargetID: row.WorkspaceTargetID,
		DependencyID:      row.DependencyID,
		Source:            row.Source,
		Status:            Status(row.Status),
		InstalledVersion:  row.InstalledVersion,
		LatestVersion:     row.LatestVersion,
		LastError:         row.LastError,
		ManifestDigest:    row.ManifestDigest,
		CreatedAt:         db.TimeFromPg(row.CreatedAt),
		UpdatedAt:         db.TimeFromPg(row.UpdatedAt),
	}
	if row.LastCheckedAt.Valid {
		checked := row.LastCheckedAt.Time
		inst.LastCheckedAt = &checked
	}
	return inst
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

func nullableText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func nullableTimestamptz(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}
