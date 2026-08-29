package skillpackages

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/skills"
)

var ErrNotInstalled = errors.New("skill Package is not installed")

type Installation struct {
	ID                string    `json:"id" validate:"required"`
	BotID             string    `json:"bot_id" validate:"required"`
	WorkspaceTargetID string    `json:"workspace_target_id" validate:"required"`
	RegistryID        string    `json:"registry_id" validate:"required"`
	PackageID         string    `json:"package_id" validate:"required"`
	Revision          string    `json:"revision" validate:"required"`
	InstalledAt       time.Time `json:"installed_at" validate:"required"`
	UpdatedAt         time.Time `json:"updated_at" validate:"required"`
}

type Requirement struct {
	RegistryID string
	PackageID  string
	Revision   string
}

type Store interface {
	DeleteBotSkillPackageInstallation(context.Context, dbsqlc.DeleteBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error)
	GetBotSkillPackageInstallation(context.Context, dbsqlc.GetBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error)
	GetBotSkillPackageInstallationByID(context.Context, dbsqlc.GetBotSkillPackageInstallationByIDParams) (dbsqlc.BotSkillPackageInstallation, error)
	ListBotSkillPackageInstallations(context.Context, pgtype.UUID) ([]dbsqlc.BotSkillPackageInstallation, error)
	UpsertBotSkillPackageInstallation(context.Context, dbsqlc.UpsertBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error)
}

type Service struct {
	queries Store
}

func NewService(queries Store) *Service {
	return &Service{queries: queries}
}

func (s *Service) List(ctx context.Context, botID string) ([]Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListBotSkillPackageInstallations(ctx, botUUID)
	if err != nil {
		return nil, err
	}
	result := make([]Installation, 0, len(rows))
	for _, row := range rows {
		result = append(result, installationFromRow(row))
	}
	return result, nil
}

func (s *Service) ListForTarget(ctx context.Context, botID, workspaceTargetID string) ([]Installation, error) {
	items, err := s.List(ctx, botID)
	if err != nil {
		return nil, err
	}
	targetID := strings.TrimSpace(workspaceTargetID)
	result := make([]Installation, 0, len(items))
	for _, item := range items {
		if item.WorkspaceTargetID == targetID {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, botID, workspaceTargetID, registryID, packageID string) (Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.queries.GetBotSkillPackageInstallation(ctx, dbsqlc.GetBotSkillPackageInstallationParams{
		BotID: botUUID, WorkspaceTargetID: strings.TrimSpace(workspaceTargetID),
		RegistryID: strings.TrimSpace(registryID), PackageID: strings.TrimSpace(packageID),
	})
	return mapInstallation(row, err)
}

func (s *Service) GetByID(ctx context.Context, botID, installationID string) (Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return Installation{}, err
	}
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.queries.GetBotSkillPackageInstallationByID(ctx, dbsqlc.GetBotSkillPackageInstallationByIDParams{
		BotID: botUUID,
		ID:    id,
	})
	return mapInstallation(row, err)
}

func (s *Service) Record(ctx context.Context, botID, workspaceTargetID string, requirement Requirement) (Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return Installation{}, err
	}
	if err := validateRequirement(requirement); err != nil {
		return Installation{}, err
	}
	row, err := s.queries.UpsertBotSkillPackageInstallation(ctx, dbsqlc.UpsertBotSkillPackageInstallationParams{
		BotID: botUUID, WorkspaceTargetID: strings.TrimSpace(workspaceTargetID),
		RegistryID: requirement.RegistryID, PackageID: requirement.PackageID, Revision: requirement.Revision,
	})
	return mapInstallation(row, err)
}

func (s *Service) Delete(ctx context.Context, botID, installationID string) (Installation, error) {
	botUUID, err := db.ParseUUID(botID)
	if err != nil {
		return Installation{}, err
	}
	id, err := db.ParseUUID(installationID)
	if err != nil {
		return Installation{}, err
	}
	row, err := s.queries.DeleteBotSkillPackageInstallation(ctx, dbsqlc.DeleteBotSkillPackageInstallationParams{
		BotID: botUUID,
		ID:    id,
	})
	return mapInstallation(row, err)
}

func validateRequirement(requirement Requirement) error {
	if !skills.IsValidRegistryID(requirement.RegistryID) || !skills.IsValidRegistryComponent(requirement.PackageID) || !isDigest(requirement.Revision) {
		return errors.New("skill Package requirement is invalid")
	}
	return nil
}

func isDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func mapInstallation(row dbsqlc.BotSkillPackageInstallation, err error) (Installation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return Installation{}, ErrNotInstalled
	}
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row), nil
}

func installationFromRow(row dbsqlc.BotSkillPackageInstallation) Installation {
	return Installation{
		ID: row.ID.String(), BotID: row.BotID.String(), WorkspaceTargetID: row.WorkspaceTargetID,
		RegistryID: row.RegistryID, PackageID: row.PackageID, Revision: row.Revision,
		InstalledAt: row.InstalledAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}
