package apps

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type postgresStore struct {
	q dbstore.Queries
}

// NewPostgresStore returns a Store backed by the sqlc queries in q. Team
// scoping comes from the pooled connection, not from the context.
func NewPostgresStore(q dbstore.Queries) Store {
	return &postgresStore{q: q}
}

func (s *postgresStore) Get(ctx context.Context, botID, workspaceTargetID, registryID, appID string) (Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.GetBotAppInstallation(ctx, dbsqlc.GetBotAppInstallationParams{
		BotID: botUUID, WorkspaceTargetID: strings.TrimSpace(workspaceTargetID),
		RegistryID: strings.TrimSpace(registryID), AppID: strings.TrimSpace(appID),
	})
	return installationResult(row, err)
}

func (s *postgresStore) GetByID(ctx context.Context, botID, installationID string) (Installation, error) {
	botUUID, id, err := installationKey(botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.GetBotAppInstallationByID(ctx, dbsqlc.GetBotAppInstallationByIDParams{BotID: botUUID, ID: id})
	return installationResult(row, err)
}

func (s *postgresStore) ListForBot(ctx context.Context, botID string) ([]Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListBotAppInstallations(ctx, botUUID)
	return installationsResult(rows, err)
}

func (s *postgresStore) ListForTarget(ctx context.Context, botID, workspaceTargetID string) ([]Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListBotAppInstallationsForTarget(ctx, dbsqlc.ListBotAppInstallationsForTargetParams{
		BotID: botUUID, WorkspaceTargetID: strings.TrimSpace(workspaceTargetID),
	})
	return installationsResult(rows, err)
}

func (s *postgresStore) Upsert(ctx context.Context, in UpsertInstallation) (Installation, error) {
	botUUID, err := db.ParseUUID(in.BotID)
	if err != nil {
		return Installation{}, err
	}
	release := in.Release
	if release == nil {
		release = []byte{}
	}
	row, err := s.q.UpsertBotAppInstallation(ctx, dbsqlc.UpsertBotAppInstallationParams{
		BotID: botUUID, WorkspaceTargetID: strings.TrimSpace(in.WorkspaceTargetID),
		RegistryID: in.RegistryID, AppID: in.AppID, Revision: in.Revision, Version: in.Version,
		Status: string(in.Status), Reason: string(in.Reason), Release: release,
	})
	return installationResult(row, err)
}

func (s *postgresStore) SetStatus(ctx context.Context, botID, installationID string, status Status, lastError string) (Installation, error) {
	botUUID, id, err := installationKey(botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpdateBotAppInstallationStatus(ctx, dbsqlc.UpdateBotAppInstallationStatusParams{
		Status: string(status), LastError: lastError, BotID: botUUID, ID: id,
	})
	return installationResult(row, err)
}

func (s *postgresStore) SetRelease(ctx context.Context, botID, installationID, revision, version string, release []byte) (Installation, error) {
	botUUID, id, err := installationKey(botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	if release == nil {
		release = []byte{}
	}
	row, err := s.q.UpdateBotAppInstallationRelease(ctx, dbsqlc.UpdateBotAppInstallationReleaseParams{
		Revision: revision, Version: version, Release: release, BotID: botUUID, ID: id,
	})
	return installationResult(row, err)
}

func (s *postgresStore) SetCheck(ctx context.Context, botID, installationID, availableRevision, availableVersion string, checkedAt time.Time) (Installation, error) {
	botUUID, id, err := installationKey(botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpdateBotAppInstallationCheck(ctx, dbsqlc.UpdateBotAppInstallationCheckParams{
		AvailableRevision: availableRevision, AvailableVersion: availableVersion,
		LastCheckedAt: pgtype.Timestamptz{Time: checkedAt, Valid: !checkedAt.IsZero()},
		BotID:         botUUID, ID: id,
	})
	return installationResult(row, err)
}

func (s *postgresStore) Delete(ctx context.Context, botID, installationID string) (Installation, error) {
	botUUID, id, err := installationKey(botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.DeleteBotAppInstallation(ctx, dbsqlc.DeleteBotAppInstallationParams{BotID: botUUID, ID: id})
	return installationResult(row, err)
}

func (s *postgresStore) ListDependencyRefs(ctx context.Context, installationID string) ([]DependencyRef, error) {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAppDependencyRefs(ctx, id)
	if err != nil {
		return nil, err
	}
	refs := make([]DependencyRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, DependencyRef{InstallationID: row.InstallationID.String(), DependencyID: row.DependencyID})
	}
	return refs, nil
}

func (s *postgresStore) ListTargetDependencyRefs(ctx context.Context, botID, workspaceTargetID string) ([]TargetDependencyRef, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAppDependencyRefsForTarget(ctx, dbsqlc.ListAppDependencyRefsForTargetParams{
		BotID: botUUID, WorkspaceTargetID: strings.TrimSpace(workspaceTargetID),
	})
	if err != nil {
		return nil, err
	}
	refs := make([]TargetDependencyRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, TargetDependencyRef{
			DependencyRef: DependencyRef{InstallationID: row.InstallationID.String(), DependencyID: row.DependencyID},
			RegistryID:    row.RegistryID, AppID: row.AppID,
		})
	}
	return refs, nil
}

func (s *postgresStore) AddDependencyRef(ctx context.Context, installationID, dependencyID string) error {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return err
	}
	_, err = s.q.UpsertAppDependencyRef(ctx, dbsqlc.UpsertAppDependencyRefParams{InstallationID: id, DependencyID: dependencyID})
	return err
}

func (s *postgresStore) RemoveDependencyRef(ctx context.Context, installationID, dependencyID string) error {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return err
	}
	_, err = s.q.DeleteAppDependencyRef(ctx, dbsqlc.DeleteAppDependencyRefParams{InstallationID: id, DependencyID: dependencyID})
	return err
}

func (s *postgresStore) ListConnectorRefs(ctx context.Context, installationID string) ([]ConnectorRef, error) {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAppConnectorRefs(ctx, id)
	if err != nil {
		return nil, err
	}
	refs := make([]ConnectorRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, ConnectorRef{
			InstallationID: row.InstallationID.String(), ConnectorType: row.ConnectorType,
			ConnectionID: row.ConnectionID, Required: row.Required,
		})
	}
	return refs, nil
}

func (s *postgresStore) ListBotConnectorRefs(ctx context.Context, botID string) ([]BotConnectorRef, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAppConnectorRefsForBot(ctx, botUUID)
	if err != nil {
		return nil, err
	}
	refs := make([]BotConnectorRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, BotConnectorRef{
			ConnectorRef: ConnectorRef{
				InstallationID: row.InstallationID.String(), ConnectorType: row.ConnectorType,
				ConnectionID: row.ConnectionID, Required: row.Required,
			},
			RegistryID: row.RegistryID, AppID: row.AppID, WorkspaceTargetID: row.WorkspaceTargetID,
		})
	}
	return refs, nil
}

func (s *postgresStore) UpsertConnectorRef(ctx context.Context, ref ConnectorRef) error {
	id, err := db.ParseUUID(ref.InstallationID)
	if err != nil {
		return err
	}
	_, err = s.q.UpsertAppConnectorRef(ctx, dbsqlc.UpsertAppConnectorRefParams{
		InstallationID: id, ConnectorType: ref.ConnectorType, ConnectionID: ref.ConnectionID, Required: ref.Required,
	})
	return err
}

func (s *postgresStore) SetConnectorRefConnection(ctx context.Context, installationID, connectorType, connectionID string) error {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return err
	}
	_, err = s.q.SetAppConnectorRefConnection(ctx, dbsqlc.SetAppConnectorRefConnectionParams{
		ConnectionID: connectionID, InstallationID: id, ConnectorType: connectorType,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotInstalled
	}
	return err
}

func (s *postgresStore) ClearConnectorRefConnection(ctx context.Context, connectionID string) error {
	_, err := s.q.ClearAppConnectorRefConnection(ctx, connectionID)
	return err
}

func (s *postgresStore) RemoveConnectorRef(ctx context.Context, installationID, connectorType string) error {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return err
	}
	_, err = s.q.DeleteAppConnectorRef(ctx, dbsqlc.DeleteAppConnectorRefParams{InstallationID: id, ConnectorType: connectorType})
	return err
}

func installationKey(botID, installationID string) (pgtype.UUID, pgtype.UUID, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return botUUID, id, nil
}

func installationResult(row dbsqlc.BotAppInstallation, err error) (Installation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return Installation{}, ErrNotInstalled
	}
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row), nil
}

func installationsResult(rows []dbsqlc.BotAppInstallation, err error) ([]Installation, error) {
	if err != nil {
		return nil, err
	}
	result := make([]Installation, 0, len(rows))
	for _, row := range rows {
		result = append(result, installationFromRow(row))
	}
	return result, nil
}

func installationFromRow(row dbsqlc.BotAppInstallation) Installation {
	inst := Installation{
		ID: row.ID.String(), BotID: row.BotID.String(), WorkspaceTargetID: row.WorkspaceTargetID,
		RegistryID: row.RegistryID, AppID: row.AppID, Revision: row.Revision, Version: row.Version,
		Status: Status(row.Status), Reason: Reason(row.Reason),
		AvailableRevision: row.AvailableRevision, AvailableVersion: row.AvailableVersion,
		LastError: row.LastError, Release: row.Release,
		InstalledAt: row.InstalledAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.LastCheckedAt.Valid {
		checked := row.LastCheckedAt.Time
		inst.LastCheckedAt = &checked
	}
	return inst
}
