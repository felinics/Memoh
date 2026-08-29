package skillpackages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type packageStoreStub struct {
	Store
	row          dbsqlc.BotSkillPackageInstallation
	rows         []dbsqlc.BotSkillPackageInstallation
	upsertErr    error
	deleteErr    error
	upsertParams dbsqlc.UpsertBotSkillPackageInstallationParams
	deleted      bool
}

func (s *packageStoreStub) ListBotSkillPackageInstallations(context.Context, pgtype.UUID) ([]dbsqlc.BotSkillPackageInstallation, error) {
	return s.rows, nil
}

func (s *packageStoreStub) UpsertBotSkillPackageInstallation(_ context.Context, arg dbsqlc.UpsertBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error) {
	s.upsertParams = arg
	return s.row, s.upsertErr
}

func (s *packageStoreStub) DeleteBotSkillPackageInstallation(context.Context, dbsqlc.DeleteBotSkillPackageInstallationParams) (dbsqlc.BotSkillPackageInstallation, error) {
	if s.deleteErr == nil {
		s.deleted = true
	}
	return s.row, s.deleteErr
}

func TestRecordUpsertsPackageRevision(t *testing.T) {
	row := packageRow()
	store := &packageStoreStub{row: row}
	service := NewService(store)

	got, err := service.Record(context.Background(), row.BotID.String(), "native", Requirement{
		RegistryID: row.RegistryID,
		PackageID:  row.PackageID,
		Revision:   row.Revision,
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if got.PackageID != row.PackageID || got.Revision != row.Revision {
		t.Fatalf("Record() = %+v, want Package %q at revision %q", got, row.PackageID, row.Revision)
	}
	if store.upsertParams.WorkspaceTargetID != "native" || store.upsertParams.RegistryID != row.RegistryID {
		t.Fatalf("upsert params = %+v", store.upsertParams)
	}
}

func TestRecordRejectsInvalidRequirement(t *testing.T) {
	store := &packageStoreStub{row: packageRow()}
	_, err := NewService(store).Record(context.Background(), packageUUID(1).String(), "native", Requirement{
		RegistryID: "OpenAI",
		PackageID:  "documents",
		Revision:   strings.Repeat("a", 64),
	})
	if err == nil {
		t.Fatal("Record() accepted an invalid Registry ID")
	}
}

func TestListForTargetFiltersPackages(t *testing.T) {
	native := packageRow()
	remote := packageRow()
	remote.ID = packageUUID(4)
	remote.WorkspaceTargetID = "remote"
	store := &packageStoreStub{rows: []dbsqlc.BotSkillPackageInstallation{native, remote}}

	items, err := NewService(store).ListForTarget(context.Background(), native.BotID.String(), "native")
	if err != nil {
		t.Fatalf("ListForTarget() error = %v", err)
	}
	if len(items) != 1 || items[0].WorkspaceTargetID != "native" {
		t.Fatalf("ListForTarget() = %+v, want only native Package", items)
	}
}

func TestDeleteMapsMissingInstallation(t *testing.T) {
	store := &packageStoreStub{row: packageRow(), deleteErr: pgx.ErrNoRows}
	_, err := NewService(store).Delete(context.Background(), store.row.BotID.String(), store.row.ID.String())
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Delete() error = %v, want ErrNotInstalled", err)
	}
	if store.deleted {
		t.Fatal("Delete() marked a missing Package as deleted")
	}
}

func packageRow() dbsqlc.BotSkillPackageInstallation {
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return dbsqlc.BotSkillPackageInstallation{
		ID: packageUUID(2), BotID: packageUUID(1), WorkspaceTargetID: "native",
		RegistryID: "openai", PackageID: "documents", Revision: strings.Repeat("a", 64),
		InstalledAt: now, UpdatedAt: now,
	}
}

func packageUUID(last byte) pgtype.UUID {
	var value [16]byte
	value[15] = last
	return pgtype.UUID{Bytes: value, Valid: true}
}
