package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

type controllerQueries struct {
	*compactionConfigQueries
	settings sqlc.GetSettingsByBotIDRow
}

func (q *controllerQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return q.settings, nil
}

type recordingCompactionRunner struct {
	configs []compaction.TriggerConfig
}

func (r *recordingCompactionRunner) Compact(_ context.Context, cfg compaction.TriggerConfig) ([]compaction.Pass, error) {
	r.configs = append(r.configs, cfg)
	return []compaction.Pass{{}}, nil
}

func newControllerPolicyService(t *testing.T) (*Service, *recordingCompactionRunner) {
	t.Helper()
	const (
		modelUUID    = "00000000-0000-0000-0000-000000000451"
		providerUUID = "00000000-0000-0000-0000-000000000452"
		botUUID      = "00000000-0000-0000-0000-000000000453"
	)
	queries := &controllerQueries{
		compactionConfigQueries: &compactionConfigQueries{
			model: sqlc.Model{
				ID:         compactionConfigUUID(t, modelUUID),
				ModelID:    "compact-model",
				ProviderID: compactionConfigUUID(t, providerUUID),
				Type:       "chat",
				Enable:     true,
				Config:     []byte(`{"context_window":200000}`),
			},
			provider: sqlc.Provider{
				ID:         compactionConfigUUID(t, providerUUID),
				Name:       "test-provider",
				ClientType: "openai-completions",
				Enable:     true,
				Config:     []byte(`{"api_key":"test-key"}`),
			},
		},
		settings: sqlc.GetSettingsByBotIDRow{
			BotID:           compactionConfigUUID(t, botUUID),
			ReasoningEffort: "medium",
			ChatModelID:     compactionConfigUUID(t, modelUUID),
		},
	}
	runner := &recordingCompactionRunner{}
	service := &Service{
		logger:            slog.New(slog.DiscardHandler),
		modelsService:     models.NewService(slog.New(slog.DiscardHandler), queries),
		queries:           queries,
		settingsService:   settings.NewService(slog.New(slog.DiscardHandler), queries, nil, nil),
		compactionService: runner,
	}
	return service, runner
}

func TestSetCompactionServicePreservesNil(t *testing.T) {
	t.Parallel()

	service := &Service{}
	service.SetCompactionService(nil)
	if service.compactionService != nil {
		t.Fatal("SetCompactionService(nil) stored a non-nil runner")
	}
}

type cutoffMessageService struct {
	recordingMessageService
	created time.Time
}

func (s *cutoffMessageService) GetByIDBySession(_ context.Context, _, messageID string) (messagepkg.Message, error) {
	if messageID != "edited" {
		return messagepkg.Message{}, errors.New("message not found")
	}
	return messagepkg.Message{ID: messageID, CreatedAt: s.created}, nil
}

func TestCompactForBudgetBoundsAnEditByItsCutoffAndReplacedTurn(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t)
	created := time.UnixMilli(1_700_000_000_000).UTC()
	service.messageService = &cutoffMessageService{created: created}
	req := ChatRequest{
		BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID, HistoryCutoffBeforeMessageID: "edited",
		TurnReplacement: &messagepkg.TurnReplacement{OldTurnID: "replaced-turn", ReplacementTurnID: "edit-turn"},
	}

	if err := service.compactForBudget(t.Context(), req, 2000, 1000, ""); err != nil || len(runner.configs) != 1 || !runner.configs[0].CandidatesBefore.Equal(created) {
		t.Fatalf("err=%v configs=%+v, want candidates bounded by the edited message", err, runner.configs)
	}
	if got := runner.configs[0].ReplacedTurnID; got != "replaced-turn" {
		t.Fatalf("replaced turn = %q, want the turn the edit replaces rewound before folding", got)
	}
	req.HistoryCutoffBeforeMessageID = "missing"
	if err := service.compactForBudget(t.Context(), req, 2000, 1000, ""); err == nil || len(runner.configs) != 1 {
		t.Fatalf("err=%v runs=%d, want an unknown cutoff to fail before compacting", err, len(runner.configs))
	}
}
