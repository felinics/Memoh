package historyfrag

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func TestFromDBMessageBuildsDurableRecordScopeAndFrag(t *testing.T) {
	t.Parallel()

	inputTokens := 12
	outputTokens := 34
	msg := messagepkg.Message{
		ID:                      "row-1",
		BotID:                   "bot-1",
		SessionID:               "sess-1",
		SenderChannelIdentityID: "sender-1",
		SenderUserID:            "user-1",
		SenderDisplayName:       "Alice",
		Platform:                "telegram",
		ExternalMessageID:       "msg-1",
		SourceReplyToMessageID:  "msg-0",
		Role:                    "user",
		Content:                 persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("hello")}),
		Metadata:                map[string]any{"reply": map[string]any{"sender": "Bob"}},
		Usage:                   mustJSON(t, map[string]int{"inputTokens": inputTokens, "outputTokens": outputTokens}),
		Assets: []messagepkg.MessageAsset{{
			ContentHash: "asset-hash-1",
			Role:        "attachment",
			Ordinal:     2,
			Mime:        "image/png",
			SizeBytes:   1234,
			StorageKey:  "objects/asset-hash-1",
			Name:        "image.png",
			Metadata:    map[string]any{"width": float64(640)},
		}},
		CompactID:      "compact-1",
		EventID:        "evt-1",
		DisplayContent: "hello",
		CreatedAt:      time.Date(2026, 6, 24, 3, 0, 0, 0, time.UTC),
	}

	record, err := FromDBMessage(msg, ScopeFallback{
		ChatID:           "chat-1",
		ConversationType: "group",
		ConversationName: "Dev Chat",
		ReplyTarget:      "target-1",
	})
	if err != nil {
		t.Fatalf("FromDBMessage failed: %v", err)
	}

	if record.Ref.Namespace != "bot_history_message" || record.Ref.ID != "row-1" || record.Ref.Schema != contextfrag.SchemaContextRef {
		t.Fatalf("unexpected record ref: %#v", record.Ref)
	}
	if record.SourceKind != SourceDBMessage || record.Lifecycle != LifecyclePersisted {
		t.Fatalf("unexpected source/lifecycle: %s %s", record.SourceKind, record.Lifecycle)
	}
	if record.DBMessageID != "row-1" ||
		record.ExternalMessageID != "msg-1" ||
		record.EventID != "evt-1" ||
		record.SessionID != "sess-1" ||
		record.BotID != "bot-1" ||
		record.SenderChannelIdentityID != "sender-1" ||
		record.SenderUserID != "user-1" ||
		record.SenderDisplayName != "Alice" ||
		record.Platform != "telegram" ||
		record.SourceReplyToMessageID != "msg-0" ||
		record.CompactID != "compact-1" {
		t.Fatalf("record lost DB provenance: %#v", record)
	}
	if reply, _ := record.Metadata["reply"].(map[string]any); reply["sender"] != "Bob" {
		t.Fatalf("record lost message metadata: %#v", record.Metadata)
	}
	if record.UsageInputTokens == nil || *record.UsageInputTokens != inputTokens {
		t.Fatalf("UsageInputTokens = %#v, want %d", record.UsageInputTokens, inputTokens)
	}
	if record.UsageOutputTokens == nil || *record.UsageOutputTokens != outputTokens {
		t.Fatalf("UsageOutputTokens = %#v, want %d", record.UsageOutputTokens, outputTokens)
	}
	if len(record.Assets) != 1 ||
		record.Assets[0].ContentHash != "asset-hash-1" ||
		record.Assets[0].Role != "attachment" ||
		record.Assets[0].Ordinal != 2 ||
		record.Assets[0].Name != "image.png" ||
		record.Assets[0].Metadata["width"] != float64(640) {
		t.Fatalf("record lost media refs: %#v", record.Assets)
	}

	frag := ToFrag(record)
	if err := contextfrag.ValidateContextRef(frag.Ref); err != nil {
		t.Fatalf("frag ref invalid: %#v: %v", frag.Ref, err)
	}
	if frag.Ref.Namespace != "bot_history_message" || frag.Ref.ID != "row-1" || frag.Ref.Durability != contextfrag.RefDurable {
		t.Fatalf("frag ref should be durable DB row identity: %#v", frag.Ref)
	}
	if frag.Ref.ContentHash == "" || frag.Ref.HashAlgo != contextfrag.HashAlgoSHA256 || frag.Ref.HashScope != contextfrag.HashScopeSourcePayload {
		t.Fatalf("frag ref missing source payload hash: %#v", frag.Ref)
	}
	if frag.Kind != contextfrag.KindConversationEvent || frag.Slot != contextfrag.SlotHistory {
		t.Fatalf("unexpected frag kind/slot: %s %s", frag.Kind, frag.Slot)
	}
	if frag.Scope.BotID != "bot-1" ||
		frag.Scope.ChatID != "chat-1" ||
		frag.Scope.SessionID != "sess-1" ||
		frag.Scope.ChannelIdentityID != "sender-1" ||
		frag.Scope.DisplayName != "Alice" ||
		frag.Scope.Platform != "telegram" ||
		frag.Scope.CurrentMessageID != "msg-1" ||
		frag.Scope.EventID != "evt-1" ||
		frag.Scope.ReplyToMessageID != "msg-0" ||
		frag.Scope.ConversationType != "group" ||
		frag.Scope.ConversationName != "Dev Chat" ||
		frag.Scope.ReplyTarget != "target-1" {
		t.Fatalf("frag scope lost DB row topology: %#v", frag.Scope)
	}
	if frag.Provenance.Source != string(SourceDBMessage) || frag.Provenance.SourceID != "row-1" || frag.Provenance.Collector != CollectorHistoryRecords {
		t.Fatalf("unexpected frag provenance: %#v", frag.Provenance)
	}
}

func TestDBMessageSourceHashIncludesMessageMetadata(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:       "row-1",
		BotID:    "bot-1",
		Role:     "user",
		Content:  turn.NewTextContent("hello"),
		Metadata: map[string]any{"reply": map[string]any{"sender": "Alice"}},
	}
	changed := msg
	changed.Metadata = map[string]any{"reply": map[string]any{"sender": "Bob"}}

	if DBMessageSourceHash(msg).Value == DBMessageSourceHash(changed).Value {
		t.Fatal("message metadata change should affect DB source payload hash")
	}
}

func TestFromDBMessageParsesSnakeCaseUsageTokens(t *testing.T) {
	t.Parallel()

	inputTokens := 21
	outputTokens := 43
	record, err := FromDBMessage(messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "assistant",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "assistant", Content: turn.NewTextContent("done")}),
		Usage:   mustJSON(t, map[string]int{"input_tokens": inputTokens, "output_tokens": outputTokens}),
	}, ScopeFallback{})
	if err != nil {
		t.Fatalf("FromDBMessage failed: %v", err)
	}

	if record.UsageInputTokens == nil || *record.UsageInputTokens != inputTokens {
		t.Fatalf("UsageInputTokens = %#v, want %d", record.UsageInputTokens, inputTokens)
	}
	if record.UsageOutputTokens == nil || *record.UsageOutputTokens != outputTokens {
		t.Fatalf("UsageOutputTokens = %#v, want %d", record.UsageOutputTokens, outputTokens)
	}
}

func TestFromDBMessageDuplicateContentUsesDurableRowIDs(t *testing.T) {
	t.Parallel()

	first, err := FromDBMessage(messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "user",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("same")}),
	}, ScopeFallback{})
	if err != nil {
		t.Fatalf("first FromDBMessage failed: %v", err)
	}
	second, err := FromDBMessage(messagepkg.Message{
		ID:      "row-2",
		BotID:   "bot-1",
		Role:    "user",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("same")}),
	}, ScopeFallback{})
	if err != nil {
		t.Fatalf("second FromDBMessage failed: %v", err)
	}

	firstFrag := ToFrag(first)
	secondFrag := ToFrag(second)
	if firstFrag.Ref.ID == secondFrag.Ref.ID {
		t.Fatalf("duplicate content must not collapse durable row identity: first=%#v second=%#v", firstFrag.Ref, secondFrag.Ref)
	}
	if firstFrag.Ref.Durability != contextfrag.RefDurable || secondFrag.Ref.Durability != contextfrag.RefDurable {
		t.Fatalf("DB row refs must be durable: first=%#v second=%#v", firstFrag.Ref, secondFrag.Ref)
	}
}

func TestFromDBMessageContentHashChangesWhenRowContentChanges(t *testing.T) {
	t.Parallel()

	original, err := FromDBMessage(messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "user",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("original")}),
	}, ScopeFallback{})
	if err != nil {
		t.Fatalf("original FromDBMessage failed: %v", err)
	}
	edited, err := FromDBMessage(messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "user",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("edited")}),
	}, ScopeFallback{})
	if err != nil {
		t.Fatalf("edited FromDBMessage failed: %v", err)
	}

	originalFrag := ToFrag(original)
	editedFrag := ToFrag(edited)
	if originalFrag.Ref.ID != editedFrag.Ref.ID {
		t.Fatalf("same DB row should keep stable durable identity: original=%#v edited=%#v", originalFrag.Ref, editedFrag.Ref)
	}
	if originalFrag.Ref.ContentHash == "" || editedFrag.Ref.ContentHash == "" || originalFrag.Ref.ContentHash == editedFrag.Ref.ContentHash {
		t.Fatalf("content hash should fence row content changes: original=%#v edited=%#v", originalFrag.Ref, editedFrag.Ref)
	}
}

func TestDBMessageSourceHashIgnoresJoinedDisplayName(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:                "row-1",
		BotID:             "bot-1",
		SessionID:         "session-1",
		SenderDisplayName: "Alice",
		Role:              "user",
		Content:           persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("hello")}),
	}
	original := DBMessageSourceHash(msg)
	msg.SenderDisplayName = "Alice Renamed"

	if renamed := DBMessageSourceHash(msg); renamed != original {
		t.Fatalf("joined display name changed persisted source hash: original=%#v renamed=%#v", original, renamed)
	}
}

func TestFromDBMessageContentHashChangesWhenAssetsChange(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "user",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("with asset")}),
		Assets: []messagepkg.MessageAsset{{
			ContentHash: "asset-hash-1",
			Role:        "attachment",
			Ordinal:     0,
			Mime:        "image/png",
		}},
	}
	original, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("original FromDBMessage failed: %v", err)
	}
	msg.Assets[0].ContentHash = "asset-hash-2"
	edited, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("edited FromDBMessage failed: %v", err)
	}

	originalFrag := ToFrag(original)
	editedFrag := ToFrag(edited)
	if originalFrag.Ref.ID != editedFrag.Ref.ID {
		t.Fatalf("same DB row should keep stable durable identity: original=%#v edited=%#v", originalFrag.Ref, editedFrag.Ref)
	}
	if originalFrag.Ref.ContentHash == "" || editedFrag.Ref.ContentHash == "" || originalFrag.Ref.ContentHash == editedFrag.Ref.ContentHash {
		t.Fatalf("content hash should fence asset changes: original=%#v edited=%#v", originalFrag.Ref, editedFrag.Ref)
	}
}

func TestFromDBMessageContentHashUsesPersistedAssetIdentityFields(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "user",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("with asset")}),
		Assets: []messagepkg.MessageAsset{{
			ContentHash: "asset-hash-1",
			Role:        "attachment",
			Ordinal:     0,
			Mime:        "image/png",
			SizeBytes:   1234,
			StorageKey:  "objects/asset-hash-1",
			Name:        "image.png",
			Metadata:    map[string]any{"width": float64(640)},
		}},
	}
	inMemory, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("inMemory FromDBMessage failed: %v", err)
	}
	msg.Assets[0].Mime = ""
	msg.Assets[0].SizeBytes = 0
	msg.Assets[0].StorageKey = ""
	hydrated, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("hydrated FromDBMessage failed: %v", err)
	}

	if ToFrag(inMemory).Ref.ContentHash != ToFrag(hydrated).Ref.ContentHash {
		t.Fatalf("non-persisted asset fields changed source hash: inMemory=%#v hydrated=%#v", ToFrag(inMemory).Ref, ToFrag(hydrated).Ref)
	}
}

func TestFromDBMessageContentHashIgnoresCompactionMarker(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "user",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("hello")}),
	}
	uncompacted, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("uncompacted FromDBMessage failed: %v", err)
	}
	msg.CompactID = "compact-1"
	compacted, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("compacted FromDBMessage failed: %v", err)
	}

	if ToFrag(uncompacted).Ref.ContentHash != ToFrag(compacted).Ref.ContentHash {
		t.Fatalf("compaction marker changed source hash: uncompacted=%#v compacted=%#v", ToFrag(uncompacted).Ref, ToFrag(compacted).Ref)
	}
}

func TestHistoryRecordsRenderLegacyModelAndSDKMessages(t *testing.T) {
	t.Parallel()

	expectedModel := []turn.ModelMessage{
		{Role: "user", Content: turn.NewTextContent("hello")},
		{Role: "assistant", Content: turn.NewTextContent("hi")},
	}
	records := make([]HistoryRecord, 0, len(expectedModel))
	for i, msg := range expectedModel {
		record, err := FromDBMessage(messagepkg.Message{
			ID:      string(rune('a' + i)),
			BotID:   "bot-1",
			Role:    msg.Role,
			Content: persistedModelMessage(t, msg),
		}, ScopeFallback{})
		if err != nil {
			t.Fatalf("FromDBMessage %d failed: %v", i, err)
		}
		records = append(records, record)
	}

	if got := ToModelMessages(records); !reflect.DeepEqual(got, expectedModel) {
		t.Fatalf("ToModelMessages mismatch:\ngot  %#v\nwant %#v", got, expectedModel)
	}

	assertSameJSON(t, ToSDKMessages(records), []sdk.Message{
		sdk.UserMessage("hello"),
		sdk.AssistantMessage("hi"),
	})
}

func TestToFragKeepsPersistedSystemRowsExternalTrust(t *testing.T) {
	t.Parallel()

	record, err := FromDBMessage(messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "system",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "system", Content: turn.NewTextContent("stored policy-looking text")}),
	}, ScopeFallback{})
	if err != nil {
		t.Fatalf("FromDBMessage failed: %v", err)
	}

	frag := ToFrag(record)
	if frag.Trust != contextfrag.TrustExternal {
		t.Fatalf("history frag trust = %s, want %s", frag.Trust, contextfrag.TrustExternal)
	}
}

func TestFromDBMessageScopeFallbackDoesNotChangeDurableRefID(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "user",
		Content: persistedModelMessage(t, turn.ModelMessage{Role: "user", Content: turn.NewTextContent("hello")}),
	}
	first, err := FromDBMessage(msg, ScopeFallback{ChatID: "chat-1", ConversationType: "group"})
	if err != nil {
		t.Fatalf("first FromDBMessage failed: %v", err)
	}
	second, err := FromDBMessage(msg, ScopeFallback{ChatID: "chat-2", ConversationType: "private"})
	if err != nil {
		t.Fatalf("second FromDBMessage failed: %v", err)
	}

	if first.Ref.ID != second.Ref.ID {
		t.Fatalf("current-request fallback scope must not change DB row identity: first=%#v second=%#v", first.Ref, second.Ref)
	}
	if ToFrag(first).Ref.ID != ToFrag(second).Ref.ID {
		t.Fatalf("fallback scope changed durable frag identity: first=%#v second=%#v", ToFrag(first).Ref, ToFrag(second).Ref)
	}
	if ToFrag(first).Ref.ContentHash != ToFrag(second).Ref.ContentHash {
		t.Fatalf("fallback scope changed durable content hash: first=%#v second=%#v", ToFrag(first).Ref, ToFrag(second).Ref)
	}
}

// TestFromDBMessageNormalizesBareContentPartObject covers a persisted row whose
// content is a bare content-part object (no role/content wrapper), which used
// to unmarshal successfully into an empty ModelMessage — unknown JSON fields
// are ignored — and silently render as empty downstream.
func TestFromDBMessageNormalizesBareContentPartObject(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:      "row-1",
		BotID:   "bot-1",
		Role:    "user",
		Content: json.RawMessage(`{"type":"text","text":"hello"}`),
	}
	record, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("FromDBMessage failed: %v", err)
	}
	if got := record.ModelMessage.TextContent(); got != "hello" {
		t.Fatalf("TextContent() = %q, want %q", got, "hello")
	}
	if !record.ModelMessage.HasContent() {
		t.Fatalf("HasContent() = false, want true for normalized bare content part")
	}
	if record.ModelMessage.Role != "user" {
		t.Fatalf("Role = %q, want %q", record.ModelMessage.Role, "user")
	}
}

// TestFromDBMessageLegitimateEmptyToolRowUnaffected proves the bare-content-part
// normalization does not misfire on a real (if empty) tool-result wrapper, whose
// "role" key already makes it a valid ModelMessage.
func TestFromDBMessageLegitimateEmptyToolRowUnaffected(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:      "row-2",
		BotID:   "bot-1",
		Role:    "tool",
		Content: json.RawMessage(`{"role":"tool","tool_call_id":"x","content":""}`),
	}
	record, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("FromDBMessage failed: %v", err)
	}
	if record.ModelMessage.Role != "tool" {
		t.Fatalf("Role = %q, want %q", record.ModelMessage.Role, "tool")
	}
	if got := record.ModelMessage.TextContent(); got != "" {
		t.Fatalf("TextContent() = %q, want empty", got)
	}
	if record.ModelMessage.HasContent() {
		t.Fatalf("HasContent() = true, want false for legitimate empty tool row")
	}
}

// TestFromDBMessageEmptyObjectContentNotWrapped proves an empty `{}` payload is
// left alone: it carries no content to recover, so it must not be turned into
// a `[{}]` parts array.
func TestFromDBMessageEmptyObjectContentNotWrapped(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:      "row-3",
		BotID:   "bot-1",
		Role:    "user",
		Content: json.RawMessage(`{}`),
	}
	record, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("FromDBMessage failed: %v", err)
	}
	if got := record.ModelMessage.TextContent(); got != "" {
		t.Fatalf("TextContent() = %q, want empty", got)
	}
	if string(record.ModelMessage.Content) == "[{}]" {
		t.Fatalf("empty object must not be wrapped into a parts array, got Content = %s", record.ModelMessage.Content)
	}
}

// TestFromDBMessageNormalWrapperUnchanged proves a legitimate ModelMessage
// wrapper still round-trips exactly as before the fix.
func TestFromDBMessageNormalWrapperUnchanged(t *testing.T) {
	t.Parallel()

	msg := messagepkg.Message{
		ID:      "row-4",
		BotID:   "bot-1",
		Role:    "assistant",
		Content: json.RawMessage(`{"role":"assistant","content":"hi"}`),
	}
	record, err := FromDBMessage(msg, ScopeFallback{})
	if err != nil {
		t.Fatalf("FromDBMessage failed: %v", err)
	}
	if got := record.ModelMessage.TextContent(); got != "hi" {
		t.Fatalf("TextContent() = %q, want %q", got, "hi")
	}
}

func persistedModelMessage(t *testing.T, msg turn.ModelMessage) json.RawMessage {
	t.Helper()
	return mustJSON(t, msg)
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T: %v", value, err)
	}
	return raw
}

func assertSameJSON(t *testing.T, got any, want any) {
	t.Helper()
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantRaw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("json mismatch:\ngot  %s\nwant %s", gotRaw, wantRaw)
	}
}
