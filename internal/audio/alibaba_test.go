package audio

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/models"
)

// Run against an isolated database initialized from 0001_init.up.sql.
// The transaction rolls back all provider/model rows and migration changes.
func TestAlibabaPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := t.Context()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(context.Background()) }()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, `SELECT set_config('memoh.team_id', '00000000-0000-0000-0000-000000000001', true)`)
	require.NoError(t, err)
	paths, err := filepath.Glob(filepath.Join("..", "..", "db", "postgres", "migrations", "*_alibaba_transcription.up.sql"))
	require.NoError(t, err)
	require.Len(t, paths, 1)
	up, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	down, err := os.ReadFile(paths[0][:len(paths[0])-len("up.sql")] + "down.sql")
	require.NoError(t, err)
	// Validate canonical -> old schema -> upgrade -> reapply -> rollback -> upgrade.
	for _, migration := range [][]byte{down, up, up, down, down, up} {
		_, err := tx.Exec(ctx, string(migration))
		require.NoError(t, err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"明天上午九点开会。"}}]}`)
	}))
	defer upstream.Close()
	queries := sqlc.New(tx)
	provider, err := queries.CreateProvider(ctx, sqlc.CreateProviderParams{
		Name: "Alibaba ASR migration test", ClientType: "alibabacloud-transcription", Enable: true,
		Config: []byte(fmt.Sprintf(`{"api_key":"test-key","base_url":%q}`, upstream.URL)), Metadata: []byte(`{}`),
	})
	require.NoError(t, err)
	var modelID pgtype.UUID
	err = tx.QueryRow(ctx, `INSERT INTO models (model_id, name, provider_id, type, enable) VALUES ('qwen3-asr-flash', 'ASR test', $1, 'transcription', true) RETURNING id`, provider.ID).Scan(&modelID)
	require.NoError(t, err)
	service := NewService(slog.New(slog.DiscardHandler), postgresstore.NewQueries(queries), NewRegistry())
	providers, err := service.ListTranscriptionProviders(ctx)
	require.NoError(t, err)
	found := false
	for _, p := range providers {
		if p.ID == provider.ID.String() {
			found = true
			require.NotEqual(t, "test-key", p.Config["api_key"])
		}
	}
	require.True(t, found, "ASR provider must be visible to the PC voice page")
	llmProviders, err := queries.ListProviders(ctx)
	require.NoError(t, err)
	for _, p := range llmProviders {
		require.NotEqual(t, provider.ID, p.ID)
	}
	discovered, err := service.FetchRemoteTranscriptionModels(ctx, provider.ID.String())
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	result, err := service.Transcribe(ctx, modelID.String(), []byte("fixture"), "test.m4a", "application/octet-stream", nil)
	require.NoError(t, err)
	require.Equal(t, "明天上午九点开会。", result.Text)
	_, err = tx.Exec(ctx, `UPDATE models SET enable=false WHERE id=$1`, modelID)
	require.NoError(t, err)
	_, err = service.Transcribe(ctx, modelID.String(), []byte("fixture"), "test.m4a", "audio/mp4", nil)
	require.ErrorContains(t, err, "disabled")
	// A rollback with configured ASR providers must fail without deleting user data.
	savepoint, err := tx.Begin(ctx)
	require.NoError(t, err)
	_, err = savepoint.Exec(ctx, string(down))
	require.Error(t, err)
	require.NoError(t, savepoint.Rollback(ctx))
	_, err = queries.GetProviderByID(ctx, provider.ID)
	require.NoError(t, err)
}

func TestAlibabaTranscriptionRegistry(t *testing.T) {
	r := NewRegistry()
	def, err := r.Get(models.ClientTypeAlibabaTranscription)
	require.NoError(t, err)
	require.Nil(t, def.Factory)
	require.NotNil(t, def.TranscriptionFactory)
	require.True(t, models.IsValidClientType(def.ClientType))
	require.False(t, models.IsLLMClientType(def.ClientType))
	p, err := def.TranscriptionFactory(map[string]any{"api_key": "test-key"})
	require.NoError(t, err)
	catalog, err := p.ListModels(t.Context())
	require.NoError(t, err)
	require.Len(t, catalog, 1)
	require.Equal(t, def.DefaultTranscriptionModel, catalog[0].ID)
	var transcriptionTypes []string
	for _, meta := range r.ListTranscriptionMeta() {
		transcriptionTypes = append(transcriptionTypes, meta.Provider)
	}
	require.Contains(t, transcriptionTypes, string(def.ClientType))
	for _, meta := range r.ListSpeechMeta() {
		require.NotEqual(t, string(def.ClientType), meta.Provider)
	}
	// Dedicated ASR registration must not change existing speech-derived registrations.
	require.Contains(t, transcriptionTypes, string(models.ClientTypeOpenAITranscription))
	require.Contains(t, transcriptionTypes, string(models.ClientTypeGoogleTranscription))
}
