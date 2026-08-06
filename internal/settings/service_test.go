package settings

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	acpfeedback "github.com/memohai/memoh/internal/agent/decision/feedback"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
)

func TestNormalizeBotSettingsReadRow_ShowToolCallsInIMDefault(t *testing.T) {
	t.Parallel()

	row := sqlc.GetSettingsByBotIDRow{
		Language:            "en",
		ReasoningEffort:     "medium",
		HeartbeatEnabled:    false,
		HeartbeatInterval:   60,
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
		HeartbeatInterval: 60,
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
		HeartbeatInterval: 60,
	})
	if got.CommandUILanguage != "zh" {
		t.Fatalf("CommandUILanguage = %q, want zh", got.CommandUILanguage)
	}

	// Empty value defaults to "auto" (mirrors the DB column default).
	def := normalizeBotSettingsReadRow(sqlc.GetSettingsByBotIDRow{
		Language:          "en",
		ReasoningEffort:   "medium",
		HeartbeatInterval: 60,
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
		"heartbeat_model_id": omitted.HeartbeatModelID,
	} {
		if ptr != nil {
			t.Fatalf("%s: omitted key must stay nil, got %q", name, *ptr)
		}
	}

	var cleared UpsertRequest
	if err := json.Unmarshal([]byte(`{"chat_model_id":"","search_provider_id":"","memory_provider_id":"","language":"","heartbeat_model_id":""}`), &cleared); err != nil {
		t.Fatal(err)
	}
	for name, ptr := range map[string]*string{
		"chat_model_id": cleared.ChatModelID, "search_provider_id": cleared.SearchProviderID,
		"memory_provider_id": cleared.MemoryProviderID, "language": cleared.Language,
		"heartbeat_model_id": cleared.HeartbeatModelID,
	} {
		if ptr == nil || *ptr != "" {
			t.Fatalf("%s: explicit empty string must decode to a non-nil empty pointer", name)
		}
	}
}

func TestNormalizeBotSettingDefaultHeartbeatInterval(t *testing.T) {
	t.Parallel()

	got := normalizeBotSetting("en", "auto", "allow", "medium", false, 0, false, 0, pgtype.Int4{})
	if got.HeartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("heartbeat interval = %d, want %d", got.HeartbeatInterval, DefaultHeartbeatInterval)
	}
	if got.HeartbeatInterval != 1440 {
		t.Fatalf("heartbeat interval = %d, want 1440", got.HeartbeatInterval)
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
		if !isValidReasoningEffort(effort) {
			t.Fatalf("isValidReasoningEffort(%q) = false, want true", effort)
		}
		got := normalizeBotSetting("en", "auto", "allow", effort, false, 60, false, 0, pgtype.Int4{})
		if got.ReasoningEffort != effort {
			t.Fatalf("normalizeBotSetting effort = %q, want %q", got.ReasoningEffort, effort)
		}
	}
}
