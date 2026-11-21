package application

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/turn"
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
	position messagepkg.HistoryPosition
}

func (s *cutoffMessageService) HistoryPositionByID(_ context.Context, _, messageID string) (messagepkg.HistoryPosition, error) {
	if messageID != "edited" {
		return messagepkg.HistoryPosition{}, errors.New("message not found")
	}
	return s.position, nil
}

func TestCompactForBudgetBoundsAnEditByItsCutoffAndReplacedTurn(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t)
	position := messagepkg.HistoryPosition{Turn: 7, Seq: 1}
	service.messageService = &cutoffMessageService{position: position}
	editTurn := int64(9)
	req := ChatRequest{
		BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID, HistoryCutoffBeforeMessageID: "edited",
		TurnPosition:    &editTurn,
		TurnReplacement: &messagepkg.TurnReplacement{OldTurnID: "replaced-turn", ReplacementTurnID: "edit-turn"},
	}

	if err := service.compactForBudget(t.Context(), req, 2000, 1000, ""); err != nil || len(runner.configs) != 1 ||
		runner.configs[0].CandidatesBefore == nil || *runner.configs[0].CandidatesBefore != position {
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

// A request's rows land at its admitted turn when its first step commits,
// after whatever other writers append meanwhile, so a live request folds only
// the history before that turn. A request admitted without a turn leaves the
// candidates open.
func TestCompactForBudgetBoundsALiveRequestByItsOwnTurn(t *testing.T) {
	t.Parallel()
	service, runner := newControllerPolicyService(t)
	service.messageService = &cutoffMessageService{}
	turnPosition := int64(12)
	req := ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID, TurnPosition: &turnPosition}

	if err := service.compactForBudget(t.Context(), req, 2000, 1000, ""); err != nil || len(runner.configs) != 1 ||
		runner.configs[0].CandidatesBefore == nil || *runner.configs[0].CandidatesBefore != (messagepkg.HistoryPosition{Turn: turnPosition}) {
		t.Fatalf("err=%v configs=%+v, want candidates before the request's own turn", err, runner.configs)
	}
	if got := runner.configs[0].ReplacedTurnID; got != "" {
		t.Fatalf("replaced turn = %q, want none for a live request", got)
	}
	req.TurnPosition = nil
	if err := service.compactForBudget(t.Context(), req, 2000, 1000, ""); err != nil || len(runner.configs) != 2 || runner.configs[1].CandidatesBefore != nil {
		t.Fatalf("err=%v configs=%+v, want open candidates without a turn", err, runner.configs)
	}
}

// A fold from rows streams its summaries through the client the runs stream
// through: a long summary is bounded by silence, not by a deadline on its
// first byte. A Discuss recovery keeps the batch it answers by the inputs-first
// policy; a continuation keeps its input and steers as capped anchors.
func TestCompactForBudgetStreamsAndKeepsByEntry(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t)
	service.messageService = &cutoffMessageService{}
	service.streamHTTPClient = &http.Client{}
	batch := []turn.ContextMessageSource{{Kind: "external", ID: "older"}, {Kind: "external", ID: "newest"}}
	anchors := []turn.ContextMessageSource{{Kind: "history", ID: "root"}}
	for _, req := range []ChatRequest{
		{discussCurrentSources: batch},
		{continuationToolCallID: "call", continuationAnchors: anchors},
	} {
		req.BotID, req.ChatID, req.ThreadID = syncCompactBotID, syncCompactBotID, syncCompactThreadID
		if err := service.compactForBudget(t.Context(), req, 2000, 1000, ""); err != nil {
			t.Fatal(err)
		}
	}
	discuss, continuation := runner.configs[0], runner.configs[1]
	if discuss.HTTPClient != service.streamHTTPClient || discuss.AnchorPolicy != compaction.AnchorsFirst || !reflect.DeepEqual(discuss.Anchors, batch) {
		t.Fatalf("discuss config = %+v, want the batch kept inputs first through the stream client", discuss)
	}
	if continuation.AnchorPolicy != compaction.AnchorsCapped || !reflect.DeepEqual(continuation.Anchors, anchors) {
		t.Fatalf("continuation config = %+v, want its anchors capped", continuation)
	}
}

func (*controllerQueries) UpgradeModelConfig(context.Context, sqlc.UpgradeModelConfigParams) (int64, error) {
	return 0, nil
}

func (*recordingCompactionRunner) Fold(context.Context, compaction.Event) ([]compaction.Pass, error) {
	return nil, errors.New("pre-turn recovery compacts history")
}
