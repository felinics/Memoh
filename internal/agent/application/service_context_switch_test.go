package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func TestContextProvenanceKeepsStoredVersionThroughReasoningProjection(t *testing.T) {
	raw, err := historyfrag.MarshalStoredSDKMessage(sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "unfinished reasoning"}}})
	if err != nil {
		t.Fatal(err)
	}
	record, err := historyfrag.FromDBMessage(messagepkg.Message{ID: "checkpoint", Role: "assistant", Content: raw, Metadata: map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true}}, historyfrag.ScopeFallback{})
	if err != nil {
		t.Fatal(err)
	}
	records := projectInterruptedHistoryReasoning([]historyfrag.HistoryRecord{record})
	got := contextProvenance([]ModelMessage{records[0].ModelMessage}, records, nil)
	if len(got) != 1 || got[0].Sources[0].ContentHash != historyfrag.MessageContentHash(record.ModelMessage) || records[0].ModelMessage.TextContent() == record.ModelMessage.TextContent() {
		t.Fatalf("projected checkpoint lost stored identity: %+v", got)
	}
}

func TestBudgetRecoveryRetainsContinuationAnchors(t *testing.T) {
	messages := []ModelMessage{{Role: "user", Content: turn.NewTextContent("root request")}, {Role: "user", Content: turn.NewTextContent("later steer")}}
	records := []historyfrag.HistoryRecord{{DBMessageID: "root", ModelMessage: messages[0]}, {DBMessageID: "steer", ModelMessage: messages[1]}}
	req := ChatRequest{PersistedUserMessageID: "steer", inputInHistory: true, continuationAnchors: []turn.ContextMessageSource{{Kind: "history", ID: "root"}, {Kind: "history", ID: "steer"}}}
	got, err := (chatHistoryLayout{historyCount: 2}).replaceHistory(t.Context(), native.RunConfig{Messages: modelMessagesToSDKMessages(messages)}, req, messages, records)
	if err != nil || len(got.ContextProvenance) != 2 || !got.ContextProvenance[0].Anchor || !got.ContextProvenance[1].Anchor {
		t.Fatalf("continued inputs lost after recovery: %+v err=%v", got.ContextProvenance, err)
	}
}

func TestContextProvenanceSurvivesToolResultNormalization(t *testing.T) {
	t.Parallel()

	call := sdkMessagesToModelMessages([]sdk.Message{{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{
		ToolCallID: "call-1", ToolName: "exec", Input: toolexec.ArgumentsFromValue(map[string]any{}),
	}}}})[0]
	result := ModelMessage{Role: "tool", ToolCallID: "call-1", Name: "exec", Content: json.RawMessage(`{"ok":true}`)}
	records := []historyfrag.HistoryRecord{
		{DBMessageID: "call-row", ModelMessage: call},
		{DBMessageID: "result-row", ModelMessage: result},
	}
	messages, _, _ := normalizeContextMessages([]ModelMessage{call, result}, nil, nil)
	got := contextProvenance(messages, records, nil)
	if len(got) != 2 || len(got[1].Sources) != 1 || got[1].Sources[0].ID != "result-row" || got[1].Sources[0].ContentHash == "" {
		t.Fatalf("normalized tool exchange lost its result source: %+v", got)
	}
}

// A deferred call whose run ended before its answer has no result row, so
// the read path closes it. The closure renders from the call's row: a fold
// that covers the call takes the closure with it.
func TestContextProvenanceNamesTheCallRowForItsClosure(t *testing.T) {
	t.Parallel()

	stored := func(id, role string, message sdk.Message) historyfrag.HistoryRecord {
		raw, err := historyfrag.MarshalStoredSDKMessage(message)
		if err != nil {
			t.Fatal(err)
		}
		record, err := historyfrag.FromDBMessage(messagepkg.Message{ID: id, Role: role, Content: raw}, historyfrag.ScopeFallback{})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	records := []historyfrag.HistoryRecord{
		stored("call-row", "assistant", sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "ask-1", ToolName: "ask_user", Input: toolexec.ArgumentsFromValue(map[string]any{}),
		}}}),
		stored("later-row", "user", sdk.UserMessage("later question")),
	}
	messages, _, _ := normalizeContextMessages([]ModelMessage{records[0].ModelMessage, records[1].ModelMessage}, nil, nil)
	got := contextProvenance(messages, records, nil)
	call := got[0].Sources
	if len(messages) != 3 || len(got) != 3 || got[1].Message.Role != sdk.MessageRoleTool || !reflect.DeepEqual(got[1].Sources, call) ||
		call[0].ID != "call-row" || got[1].Anchor || got[2].Sources[0].ID != "later-row" {
		t.Fatalf("closure provenance = %+v, want the closure named by its call row", got)
	}
}

// A decision whose run ended keeps its outcome on the call and has no result
// row, so the read path renders the result from the decision. That result
// renders from the call's row as a closure does, and a result moved from its
// own row behind the call keeps that row: a fold takes each with what it
// renders from.
func TestContextProvenanceKeepsTheSourceOfAMovedDecisionResult(t *testing.T) {
	t.Parallel()

	stored := func(id, role string, message sdk.Message) historyfrag.HistoryRecord {
		raw, err := historyfrag.MarshalStoredSDKMessage(message)
		if err != nil {
			t.Fatal(err)
		}
		record, err := historyfrag.FromDBMessage(messagepkg.Message{ID: id, Role: role, Content: raw}, historyfrag.ScopeFallback{})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	decided := func(id string) sdk.MessagePart {
		return sdk.ToolCallPart{
			ToolCallID: id, ToolName: "ask_user", Input: toolexec.ArgumentsFromValue(map[string]any{}),
			ProviderMetadata: partmeta.Set(nil, partmeta.KeyUserInput, map[string]any{"status": "canceled"}),
		}
	}
	records := []historyfrag.HistoryRecord{
		stored("call-row", "assistant", sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{decided("ask-1"), decided("ask-2")}}),
		stored("narration-row", "assistant", sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.TextPart{Text: "flushed late"},
			sdk.ToolCallPart{ToolCallID: "other", ToolName: "exec", Input: toolexec.ArgumentsFromValue(map[string]any{})},
		}}),
		stored("result-row", "tool", sdk.ToolMessage(
			sdk.ToolResultPart{ToolCallID: "ask-2", ToolName: "ask_user", Result: toolexec.OutputFromValue(map[string]any{"answer": "b"})},
			sdk.ToolResultPart{ToolCallID: "other", ToolName: "exec", Result: toolexec.OutputFromValue("kept")},
		)),
	}
	input := make([]ModelMessage, 0, len(records))
	for _, record := range records {
		input = append(input, record.ModelMessage)
	}
	messages, _, _ := normalizeContextMessages(input, nil, nil)
	got := contextProvenance(messages, records, nil)
	if len(got) != len(messages) {
		t.Fatalf("provenance = %+v, want every message named", got)
	}
	sources := map[string]string{}
	for _, p := range got {
		for _, part := range p.Message.Content {
			if result, ok := part.(sdk.ToolResultPart); ok {
				sources[result.ToolCallID] = p.Sources[0].ID
			}
		}
	}
	if sources["ask-1"] != "call-row" || sources["ask-2"] != "result-row" || sources["other"] != "result-row" {
		t.Fatalf("result sources = %v, want the decision's result from its call row and the moved result from its own row", sources)
	}
}

// A continuation renders everything as history: the turn before, the run's
// request row, its deferred call and the approved result. Each message names
// the row it was loaded from, a summary its artifact, and a pipeline message
// its inbound source; only the request row and the live input are kept in
// place, and a message with no source is not part of the view.
func TestContextProvenanceNamesWhatEachMessageRenders(t *testing.T) {
	t.Parallel()

	text := func(role, content string) ModelMessage {
		return ModelMessage{Role: role, Content: turn.NewTextContent(content)}
	}
	messages := []ModelMessage{
		text("user", "<summary>older</summary>"),
		text("user", "earlier question"),
		text("assistant", "earlier answer"),
		text("user", "continued request"),
		text("assistant", "deferred call"),
		text("tool", "approved result"),
		text("user", "unsourced"),
	}
	pipeline := text("user", "pipeline message")
	pipeline.ContextSource = &turn.ContextMessageSource{Kind: "external", ID: "rc-1"}
	live := text("user", "live input")
	live.ContextSource = &turn.ContextMessageSource{Kind: "external", ID: "in-1", Current: true}
	messages = append(messages, pipeline, live)
	records := []historyfrag.HistoryRecord{{ModelMessage: messages[0], SourceKind: historyfrag.SourceCompactionLog, Ref: contextfrag.ContextRef{ID: "artifact-0"}}}
	for i, id := range []string{"old-user", "old-assistant", "request", "call", "result"} {
		records = append(records, historyfrag.HistoryRecord{ModelMessage: messages[i+1], DBMessageID: id})
	}
	req := ChatRequest{PersistedUserMessageID: "request", inputInHistory: true}

	got := contextProvenance(messages, records, compactionProtectedSources(req))
	want := []struct {
		kind, id string
		anchor   bool
	}{
		{"summary", "artifact-0", false},
		{"history", "old-user", false},
		{"history", "old-assistant", false},
		{"history", "request", true},
		{"history", "call", false},
		{"history", "result", false},
		{"external", "rc-1", false},
		{"external", "in-1", true},
	}
	if len(got) != len(want) {
		t.Fatalf("provenance for %d messages, want %d", len(got), len(want))
	}
	for i, w := range want {
		source := turn.ContextMessageSource{Kind: w.kind, ID: w.id}
		if len(got[i].Sources) != 1 || got[i].Sources[0].Kind != source.Kind || got[i].Sources[0].ID != source.ID || got[i].Anchor != w.anchor {
			t.Fatalf("message %d renders %v anchor=%v, want %v anchor=%v", i, got[i].Sources, got[i].Anchor, source, w.anchor)
		}
	}
	if !reflect.DeepEqual(stepProvenance([]messagepkg.Message{
		{ID: "steer", Role: "user"}, {ID: "feedback", Role: "user", Metadata: map[string]any{messagepkg.MessageSourceMetadataKey: messagepkg.MessageSourceInternalFeedback}}, {ID: "call", Role: "assistant"},
	}, []string{"steer"}), []native.MessageProvenance{
		{Sources: []turn.ContextMessageSource{{Kind: "history", ID: "steer"}}, Anchor: true},
		{Sources: []turn.ContextMessageSource{{Kind: "history", ID: "feedback"}}},
		{Sources: []turn.ContextMessageSource{{Kind: "history", ID: "call"}}},
	}) {
		t.Fatal("a step's steer is kept in place; its runtime feedback and output are not")
	}
}

type switchRunner struct {
	cfg    compaction.TriggerConfig
	passes []compaction.Pass
	err    error
}

func (r *switchRunner) Compact(_ context.Context, cfg compaction.TriggerConfig) ([]compaction.Pass, error) {
	r.cfg = cfg
	return r.passes, r.err
}

// The event an armed run asks for reads the sources its request renders, not
// a window of the session, keeps the run's input and steers in place and its
// latest step outside the history budget, and splices every pass it commits
// into the request, which must then fit. A failed event names its reason.
func TestSwitchContextFoldsTheRequestView(t *testing.T) {
	t.Parallel()

	rows := func(ids ...string) []turn.ContextMessageSource {
		sources := make([]turn.ContextMessageSource, len(ids))
		for i, id := range ids {
			sources[i] = turn.ContextMessageSource{Kind: "history", ID: id}
		}
		return sources
	}
	passes := []compaction.Pass{
		{ArtifactID: "a1", Summary: "first", Covered: rows("old-user")},
		{ArtifactID: "a2", Summary: "second", Covered: rows("old-user", "call"), Absorbed: []string{"a1"}},
	}
	for _, tc := range []struct {
		name   string
		saved  int // tokens each spliced pass takes off the request
		err    error
		want   error
		reason string
	}{
		{name: "fits", saved: 500},
		{name: "still over", saved: 100, reason: "still_over", want: contextfrag.ErrBudgetUnsatisfied},
		{name: "summarizer down", err: &compaction.EventError{Reason: compaction.ReasonSummarizerFailed, Err: errors.New("503")}, reason: "summarizer_failed", want: contextfrag.ErrBudgetUnsatisfied},
		{name: "protected overflow", err: &compaction.EventError{Reason: compaction.ReasonProtectedOverflow, Err: errors.New("tail")}, reason: "protected_overflow", want: contextfrag.ErrProtectedContextOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service, _ := newControllerPolicyService(t)
			runner := &switchRunner{passes: passes, err: tc.err}
			if tc.err != nil {
				runner.passes = nil
			}
			service.compactionService = runner
			committer := &agentStepCommitter{inputSourceIDs: []string{"input"}}
			var folds []native.ContextFold
			sw := native.ContextSwitch{
				View: rows("old-user", "request", "call", "steer", "latest"), Anchors: rows("request", "steer"), Tail: rows("latest"),
				HistoryBudgetTokens: 900, UsageTokens: 3000, AllowanceTokens: 2000,
				Fold: func(fold native.ContextFold) int {
					folds = append(folds, fold)
					return 3000 - tc.saved*len(folds)
				},
			}
			req := ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID, RunID: "run"}

			detail, err := service.switchContext(req, committer, "")(t.Context(), sw)
			cfg := runner.cfg
			tail := turn.ContextMessageSource{Kind: "history", ID: "latest", Current: true}
			if !reflect.DeepEqual(cfg.View, sw.View) || !reflect.DeepEqual(cfg.Anchors, rows("request", "steer", "input")) || !cfg.CandidatesFrom.IsZero() ||
				!reflect.DeepEqual(cfg.ProtectedSources, []turn.ContextMessageSource{tail}) || cfg.HistoryBudgetTokens != 900 {
				t.Fatalf("event config view=%v anchors=%v from=%v protected=%v budget=%d", cfg.View, cfg.Anchors, cfg.CandidatesFrom, cfg.ProtectedSources, cfg.HistoryBudgetTokens)
			}
			if tc.want == nil {
				if err != nil || len(folds) != 2 || folds[1].Artifact != "a2" || !reflect.DeepEqual(folds[1].Summary, compaction.SummaryMessage("second")) ||
					!reflect.DeepEqual(folds[1].Absorbed, []string{"a1"}) || detail != "trigger=in_run passes=2 request_tokens=3000->2000 allowance=2000" {
					t.Fatalf("err=%v folds=%+v detail=%q, want both passes spliced into a request that fits", err, folds, detail)
				}
				return
			}
			if !errors.Is(err, tc.want) || !strings.HasSuffix(detail, "reason="+tc.reason) {
				t.Fatalf("err=%v detail=%q, want %v with reason %s", err, detail, tc.want, tc.reason)
			}
		})
	}
}

// A retry renders its input from the request it replaces, which its first
// step hides; the event reads and keeps in place the copy that step wrote,
// with the same content.
func TestSwitchContextReadsARetriedRequestFromItsCopy(t *testing.T) {
	t.Parallel()

	service, _ := newControllerPolicyService(t)
	runner := &switchRunner{passes: []compaction.Pass{{ArtifactID: "a1", Summary: "s"}}}
	service.compactionService = runner
	committer := &agentStepCommitter{inputSourceIDs: []string{"copy"}, retriedRequest: "request", requestCopy: "copy"}
	request := turn.ContextMessageSource{Kind: "history", ID: "request", ContentHash: "h"}
	older := turn.ContextMessageSource{Kind: "history", ID: "older"}
	sw := native.ContextSwitch{
		View: []turn.ContextMessageSource{older, request}, Anchors: []turn.ContextMessageSource{request},
		Tail: []turn.ContextMessageSource{{Kind: "history", ID: "latest"}}, HistoryBudgetTokens: 900, UsageTokens: 3000, AllowanceTokens: 2000,
		Fold: func(native.ContextFold) int { return 1000 },
	}
	req := ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID, RunID: "run"}
	if _, err := service.switchContext(req, committer, "")(t.Context(), sw); err != nil {
		t.Fatal(err)
	}
	copied := turn.ContextMessageSource{Kind: "history", ID: "copy", ContentHash: "h"}
	if !reflect.DeepEqual(runner.cfg.View, []turn.ContextMessageSource{older, copied}) ||
		!reflect.DeepEqual(runner.cfg.Anchors, []turn.ContextMessageSource{copied, {Kind: "history", ID: "copy"}}) ||
		!reflect.DeepEqual(sw.View, []turn.ContextMessageSource{older, request}) {
		t.Fatalf("view=%v anchors=%v run view=%v, want the copy read and kept in place of the hidden request", runner.cfg.View, runner.cfg.Anchors, sw.View)
	}
}
