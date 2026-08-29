package command

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/settings"
)

type reasoningCommandQueries struct {
	dbstore.Queries
	model          sqlc.Model
	provider       sqlc.Provider
	modelErr       error
	providerErr    error
	upsertAttempts int
}

func (q *reasoningCommandQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{
		Language:           settings.DefaultLanguage,
		CommandUiLanguage:  settings.DefaultCommandUILanguage,
		ReasoningEffort:    settings.DefaultReasoningEffort,
		ChatModelID:        q.model.ID,
		ChatRuntime:        settings.ChatRuntimeModel,
		ChatAcpProjectPath: settings.DefaultACPProjectPath,
		ChatAcpProjectMode: settings.DefaultACPProjectMode,
	}, nil
}

func (q *reasoningCommandQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	if q.modelErr != nil {
		return sqlc.Model{}, q.modelErr
	}
	return q.model, nil
}

func (q *reasoningCommandQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	if q.providerErr != nil {
		return sqlc.Provider{}, q.providerErr
	}
	return q.provider, nil
}

// GetBotByID is the first query UpsertBot performs. Rejection tests use the call
// count as a persistence-boundary assertion: invalid selections must stop before
// any write path is entered.
func (q *reasoningCommandQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	q.upsertAttempts++
	return sqlc.GetBotByIDRow{}, errors.New("unexpected settings upsert")
}

func newReasoningCommandHarness(
	t *testing.T,
	modelConfig string,
	modelErr error,
	providerErr error,
) (*Handler, *reasoningCommandQueries, string) {
	t.Helper()

	botID := "00000000-0000-0000-0000-000000000610"
	modelID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000611"), Valid: true}
	providerID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000612"), Valid: true}
	queries := &reasoningCommandQueries{
		model: sqlc.Model{
			ID:         modelID,
			ModelID:    "reasoning-test-model",
			ProviderID: providerID,
			Type:       string(models.ModelTypeChat),
			Enable:     true,
			Config:     []byte(modelConfig),
		},
		provider: sqlc.Provider{
			ID:         providerID,
			Name:       "reasoning-test-provider",
			ClientType: string(models.ClientTypeOpenAICodex),
			Enable:     true,
		},
		modelErr:    modelErr,
		providerErr: providerErr,
	}
	logger := slog.New(slog.DiscardHandler)
	settingsService := settings.NewService(logger, queries, nil, nil)
	handler := NewHandler(
		logger,
		&fakeRoleResolver{role: "owner"},
		nil,
		settingsService,
		nil,
		models.NewService(logger, queries),
		providers.NewService(logger, queries, ""),
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return handler, queries, botID
}

func executeReasoningCommand(t *testing.T, handler *Handler, botID, text string) *Result {
	t.Helper()
	result, err := handler.ExecuteResult(context.Background(), ExecuteInput{
		BotID:             botID,
		ChannelIdentityID: "owner-channel-identity",
		Text:              text,
		Locale:            "en",
	})
	if err != nil {
		t.Fatalf("ExecuteResult(%q): %v", text, err)
	}
	if result == nil {
		t.Fatalf("ExecuteResult(%q) returned nil", text)
	}
	return result
}

func TestReasoningSetOffRequiresDisableCapability(t *testing.T) {
	t.Parallel()

	handler, queries, botID := newReasoningCommandHarness(t,
		`{"compatibilities":["reasoning"],"thinking_mode":"toggle","reasoning_efforts":["minimal","low","max"]}`,
		nil, nil,
	)
	result := executeReasoningCommand(t, handler, botID, "/reasoning set off")

	if got, want := result.Text, `Unknown level "off" — available levels: minimal, low, max.`; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
	if queries.upsertAttempts != 0 {
		t.Fatalf("invalid off selection entered persistence path %d time(s)", queries.upsertAttempts)
	}
}

func TestReasoningResolvedUnsupportedDoesNotUseFallback(t *testing.T) {
	t.Parallel()

	handler, queries, botID := newReasoningCommandHarness(t,
		`{"thinking_mode":"none"}`,
		nil, nil,
	)
	for _, command := range []string{"/reasoning", "/reasoning set high"} {
		result := executeReasoningCommand(t, handler, botID, command)
		if got, want := result.Text, "The current model doesn't support reasoning."; got != want {
			t.Fatalf("%s response = %q, want %q", command, got, want)
		}
		if result.Interactive != nil {
			t.Fatalf("%s returned invented choices: %+v", command, result.Interactive)
		}
	}
	if queries.upsertAttempts != 0 {
		t.Fatalf("unsupported model entered persistence path %d time(s)", queries.upsertAttempts)
	}
}

func TestReasoningAlwaysOnModelReportsNoControl(t *testing.T) {
	t.Parallel()

	handler, queries, botID := newReasoningCommandHarness(t,
		`{"compatibilities":["reasoning"],"thinking_mode":"always"}`,
		nil, nil,
	)
	for _, command := range []string{"/reasoning", "/reasoning set high"} {
		result := executeReasoningCommand(t, handler, botID, command)
		if got, want := result.Text, "The current model always reasons and exposes no reasoning controls."; got != want {
			t.Fatalf("%s response = %q, want %q", command, got, want)
		}
		if result.Interactive != nil {
			t.Fatalf("%s returned empty choices: %+v", command, result.Interactive)
		}
	}
	if queries.upsertAttempts != 0 {
		t.Fatalf("always-on model entered persistence path %d time(s)", queries.upsertAttempts)
	}
}

func TestReasoningLookupFailuresFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		modelErr    error
		providerErr error
	}{
		{name: "model lookup", modelErr: errors.New("SECRET model diagnostic")},
		{name: "provider lookup", providerErr: errors.New("SECRET provider diagnostic")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, queries, botID := newReasoningCommandHarness(t,
				`{"compatibilities":["reasoning"],"thinking_mode":"toggle","reasoning_efforts":["disable","low","high"]}`,
				tt.modelErr, tt.providerErr,
			)
			result := executeReasoningCommand(t, handler, botID, "/reasoning set high")

			if got, want := result.Text, "Reasoning isn't available right now."; got != want {
				t.Fatalf("response = %q, want %q", got, want)
			}
			if strings.Contains(result.Text, "SECRET") {
				t.Fatalf("response leaked lookup diagnostic: %q", result.Text)
			}
			if queries.upsertAttempts != 0 {
				t.Fatalf("lookup failure entered persistence path %d time(s)", queries.upsertAttempts)
			}
		})
	}
}

func TestReasoningSetUsageFollowsResolvedModel(t *testing.T) {
	t.Parallel()

	handler, queries, botID := newReasoningCommandHarness(t,
		`{"compatibilities":["reasoning"],"thinking_mode":"toggle","reasoning_efforts":["minimal","low","max"]}`,
		nil, nil,
	)
	result := executeReasoningCommand(t, handler, botID, "/reasoning set")

	if got, want := result.Text, "Usage: /reasoning set <minimal|low|max>"; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
	if queries.upsertAttempts != 0 {
		t.Fatalf("usage request entered persistence path %d time(s)", queries.upsertAttempts)
	}
}
