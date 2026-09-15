package workspacedeps

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Use the generated methods through a narrow interface without expanding the
// observation store contract implemented by external adapters and test fakes.
type desiredQueries interface {
	GetDesiredDependencyInstallation(context.Context, dbsqlc.GetDesiredDependencyInstallationParams) (dbsqlc.BotDependencyDesiredInstallation, error)
	ListDesiredDependencyInstallations(context.Context, pgtype.UUID) ([]dbsqlc.BotDependencyDesiredInstallation, error)
	ListDesiredDependencyRepairPage(context.Context, dbsqlc.ListDesiredDependencyRepairPageParams) ([]dbsqlc.BotDependencyDesiredInstallation, error)
	QueueDesiredDependencyRepair(context.Context, dbsqlc.QueueDesiredDependencyRepairParams) (dbsqlc.BotDependencyDesiredInstallation, error)
	SetDesiredDependencyRepairResult(context.Context, dbsqlc.SetDesiredDependencyRepairResultParams) (dbsqlc.BotDependencyDesiredInstallation, error)
	ClaimDesiredDependencyRepair(context.Context, dbsqlc.ClaimDesiredDependencyRepairParams) (dbsqlc.ClaimDesiredDependencyRepairRow, error)
	RevokeDesiredDependencyInstallation(context.Context, dbsqlc.RevokeDesiredDependencyInstallationParams) (int64, error)
	FinishAuthorizedDependencyOperation(context.Context, dbsqlc.FinishAuthorizedDependencyOperationParams) (dbsqlc.FinishAuthorizedDependencyOperationRow, error)
	FinishDesiredDependencyRepair(context.Context, dbsqlc.FinishDesiredDependencyRepairParams) (dbsqlc.BotDependencyInstallation, error)
}

func (s *postgresStore) desiredQueries() (desiredQueries, error) {
	q, ok := s.q.(desiredQueries)
	if !ok {
		return nil, errors.New("dependency desired queries unavailable")
	}
	return q, nil
}

func (s *postgresStore) GetDesired(ctx context.Context, key InstallationKey) (DesiredInstallation, error) {
	q, err := s.desiredQueries()
	if err != nil {
		return DesiredInstallation{}, err
	}
	botID, err := parseBotID(key.BotID)
	if err != nil {
		return DesiredInstallation{}, err
	}
	row, err := q.GetDesiredDependencyInstallation(ctx, dbsqlc.GetDesiredDependencyInstallationParams{BotID: botID, DependencyID: key.DependencyID})
	return desiredResult(row, err)
}

func (s *postgresStore) ListDesired(ctx context.Context, botID string) ([]DesiredInstallation, error) {
	q, err := s.desiredQueries()
	if err != nil {
		return nil, err
	}
	botUUID, err := parseBotID(botID)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListDesiredDependencyInstallations(ctx, botUUID)
	return desiredResults(rows, err)
}

func (s *postgresStore) ListDesiredPage(ctx context.Context, after InstallationKey, size int) ([]DesiredInstallation, error) {
	q, err := s.desiredQueries()
	if err != nil {
		return nil, err
	}
	if after.BotID == "" {
		after.BotID = "00000000-0000-0000-0000-000000000000"
	}
	botID, err := parseBotID(after.BotID)
	if err != nil {
		return nil, err
	}
	if size <= 0 || size > 256 {
		size = 128
	}
	rows, err := q.ListDesiredDependencyRepairPage(ctx, dbsqlc.ListDesiredDependencyRepairPageParams{AfterBotID: botID, AfterDependencyID: after.DependencyID, PageSize: int32(size)})
	return desiredResults(rows, err)
}

func (s *postgresStore) QueueRepair(ctx context.Context, target DesiredInstallation, operationID string, now time.Time, manual bool) (DesiredInstallation, error) {
	q, err := s.desiredQueries()
	if err != nil {
		return DesiredInstallation{}, err
	}
	botID, err := parseBotID(target.BotID)
	if err != nil {
		return DesiredInstallation{}, err
	}
	row, err := q.QueueDesiredDependencyRepair(ctx, dbsqlc.QueueDesiredDependencyRepairParams{BotID: botID, DependencyID: target.DependencyID, DesiredRevision: target.Revision, OperationID: operationID, NowAt: pgtype.Timestamptz{Time: now, Valid: true}, ManualRetry: manual})
	return desiredCASResult(row, err)
}

func (s *postgresStore) SetRepairResult(ctx context.Context, target DesiredInstallation, status RepairStatus, attempts int, next *time.Time, code string) (DesiredInstallation, error) {
	q, err := s.desiredQueries()
	if err != nil {
		return DesiredInstallation{}, err
	}
	botID, err := parseBotID(target.BotID)
	if err != nil {
		return DesiredInstallation{}, err
	}
	if attempts < 0 {
		attempts = 0
	} else if attempts > math.MaxInt32 {
		attempts = math.MaxInt32
	}
	row, err := q.SetDesiredDependencyRepairResult(ctx, dbsqlc.SetDesiredDependencyRepairResultParams{BotID: botID, DependencyID: target.DependencyID, DesiredRevision: target.Revision, OperationID: target.RepairOperationID, ExpectedRepairStatus: string(target.RepairStatus), RepairStatus: string(status), RepairAttempts: int32(attempts), RepairNextAttemptAt: nullableTimestamptz(next), RepairLastErrorCode: code})
	return desiredCASResult(row, err)
}

func (s *postgresStore) ClaimRepair(ctx context.Context, target DesiredInstallation, intent *OperationReceipt) (Installation, error) {
	q, err := s.desiredQueries()
	if err != nil {
		return Installation{}, err
	}
	botID, err := parseBotID(target.BotID)
	if err != nil {
		return Installation{}, err
	}
	data, err := encodeOperationIntent(intent)
	if err != nil {
		return Installation{}, err
	}
	row, err := q.ClaimDesiredDependencyRepair(ctx, dbsqlc.ClaimDesiredDependencyRepairParams{BotID: botID, DependencyID: target.DependencyID, DesiredRevision: target.Revision, OperationID: target.RepairOperationID, OperationIntent: data})
	return operationResult(dbsqlc.BotDependencyInstallation(row), err)
}

func (s *postgresStore) RevokeDesired(ctx context.Context, key InstallationKey, operationID, actor string) error {
	q, err := s.desiredQueries()
	if err != nil {
		return err
	}
	botID, err := parseBotID(key.BotID)
	if err != nil {
		return err
	}
	_, err = q.RevokeDesiredDependencyInstallation(ctx, dbsqlc.RevokeDesiredDependencyInstallationParams{BotID: botID, DependencyID: key.DependencyID, OperationID: operationID, Actor: actor})
	return err
}

func (s *postgresStore) FinishAuthorized(ctx context.Context, d DesiredInstallation, operationID string) (Installation, error) {
	q, err := s.desiredQueries()
	if err != nil {
		return Installation{}, err
	}
	botID, err := parseBotID(d.BotID)
	if err != nil {
		return Installation{}, err
	}
	entrypoints, err := json.Marshal(d.Entrypoints)
	if err != nil {
		return Installation{}, err
	}
	row, err := q.FinishAuthorizedDependencyOperation(ctx, dbsqlc.FinishAuthorizedDependencyOperationParams{BotID: botID, DependencyID: d.DependencyID, OperationID: operationID, Version: d.Version, SourceUrl: d.SourceURL, RegistryID: d.RegistryID, DefinitionRevision: d.DefinitionRevision, ManifestDigest: d.ManifestDigest, Actor: d.AuthorizedByActor, PlatformOs: d.Platform.OS, PlatformArch: d.Platform.Arch, PlatformLibc: d.Platform.Libc, Entrypoints: entrypoints, InstallationID: d.InstallationID, PayloadPath: d.PayloadPath, StoreRoot: d.StoreRoot})
	return operationResult(dbsqlc.BotDependencyInstallation(row), err)
}

func (s *postgresStore) FinishRepair(ctx context.Context, d DesiredInstallation, operationID string) (Installation, error) {
	q, err := s.desiredQueries()
	if err != nil {
		return Installation{}, err
	}
	botID, err := parseBotID(d.BotID)
	if err != nil {
		return Installation{}, err
	}
	entrypoints, err := json.Marshal(d.Entrypoints)
	if err != nil {
		return Installation{}, err
	}
	row, err := q.FinishDesiredDependencyRepair(ctx, dbsqlc.FinishDesiredDependencyRepairParams{BotID: botID, DependencyID: d.DependencyID, OperationID: operationID, Version: d.Version, DesiredRevision: d.Revision, Entrypoints: entrypoints, InstallationID: d.InstallationID, PayloadPath: d.PayloadPath, StoreRoot: d.StoreRoot})
	return operationResult(row, err)
}

func desiredCASResult(row dbsqlc.BotDependencyDesiredInstallation, err error) (DesiredInstallation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return DesiredInstallation{}, ErrDesiredTargetChanged
	}
	return desiredResult(row, err)
}

func desiredResult(row dbsqlc.BotDependencyDesiredInstallation, err error) (DesiredInstallation, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return DesiredInstallation{}, ErrInstallationNotFound
	}
	if err != nil {
		return DesiredInstallation{}, err
	}
	return desiredFromRow(row), nil
}

func desiredResults(rows []dbsqlc.BotDependencyDesiredInstallation, err error) ([]DesiredInstallation, error) {
	if err != nil {
		return nil, err
	}
	out := make([]DesiredInstallation, 0, len(rows))
	for _, row := range rows {
		out = append(out, desiredFromRow(row))
	}
	return out, nil
}

func desiredFromRow(r dbsqlc.BotDependencyDesiredInstallation) DesiredInstallation {
	d := DesiredInstallation{InstallationKey: InstallationKey{BotID: uuidString(r.BotID), DependencyID: r.DependencyID}, Revision: r.DesiredRevision, Version: r.Version, SourceURL: r.SourceUrl, RegistryID: r.RegistryID, DefinitionRevision: r.DefinitionRevision, ManifestDigest: r.ManifestDigest, AutoRepairAuthorizedAt: db.TimeFromPg(r.AuthorizedAt), AuthorizedByOperationID: r.AuthorizedByOperationID, AuthorizedByActor: r.AuthorizedByActor, Platform: Platform{OS: r.PlatformOs, Arch: r.PlatformArch, Libc: r.PlatformLibc}, InstallationID: r.InstallationID, PayloadPath: r.PayloadPath, StoreRoot: r.StoreRoot, RepairStatus: RepairStatus(r.RepairStatus), RepairOperationID: r.RepairOperationID, RepairAttempts: int(r.RepairAttempts), RepairLastErrorCode: r.RepairLastErrorCode, CreatedAt: db.TimeFromPg(r.CreatedAt), UpdatedAt: db.TimeFromPg(r.UpdatedAt)}
	_ = json.Unmarshal(r.Entrypoints, &d.Entrypoints)
	if r.RepairNextAttemptAt.Valid {
		t := db.TimeFromPg(r.RepairNextAttemptAt)
		d.RepairNextAttemptAt = &t
	}
	return d
}
