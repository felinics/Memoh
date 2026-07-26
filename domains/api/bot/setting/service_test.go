package setting

import (
	"context"
	"errors"
	"testing"

	acpfeedback "github.com/memohai/memoh/domains/agent/decision/feedback"
	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
)

type modelReaderFake struct {
	exists      bool
	ids         []string
	existsInput string
	listInput   string
}

func (f *modelReaderFake) ModelExists(_ context.Context, id string) (bool, error) {
	f.existsInput = id
	return f.exists, nil
}

func (f *modelReaderFake) ListModelIDs(_ context.Context, modelID string) ([]string, error) {
	f.listInput = modelID
	return append([]string(nil), f.ids...), nil
}

func TestNormalizeBotSettingsReadRow_ShowToolCallsInIMDefault(t *testing.T) {
	t.Parallel()

	row := settingpersistence.Record{
		Language:            "en",
		ReasoningEnabled:    false,
		ReasoningEffort:     "medium",
		HeartbeatEnabled:    false,
		HeartbeatInterval:   60,
		CompactionEnabled:   false,
		CompactionThreshold: 0,
		CompactionRatio:     80,
		ShowToolCallsInIM:   false,
	}
	got := normalizeBotSettingsRecord(row)
	if got.ShowToolCallsInIM {
		t.Fatalf("expected default ShowToolCallsInIM=false, got true")
	}
}

func TestNormalizeBotSettingsReadRow_ShowToolCallsInIMPropagates(t *testing.T) {
	t.Parallel()

	row := settingpersistence.Record{
		Language:          "en",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
		CompactionRatio:   80,
		ShowToolCallsInIM: true,
	}
	got := normalizeBotSettingsRecord(row)
	if !got.ShowToolCallsInIM {
		t.Fatalf("expected ShowToolCallsInIM=true to propagate from row")
	}
}

func TestNormalizeBotSettingsReadRow_CommandUILanguage(t *testing.T) {
	t.Parallel()

	// Explicit value propagates from the read row.
	got := normalizeBotSettingsRecord(settingpersistence.Record{
		Language:          "en",
		CommandUILanguage: "zh",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
		CompactionRatio:   80,
	})
	if got.CommandUILanguage != "zh" {
		t.Fatalf("CommandUILanguage = %q, want zh", got.CommandUILanguage)
	}

	// Empty value defaults to "auto" (mirrors the DB column default).
	def := normalizeBotSettingsRecord(settingpersistence.Record{
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

	got := normalizeBotSettingsRecord(settingpersistence.Record{
		Language:           "en",
		ReasoningEffort:    "medium",
		HeartbeatInterval:  60,
		CompactionRatio:    80,
		ChatRuntime:        ChatRuntimeACPAgent,
		ChatACPAgentID:     "Codex",
		ChatACPProjectPath: "/data/app",
		ChatACPProjectMode: "project",
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

	def := normalizeBotSettingsRecord(settingpersistence.Record{
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

func TestResolveModelIDPreservesInternalUUID(t *testing.T) {
	t.Parallel()

	const modelID = "11111111-1111-1111-1111-111111111111"
	reader := &modelReaderFake{exists: true}
	service := NewService(nil, nil, reader, nil, nil)

	got, err := service.resolveModelID(t.Context(), modelID)
	if err != nil {
		t.Fatalf("resolveModelID() error = %v", err)
	}
	if got != modelID {
		t.Fatalf("resolveModelID() = %q, want %q", got, modelID)
	}
	if reader.existsInput != modelID || reader.listInput != "" {
		t.Fatalf("reader calls = exists(%q), list(%q)", reader.existsInput, reader.listInput)
	}
}

func TestResolveModelIDConvertsExternalModelIDThroughReader(t *testing.T) {
	t.Parallel()

	const persistedID = "22222222-2222-2222-2222-222222222222"
	reader := &modelReaderFake{ids: []string{persistedID}}
	service := NewService(nil, nil, reader, nil, nil)

	got, err := service.resolveModelID(t.Context(), "provider-model-slug")
	if err != nil {
		t.Fatalf("resolveModelID() error = %v", err)
	}
	if got != persistedID {
		t.Fatalf("resolveModelID() = %q, want %q", got, persistedID)
	}
	if reader.existsInput != "" || reader.listInput != "provider-model-slug" {
		t.Fatalf("reader calls = exists(%q), list(%q)", reader.existsInput, reader.listInput)
	}
}
