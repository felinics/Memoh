package application

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/contextview"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func TestDedupePersistedCurrentUserMessageUsesHistoryRecordProvenance(t *testing.T) {
	t.Parallel()

	history := []historyfrag.HistoryRecord{
		historyRecord("row-1", ModelMessage{
			Role:    "user",
			Content: newTextContent("---\nmessage-id: qq-msg-1\nchannel: qq\n---\nhello"),
		}, func(record *historyfrag.HistoryRecord) {
			record.ExternalMessageID = "qq-msg-1"
			record.Platform = "qq"
			record.SenderChannelIdentityID = "channel-identity-1"
		}),
		historyRecord("row-2", ModelMessage{
			Role:    "assistant",
			Content: newTextContent("ok"),
		}, nil),
	}

	got := dedupePersistedCurrentUserMessage(history, ChatRequest{
		UserMessagePersisted:    true,
		RouteID:                 "route-1",
		ExternalMessageID:       "qq-msg-1",
		CurrentChannel:          "qq",
		SourceChannelIdentityID: "channel-identity-1",
	})

	if len(got) != 1 {
		t.Fatalf("expected 1 message after dedupe, got %d", len(got))
	}
	if got[0].ModelMessage.Role != "assistant" {
		t.Fatalf("unexpected remaining role: %s", got[0].ModelMessage.Role)
	}
}

func TestReplaceCompactedHistoryRecordsUsesTypedSummaryRecord(t *testing.T) {
	t.Parallel()

	records := []historyfrag.HistoryRecord{
		historyRecord("row-1", ModelMessage{Role: "user", Content: newTextContent("old 1")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "compact-1"
		}),
		historyRecord("row-2", ModelMessage{Role: "assistant", Content: newTextContent("old 2")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "compact-1"
		}),
		historyRecord("row-3", ModelMessage{Role: "user", Content: newTextContent("new")}, nil),
	}

	got := replaceCompactedHistoryRecords(records, map[string]string{"compact-1": "condensed"}, contextfrag.Scope{})
	wantMessages := []ModelMessage{
		{Role: "user", Content: newTextContent("<summary>\ncondensed\n</summary>")},
		{Role: "user", Content: newTextContent("new")},
	}
	if gotMessages := historyfrag.ToModelMessages(got); !reflect.DeepEqual(gotMessages, wantMessages) {
		t.Fatalf("replacement messages mismatch:\ngot  %#v\nwant %#v", gotMessages, wantMessages)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d", len(got))
	}
	summary := got[0]
	if summary.SourceKind != historyfrag.SourceCompactionLog || summary.Lifecycle != historyfrag.LifecycleActiveSummary {
		t.Fatalf("summary record source/lifecycle mismatch: %#v", summary)
	}
	if summary.Kind != contextfrag.KindConversationSummary {
		t.Fatalf("summary should be conversation_summary, got %s", summary.Kind)
	}
	if summary.Ref.Namespace != "compaction_log" || summary.Ref.ID != "compact-1" || summary.Ref.Durability != contextfrag.RefDurable {
		t.Fatalf("summary ref should be durable compaction log identity: %#v", summary.Ref)
	}
	if summary.Coverage == nil || len(summary.Coverage.CoveredRefs) != 2 {
		t.Fatalf("summary should cover compacted records: %#v", summary.Coverage)
	}
	if summary.Coverage.CoveredRefs[0].ID != "row-1" || summary.Coverage.CoveredRefs[1].ID != "row-2" {
		t.Fatalf("summary coverage should preserve covered record refs: %#v", summary.Coverage.CoveredRefs)
	}
	if frag := historyfrag.ToFrag(summary); frag.Kind != contextfrag.KindConversationSummary || frag.Slot != contextfrag.SlotHistory || frag.Coverage == nil {
		t.Fatalf("summary frag should carry active summary coverage: %#v", frag)
	}
}

func TestReplaceCompactedHistoryRecordsScopesSummaryToConversationNotFirstSender(t *testing.T) {
	t.Parallel()

	records := []historyfrag.HistoryRecord{
		historyRecord("row-1", ModelMessage{Role: "user", Content: newTextContent("old 1")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "compact-1"
			record.Scope = contextfrag.Scope{
				BotID:             "bot-1",
				ChatID:            "chat-1",
				SessionID:         "sess-1",
				ChannelIdentityID: "sender-1",
				DisplayName:       "Alice",
				CurrentMessageID:  "msg-1",
				EventID:           "evt-1",
				ReplyToMessageID:  "msg-0",
			}
		}),
		historyRecord("row-2", ModelMessage{Role: "assistant", Content: newTextContent("old 2")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "compact-1"
		}),
	}

	conversationScope := compactionSummaryScope("bot-1", "chat-1", "sess-1", "group", "Dev Chat", "target-1")
	got := replaceCompactedHistoryRecords(records, map[string]string{"compact-1": "condensed"}, conversationScope)
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}

	scope := got[0].Scope
	if scope.ChannelIdentityID != "" || scope.DisplayName != "" || scope.CurrentMessageID != "" ||
		scope.EventID != "" || scope.ReplyToMessageID != "" {
		t.Fatalf("summary scope must not carry first sender's identity: %#v", scope)
	}
	if scope.BotID != "bot-1" || scope.ChatID != "chat-1" || scope.SessionID != "sess-1" ||
		scope.ConversationType != "group" || scope.ConversationName != "Dev Chat" || scope.ReplyTarget != "target-1" {
		t.Fatalf("summary scope must carry conversation topology: %#v", scope)
	}
}

func TestReplaceCompactedHistoryRecordsKeepsOriginalGroupWithoutSummary(t *testing.T) {
	t.Parallel()

	records := []historyfrag.HistoryRecord{
		historyRecord("row-1", ModelMessage{Role: "user", Content: newTextContent("old 1")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "compact-1"
		}),
		historyRecord("row-2", ModelMessage{Role: "assistant", Content: newTextContent("old 2")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "compact-1"
		}),
	}

	gotMissing := replaceCompactedHistoryRecords(records, map[string]string{}, contextfrag.Scope{})
	if gotMessages := historyfrag.ToModelMessages(gotMissing); !reflect.DeepEqual(gotMessages, historyfrag.ToModelMessages(records)) {
		t.Fatalf("missing summary should keep original group:\ngot  %#v\nwant %#v", gotMessages, historyfrag.ToModelMessages(records))
	}

	gotEmpty := replaceCompactedHistoryRecords(records, map[string]string{"compact-1": ""}, contextfrag.Scope{})
	if gotMessages := historyfrag.ToModelMessages(gotEmpty); !reflect.DeepEqual(gotMessages, historyfrag.ToModelMessages(records)) {
		t.Fatalf("empty summary should keep original group:\ngot  %#v\nwant %#v", gotMessages, historyfrag.ToModelMessages(records))
	}

	// A legacy status='ok' log can hold a whitespace-only summary (main never
	// trimmed). Substituting it would drop the raw rows for nothing, and the
	// reclaim SQL treats such rows as still eligible — the read path must agree.
	gotWhitespace := replaceCompactedHistoryRecords(records, map[string]string{"compact-1": "  \n\t"}, contextfrag.Scope{})
	if gotMessages := historyfrag.ToModelMessages(gotWhitespace); !reflect.DeepEqual(gotMessages, historyfrag.ToModelMessages(records)) {
		t.Fatalf("whitespace-only summary should keep original group:\ngot  %#v\nwant %#v", gotMessages, historyfrag.ToModelMessages(records))
	}
}

func TestReplaceCompactedHistoryRecordsKeepsMustKeepIslandOrdering(t *testing.T) {
	t.Parallel()

	// The selector now marks only the contiguous run before a must-keep island
	// (the ask_user exchange) under one compact_id; the island and the run after
	// it stay raw. The read path must drop the summary in place and preserve
	// order — "mid q" stays AFTER the ask_user exchange, never folded before it.
	records := []historyfrag.HistoryRecord{
		historyRecord("row-old", ModelMessage{Role: "user", Content: newTextContent("old q")}, func(r *historyfrag.HistoryRecord) {
			r.CompactID = "compact-1"
		}),
		historyRecord("row-ask-call", ModelMessage{Role: "assistant", Content: newTextContent("ask you something")}, nil),
		historyRecord("row-ask-result", ModelMessage{Role: "tool", Content: newTextContent("answered")}, nil),
		historyRecord("row-mid", ModelMessage{Role: "user", Content: newTextContent("mid q")}, nil),
		historyRecord("row-current", ModelMessage{Role: "user", Content: newTextContent("current")}, nil),
	}

	got := replaceCompactedHistoryRecords(records, map[string]string{"compact-1": "condensed"}, contextfrag.Scope{})
	want := []ModelMessage{
		{Role: "user", Content: newTextContent("<summary>\ncondensed\n</summary>")},
		{Role: "assistant", Content: newTextContent("ask you something")},
		{Role: "tool", Content: newTextContent("answered")},
		{Role: "user", Content: newTextContent("mid q")},
		{Role: "user", Content: newTextContent("current")},
	}
	if gotMessages := historyfrag.ToModelMessages(got); !reflect.DeepEqual(gotMessages, want) {
		t.Fatalf("must-keep island ordering broken:\ngot  %#v\nwant %#v", gotMessages, want)
	}
}

func TestHistoryContextFragsForMessagesCarriesActiveSummaryCoverage(t *testing.T) {
	t.Parallel()

	covered := []contextfrag.ContextRef{
		{Namespace: "bot_history_message", ID: "row-1", Schema: contextfrag.SchemaContextRef, Durability: contextfrag.RefDurable},
	}
	summary := historyfrag.SummaryRecord("compact-1", "condensed", covered, contextfrag.Scope{BotID: "bot-1"})
	records := []historyfrag.HistoryRecord{
		summary,
		historyRecord("row-2", ModelMessage{Role: "user", Content: newTextContent("new")}, nil),
	}
	messages := []ModelMessage{
		summary.ModelMessage,
		{Role: "user", Content: newTextContent("new")},
	}

	frags := historyContextFragsForMessages(messages, records)

	if len(frags) != 1 {
		t.Fatalf("summary frags = %d, want 1: %#v", len(frags), frags)
	}
	if frags[0].ID != "message.000" || frags[0].Provenance.Index != 0 {
		t.Fatalf("summary frag should align with final message index: %#v", frags[0])
	}
	if frags[0].Kind != contextfrag.KindConversationSummary || frags[0].Coverage == nil {
		t.Fatalf("summary frag lost kind/coverage: %#v", frags[0])
	}

	cfg := native.RunConfig{
		Messages:     modelMessagesToSDKMessages(messages),
		ContextFrags: frags,
	}.RefreshContextFrag()
	if len(cfg.ContextManifest.CoverageTrace) != 1 {
		t.Fatalf("run config manifest lost summary coverage: %#v", cfg.ContextManifest)
	}
	summaryItems := 0
	for _, item := range cfg.ContextManifest.Items {
		if item.Kind == contextfrag.KindConversationSummary {
			summaryItems++
		}
	}
	if summaryItems != 1 {
		t.Fatalf("run config manifest summary items = %d, want 1: %#v", summaryItems, cfg.ContextManifest.Items)
	}

	cfg.ContextQueryMaterialized = true
	cfg.ContextSourceFrags = buildProviderSourceFrags(context.Background(), cfg, nil, nil)
	providerCfg := contextview.ApplyProviderRunConfig(context.Background(), nil, cfg)
	if !reflect.DeepEqual(providerCfg.Messages, cfg.Messages) {
		t.Fatalf("provider messages changed: got %#v want %#v", providerCfg.Messages, cfg.Messages)
	}
	if len(providerCfg.ContextManifest.CoverageTrace) != 1 {
		t.Fatalf("provider manifest lost summary coverage: %#v", providerCfg.ContextManifest)
	}
	providerSummaryItems := 0
	for _, item := range providerCfg.ContextManifest.Items {
		if item.Kind == contextfrag.KindConversationSummary {
			providerSummaryItems++
			if item.Ref.Namespace != summary.Ref.Namespace || item.Ref.ID != summary.Ref.ID ||
				item.Ref.Version != summary.Ref.Version || item.Ref.Durability != contextfrag.RefDurable ||
				item.Ref.ContentHash == "" {
				t.Fatalf("provider summary ref lost durable identity: got %#v source %#v", item.Ref, summary.Ref)
			}
		}
	}
	if providerSummaryItems != 1 {
		t.Fatalf("provider manifest summary items = %d, want 1: %#v", providerSummaryItems, providerCfg.ContextManifest.Items)
	}
}

func TestHistoryContextFragsUseRetainedSummaryRecordsAfterTrim(t *testing.T) {
	t.Parallel()

	firstCovered := []contextfrag.ContextRef{{Namespace: "bot_history_message", ID: "old-covered", Schema: contextfrag.SchemaContextRef, Durability: contextfrag.RefDurable}}
	secondCovered := []contextfrag.ContextRef{{Namespace: "bot_history_message", ID: "new-covered", Schema: contextfrag.SchemaContextRef, Durability: contextfrag.RefDurable}}
	first := historyfrag.SummaryRecord("compact-old", "same summary", firstCovered, contextfrag.Scope{})
	second := historyfrag.SummaryRecord("compact-new", "same summary", secondCovered, contextfrag.Scope{})
	records := []historyfrag.HistoryRecord{
		first,
		historyRecord("row-long", ModelMessage{Role: "user", Content: newTextContent(strings.Repeat("x", 400))}, nil),
		second,
	}

	messages, retained, _ := trimMessagesAndRecordsByTokens(nil, records, 20)
	frags := historyContextFragsForMessages(messages, retained)

	if len(frags) != 1 || frags[0].Coverage == nil || len(frags[0].Coverage.CoveredRefs) != 1 {
		t.Fatalf("summary frag coverage mismatch: %#v", frags)
	}
	if got := frags[0].Coverage.CoveredRefs[0].ID; got != "new-covered" {
		t.Fatalf("summary coverage = %q, want retained summary coverage", got)
	}
}

func TestTotalCompactableHistoryTokensExcludesSummaries(t *testing.T) {
	t.Parallel()

	summary := historyfrag.SummaryRecord("compact-big", strings.Repeat("s", 4000), nil, contextfrag.Scope{})
	raw := historyRecord("row-1", ModelMessage{Role: "user", Content: newTextContent(strings.Repeat("r", 400))}, nil)
	records := []historyfrag.HistoryRecord{summary, raw}

	compactable := totalCompactableHistoryTokens(records)
	if compactable <= 0 {
		t.Fatal("raw rows must count toward the compactable estimate")
	}
	if want := estimateMessageTokens(raw.ModelMessage); compactable != want {
		t.Fatalf("compactable = %d, want raw-only estimate %d", compactable, want)
	}
}

func TestHistoryScopeFallbackFromChatRequestUsesRequestTopology(t *testing.T) {
	t.Parallel()

	got := historyScopeFallbackFromChatRequest(ChatRequest{
		ChatID:           " chat-1 ",
		ConversationType: " group ",
		ConversationName: " Dev Chat ",
		ReplyTarget:      " target-1 ",
	})

	if got.ChatID != "chat-1" ||
		got.ConversationType != "group" ||
		got.ConversationName != "Dev Chat" ||
		got.ReplyTarget != "target-1" {
		t.Fatalf("unexpected fallback: %#v", got)
	}
}

func TestResumeHistoryFallbackDoesNotUseBotIDAsChatID(t *testing.T) {
	t.Parallel()

	userInputFallback := historyScopeFallbackFromUserInputRequest(userinput.Request{
		BotID:            "bot-1",
		ConversationType: "group",
		ReplyTarget:      "target-1",
	})
	if userInputFallback.ChatID != "" {
		t.Fatalf("user input fallback ChatID = %q, want empty", userInputFallback.ChatID)
	}
	if userInputFallback.ConversationType != "group" || userInputFallback.ReplyTarget != "target-1" {
		t.Fatalf("user input fallback lost topology: %#v", userInputFallback)
	}

	approvalFallback := historyScopeFallbackFromToolApprovalRequest(toolapproval.Request{
		BotID:            "bot-1",
		ConversationType: "direct",
		ReplyTarget:      "target-2",
	})
	if approvalFallback.ChatID != "" {
		t.Fatalf("approval fallback ChatID = %q, want empty", approvalFallback.ChatID)
	}
	if approvalFallback.ConversationType != "direct" || approvalFallback.ReplyTarget != "target-2" {
		t.Fatalf("approval fallback lost topology: %#v", approvalFallback)
	}
}

func dbHistoryRow(t *testing.T, id string, role string, content json.RawMessage, mutate func(*messagepkg.Message)) messagepkg.Message {
	t.Helper()
	msg := messagepkg.Message{
		ID:      id,
		BotID:   "bot-1",
		Role:    role,
		Content: content,
	}
	if mutate != nil {
		mutate(&msg)
	}
	return msg
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
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

func historyRecord(id string, msg ModelMessage, mutate func(*historyfrag.HistoryRecord)) historyfrag.HistoryRecord {
	record := historyfrag.HistoryRecord{
		Ref: contextfrag.ContextRef{
			Namespace:   "bot_history_message",
			ID:          id,
			Version:     1,
			HashAlgo:    contextfrag.HashAlgoSHA256,
			HashScope:   contextfrag.HashScopeSourcePayload,
			ContentHash: testHistorySourceHash(id),
			Schema:      contextfrag.SchemaContextRef,
			Durability:  contextfrag.RefDurable,
		},
		Kind:         contextfrag.KindConversationEvent,
		SourceKind:   historyfrag.SourceDBMessage,
		Lifecycle:    historyfrag.LifecyclePersisted,
		ModelMessage: msg,
		DBMessageID:  id,
	}
	if mutate != nil {
		mutate(&record)
	}
	return record
}

type recordingCompactionLogQueries struct {
	dbstore.Queries
	logs      []sqlc.BotHistoryMessageCompact
	refs      map[pgtype.UUID][]sqlc.ListMessageRefsByCompactIDRow
	byID      map[pgtype.UUID]sqlc.BotHistoryMessageCompact
	sessionID pgtype.UUID
	listCalls int
	refCalls  []pgtype.UUID
	getCalls  []pgtype.UUID
	listErr   error
}

func (q *recordingCompactionLogQueries) GetCompactionLogByID(_ context.Context, compactID pgtype.UUID) (sqlc.BotHistoryMessageCompact, error) {
	q.getCalls = append(q.getCalls, compactID)
	return q.byID[compactID], nil
}

func (q *recordingCompactionLogQueries) ListCompactionArtifactParentIDsBySuccessor(_ context.Context, arg sqlc.ListCompactionArtifactParentIDsBySuccessorParams) ([]pgtype.UUID, error) {
	var ids []pgtype.UUID
	for _, row := range q.byID {
		if row.Status == "ok" && row.SupersededBy == arg.SuccessorID && row.BotID == arg.BotID && row.SessionID == arg.SessionID {
			ids = append(ids, row.ID)
		}
	}
	return ids, nil
}

func (q *recordingCompactionLogQueries) ListCompactionArtifactLineageBySession(_ context.Context, sessionID pgtype.UUID) ([]sqlc.BotHistoryMessageCompact, error) {
	q.sessionID = sessionID
	q.listCalls++
	return q.logs, q.listErr
}

func persistedCoverage(t *testing.T, id string) []byte {
	t.Helper()
	raw, err := json.Marshal([]compaction.CoveredSource{{
		Ref: contextfrag.ContextRef{
			Namespace:   "bot_history_message",
			ID:          id,
			Version:     1,
			HashAlgo:    contextfrag.HashAlgoSHA256,
			HashScope:   contextfrag.HashScopeSourcePayload,
			ContentHash: testHistorySourceHash(id),
			Schema:      contextfrag.SchemaContextRef,
			Durability:  contextfrag.RefDurable,
		},
	}})
	if err != nil {
		t.Fatalf("marshal persisted coverage: %v", err)
	}
	return raw
}

func testHistorySourceHash(id string) string {
	return "test-source-hash-" + id
}

func recordTexts(records []historyfrag.HistoryRecord) []string {
	texts := make([]string, 0, len(records))
	for _, record := range records {
		texts = append(texts, record.ModelMessage.TextContent())
	}
	return texts
}

func mustReplaceCompactedMessages(t *testing.T, resolver *Service, sessionID string, scope contextfrag.Scope, records []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	t.Helper()
	replaced, err := resolver.replaceCompactedMessages(context.Background(), sessionID, scope, records, compactionArtifactBoundary{})
	if err != nil {
		t.Fatalf("replaceCompactedMessages: %v", err)
	}
	return replaced
}

func (q *recordingCompactionLogQueries) ListMessageRefsByCompactID(_ context.Context, compactID pgtype.UUID) ([]sqlc.ListMessageRefsByCompactIDRow, error) {
	q.refCalls = append(q.refCalls, compactID)
	return q.refs[compactID], nil
}

func mustPGUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return id
}
