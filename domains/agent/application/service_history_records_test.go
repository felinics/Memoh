package application

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	toolapproval "github.com/memohai/memoh/domains/agent/decision/approval"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/agent/engine"
)

func TestDedupePersistedCurrentUserMessageUsesHistoryRecordProvenance(t *testing.T) {
	t.Parallel()

	hist := []history.HistoryRecord{
		historyRecord("row-1", agentdomain.ModelMessage{
			Role:    "user",
			Content: agentdomain.NewTextContent("---\nmessage-id: qq-msg-1\nchannel: qq\n---\nhello"),
		}, func(record *history.HistoryRecord) {
			record.ExternalMessageID = "qq-msg-1"
			record.Platform = "qq"
			record.SenderChannelIdentityID = "channel-identity-1"
		}),
		historyRecord("row-2", agentdomain.ModelMessage{
			Role:    "assistant",
			Content: agentdomain.NewTextContent("ok"),
		}, nil),
	}

	got := dedupePersistedCurrentUserMessage(hist, ChatRequest{
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

	records := []history.HistoryRecord{
		historyRecord("row-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("old 1")}, func(record *history.HistoryRecord) {
			record.CompactID = "compact-1"
		}),
		historyRecord("row-2", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("old 2")}, func(record *history.HistoryRecord) {
			record.CompactID = "compact-1"
		}),
		historyRecord("row-3", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("new")}, nil),
	}

	got := replaceCompactedHistoryRecords(records, map[string]string{"compact-1": "condensed"}, fragment.Scope{})
	wantMessages := []agentdomain.ModelMessage{
		{Role: "user", Content: agentdomain.NewTextContent("<summary>\ncondensed\n</summary>")},
		{Role: "user", Content: agentdomain.NewTextContent("new")},
	}
	if gotMessages := history.ToModelMessages(got); !reflect.DeepEqual(gotMessages, wantMessages) {
		t.Fatalf("replacement messages mismatch:\ngot  %#v\nwant %#v", gotMessages, wantMessages)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d", len(got))
	}
	summary := got[0]
	if summary.SourceKind != history.SourceCompactionLog || summary.Lifecycle != history.LifecycleActiveSummary {
		t.Fatalf("summary record source/lifecycle mismatch: %#v", summary)
	}
	if summary.Kind != fragment.KindConversationSummary {
		t.Fatalf("summary should be conversation_summary, got %s", summary.Kind)
	}
	if summary.Ref.Namespace != "compaction_log" || summary.Ref.ID != "compact-1" || summary.Ref.Durability != fragment.RefDurable {
		t.Fatalf("summary ref should be durable compaction log identity: %#v", summary.Ref)
	}
	if summary.Coverage == nil || len(summary.Coverage.CoveredRefs) != 2 {
		t.Fatalf("summary should cover compacted records: %#v", summary.Coverage)
	}
	if summary.Coverage.CoveredRefs[0].ID != "row-1" || summary.Coverage.CoveredRefs[1].ID != "row-2" {
		t.Fatalf("summary coverage should preserve covered record refs: %#v", summary.Coverage.CoveredRefs)
	}
	if frag := history.ToFrag(summary); frag.Kind != fragment.KindConversationSummary || frag.Slot != fragment.SlotHistory || frag.Coverage == nil {
		t.Fatalf("summary frag should carry active summary coverage: %#v", frag)
	}
}

func TestReplaceCompactedHistoryRecordsScopesSummaryToConversationNotFirstSender(t *testing.T) {
	t.Parallel()

	records := []history.HistoryRecord{
		historyRecord("row-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("old 1")}, func(record *history.HistoryRecord) {
			record.CompactID = "compact-1"
			record.Scope = fragment.Scope{
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
		historyRecord("row-2", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("old 2")}, func(record *history.HistoryRecord) {
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

	records := []history.HistoryRecord{
		historyRecord("row-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("old 1")}, func(record *history.HistoryRecord) {
			record.CompactID = "compact-1"
		}),
		historyRecord("row-2", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("old 2")}, func(record *history.HistoryRecord) {
			record.CompactID = "compact-1"
		}),
	}

	gotMissing := replaceCompactedHistoryRecords(records, map[string]string{}, fragment.Scope{})
	if gotMessages := history.ToModelMessages(gotMissing); !reflect.DeepEqual(gotMessages, history.ToModelMessages(records)) {
		t.Fatalf("missing summary should keep original group:\ngot  %#v\nwant %#v", gotMessages, history.ToModelMessages(records))
	}

	gotEmpty := replaceCompactedHistoryRecords(records, map[string]string{"compact-1": ""}, fragment.Scope{})
	if gotMessages := history.ToModelMessages(gotEmpty); !reflect.DeepEqual(gotMessages, history.ToModelMessages(records)) {
		t.Fatalf("empty summary should keep original group:\ngot  %#v\nwant %#v", gotMessages, history.ToModelMessages(records))
	}

	// A legacy status='ok' log can hold a whitespace-only summary (main never
	// trimmed). Substituting it would drop the raw rows for nothing, and the
	// reclaim SQL treats such rows as still eligible — the read path must agree.
	gotWhitespace := replaceCompactedHistoryRecords(records, map[string]string{"compact-1": "  \n\t"}, fragment.Scope{})
	if gotMessages := history.ToModelMessages(gotWhitespace); !reflect.DeepEqual(gotMessages, history.ToModelMessages(records)) {
		t.Fatalf("whitespace-only summary should keep original group:\ngot  %#v\nwant %#v", gotMessages, history.ToModelMessages(records))
	}
}

func TestReplaceCompactedHistoryRecordsKeepsMustKeepIslandOrdering(t *testing.T) {
	t.Parallel()

	// The selector now marks only the contiguous run before a must-keep island
	// (the ask_user exchange) under one compact_id; the island and the run after
	// it stay raw. The read path must drop the summary in place and preserve
	// order — "mid q" stays AFTER the ask_user exchange, never folded before it.
	records := []history.HistoryRecord{
		historyRecord("row-old", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("old q")}, func(r *history.HistoryRecord) {
			r.CompactID = "compact-1"
		}),
		historyRecord("row-ask-call", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("ask you something")}, nil),
		historyRecord("row-ask-result", agentdomain.ModelMessage{Role: "tool", Content: agentdomain.NewTextContent("answered")}, nil),
		historyRecord("row-mid", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("mid q")}, nil),
		historyRecord("row-current", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("current")}, nil),
	}

	got := replaceCompactedHistoryRecords(records, map[string]string{"compact-1": "condensed"}, fragment.Scope{})
	want := []agentdomain.ModelMessage{
		{Role: "user", Content: agentdomain.NewTextContent("<summary>\ncondensed\n</summary>")},
		{Role: "assistant", Content: agentdomain.NewTextContent("ask you something")},
		{Role: "tool", Content: agentdomain.NewTextContent("answered")},
		{Role: "user", Content: agentdomain.NewTextContent("mid q")},
		{Role: "user", Content: agentdomain.NewTextContent("current")},
	}
	if gotMessages := history.ToModelMessages(got); !reflect.DeepEqual(gotMessages, want) {
		t.Fatalf("must-keep island ordering broken:\ngot  %#v\nwant %#v", gotMessages, want)
	}
}

func TestHistoryContextFragsForMessagesCarriesActiveSummaryCoverage(t *testing.T) {
	t.Parallel()

	covered := []fragment.ContextRef{
		{Namespace: "bot_history_message", ID: "row-1", Schema: fragment.SchemaContextRef, Durability: fragment.RefDurable},
	}
	summary := history.SummaryRecord("compact-1", "condensed", covered, fragment.Scope{BotID: "bot-1"})
	records := []history.HistoryRecord{
		summary,
		historyRecord("row-2", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("new")}, nil),
	}
	messages := []agentdomain.ModelMessage{
		summary.ModelMessage,
		{Role: "user", Content: agentdomain.NewTextContent("new")},
	}

	frags := historyContextFragsForMessages(messages, records)

	if len(frags) != 1 {
		t.Fatalf("summary frags = %d, want 1: %#v", len(frags), frags)
	}
	if frags[0].ID != "message.000" || frags[0].Provenance.Index != 0 {
		t.Fatalf("summary frag should align with final message index: %#v", frags[0])
	}
	if frags[0].Kind != fragment.KindConversationSummary || frags[0].Coverage == nil {
		t.Fatalf("summary frag lost kind/coverage: %#v", frags[0])
	}

	cfg := engine.RunConfig{
		Messages:     modelMessagesToSDKMessages(messages),
		ContextFrags: frags,
	}.RefreshContextFrag()
	if len(cfg.ContextManifest.CoverageTrace) != 1 {
		t.Fatalf("run config manifest lost summary coverage: %#v", cfg.ContextManifest)
	}
	summaryItems := 0
	for _, item := range cfg.ContextManifest.Items {
		if item.Kind == fragment.KindConversationSummary {
			summaryItems++
		}
	}
	if summaryItems != 1 {
		t.Fatalf("run config manifest summary items = %d, want 1: %#v", summaryItems, cfg.ContextManifest.Items)
	}
}

func TestHistoryContextFragsUseRetainedSummaryRecordsAfterTrim(t *testing.T) {
	t.Parallel()

	firstCovered := []fragment.ContextRef{{Namespace: "bot_history_message", ID: "old-covered", Schema: fragment.SchemaContextRef, Durability: fragment.RefDurable}}
	secondCovered := []fragment.ContextRef{{Namespace: "bot_history_message", ID: "new-covered", Schema: fragment.SchemaContextRef, Durability: fragment.RefDurable}}
	first := history.SummaryRecord("compact-old", "same summary", firstCovered, fragment.Scope{})
	second := history.SummaryRecord("compact-new", "same summary", secondCovered, fragment.Scope{})
	records := []history.HistoryRecord{
		first,
		historyRecord("row-long", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent(strings.Repeat("x", 400))}, nil),
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

	summary := history.SummaryRecord("compact-big", strings.Repeat("s", 4000), nil, fragment.Scope{})
	raw := historyRecord("row-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent(strings.Repeat("r", 400))}, nil)
	records := []history.HistoryRecord{summary, raw}

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

func historyRecord(id string, msg agentdomain.ModelMessage, mutate func(*history.HistoryRecord)) history.HistoryRecord {
	record := history.HistoryRecord{
		Ref: fragment.ContextRef{
			Namespace:   "bot_history_message",
			ID:          id,
			Version:     1,
			HashAlgo:    fragment.HashAlgoSHA256,
			HashScope:   fragment.HashScopeSourcePayload,
			ContentHash: testHistorySourceHash(id),
			Schema:      fragment.SchemaContextRef,
			Durability:  fragment.RefDurable,
		},
		Kind:         fragment.KindConversationEvent,
		SourceKind:   history.SourceDBMessage,
		Lifecycle:    history.LifecyclePersisted,
		ModelMessage: msg,
		DBMessageID:  id,
	}
	if mutate != nil {
		mutate(&record)
	}
	return record
}

type recordingCompactionLogQueries struct {
	logs      []compaction.ArtifactRecord
	refs      map[string][]string
	byID      map[string]compaction.ArtifactRecord
	sessionID string
	listCalls int
	refCalls  []string
	getCalls  []string
	listErr   error
}

func serviceWithCompactionReads(reader *recordingCompactionLogQueries) *Service {
	return &Service{
		compactionArtifacts:   reader,
		compactionMessageRefs: reader,
	}
}

func (q *recordingCompactionLogQueries) GetArtifact(_ context.Context, compactID string) (compaction.ArtifactRecord, error) {
	q.getCalls = append(q.getCalls, compactID)
	return q.byID[compactID], nil
}

func (q *recordingCompactionLogQueries) ListParentIDs(_ context.Context, input compaction.ArtifactParentsInput) ([]string, error) {
	var ids []string
	for _, row := range q.byID {
		if row.Status == "ok" && row.SupersededBy == input.SuccessorID && row.BotID == input.BotID && row.SessionID == input.SessionID {
			ids = append(ids, row.ID)
		}
	}
	return ids, nil
}

func (q *recordingCompactionLogQueries) ListArtifactsBySession(_ context.Context, sessionID string) ([]compaction.ArtifactRecord, error) {
	q.sessionID = sessionID
	q.listCalls++
	return q.logs, q.listErr
}

func persistedCoverage(t *testing.T, id string) []byte {
	t.Helper()
	raw, err := json.Marshal([]compaction.CoveredSource{{
		Ref: fragment.ContextRef{
			Namespace:   "bot_history_message",
			ID:          id,
			Version:     1,
			HashAlgo:    fragment.HashAlgoSHA256,
			HashScope:   fragment.HashScopeSourcePayload,
			ContentHash: testHistorySourceHash(id),
			Schema:      fragment.SchemaContextRef,
			Durability:  fragment.RefDurable,
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

func recordTexts(records []history.HistoryRecord) []string {
	texts := make([]string, 0, len(records))
	for _, record := range records {
		texts = append(texts, record.ModelMessage.TextContent())
	}
	return texts
}

func mustReplaceCompactedMessages(t *testing.T, resolver *Service, sessionID string, scope fragment.Scope, records []history.HistoryRecord) []history.HistoryRecord {
	t.Helper()
	replaced, err := resolver.replaceCompactedMessages(context.Background(), sessionID, scope, records, compactionArtifactBoundary{})
	if err != nil {
		t.Fatalf("replaceCompactedMessages: %v", err)
	}
	return replaced
}

func (q *recordingCompactionLogQueries) ListCompactionMessageRefs(_ context.Context, compactID string) ([]string, error) {
	q.refCalls = append(q.refCalls, compactID)
	return q.refs[compactID], nil
}
