package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/chatgptplan"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
)

type chatGPTFailureQueries struct {
	dbstore.Queries
	provider sqlc.Provider
	model    sqlc.Model
	failure  error
}

func (q *chatGPTFailureQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func (q *chatGPTFailureQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return q.model, nil
}

func (*chatGPTFailureQueries) SupportsTransactions() bool { return true }

func (q *chatGPTFailureQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	return fn(q)
}

func (q *chatGPTFailureQueries) LockChatGPTProviderSession(context.Context, pgtype.UUID) (sqlc.ChatgptProviderSession, error) {
	return sqlc.ChatgptProviderSession{}, q.failure
}

// Exercise the actual service/handler paths, including the classification gate
// on model import. Credential failures must retain their stable identity before
// legacy HTTP-error conversion, without exposing private diagnostics.
func TestChatGPTFailuresAcrossModelConfigurationEndpoints(t *testing.T) {
	t.Parallel()
	providerID, err := db.ParseUUID("00000000-0000-0000-0000-000000000010")
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := db.ParseUUID("00000000-0000-0000-0000-000000000011")
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []struct {
		name   string
		err    error
		code   apperror.Code
		status int
	}{
		{"disconnected", chatgptplan.ErrNotConnected, apperror.CodeChatGPTNotConnected, http.StatusConflict},
		{"quota", chatgptplan.ErrQuota, apperror.CodeChatGPTUsageLimit, http.StatusTooManyRequests},
		{"unavailable", chatgptplan.ErrUpstream, apperror.CodeChatGPTUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(failure.name, func(t *testing.T) {
			q := &chatGPTFailureQueries{
				provider: sqlc.Provider{ID: providerID, Name: "ChatGPT", ClientType: string(models.ClientTypeOpenAIChatGPT), Config: []byte(`{}`), Metadata: []byte(`{}`)},
				model:    sqlc.Model{ID: modelID, ProviderID: providerID, ModelID: "test-model", Type: string(models.ModelTypeChat), Config: []byte(`{}`)},
				failure:  errors.Join(failure.err, errors.New("SECRET provider diagnostic")),
			}
			sessions := chatgptplan.NewSessionService(q, config.Config{Auth: config.AuthConfig{AgentCredentialsEncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}})
			providerService := providers.NewService(slog.Default(), q, "")
			providerService.SetChatGPTSessions(sessions)
			modelService := models.NewService(slog.Default(), q)
			modelService.SetChatGPTSessions(sessions)
			providerHandler := NewProvidersHandler(slog.Default(), providerService, modelService)
			modelHandler := NewModelsHandler(slog.Default(), modelService, providerService)
			for _, endpoint := range []struct {
				path string
				id   string
				run  echo.HandlerFunc
			}{
				{"/providers/:id/import-models", providerID.String(), providerHandler.ImportModels},
				{"/providers/:id/test", providerID.String(), providerHandler.Test},
				{"/models/:id/test", modelID.String(), modelHandler.Test},
			} {
				t.Run(endpoint.path, func(t *testing.T) {
					e := echo.New()
					c := e.NewContext(httptest.NewRequest(http.MethodPost, strings.ReplaceAll(endpoint.path, ":id", endpoint.id), nil), httptest.NewRecorder())
					c.SetPath(endpoint.path)
					c.SetParamNames("id")
					c.SetParamValues(endpoint.id)
					err := endpoint.run(c)
					if got := apperror.CodeOf(err); got != failure.code {
						t.Fatalf("code = %q, want %q; error = %v", got, failure.code, err)
					}
					problem, ok := apperror.ProblemFrom(err, "request-chatgpt")
					if !ok || problem.Status != failure.status {
						t.Fatalf("problem = %#v, want status %d", problem, failure.status)
					}
					body, marshalErr := json.Marshal(problem)
					if marshalErr != nil || strings.Contains(string(body), "SECRET") {
						t.Fatalf("invalid or private problem response: %s, %v", body, marshalErr)
					}
					if !errors.Is(apperror.CauseOf(err), q.failure) {
						t.Fatal("private cause was not retained for logging")
					}
				})
			}
		})
	}
}
