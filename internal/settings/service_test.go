package settings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	acpfeedback "github.com/memohai/memoh/internal/agent/decision/feedback"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/models"
)

type auxiliaryVisionSettingsQueries struct {
	dbstore.Queries
	model    sqlc.Model
	provider sqlc.Provider
}

type memoryClearSettingsQueries struct {
	dbstore.Queries
	upsert sqlc.UpsertBotSettingsParams
}

func (*memoryClearSettingsQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{
		Language:          "en",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
		CompactionRatio:   80,
	}, nil
}

func (*memoryClearSettingsQueries) GetBotOverlayConfig(context.Context, pgtype.UUID) (sqlc.GetBotOverlayConfigRow, error) {
	return sqlc.GetBotOverlayConfigRow{}, nil
}

func (*memoryClearSettingsQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{
		Language:          "en",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
		CompactionRatio:   80,
		MemoryProviderID: pgtype.UUID{
			Bytes: uuid.MustParse("22222222-2222-2222-2222-222222222222"),
			Valid: true,
		},
	}, nil
}

func (q *memoryClearSettingsQueries) UpsertBotSettings(_ context.Context, params sqlc.UpsertBotSettingsParams) (sqlc.UpsertBotSettingsRow, error) {
	q.upsert = params
	return sqlc.UpsertBotSettingsRow{
		Language:          params.Language,
		ReasoningEffort:   params.ReasoningEffort,
		HeartbeatInterval: params.HeartbeatInterval,
		CompactionRatio:   params.CompactionRatio,
		MemoryProviderID:  params.MemoryProviderID,
	}, nil
}

func (q *auxiliaryVisionSettingsQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return q.model, nil
}

func (q *auxiliaryVisionSettingsQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func TestNormalizeBotSettingsReadRow_ShowToolCallsInIMDefault(t *testing.T) {
	t.Parallel()

	row := sqlc.GetSettingsByBotIDRow{
		Language:            "en",
		ReasoningEnabled:    false,
		ReasoningEffort:     "medium",
		HeartbeatEnabled:    false,
		HeartbeatInterval:   60,
		CompactionEnabled:   false,
		CompactionThreshold: 0,
		CompactionRatio:     80,
		ShowToolCallsInIm:   false,
	}
	got := normalizeBotSettingsReadRow(row)
	if got.ShowToolCallsInIM {
		t.Fatalf("expected default ShowToolCallsInIM=false, got true")
	}
}

func TestUpsertBotEmptyMemoryProviderExplicitlyClearsSelection(t *testing.T) {
	t.Parallel()

	queries := &memoryClearSettingsQueries{}
	service := NewService(slog.Default(), queries, nil, nil)
	empty := ""
	if _, err := service.UpsertBot(t.Context(), "11111111-1111-1111-1111-111111111111", UpsertRequest{
		MemoryProviderID: &empty,
	}); err != nil {
		t.Fatalf("clear memory provider: %v", err)
	}
	if !queries.upsert.MemoryProviderIDSet {
		t.Fatal("empty memory_provider_id was treated as an omitted field")
	}
	if queries.upsert.MemoryProviderID.Valid {
		t.Fatalf("clear memory provider resolved to a valid UUID: %#v", queries.upsert.MemoryProviderID)
	}
}

func TestNormalizeBotSettingsReadRow_ShowToolCallsInIMPropagates(t *testing.T) {
	t.Parallel()

	row := sqlc.GetSettingsByBotIDRow{
		Language:          "en",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
		CompactionRatio:   80,
		ShowToolCallsInIm: true,
	}
	got := normalizeBotSettingsReadRow(row)
	if !got.ShowToolCallsInIM {
		t.Fatalf("expected ShowToolCallsInIM=true to propagate from row")
	}
}

func TestNormalizeBotSettingsReadRow_AuxiliaryVision(t *testing.T) {
	t.Parallel()

	defaults := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{
		Language:          "en",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
		CompactionRatio:   80,
	})
	if defaults.AuxiliaryVisionMode != AuxiliaryVisionInherit {
		t.Fatalf("default auxiliary vision mode = %q, want %q", defaults.AuxiliaryVisionMode, AuxiliaryVisionInherit)
	}
	if defaults.AuxiliaryVisionMaxRetries != InheritVisionMaxRetries {
		t.Fatalf("default auxiliary vision retries = %d, want %d", defaults.AuxiliaryVisionMaxRetries, InheritVisionMaxRetries)
	}
	if defaults.AuxiliaryVisionTimeoutSeconds != 0 {
		t.Fatalf("default auxiliary vision timeout = %d, want 0", defaults.AuxiliaryVisionTimeoutSeconds)
	}

	modelID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	got := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{
		Language:                      "en",
		ReasoningEffort:               "medium",
		HeartbeatInterval:             60,
		CompactionRatio:               80,
		AuxiliaryVisionMode:           " ENABLED ",
		AuxiliaryVisionModelID:        pgtype.UUID{Bytes: modelID, Valid: true},
		AuxiliaryVisionPrompt:         " describe this image ",
		AuxiliaryVisionMaxRetries:     pgtype.Int4{Int32: 3, Valid: true},
		AuxiliaryVisionTimeoutSeconds: pgtype.Int4{Int32: 45, Valid: true},
	})
	if got.AuxiliaryVisionMode != AuxiliaryVisionEnabled {
		t.Fatalf("auxiliary vision mode = %q, want %q", got.AuxiliaryVisionMode, AuxiliaryVisionEnabled)
	}
	if got.AuxiliaryVisionModelID != modelID.String() {
		t.Fatalf("auxiliary vision model = %q, want %q", got.AuxiliaryVisionModelID, modelID)
	}
	if got.AuxiliaryVisionPrompt != "describe this image" {
		t.Fatalf("auxiliary vision prompt = %q", got.AuxiliaryVisionPrompt)
	}
	if got.AuxiliaryVisionMaxRetries != 3 || got.AuxiliaryVisionTimeoutSeconds != 45 {
		t.Fatalf("auxiliary vision limits = retries %d, timeout %d", got.AuxiliaryVisionMaxRetries, got.AuxiliaryVisionTimeoutSeconds)
	}
}

func TestInvalidAuxiliaryVisionSettingUsesStableErrorCode(t *testing.T) {
	t.Parallel()

	err := invalidAuxiliaryVisionSetting("auxiliary_vision_timeout_seconds")
	if got := apperror.CodeOf(err); got != apperror.CodeSettingsAuxiliaryVisionInvalid {
		t.Fatalf("error code = %q, want %q", got, apperror.CodeSettingsAuxiliaryVisionInvalid)
	}
	if got := apperror.ArgsOf(err)["field"]; got != "auxiliary_vision_timeout_seconds" {
		t.Fatalf("error field = %q", got)
	}
}

func TestValidateAuxiliaryVisionModel(t *testing.T) {
	t.Parallel()

	providerID := pgtype.UUID{Bytes: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Valid: true}
	config, err := json.Marshal(models.ModelConfig{Compatibilities: []string{models.CompatVision}})
	if err != nil {
		t.Fatalf("marshal model config: %v", err)
	}
	queries := &auxiliaryVisionSettingsQueries{
		model: sqlc.Model{
			ProviderID: providerID,
			Type:       string(models.ModelTypeChat),
			Enable:     true,
			Config:     config,
		},
		provider: sqlc.Provider{Enable: true},
	}
	service := &Service{queries: queries}
	if err := service.validateAuxiliaryVisionModel(t.Context(), pgtype.UUID{}); err != nil {
		t.Fatalf("validate enabled vision model: %v", err)
	}

	queries.provider.Enable = false
	err = service.validateAuxiliaryVisionModel(t.Context(), pgtype.UUID{})
	if got := apperror.CodeOf(err); got != apperror.CodeSettingsAuxiliaryVisionInvalid {
		t.Fatalf("disabled provider error code = %q, want %q", got, apperror.CodeSettingsAuxiliaryVisionInvalid)
	}
}

func TestNormalizeBotSettingsReadRow_CommandUILanguage(t *testing.T) {
	t.Parallel()

	// Explicit value propagates from the read row.
	got := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{
		Language:          "en",
		CommandUiLanguage: "zh",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
		CompactionRatio:   80,
	})
	if got.CommandUILanguage != "zh" {
		t.Fatalf("CommandUILanguage = %q, want zh", got.CommandUILanguage)
	}

	// Empty value defaults to "auto" (mirrors the DB column default).
	def := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{
		Language:          "en",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
		CompactionRatio:   80,
	})
	if def.CommandUILanguage != DefaultCommandUILanguage {
		t.Fatalf("default CommandUILanguage = %q, want %q", def.CommandUILanguage, DefaultCommandUILanguage)
	}
}

func TestNormalizeBotSettingsReadRow_ChatRuntimeFields(t *testing.T) {
	t.Parallel()

	got := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{
		Language:           "en",
		ReasoningEffort:    "medium",
		HeartbeatInterval:  60,
		CompactionRatio:    80,
		ChatRuntime:        ChatRuntimeACPAgent,
		ChatAcpAgentID:     pgtype.Text{String: "Codex", Valid: true},
		ChatAcpProjectPath: "/data/app",
		ChatAcpProjectMode: "project",
	})
	if got.ChatRuntime != ChatRuntimeACPAgent {
		t.Fatalf("ChatRuntime = %q, want %q", got.ChatRuntime, ChatRuntimeACPAgent)
	}
	if got.ChatACPAgentID != "codex" {
		t.Fatalf("ChatACPAgentID = %q, want codex", got.ChatACPAgentID)
	}
	if got.ChatACPProjectPath != "/data/app" {
		t.Fatalf("ChatACPProjectPath = %q, want /data/app", got.ChatACPProjectPath)
	}

	def := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{
		Language:          "en",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
		CompactionRatio:   80,
	})
	if def.ChatRuntime != ChatRuntimeModel || def.ChatACPProjectPath != DefaultACPProjectPath || def.ChatACPProjectMode != DefaultACPProjectMode {
		t.Fatalf("default chat runtime fields = %#v", def)
	}
}

func TestValidateChatRuntimeSettings(t *testing.T) {
	t.Parallel()

	metadata := []byte(`{"acp":{"agents":{"codex":{"enabled":true,"setup_mode":"api_key","managed":{"api_key":"sk-test"}}}}}`)
	valid := Settings{
		ChatModelID:        "11111111-1111-1111-1111-111111111111",
		ChatRuntime:        ChatRuntimeACPAgent,
		ChatACPAgentID:     "codex",
		ChatACPProjectPath: DefaultACPProjectPath,
		ChatACPProjectMode: DefaultACPProjectMode,
	}
	if err := validateChatRuntimeSettings(metadata, valid); err != nil {
		t.Fatalf("validateChatRuntimeSettings(valid) error = %v", err)
	}

	noModel := valid
	noModel.ChatModelID = ""
	if err := validateChatRuntimeSettings(metadata, noModel); err != nil {
		t.Fatalf("validateChatRuntimeSettings without chat model error = %v, want nil", err)
	}

	disabled := valid
	if err := validateChatRuntimeSettings([]byte(`{"acp":{"agents":{"codex":{"enabled":false}}}}`), disabled); feedbackCode(err) != acpfeedback.CodeAgentNotEnabled {
		t.Fatalf("validateChatRuntimeSettings disabled agent code = %q, want %q", feedbackCode(err), acpfeedback.CodeAgentNotEnabled)
	}

	missingKey := valid
	if err := validateChatRuntimeSettings([]byte(`{"acp":{"agents":{"codex":{"enabled":true,"setup_mode":"api_key","managed":{}}}}}`), missingKey); feedbackCode(err) != acpfeedback.CodeAgentNotConfigured {
		t.Fatalf("validateChatRuntimeSettings missing api key code = %q, want %q", feedbackCode(err), acpfeedback.CodeAgentNotConfigured)
	}
}

func feedbackCode(err error) string {
	var feedback *acpfeedback.Error
	if errors.As(err, &feedback) {
		return feedback.Code
	}
	return ""
}

func TestUpsertRequestShowToolCallsInIM_PointerSemantics(t *testing.T) {
	t.Parallel()

	// When the field is nil, the UpsertRequest should not touch the current
	// setting. When non-nil, the dereferenced value should win. We exercise
	// the small gate block without hitting the database.
	current := Settings{ShowToolCallsInIM: true}

	var req UpsertRequest
	if req.ShowToolCallsInIM != nil {
		current.ShowToolCallsInIM = *req.ShowToolCallsInIM
	}
	if !current.ShowToolCallsInIM {
		t.Fatalf("nil pointer must leave current value unchanged")
	}

	off := false
	req.ShowToolCallsInIM = &off
	if req.ShowToolCallsInIM != nil {
		current.ShowToolCallsInIM = *req.ShowToolCallsInIM
	}
	if current.ShowToolCallsInIM {
		t.Fatalf("explicit false pointer must clear the flag")
	}
}

func TestNormalizeBotSettingDefaultHeartbeatInterval(t *testing.T) {
	t.Parallel()

	got := normalizeBotSetting("en", "auto", "allow", false, "medium", false, 0, false, 0, 80)
	if got.HeartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("heartbeat interval = %d, want %d", got.HeartbeatInterval, DefaultHeartbeatInterval)
	}
	if got.HeartbeatInterval != 1440 {
		t.Fatalf("heartbeat interval = %d, want 1440", got.HeartbeatInterval)
	}
}

func TestReasoningEffortAllowsFullModelLadder(t *testing.T) {
	t.Parallel()

	for _, effort := range []string{"none", "low", "medium", "high", "xhigh"} {
		if !isValidReasoningEffort(effort) {
			t.Fatalf("isValidReasoningEffort(%q) = false, want true", effort)
		}
		got := normalizeBotSetting("en", "auto", "allow", true, effort, false, 60, false, 0, 80)
		if got.ReasoningEffort != effort {
			t.Fatalf("normalizeBotSetting effort = %q, want %q", got.ReasoningEffort, effort)
		}
	}
}
