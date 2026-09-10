package packages

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

func (s *postgresStore) Get(ctx context.Context, botID, workspaceTargetID, registryID, packageID string) (Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.GetBotPackageInstallation(ctx, dbsqlc.GetBotPackageInstallationParams{
		BotID: botUUID, WorkspaceTargetID: strings.TrimSpace(workspaceTargetID),
		RegistryID: strings.TrimSpace(registryID), PackageID: strings.TrimSpace(packageID),
	})
	return installationResult(row, err)
}

func (s *postgresStore) GetByID(ctx context.Context, botID, installationID string) (Installation, error) {
	botUUID, id, err := installationKey(botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.GetBotPackageInstallationByID(ctx, dbsqlc.GetBotPackageInstallationByIDParams{BotID: botUUID, ID: id})
	return installationResult(row, err)
}

func (s *postgresStore) ListForBot(ctx context.Context, botID string) ([]Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListBotPackageInstallations(ctx, botUUID)
	return installationsResult(rows, err)
}

func (s *postgresStore) ListForTarget(ctx context.Context, botID, workspaceTargetID string) ([]Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListBotPackageInstallationsForTarget(ctx, dbsqlc.ListBotPackageInstallationsForTargetParams{
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
	row, err := s.q.UpsertBotPackageInstallation(ctx, dbsqlc.UpsertBotPackageInstallationParams{
		BotID: botUUID, WorkspaceTargetID: strings.TrimSpace(in.WorkspaceTargetID),
		RegistryID: in.RegistryID, PackageID: in.PackageID, Revision: in.Revision, Version: in.Version,
		Status: string(in.Status), Reason: string(in.Reason), Release: release,
	})
	return installationResult(row, err)
}

func (s *postgresStore) SetStatus(ctx context.Context, botID, installationID string, status Status, lastError string) (Installation, error) {
	botUUID, id, err := installationKey(botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpdateBotPackageInstallationStatus(ctx, dbsqlc.UpdateBotPackageInstallationStatusParams{
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
	row, err := s.q.UpdateBotPackageInstallationRelease(ctx, dbsqlc.UpdateBotPackageInstallationReleaseParams{
		Revision: revision, Version: version, Release: release, BotID: botUUID, ID: id,
	})
	return installationResult(row, err)
}

func (s *postgresStore) SetCheck(ctx context.Context, botID, installationID, availableRevision, availableVersion string, checkedAt time.Time) (Installation, error) {
	botUUID, id, err := installationKey(botID, installationID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.q.UpdateBotPackageInstallationCheck(ctx, dbsqlc.UpdateBotPackageInstallationCheckParams{
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
	row, err := s.q.DeleteBotPackageInstallation(ctx, dbsqlc.DeleteBotPackageInstallationParams{BotID: botUUID, ID: id})
	return installationResult(row, err)
}

func (s *postgresStore) ListDependencyRefs(ctx context.Context, installationID string) ([]DependencyRef, error) {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListPackageDependencyRefs(ctx, id)
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
	rows, err := s.q.ListPackageDependencyRefsForTarget(ctx, dbsqlc.ListPackageDependencyRefsForTargetParams{
		BotID: botUUID, WorkspaceTargetID: strings.TrimSpace(workspaceTargetID),
	})
	if err != nil {
		return nil, err
	}
	refs := make([]TargetDependencyRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, TargetDependencyRef{
			DependencyRef: DependencyRef{InstallationID: row.InstallationID.String(), DependencyID: row.DependencyID},
			RegistryID:    row.RegistryID, PackageID: row.PackageID,
		})
	}
	return refs, nil
}

func (s *postgresStore) AddDependencyRef(ctx context.Context, installationID, dependencyID string) error {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return err
	}
	_, err = s.q.UpsertPackageDependencyRef(ctx, dbsqlc.UpsertPackageDependencyRefParams{InstallationID: id, DependencyID: dependencyID})
	return err
}

func (s *postgresStore) RemoveDependencyRef(ctx context.Context, installationID, dependencyID string) error {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return err
	}
	_, err = s.q.DeletePackageDependencyRef(ctx, dbsqlc.DeletePackageDependencyRefParams{InstallationID: id, DependencyID: dependencyID})
	return err
}

func (s *postgresStore) ListConnectorRefs(ctx context.Context, installationID string) ([]ConnectorRef, error) {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListPackageConnectorRefs(ctx, id)
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
	rows, err := s.q.ListPackageConnectorRefsForBot(ctx, botUUID)
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
			RegistryID: row.RegistryID, PackageID: row.PackageID, WorkspaceTargetID: row.WorkspaceTargetID,
		})
	}
	return refs, nil
}

func (s *postgresStore) UpsertConnectorRef(ctx context.Context, ref ConnectorRef) error {
	id, err := db.ParseUUID(ref.InstallationID)
	if err != nil {
		return err
	}
	_, err = s.q.UpsertPackageConnectorRef(ctx, dbsqlc.UpsertPackageConnectorRefParams{
		InstallationID: id, ConnectorType: ref.ConnectorType, ConnectionID: ref.ConnectionID, Required: ref.Required,
	})
	return err
}

func (s *postgresStore) SetConnectorRefConnection(ctx context.Context, installationID, connectorType, connectionID string) error {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return err
	}
	_, err = s.q.SetPackageConnectorRefConnection(ctx, dbsqlc.SetPackageConnectorRefConnectionParams{
		ConnectionID: connectionID, InstallationID: id, ConnectorType: connectorType,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotInstalled
	}
	return err
}

func (s *postgresStore) ClearConnectorRefConnection(ctx context.Context, connectionID string) error {
	_, err := s.q.ClearPackageConnectorRefConnection(ctx, connectionID)
	return err
}

func (s *postgresStore) RemoveConnectorRef(ctx context.Context, installationID, connectorType string) error {
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return err
	}
	_, err = s.q.DeletePackageConnectorRef(ctx, dbsqlc.DeletePackageConnectorRefParams{InstallationID: id, ConnectorType: connectorType})
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

func installationResult(row dbsqlc.BotPackageInstallation, err error) (Installation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return Installation{}, ErrNotInstalled
	}
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row), nil
}

func installationsResult(rows []dbsqlc.BotPackageInstallation, err error) ([]Installation, error) {
	if err != nil {
		return nil, err
	}
	result := make([]Installation, 0, len(rows))
	for _, row := range rows {
		result = append(result, installationFromRow(row))
	}
	return result, nil
}

func installationFromRow(row dbsqlc.BotPackageInstallation) Installation {
	inst := Installation{
		ID: row.ID.String(), BotID: row.BotID.String(), WorkspaceTargetID: row.WorkspaceTargetID,
		RegistryID: row.RegistryID, PackageID: row.PackageID, Revision: row.Revision, Version: row.Version,
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
