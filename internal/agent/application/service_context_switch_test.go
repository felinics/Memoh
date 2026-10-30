package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
)

func TestContextProvenanceKeepsTheStoredRowThroughReasoningProjection(t *testing.T) {
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
	if len(got) != 1 || got[0].Sources[0] != (turn.ContextMessageSource{Kind: "history", ID: "checkpoint"}) || records[0].ModelMessage.TextContent() == record.ModelMessage.TextContent() {
		t.Fatalf("projected checkpoint lost stored identity: %+v", got)
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
	if len(got) != 2 || len(got[1].Sources) != 1 || got[1].Sources[0].ID != "result-row" {
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

	got := contextProvenance(messages, records, compactionInputs(req))
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
	event  compaction.Event
	passes []compaction.Pass
	err    error
}

func (*switchRunner) Compact(context.Context, compaction.TriggerConfig) ([]compaction.Pass, error) {
	return nil, errors.New("an armed run folds its request")
}

// Fold commits its passes the way the engine does: each is spliced into the
// caller's request as it commits.
func (r *switchRunner) Fold(_ context.Context, ev compaction.Event) ([]compaction.Pass, error) {
	r.event = ev
	if r.err != nil {
		return nil, r.err
	}
	for _, pass := range r.passes {
		ev.Request, ev.Sources = ev.Apply(pass)
	}
	return r.passes, nil
}

// The event an armed run asks for forks the request the run holds: its
// sources as the rows hold them, the run's input and steers anchors, and the
// run's own model as summarizer. Every
// pass it commits is spliced into the request, which must then fit. A failed
// event names its reason.
func TestSwitchContextFoldsTheRunsRequest(t *testing.T) {
	t.Parallel()

	rows := func(ids ...string) []turn.ContextMessageSource {
		sources := make([]turn.ContextMessageSource, len(ids))
		for i, id := range ids {
			sources[i] = turn.ContextMessageSource{Kind: "history", ID: id}
		}
		return sources
	}
	passes := []compaction.Pass{
		{SummaryID: "a1", Summary: "first", Order: []int{-1, 1, 2, 3, 4, 5}, More: true},
		{SummaryID: "a2", Summary: "second", Order: []int{1, -1, 3, 4, 5}},
	}
	provenance := []*native.MessageProvenance{
		{Sources: rows("old-user")}, {Sources: rows("request"), Anchor: true}, {Sources: rows("call")}, nil, {Sources: rows("steer")}, {Sources: rows("latest")},
	}
	for _, tc := range []struct {
		name   string
		saved  int // tokens each spliced pass takes off the request
		none   bool
		err    error
		want   error
		reason string
	}{
		{name: "fits", saved: 500},
		{name: "still over", saved: 100, reason: "still_over", want: contextfrag.ErrBudgetUnsatisfied},
		{name: "nothing folds", none: true, reason: "protected_overflow", want: contextfrag.ErrProtectedContextOverflow},
		{name: "summarizer down", err: &compaction.EventError{Reason: compaction.ReasonSummarizerFailed, Err: errors.New("503")}, reason: "summarizer_failed", want: contextfrag.ErrBudgetUnsatisfied},
		{name: "protected overflow", err: &compaction.EventError{Reason: compaction.ReasonProtectedOverflow, Err: errors.New("tail")}, reason: "protected_overflow", want: contextfrag.ErrProtectedContextOverflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service, _ := newControllerPolicyService(t)
			runner := &switchRunner{passes: passes, err: tc.err}
			if tc.none {
				runner.passes = nil
			}
			service.compactionService = runner
			committer := &agentStepCommitter{inputSourceIDs: []string{"steer"}}
			var folds []native.ContextFold
			request := sdk.Request{Model: "run-model", Messages: make([]sdk.Message, len(provenance))}
			summarized := false
			sw := native.ContextSwitch{
				Request: request, Provenance: provenance,
				UsageTokens: 3000, AllowanceTokens: 2000, WindowTokens: 4000, OutputTokens: 1000,
				Summarize: func(context.Context, sdk.Request) (sdk.ModelResult, error) {
					summarized = true
					return sdk.ModelResult{}, nil
				},
				Fold: func(fold native.ContextFold) native.ContextSplice {
					folds = append(folds, fold)
					return native.ContextSplice{Request: request, Provenance: provenance, Tokens: 3000 - tc.saved*len(folds)}
				},
			}
			req := ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID, RunID: "run"}

			detail, err := service.switchContext(req, committer, "model-row")(t.Context(), sw)
			ev := runner.event
			anchors := []bool{false, true, false, false, true, false}
			for i, source := range ev.Sources {
				if source.Anchor != anchors[i] || (provenance[i] == nil) != (len(source.Sources) == 0) {
					t.Fatalf("source %d = %+v, want anchor=%v and runtime context only where the run rebuilds it", i, source, anchors[i])
				}
			}
			if ev.Window != 4000 || ev.MaxOutputTokens != 1000 || !ev.FixedOutput || ev.Allowance != 2000 || ev.ModelRecordID != "model-row" || ev.Request.Model != "run-model" {
				t.Fatalf("event = %+v, want the run's request, window, fixed output and allowance", ev)
			}
			if _, _ = ev.Summarizer(t.Context(), sdk.Request{}); !summarized {
				t.Fatal("the event does not ask the run's own model")
			}
			if tc.want == nil {
				if err != nil || len(folds) != 2 || folds[1].SummaryID != "a2" || !reflect.DeepEqual(folds[1].Summary, compaction.SummaryMessage("second")) ||
					!slices.Equal(folds[1].Order, passes[1].Order) || detail != "trigger=in_run passes=2 request_tokens=3000->2000 allowance=2000" {
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
// step hides; the event folds onto and keeps in place the copy that step
// wrote, with the same content.
func TestSwitchContextReadsARetriedRequestFromItsCopy(t *testing.T) {
	t.Parallel()

	service, _ := newControllerPolicyService(t)
	runner := &switchRunner{passes: []compaction.Pass{{SummaryID: "a1", Summary: "s"}}}
	service.compactionService = runner
	committer := &agentStepCommitter{inputSourceIDs: []string{"copy"}, retriedRequest: "request", requestCopy: "copy"}
	request := turn.ContextMessageSource{Kind: "history", ID: "request"}
	older := turn.ContextMessageSource{Kind: "history", ID: "older"}
	provenance := []*native.MessageProvenance{{Sources: []turn.ContextMessageSource{older}}, {Sources: []turn.ContextMessageSource{request}, Anchor: true}, {Sources: []turn.ContextMessageSource{{Kind: "history", ID: "latest"}}}}
	sw := native.ContextSwitch{
		Request: sdk.Request{Messages: make([]sdk.Message, 3)}, Provenance: provenance, UsageTokens: 3000, AllowanceTokens: 2000,
		Fold: func(native.ContextFold) native.ContextSplice { return native.ContextSplice{Tokens: 1000} },
	}
	req := ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID, RunID: "run"}
	if _, err := service.switchContext(req, committer, "")(t.Context(), sw); err != nil {
		t.Fatal(err)
	}
	copied := turn.ContextMessageSource{Kind: "history", ID: "copy"}
	sources := runner.event.Sources
	if !reflect.DeepEqual(sources[0].Sources, []turn.ContextMessageSource{older}) || !reflect.DeepEqual(sources[1].Sources, []turn.ContextMessageSource{copied}) || !sources[1].Anchor ||
		!reflect.DeepEqual(provenance[1].Sources, []turn.ContextMessageSource{request}) {
		t.Fatalf("sources=%+v run provenance=%+v, want the copy folded and kept in place of the hidden request", sources, provenance[1])
	}
}

// The run's query renders the row that holds its request: the event names
// that row once the run's first step wrote it, so a fold reads the request
// and keeps it in place before the summary it commits.
func TestSwitchContextNamesTheRunsRequestByItsRow(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		inputs []string
		want   turn.ContextMessageSource
	}{
		{name: "written", inputs: []string{"request-row", "steer-row"}, want: turn.ContextMessageSource{Kind: "history", ID: "request-row"}},
		{name: "not written yet", want: requestSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service, _ := newControllerPolicyService(t)
			runner := &switchRunner{passes: []compaction.Pass{{SummaryID: "a1", Summary: "s"}}}
			service.compactionService = runner
			committer := &agentStepCommitter{inputSourceIDs: tc.inputs}
			provenance := []*native.MessageProvenance{
				{Sources: []turn.ContextMessageSource{{Kind: "history", ID: "older"}}},
				{Sources: []turn.ContextMessageSource{requestSource}, Anchor: true},
				{Sources: []turn.ContextMessageSource{{Kind: "history", ID: "latest"}}},
			}
			sw := native.ContextSwitch{
				Request: sdk.Request{Messages: make([]sdk.Message, 3)}, Provenance: provenance, UsageTokens: 3000, AllowanceTokens: 2000,
				Fold: func(native.ContextFold) native.ContextSplice { return native.ContextSplice{Tokens: 1000} },
			}
			req := ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID, RunID: "run"}
			if _, err := service.switchContext(req, committer, "")(t.Context(), sw); err != nil {
				t.Fatal(err)
			}
			if source := runner.event.Sources[1]; !source.Anchor || !reflect.DeepEqual(source.Sources, []turn.ContextMessageSource{tc.want}) {
				t.Fatalf("request source = %+v, want %+v kept in place", source, tc.want)
			}
		})
	}
}

// A Discuss run keeps the batch it answers while it leaves the summary room;
// any other run keeps its input and steers within a cap. An edit or retry
// that folds before its first step replaces the turn names that turn, so the
// event moves the session to the checkpoint the replacement keeps first.
func TestSwitchContextChoosesWhatAFoldKeeps(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		req      ChatRequest
		started  bool
		anchors  compaction.AnchorPolicy
		replaced string
	}{
		{name: "chat", anchors: compaction.AnchorsCapped},
		{name: "discuss", req: ChatRequest{SessionType: sessionpkg.TypeDiscuss}, anchors: compaction.AnchorsFirst},
		{name: "retry before its first step", req: ChatRequest{TurnReplacement: &messagepkg.TurnReplacement{OldTurnID: "old-turn"}}, anchors: compaction.AnchorsCapped, replaced: "old-turn"},
		{name: "retry after its first step", req: ChatRequest{TurnReplacement: &messagepkg.TurnReplacement{OldTurnID: "old-turn"}}, started: true, anchors: compaction.AnchorsCapped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			service, _ := newControllerPolicyService(t)
			runner := &switchRunner{passes: []compaction.Pass{{SummaryID: "a1", Summary: "s"}}}
			service.compactionService = runner
			req := tc.req
			req.BotID, req.ChatID, req.ThreadID, req.RunID = syncCompactBotID, syncCompactBotID, syncCompactThreadID, "run"
			committer := &agentStepCommitter{req: req, replacementStarted: tc.started}
			sw := native.ContextSwitch{
				Request: sdk.Request{Messages: make([]sdk.Message, 1)}, Provenance: []*native.MessageProvenance{{Sources: []turn.ContextMessageSource{{Kind: "history", ID: "row"}}}},
				UsageTokens: 3000, AllowanceTokens: 2000,
				Fold: func(native.ContextFold) native.ContextSplice { return native.ContextSplice{Tokens: 1000} },
			}
			if _, err := service.switchContext(req, committer, "")(t.Context(), sw); err != nil {
				t.Fatal(err)
			}
			if ev := runner.event; ev.AnchorPolicy != tc.anchors || ev.ReplacedTurnID != tc.replaced {
				t.Fatalf("anchors=%v replaced=%q, want %v and %q", ev.AnchorPolicy, ev.ReplacedTurnID, tc.anchors, tc.replaced)
			}
		})
	}
}
