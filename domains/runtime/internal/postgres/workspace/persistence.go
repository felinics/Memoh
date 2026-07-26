package workspace

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/memoh/domains/runtime/container"
	dbsqlc "github.com/memohai/memoh/domains/runtime/internal/postgres/sqlc"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/db"
)

type workspaceQueries interface {
	DeleteContainerByBotID(context.Context, pgtype.UUID) error
	GetBotWorkspaceResourceLimits(context.Context, pgtype.UUID) (dbsqlc.RuntimeBotWorkspaceResourceLimit, error)
	GetContainerByBotID(context.Context, pgtype.UUID) (dbsqlc.RuntimeContainer, error)
	GetVersionSnapshotRuntimeName(context.Context, dbsqlc.GetVersionSnapshotRuntimeNameParams) (string, error)
	InsertLifecycleEvent(context.Context, dbsqlc.InsertLifecycleEventParams) error
	InsertVersion(context.Context, dbsqlc.InsertVersionParams) (dbsqlc.RuntimeContainerVersion, error)
	ListAutoStartContainers(context.Context) ([]dbsqlc.RuntimeContainer, error)
	ListSnapshotsWithVersionByContainerID(context.Context, string) ([]dbsqlc.ListSnapshotsWithVersionByContainerIDRow, error)
	ListVersionsByContainerID(context.Context, string) ([]dbsqlc.ListVersionsByContainerIDRow, error)
	NextVersion(context.Context, string) (int32, error)
	UpdateContainerStarted(context.Context, pgtype.UUID) error
	UpdateContainerStatus(context.Context, dbsqlc.UpdateContainerStatusParams) error
	UpdateContainerStopped(context.Context, pgtype.UUID) error
	UpsertBotWorkspaceResourceLimits(context.Context, dbsqlc.UpsertBotWorkspaceResourceLimitsParams) (dbsqlc.RuntimeBotWorkspaceResourceLimit, error)
	UpsertContainer(context.Context, dbsqlc.UpsertContainerParams) error
	UpsertSnapshot(context.Context, dbsqlc.UpsertSnapshotParams) (dbsqlc.RuntimeSnapshot, error)
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Store adapts the current generated Runtime statements to Workspace consumer
// ports. Generated types and transaction handles never escape.
type Store struct {
	queries      workspaceQueries
	transactions transactionBeginner
}

var (
	_ runtimeworkspace.ContainerStore     = (*Store)(nil)
	_ runtimeworkspace.ResourceLimitStore = (*Store)(nil)
	_ runtimeworkspace.VersionStore       = (*Store)(nil)
)

func NewStore(pool *pgxpool.Pool) *Store {
	return newStore(dbsqlc.New(pool), pool)
}

func newStore(queries workspaceQueries, transactions transactionBeginner) *Store {
	return &Store{queries: queries, transactions: transactions}
}

func (s *Store) FindContainer(ctx context.Context, botID string) (runtimeworkspace.ContainerRecord, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return runtimeworkspace.ContainerRecord{}, err
	}
	row, err := s.queries.GetContainerByBotID(ctx, id)
	if err != nil {
		return runtimeworkspace.ContainerRecord{}, mapQueryError(err)
	}
	return containerRecord(row), nil
}

func (s *Store) ListAutoStartContainers(ctx context.Context) ([]runtimeworkspace.ContainerRecord, error) {
	rows, err := s.queries.ListAutoStartContainers(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]runtimeworkspace.ContainerRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, containerRecord(row))
	}
	return records, nil
}

func (s *Store) UpsertContainer(ctx context.Context, command runtimeworkspace.UpsertContainerCommand) error {
	botID, err := db.ParseUUID(command.BotID)
	if err != nil {
		return err
	}
	return s.queries.UpsertContainer(ctx, dbsqlc.UpsertContainerParams{
		BotID:            botID,
		ContainerID:      command.ContainerID,
		ContainerName:    command.ContainerID,
		Image:            command.Image,
		Status:           command.Status,
		Namespace:        command.Namespace,
		AutoStart:        command.AutoStart,
		ContainerPath:    command.ContainerPath,
		WorkspaceBackend: command.WorkspaceBackend,
	})
}

func (s *Store) DeleteContainer(ctx context.Context, botID string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.DeleteContainerByBotID(ctx, id)
}

func (s *Store) MarkContainerStarted(ctx context.Context, botID string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.UpdateContainerStarted(ctx, id)
}

func (s *Store) MarkContainerStopped(ctx context.Context, botID string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.UpdateContainerStopped(ctx, id)
}

func (s *Store) MarkContainerStatus(ctx context.Context, botID, status string) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	return s.queries.UpdateContainerStatus(ctx, dbsqlc.UpdateContainerStatusParams{BotID: id, Status: status})
}

func (s *Store) FindResourceLimits(ctx context.Context, botID string) (container.ResourceLimits, error) {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return container.ResourceLimits{}, err
	}
	row, err := s.queries.GetBotWorkspaceResourceLimits(ctx, id)
	if err != nil {
		return container.ResourceLimits{}, mapQueryError(err)
	}
	return container.ResourceLimits{
		CPUMillicores: row.CpuMillicores,
		MemoryBytes:   row.MemoryBytes,
		StorageBytes:  row.StorageBytes,
	}, nil
}

func (s *Store) SaveResourceLimits(ctx context.Context, botID string, limits container.ResourceLimits) error {
	id, err := db.ParseUUID(botID)
	if err != nil {
		return err
	}
	_, err = s.queries.UpsertBotWorkspaceResourceLimits(ctx, dbsqlc.UpsertBotWorkspaceResourceLimitsParams{
		BotID:         id,
		CpuMillicores: limits.CPUMillicores,
		MemoryBytes:   limits.MemoryBytes,
		StorageBytes:  limits.StorageBytes,
	})
	return err
}

func (s *Store) ListSnapshots(ctx context.Context, containerID string) ([]runtimeworkspace.SnapshotRecord, error) {
	rows, err := s.queries.ListSnapshotsWithVersionByContainerID(ctx, containerID)
	if err != nil {
		return nil, err
	}
	records := make([]runtimeworkspace.SnapshotRecord, 0, len(rows))
	for _, row := range rows {
		var version *int
		if row.Version.Valid {
			value := int(row.Version.Int32)
			version = &value
		}
		records = append(records, runtimeworkspace.SnapshotRecord{
			RuntimeSnapshotName:       row.RuntimeSnapshotName,
			DisplayName:               db.TextToString(row.DisplayName),
			ParentRuntimeSnapshotName: db.TextToString(row.ParentRuntimeSnapshotName),
			Snapshotter:               row.Snapshotter,
			Source:                    row.Source,
			CreatedAt:                 db.TimeFromPg(row.CreatedAt),
			Version:                   version,
		})
	}
	return records, nil
}

func (s *Store) ListVersions(ctx context.Context, containerID string) ([]runtimeworkspace.VersionRecord, error) {
	rows, err := s.queries.ListVersionsByContainerID(ctx, containerID)
	if err != nil {
		return nil, err
	}
	records := make([]runtimeworkspace.VersionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, runtimeworkspace.VersionRecord{
			ID:                  db.UUIDString(row.ID),
			Version:             int(row.Version),
			RuntimeSnapshotName: row.RuntimeSnapshotName,
			DisplayName:         db.TextToString(row.DisplayName),
			CreatedAt:           db.TimeFromPg(row.CreatedAt),
		})
	}
	return records, nil
}

func (s *Store) FindVersionSnapshotName(ctx context.Context, containerID string, version int) (string, error) {
	// The column is int32; rejecting an out-of-range version beats truncating
	// it into a different version that happens to exist.
	if version < 0 || version > math.MaxInt32 {
		return "", fmt.Errorf("%w: version %d is out of range", runtimeworkspace.ErrRecordNotFound, version)
	}
	name, err := s.queries.GetVersionSnapshotRuntimeName(ctx, dbsqlc.GetVersionSnapshotRuntimeNameParams{
		ContainerID: containerID,
		Version:     int32(version),
	})
	return name, mapQueryError(err)
}

func (s *Store) RecordSnapshotVersion(ctx context.Context, command runtimeworkspace.RecordSnapshotVersionCommand) (runtimeworkspace.RecordedSnapshotVersion, error) {
	if s.transactions == nil {
		return runtimeworkspace.RecordedSnapshotVersion{}, runtimeworkspace.ErrTransactionsRequired
	}
	tx, err := s.transactions.Begin(ctx)
	if err != nil {
		return runtimeworkspace.RecordedSnapshotVersion{}, err
	}
	if tx == nil {
		return runtimeworkspace.RecordedSnapshotVersion{}, runtimeworkspace.ErrTransactionsRequired
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recorded, err := recordSnapshotVersion(ctx, dbsqlc.New(tx), command)
	if err != nil {
		return runtimeworkspace.RecordedSnapshotVersion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return runtimeworkspace.RecordedSnapshotVersion{}, err
	}
	return recorded, nil
}

func recordSnapshotVersion(ctx context.Context, queries workspaceQueries, command runtimeworkspace.RecordSnapshotVersionCommand) (runtimeworkspace.RecordedSnapshotVersion, error) {
	snapshot, err := queries.UpsertSnapshot(ctx, dbsqlc.UpsertSnapshotParams{
		ContainerID:               command.ContainerID,
		RuntimeSnapshotName:       command.RuntimeSnapshotName,
		DisplayName:               optionalPostgresText(command.DisplayName),
		ParentRuntimeSnapshotName: optionalPostgresText(command.ParentRuntimeSnapshotName),
		Snapshotter:               command.Snapshotter,
		Source:                    command.Source,
	})
	if err != nil {
		return runtimeworkspace.RecordedSnapshotVersion{}, err
	}
	version, err := queries.NextVersion(ctx, command.ContainerID)
	if err != nil {
		return runtimeworkspace.RecordedSnapshotVersion{}, err
	}
	row, err := queries.InsertVersion(ctx, dbsqlc.InsertVersionParams{
		ContainerID: command.ContainerID,
		SnapshotID:  snapshot.ID,
		Version:     version,
	})
	if err != nil {
		return runtimeworkspace.RecordedSnapshotVersion{}, err
	}
	return runtimeworkspace.RecordedSnapshotVersion{
		ID:        db.UUIDString(row.ID),
		Version:   int(version),
		CreatedAt: db.TimeFromPg(row.CreatedAt),
	}, nil
}

func (s *Store) InsertLifecycleEvent(ctx context.Context, containerID, eventType string, payload []byte) error {
	return s.queries.InsertLifecycleEvent(ctx, dbsqlc.InsertLifecycleEventParams{
		ID:          fmt.Sprintf("%s-%d", containerID, time.Now().UnixNano()),
		ContainerID: containerID,
		EventType:   eventType,
		Payload:     append([]byte(nil), payload...),
	})
}

func containerRecord(row dbsqlc.RuntimeContainer) runtimeworkspace.ContainerRecord {
	return runtimeworkspace.ContainerRecord{
		BotID:            db.UUIDString(row.BotID),
		ContainerID:      row.ContainerID,
		Image:            row.Image,
		Status:           row.Status,
		Namespace:        row.Namespace,
		ContainerPath:    row.ContainerPath,
		WorkspaceBackend: row.WorkspaceBackend,
		CreatedAt:        db.TimeFromPg(row.CreatedAt),
		UpdatedAt:        db.TimeFromPg(row.UpdatedAt),
	}
}

func optionalPostgresText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}
