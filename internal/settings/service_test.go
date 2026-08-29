package settings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	acpfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/reasoning"
)

type stubReasoningOptionsResolver struct {
	opts    reasoning.Options
	err     error
	calls   int
	modelID string
}

func (r *stubReasoningOptionsResolver) ResolveReasoningOptions(_ context.Context, modelID string) (reasoning.Options, error) {
	r.calls++
	r.modelID = modelID
	return r.opts, r.err
}

func TestApplyReasoningPolicy(t *testing.T) {
	t.Parallel()

	modelID := "00000000-0000-0000-0000-000000000701"
	supported := reasoning.Options{
		Supported:     true,
		CanDisable:    false,
		Efforts:       []string{reasoning.EffortLow, reasoning.EffortMedium, reasoning.EffortHigh},
		DefaultEffort: reasoning.EffortMedium,
	}

	t.Run("explicit advertised tier is canonicalized", func(t *testing.T) {
		resolver := &stubReasoningOptionsResolver{opts: supported}
		service := &Service{reasoningResolver: resolver}
		current := Settings{ChatModelID: modelID, ReasoningEffort: reasoning.EffortLow}
		requested := " HIGH "
		if err := service.applyReasoningPolicy(context.Background(), &current, UpsertRequest{ReasoningEffort: &requested}); err != nil {
			t.Fatal(err)
		}
		if current.ReasoningEffort != reasoning.EffortHigh {
			t.Fatalf("reasoning effort = %q, want high", current.ReasoningEffort)
		}
	})

	t.Run("explicit off is rejected for always-on model", func(t *testing.T) {
		resolver := &stubReasoningOptionsResolver{opts: supported}
		service := &Service{reasoningResolver: resolver}
		current := Settings{ChatModelID: modelID, ReasoningEffort: reasoning.EffortHigh}
		requested := "off"
		err := service.applyReasoningPolicy(context.Background(), &current, UpsertRequest{ReasoningEffort: &requested})
		var invalid *InvalidReasoningEffortError
		if !errors.As(err, &invalid) || invalid.Effort != reasoning.EffortDisable {
			t.Fatalf("error = %#v, want canonical invalid disable error", err)
		}
		if current.ReasoningEffort != reasoning.EffortHigh {
			t.Fatalf("rejected write changed effort to %q", current.ReasoningEffort)
		}
	})

	t.Run("model switch reconciles stale tier", func(t *testing.T) {
		resolver := &stubReasoningOptionsResolver{opts: supported}
		service := &Service{reasoningResolver: resolver}
		current := Settings{ChatModelID: modelID, ReasoningEffort: reasoning.EffortXHigh}
		newModelID := modelID
		if err := service.applyReasoningPolicy(context.Background(), &current, UpsertRequest{ChatModelID: &newModelID}); err != nil {
			t.Fatal(err)
		}
		if current.ReasoningEffort != reasoning.EffortMedium {
			t.Fatalf("reasoning effort = %q, want model default medium", current.ReasoningEffort)
		}
	})

	t.Run("combined model payload reconciles stale explicit tier", func(t *testing.T) {
		resolver := &stubReasoningOptionsResolver{opts: supported}
		service := &Service{reasoningResolver: resolver}
		current := Settings{ChatModelID: modelID, ReasoningEffort: reasoning.EffortLow}
		newModelID := modelID
		requested := reasoning.EffortXHigh
		if err := service.applyReasoningPolicy(context.Background(), &current, UpsertRequest{
			ChatModelID:     &newModelID,
			ReasoningEffort: &requested,
		}); err != nil {
			t.Fatal(err)
		}
		if current.ReasoningEffort != reasoning.EffortMedium {
			t.Fatalf("reasoning effort = %q, want model default medium", current.ReasoningEffort)
		}
	})

	t.Run("unsupported model preserves dormant preference", func(t *testing.T) {
		resolver := &stubReasoningOptionsResolver{}
		service := &Service{reasoningResolver: resolver}
		current := Settings{ChatModelID: modelID, ReasoningEffort: reasoning.EffortXHigh}
		newModelID := modelID
		if err := service.applyReasoningPolicy(context.Background(), &current, UpsertRequest{ChatModelID: &newModelID}); err != nil {
			t.Fatal(err)
		}
		if current.ReasoningEffort != reasoning.EffortXHigh {
			t.Fatalf("dormant preference = %q, want xhigh", current.ReasoningEffort)
		}
	})

	t.Run("always-on model preserves dormant preference", func(t *testing.T) {
		resolver := &stubReasoningOptionsResolver{opts: reasoning.Options{Supported: true}}
		service := &Service{reasoningResolver: resolver}
		current := Settings{ChatModelID: modelID, ReasoningEffort: reasoning.EffortXHigh}
		newModelID := modelID
		if err := service.applyReasoningPolicy(context.Background(), &current, UpsertRequest{ChatModelID: &newModelID}); err != nil {
			t.Fatal(err)
		}
		if current.ReasoningEffort != reasoning.EffortXHigh {
			t.Fatalf("dormant preference = %q, want xhigh", current.ReasoningEffort)
		}
	})

	t.Run("always-on model rejects an explicit tier", func(t *testing.T) {
		resolver := &stubReasoningOptionsResolver{opts: reasoning.Options{Supported: true}}
		service := &Service{reasoningResolver: resolver}
		current := Settings{ChatModelID: modelID, ReasoningEffort: reasoning.EffortHigh}
		requested := reasoning.EffortLow
		err := service.applyReasoningPolicy(context.Background(), &current, UpsertRequest{ReasoningEffort: &requested})
		var invalid *InvalidReasoningEffortError
		if !errors.As(err, &invalid) || invalid.Effort != reasoning.EffortLow {
			t.Fatalf("error = %#v, want invalid low error", err)
		}
		if current.ReasoningEffort != reasoning.EffortHigh {
			t.Fatalf("rejected write changed effort to %q", current.ReasoningEffort)
		}
	})

	t.Run("lookup failure fails closed", func(t *testing.T) {
		resolver := &stubReasoningOptionsResolver{err: errors.New("SECRET provider failure")}
		service := &Service{reasoningResolver: resolver}
		current := Settings{ChatModelID: modelID, ReasoningEffort: reasoning.EffortLow}
		requested := reasoning.EffortHigh
		err := service.applyReasoningPolicy(context.Background(), &current, UpsertRequest{ReasoningEffort: &requested})
		if !errors.Is(err, ErrReasoningOptionsUnavailable) {
			t.Fatalf("error = %v, want ErrReasoningOptionsUnavailable", err)
		}
	})

	t.Run("unrelated write skips capability lookup", func(t *testing.T) {
		resolver := &stubReasoningOptionsResolver{err: errors.New("must not be called")}
		service := &Service{reasoningResolver: resolver}
		current := Settings{ChatModelID: modelID, ReasoningEffort: reasoning.EffortLow}
		language := "zh"
		if err := service.applyReasoningPolicy(context.Background(), &current, UpsertRequest{Language: &language}); err != nil {
			t.Fatal(err)
		}
		if resolver.calls != 0 {
			t.Fatalf("resolver called %d time(s)", resolver.calls)
		}
	})
}

type reasoningPolicyQueries struct {
	dbstore.Queries
	botID          pgtype.UUID
	currentModelID pgtype.UUID
	defaultAgentID pgtype.UUID
	storedEffort   string
	upsertCalls    int
	lastUpsert     sqlc.UpsertBotSettingsParams
	transactions   bool
	botAgent       sqlc.BotAgent
	events         []string
}

func (q *reasoningPolicyQueries) SupportsTransactions() bool { return q.transactions }

func (q *reasoningPolicyQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	q.events = append(q.events, "transaction")
	return fn(q)
}

func (q *reasoningPolicyQueries) LockBotForAgentMutation(context.Context, pgtype.UUID) (pgtype.UUID, error) {
	q.events = append(q.events, "lock-bot")
	return q.botID, nil
}

func (q *reasoningPolicyQueries) GetBotAgentByID(context.Context, sqlc.GetBotAgentByIDParams) (sqlc.BotAgent, error) {
	q.events = append(q.events, "get-agent")
	return q.botAgent, nil
}

func (q *reasoningPolicyQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{
		ID:              q.botID,
		Language:        DefaultLanguage,
		ReasoningEffort: q.storedEffort,
		ChatModelID:     q.currentModelID,
	}, nil
}

func (*reasoningPolicyQueries) GetBotOverlayConfig(context.Context, pgtype.UUID) (sqlc.GetBotOverlayConfigRow, error) {
	return sqlc.GetBotOverlayConfigRow{}, nil
}

func (q *reasoningPolicyQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{
		BotID:                  q.botID,
		Language:               DefaultLanguage,
		CommandUiLanguage:      DefaultCommandUILanguage,
		ReasoningEffort:        q.storedEffort,
		ChatModelID:            q.currentModelID,
		DefaultBotAgentID:      q.defaultAgentID,
		ChatRuntime:            ChatRuntimeModel,
		ChatAcpProjectPath:     DefaultACPProjectPath,
		ChatAcpProjectMode:     DefaultACPProjectMode,
		ToolApprovalConfig:     []byte(`{}`),
		CompactionThreshold:    0,
		CompactionEnabled:      false,
		PersistFullToolResults: false,
	}, nil
}

func (*reasoningPolicyQueries) GetModelByID(_ context.Context, id pgtype.UUID) (sqlc.Model, error) {
	return sqlc.Model{ID: id}, nil
}

func (q *reasoningPolicyQueries) UpsertBotSettings(_ context.Context, arg sqlc.UpsertBotSettingsParams) (sqlc.UpsertBotSettingsRow, error) {
	q.events = append(q.events, "upsert-settings")
	q.upsertCalls++
	q.lastUpsert = arg
	modelID := q.currentModelID
	if arg.ChatModelIDSet {
		modelID = arg.ChatModelID
	}
	defaultAgentID := q.defaultAgentID
	if arg.DefaultBotAgentIDSet {
		defaultAgentID = arg.DefaultBotAgentID
	}
	return sqlc.UpsertBotSettingsRow{
		BotID:               q.botID,
		Language:            arg.Language,
		CommandUiLanguage:   arg.CommandUiLanguage,
		ReasoningEffort:     arg.ReasoningEffort,
		CompactionEnabled:   arg.CompactionEnabled,
		CompactionThreshold: arg.CompactionThreshold,
		ChatModelID:         modelID,
		DefaultBotAgentID:   defaultAgentID,
		ChatRuntime:         arg.ChatRuntime,
		ChatAcpAgentID:      arg.ChatAcpAgentID,
		ChatAcpProjectPath:  arg.ChatAcpProjectPath,
		ChatAcpProjectMode:  arg.ChatAcpProjectMode,
		ToolApprovalConfig:  arg.ToolApprovalConfig,
		OverlayConfig:       arg.OverlayConfig,
	}, nil
}

func TestUpsertBotSettingsRechecksDefaultAgentAfterBotLock(t *testing.T) {
	botID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000740"), Valid: true}
	agentID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000741"), Valid: true}
	params := sqlc.UpsertBotSettingsParams{
		ID:                   botID,
		DefaultBotAgentID:    agentID,
		DefaultBotAgentIDSet: true,
	}

	t.Run("active agent is assigned", func(t *testing.T) {
		queries := &reasoningPolicyQueries{
			botID:        botID,
			transactions: true,
			botAgent:     sqlc.BotAgent{ID: agentID, BotID: botID, Enabled: true},
		}
		service := &Service{queries: queries}
		if _, err := service.upsertBotSettings(context.Background(), params); err != nil {
			t.Fatalf("upsertBotSettings() error = %v", err)
		}
		assertSettingsEvents(t, queries.events, []string{"transaction", "lock-bot", "get-agent", "upsert-settings"})
	})

	t.Run("agent disabled before lock release is rejected", func(t *testing.T) {
		queries := &reasoningPolicyQueries{
			botID:        botID,
			transactions: true,
			botAgent:     sqlc.BotAgent{ID: agentID, BotID: botID, Enabled: false},
		}
		service := &Service{queries: queries}
		if _, err := service.upsertBotSettings(context.Background(), params); !errors.Is(err, botagents.ErrUnavailable) {
			t.Fatalf("upsertBotSettings() error = %v, want %v", err, botagents.ErrUnavailable)
		}
		assertSettingsEvents(t, queries.events, []string{"transaction", "lock-bot", "get-agent"})
	})
}

func assertSettingsEvents(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestUpsertBotLegacyNativeRuntimeClearsDefaultAgent(t *testing.T) {
	t.Parallel()

	botID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000730"), Valid: true}
	agentID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000731"), Valid: true}
	queries := &reasoningPolicyQueries{
		botID:          botID,
		defaultAgentID: agentID,
	}
	service := NewService(slog.Default(), queries, nil, nil)
	runtime := ChatRuntimeModel

	got, err := service.UpsertBot(context.Background(), uuid.UUID(botID.Bytes).String(), UpsertRequest{
		ChatRuntime: &runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !queries.lastUpsert.DefaultBotAgentIDSet {
		t.Fatal("DefaultBotAgentIDSet = false, want true for legacy Native request")
	}
	if queries.lastUpsert.DefaultBotAgentID.Valid {
		t.Fatalf("DefaultBotAgentID = %#v, want NULL", queries.lastUpsert.DefaultBotAgentID)
	}
	if got.DefaultBotAgentID != "" || got.ChatRuntime != ChatRuntimeModel {
		t.Fatalf("settings = %#v, want Native without default Agent", got)
	}
}

func TestUpsertBotUnrelatedWritePreservesDefaultAgentWithoutRevalidation(t *testing.T) {
	t.Parallel()

	botID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000732"), Valid: true}
	agentID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000733"), Valid: true}
	queries := &reasoningPolicyQueries{
		botID:          botID,
		defaultAgentID: agentID,
	}
	// No Bot Agent service is installed on purpose: an unrelated write must not
	// look up or validate the already persisted default Agent.
	service := NewService(slog.Default(), queries, nil, nil)
	language := "zh"

	got, err := service.UpsertBot(context.Background(), uuid.UUID(botID.Bytes).String(), UpsertRequest{
		Language: &language,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queries.lastUpsert.DefaultBotAgentIDSet {
		t.Fatal("DefaultBotAgentIDSet = true, want existing binding preserved by partial update")
	}
	if got.DefaultBotAgentID != uuid.UUID(agentID.Bytes).String() {
		t.Fatalf("DefaultBotAgentID = %q, want %q", got.DefaultBotAgentID, uuid.UUID(agentID.Bytes).String())
	}
}

func TestUpsertBotReconcilesReasoningOnModelChange(t *testing.T) {
	t.Parallel()

	botID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000710"), Valid: true}
	oldModelID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000711"), Valid: true}
	newModelID := uuid.MustParse("00000000-0000-0000-0000-000000000712")
	queries := &reasoningPolicyQueries{
		botID:          botID,
		currentModelID: oldModelID,
		storedEffort:   reasoning.EffortXHigh,
	}
	resolver := &stubReasoningOptionsResolver{opts: reasoning.Options{
		Supported:     true,
		Efforts:       []string{reasoning.EffortLow, reasoning.EffortMedium, reasoning.EffortHigh},
		DefaultEffort: reasoning.EffortMedium,
	}}
	service := NewService(slog.Default(), queries, nil, nil)
	service.SetReasoningOptionsResolver(resolver)
	newModelIDString := newModelID.String()

	if _, err := service.UpsertBot(context.Background(), uuid.UUID(botID.Bytes).String(), UpsertRequest{ChatModelID: &newModelIDString}); err != nil {
		t.Fatal(err)
	}
	if queries.upsertCalls != 1 {
		t.Fatalf("UpsertBotSettings calls = %d, want 1", queries.upsertCalls)
	}
	if queries.lastUpsert.ReasoningEffort != reasoning.EffortMedium {
		t.Fatalf("persisted reasoning effort = %q, want medium", queries.lastUpsert.ReasoningEffort)
	}
	if resolver.modelID != newModelIDString {
		t.Fatalf("resolved model = %q, want %q", resolver.modelID, newModelIDString)
	}
}

func TestUpsertBotRejectsInvalidReasoningBeforePersistence(t *testing.T) {
	t.Parallel()

	botID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000720"), Valid: true}
	modelID := pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000721"), Valid: true}
	queries := &reasoningPolicyQueries{
		botID:          botID,
		currentModelID: modelID,
		storedEffort:   reasoning.EffortHigh,
	}
	service := NewService(slog.Default(), queries, nil, nil)
	service.SetReasoningOptionsResolver(&stubReasoningOptionsResolver{opts: reasoning.Options{
		Supported:     true,
		CanDisable:    false,
		Efforts:       []string{reasoning.EffortLow, reasoning.EffortHigh},
		DefaultEffort: reasoning.EffortLow,
	}})
	requested := "off"

	_, err := service.UpsertBot(context.Background(), uuid.UUID(botID.Bytes).String(), UpsertRequest{ReasoningEffort: &requested})
	var invalid *InvalidReasoningEffortError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want InvalidReasoningEffortError", err)
	}
	if queries.upsertCalls != 0 {
		t.Fatalf("invalid effort reached persistence %d time(s)", queries.upsertCalls)
	}
}

func TestNormalizeBotSettingsReadRow_ShowToolCallsInIMDefault(t *testing.T) {
	t.Parallel()

	row := sqlc.GetSettingsByBotIDRow{
		Language:            "en",
		ReasoningEffort:     "medium",
		CompactionEnabled:   false,
		CompactionThreshold: 0,
		ShowToolCallsInIm:   false,
	}
	got := normalizeBotSettingsReadRow(row)
	if got.ShowToolCallsInIM {
		t.Fatalf("expected default ShowToolCallsInIM=false, got true")
	}
}

func TestNormalizeBotSettingsReadRow_ShowToolCallsInIMPropagates(t *testing.T) {
	t.Parallel()

	row := sqlc.GetSettingsByBotIDRow{
		Language:          "en",
		ReasoningEffort:   "medium",
		ShowToolCallsInIm: true,
	}
	got := normalizeBotSettingsReadRow(row)
	if !got.ShowToolCallsInIM {
		t.Fatalf("expected ShowToolCallsInIM=true to propagate from row")
	}
}

func TestNormalizeBotSettingsReadRow_CommandUILanguage(t *testing.T) {
	t.Parallel()

	// Explicit value propagates from the read row.
	got := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{
		Language:          "en",
		CommandUiLanguage: "zh",
		ReasoningEffort:   "medium",
	})
	if got.CommandUILanguage != "zh" {
		t.Fatalf("CommandUILanguage = %q, want zh", got.CommandUILanguage)
	}

	// Empty value defaults to "auto" (mirrors the DB column default).
	def := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{
		Language:        "en",
		ReasoningEffort: "medium",
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
		Language:        "en",
		ReasoningEffort: "medium",
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

func TestUpsertRequestClearableFields_JSONSemantics(t *testing.T) {
	t.Parallel()

	// The autosaving web client relies on this contract: an omitted key must
	// decode to nil (keep current value), while an explicit empty string must
	// decode to a non-nil pointer (clear the reference).
	var omitted UpsertRequest
	if err := json.Unmarshal([]byte(`{"reasoning_effort":"low"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	for name, ptr := range map[string]*string{
		"chat_model_id": omitted.ChatModelID, "image_model_id": omitted.ImageModelID,
		"search_provider_id": omitted.SearchProviderID, "memory_provider_id": omitted.MemoryProviderID,
		"tts_model_id": omitted.TtsModelID, "transcription_model_id": omitted.TranscriptionModelID,
		"video_model_id": omitted.VideoModelID, "language": omitted.Language,
	} {
		if ptr != nil {
			t.Fatalf("%s: omitted key must stay nil, got %q", name, *ptr)
		}
	}

	var cleared UpsertRequest
	if err := json.Unmarshal([]byte(`{"chat_model_id":"","search_provider_id":"","memory_provider_id":"","language":""}`), &cleared); err != nil {
		t.Fatal(err)
	}
	for name, ptr := range map[string]*string{
		"chat_model_id": cleared.ChatModelID, "search_provider_id": cleared.SearchProviderID,
		"memory_provider_id": cleared.MemoryProviderID, "language": cleared.Language,
	} {
		if ptr == nil || *ptr != "" {
			t.Fatalf("%s: explicit empty string must decode to a non-nil empty pointer", name)
		}
	}
}

func TestNormalizeCompactionTargetPercent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value pgtype.Int4
		want  *int
	}{
		{name: "null uses controller default"},
		{name: "minimum is valid", value: pgtype.Int4{Int32: 1, Valid: true}, want: settingsIntPointer(1)},
		{name: "default override stays explicit", value: pgtype.Int4{Int32: 40, Valid: true}, want: settingsIntPointer(40)},
		{name: "maximum is valid", value: pgtype.Int4{Int32: 99, Valid: true}, want: settingsIntPointer(99)},
		{name: "zero normalizes to null", value: pgtype.Int4{Valid: true}},
		{name: "one hundred normalizes to null", value: pgtype.Int4{Int32: 100, Valid: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := normalizeCompactionTargetPercent(tc.value)
			if !equalOptionalInt(got, tc.want) {
				t.Fatalf("normalizeCompactionTargetPercent(%v) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestApplyCompactionTargetPercentOverride(t *testing.T) {
	t.Parallel()

	current := settingsIntPointer(55)
	got, set := applyCompactionTargetPercentOverride(current, nil)
	if set || !equalOptionalInt(got, current) {
		t.Fatalf("omitted override = (%v, %t), want preserved %v and set=false", got, set, current)
	}

	got, set = applyCompactionTargetPercentOverride(current, settingsIntPointer(30))
	if !set || !equalOptionalInt(got, settingsIntPointer(30)) {
		t.Fatalf("valid override = (%v, %t), want 30 and set=true", got, set)
	}

	got, set = applyCompactionTargetPercentOverride(current, settingsIntPointer(0))
	if !set || got != nil {
		t.Fatalf("clear sentinel = (%v, %t), want nil and set=true", got, set)
	}

	if got := nullableCompactionTargetPercent(settingsIntPointer(30)); !got.Valid || got.Int32 != 30 {
		t.Fatalf("nullableCompactionTargetPercent(30) = %v, want valid 30", got)
	}
	if got := nullableCompactionTargetPercent(nil); got.Valid {
		t.Fatalf("nullableCompactionTargetPercent(nil) = %v, want null", got)
	}
}

func settingsIntPointer(value int) *int {
	return &value
}

func equalOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func TestReasoningEffortAllowsFullModelLadder(t *testing.T) {
	t.Parallel()

	for _, effort := range []string{"none", "low", "medium", "high", "xhigh"} {
		if !hasReasoningEffortValue(effort) {
			t.Fatalf("hasReasoningEffortValue(%q) = false, want true", effort)
		}
		got := normalizeBotSetting("en", "auto", "allow", effort, false, 0, pgtype.Int4{})
		if got.ReasoningEffort != effort {
			t.Fatalf("normalizeBotSetting effort = %q, want %q", got.ReasoningEffort, effort)
		}
	}
}
