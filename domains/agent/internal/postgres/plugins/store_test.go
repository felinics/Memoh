package plugins

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	pluginspersistence "github.com/memohai/memoh/domains/agent/extension/plugins/persistence"
	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

const (
	pluginBotID          = "11111111-1111-4111-8111-111111111111"
	pluginInstallationID = "22222222-2222-4222-8222-222222222222"
	pluginResourceID     = "33333333-3333-4333-8333-333333333333"
)

type queriesStub struct {
	installationRow dbsqlc.AgentBotPluginInstallation
	resourceRow     dbsqlc.AgentBotPluginResource
	createArg       dbsqlc.CreateBotPluginInstallationParams
	deleteArg       dbsqlc.DeleteBotPluginInstallationParams
	deleteResources pgtype.UUID
	findArg         dbsqlc.GetBotPluginInstallationByIDParams
	findErr         error
	listBotID       pgtype.UUID
	listResourceID  pgtype.UUID
	listResourceErr error
	updateArg       dbsqlc.UpdateBotPluginInstallationStatusParams
	updateErr       error
	upsertArg       dbsqlc.UpsertBotPluginResourceParams
}

func (q *queriesStub) CreateBotPluginInstallation(_ context.Context, arg dbsqlc.CreateBotPluginInstallationParams) (dbsqlc.AgentBotPluginInstallation, error) {
	q.createArg = arg
	return q.installationRow, nil
}

func (q *queriesStub) DeleteBotPluginInstallation(_ context.Context, arg dbsqlc.DeleteBotPluginInstallationParams) error {
	q.deleteArg = arg
	return nil
}

func (q *queriesStub) DeleteBotPluginResources(_ context.Context, id pgtype.UUID) error {
	q.deleteResources = id
	return nil
}

func (q *queriesStub) GetBotPluginInstallationByID(_ context.Context, arg dbsqlc.GetBotPluginInstallationByIDParams) (dbsqlc.AgentBotPluginInstallation, error) {
	q.findArg = arg
	return q.installationRow, q.findErr
}

func (q *queriesStub) ListBotPluginInstallations(_ context.Context, id pgtype.UUID) ([]dbsqlc.AgentBotPluginInstallation, error) {
	q.listBotID = id
	return []dbsqlc.AgentBotPluginInstallation{q.installationRow}, nil
}

func (q *queriesStub) ListBotPluginResources(_ context.Context, id pgtype.UUID) ([]dbsqlc.AgentBotPluginResource, error) {
	q.listResourceID = id
	return []dbsqlc.AgentBotPluginResource{q.resourceRow}, q.listResourceErr
}

func (q *queriesStub) UpdateBotPluginInstallationStatus(_ context.Context, arg dbsqlc.UpdateBotPluginInstallationStatusParams) (dbsqlc.AgentBotPluginInstallation, error) {
	q.updateArg = arg
	return q.installationRow, q.updateErr
}

func (q *queriesStub) UpsertBotPluginResource(_ context.Context, arg dbsqlc.UpsertBotPluginResourceParams) (dbsqlc.AgentBotPluginResource, error) {
	q.upsertArg = arg
	return q.resourceRow, nil
}

func TestStoreMapsPluginInstallations(t *testing.T) {
	t.Parallel()

	botID := mustUUID(t, pluginBotID)
	installationID := mustUUID(t, pluginInstallationID)
	installedAt := time.Date(2026, time.July, 23, 1, 2, 3, 0, time.UTC)
	updatedAt := installedAt.Add(time.Minute)
	queries := &queriesStub{installationRow: dbsqlc.AgentBotPluginInstallation{
		ID: installationID, BotID: botID, PluginID: "github", PluginName: "GitHub", Version: "1.0.0",
		Status: "ready", Enabled: true, Config: []byte(`{"variables":{}}`), Metadata: []byte(`{"source":"test"}`),
		Manifest: []byte(`{"id":"github"}`), InstalledAt: pgtype.Timestamptz{Time: installedAt, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: updatedAt, Valid: true},
	}}
	store := NewStore(queries)

	items, err := store.ListInstallations(t.Context(), pluginBotID)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListInstallations() = %#v, %v", items, err)
	}
	if got := items[0]; got.ID != pluginInstallationID || got.BotID != pluginBotID || got.PluginID != "github" || !got.InstalledAt.Equal(installedAt) || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("installation = %#v", got)
	}
	if queries.listBotID != botID {
		t.Fatalf("ListBotPluginInstallations id = %v", queries.listBotID)
	}

	created, err := store.CreateInstallation(t.Context(), pluginspersistence.CreateInstallationInput{
		BotID: pluginBotID, PluginID: "github", PluginName: "GitHub", Version: "1.0.0", Status: "ready", Enabled: true,
		Config: []byte(`{"variables":{"TOKEN":"secret"}}`), Metadata: []byte(`{}`), Manifest: []byte(`{"id":"github"}`),
	})
	if err != nil || created.ID != pluginInstallationID {
		t.Fatalf("CreateInstallation() = %#v, %v", created, err)
	}
	if queries.createArg.BotID != botID || queries.createArg.PluginID != "github" || string(queries.createArg.Manifest) != `{"id":"github"}` {
		t.Fatalf("CreateBotPluginInstallation params = %#v", queries.createArg)
	}

	if _, err := store.FindInstallation(t.Context(), pluginBotID, pluginInstallationID); err != nil {
		t.Fatalf("FindInstallation() error = %v", err)
	}
	if queries.findArg.BotID != botID || queries.findArg.ID != installationID {
		t.Fatalf("GetBotPluginInstallationByID params = %#v", queries.findArg)
	}
	if _, err := store.UpdateInstallationStatus(t.Context(), pluginspersistence.InstallationStatusUpdate{
		BotID: pluginBotID, InstallationID: pluginInstallationID, Status: "disabled", Enabled: false,
	}); err != nil {
		t.Fatalf("UpdateInstallationStatus() error = %v", err)
	}
	if queries.updateArg.ID != installationID || queries.updateArg.Status != "disabled" || queries.updateArg.Enabled {
		t.Fatalf("UpdateBotPluginInstallationStatus params = %#v", queries.updateArg)
	}
	if err := store.DeleteInstallation(t.Context(), pluginBotID, pluginInstallationID); err != nil {
		t.Fatalf("DeleteInstallation() error = %v", err)
	}
	if queries.deleteArg.BotID != botID || queries.deleteArg.ID != installationID {
		t.Fatalf("DeleteBotPluginInstallation params = %#v", queries.deleteArg)
	}
}

func TestStoreMapsPluginResources(t *testing.T) {
	t.Parallel()

	installationID := mustUUID(t, pluginInstallationID)
	resourceID := mustUUID(t, pluginResourceID)
	createdAt := time.Date(2026, time.July, 23, 2, 3, 4, 0, time.UTC)
	queries := &queriesStub{resourceRow: dbsqlc.AgentBotPluginResource{
		ID: resourceID, InstallationID: installationID, ResourceType: "mcp", ResourceKey: "github",
		ResourceID: "connection-1", Status: "ready", Metadata: []byte(`{"visible":true}`),
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
	}}
	store := NewStore(queries)

	items, err := store.ListResources(t.Context(), pluginInstallationID)
	if err != nil || len(items) != 1 {
		t.Fatalf("ListResources() = %#v, %v", items, err)
	}
	if got := items[0]; got.ID != pluginResourceID || got.InstallationID != pluginInstallationID || got.Type != "mcp" || got.Key != "github" || !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("resource = %#v", got)
	}
	if queries.listResourceID != installationID {
		t.Fatalf("ListBotPluginResources id = %v", queries.listResourceID)
	}
	if err := store.UpsertResource(t.Context(), pluginspersistence.ResourceUpsert{
		InstallationID: pluginInstallationID, Type: "skill", Key: "review", ResourceID: "/skills/review", Status: "bundled", Metadata: []byte(`{}`),
	}); err != nil {
		t.Fatalf("UpsertResource() error = %v", err)
	}
	if queries.upsertArg.InstallationID != installationID || queries.upsertArg.ResourceType != "skill" || queries.upsertArg.ResourceKey != "review" {
		t.Fatalf("UpsertBotPluginResource params = %#v", queries.upsertArg)
	}
	if err := store.DeleteResources(t.Context(), pluginInstallationID); err != nil {
		t.Fatalf("DeleteResources() error = %v", err)
	}
	if queries.deleteResources != installationID {
		t.Fatalf("DeleteBotPluginResources id = %v", queries.deleteResources)
	}
}

func TestStoreRejectsInvalidPluginIDs(t *testing.T) {
	t.Parallel()

	store := NewStore(&queriesStub{})
	if _, err := store.ListInstallations(t.Context(), "not-a-uuid"); err == nil {
		t.Fatal("ListInstallations() error = nil")
	}
	if _, err := store.FindInstallation(t.Context(), pluginBotID, "not-a-uuid"); err == nil {
		t.Fatal("FindInstallation() error = nil")
	}
	if err := store.UpsertResource(t.Context(), pluginspersistence.ResourceUpsert{InstallationID: "not-a-uuid"}); err == nil {
		t.Fatal("UpsertResource() error = nil")
	}
}

func TestStoreTreatsMissingResourceRowsAsEmpty(t *testing.T) {
	t.Parallel()

	store := NewStore(&queriesStub{listResourceErr: pgx.ErrNoRows})
	resources, err := store.ListResources(t.Context(), pluginInstallationID)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if resources == nil || len(resources) != 0 {
		t.Fatalf("ListResources() = %#v, want empty slice", resources)
	}
}

func TestStoreMapsMissingInstallationRows(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Store) error
	}{
		{
			name: "find",
			run: func(store *Store) error {
				_, err := store.FindInstallation(t.Context(), pluginBotID, pluginInstallationID)
				return err
			},
		},
		{
			name: "update",
			run: func(store *Store) error {
				_, err := store.UpdateInstallationStatus(t.Context(), pluginspersistence.InstallationStatusUpdate{
					BotID: pluginBotID, InstallationID: pluginInstallationID,
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries := &queriesStub{findErr: pgx.ErrNoRows, updateErr: pgx.ErrNoRows}
			err := test.run(NewStore(queries))
			if !errors.Is(err, pluginspersistence.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
			if errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("error leaked pgx.ErrNoRows: %v", err)
			}
		})
	}
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
