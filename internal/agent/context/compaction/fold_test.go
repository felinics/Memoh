package compaction

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/agent/turn"
)

// scriptedSummarizer answers fork requests in turn, repeating its last
// answer, and records each request.
type scriptedSummarizer struct {
	answers  []sdk.ModelResult
	requests []sdk.Request
}

func (s *scriptedSummarizer) summarize(_ context.Context, req sdk.Request) (sdk.ModelResult, error) {
	s.requests = append(s.requests, req)
	answer := s.answers[min(len(s.requests), len(s.answers))-1]
	return answer, nil
}

func summaryAnswer(text string) sdk.ModelResult {
	return sdk.ModelResult{Text: text, FinishReason: sdk.FinishReasonStop}
}

// foldSession is a session whose history rows the requests below render.
type foldSession struct {
	t       *testing.T
	q       *fakeQueries
	svc     *Service
	botID   string
	session string
}

func newFoldSession(t *testing.T) *foldSession {
	t.Helper()
	q := &fakeQueries{}
	return &foldSession{t: t, q: q, svc: newMachineryService(q), botID: uuid.NewString(), session: uuid.NewString()}
}

// row adds a context row to the session and returns the source naming it.
func (s *foldSession) row() turn.ContextMessageSource {
	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	s.q.history = append(s.q.history, candidateRow{ID: id, Role: "user", Content: []byte(`"row"`)})
	return turn.ContextMessageSource{Kind: "history", ID: id.String()}
}

func callMessage(id string) sdk.Message {
	return sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{ToolCallID: id, ToolName: "read"}}}
}

func resultMessage(id, output string) sdk.Message {
	return sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: id, ToolName: "read", Result: toolexec.OutputFromValue(output)})
}

func historySource(sources ...turn.ContextMessageSource) Source {
	return Source{Sources: sources}
}

// request is one run's next request: the checkpoint's summary, three rows
// of earlier work, the run's input, and the latest step. The earlier step
// is larger than the verbatim budget leaves beside the latest one, so cut,
// where the messages the event keeps after the summary start, is the latest
// step.
type request struct {
	messages []sdk.Message
	sources  []Source
	cut      int
	rows     []turn.ContextMessageSource
}

func (s *foldSession) request() request {
	held := s.q.hold("Earlier work summarized.")
	summary := turn.ContextMessageSource{Kind: "summary", ID: summaryIDs(held)[0]}
	rows := []turn.ContextMessageSource{s.row(), s.row(), s.row(), s.row(), s.row(), s.row()}
	return request{
		messages: []sdk.Message{
			SummaryMessage("Earlier work summarized."),
			sdk.UserMessage("Please audit the parser module and list every unsafe cast you find in it."),
			callMessage("call_1"),
			resultMessage("call_1", strings.Repeat("parser.go:12 unsafe cast of int64 to int32\n", 500)),
			sdk.UserMessage("RUN_INPUT: fix the casts the audit found"),
			callMessage("call_2"),
			resultMessage("call_2", "patched parser.go"),
		},
		sources: []Source{
			historySource(summary),
			historySource(rows[0]),
			historySource(rows[1]),
			historySource(rows[2]),
			{Sources: []turn.ContextMessageSource{rows[3]}, Anchor: true},
			historySource(rows[4]),
			historySource(rows[5]),
		},
		cut:  5,
		rows: rows,
	}
}

func (s *foldSession) event(r request, summarizer *scriptedSummarizer) Event {
	req := sdk.Request{Model: "run-model", System: "You are the bot.", Messages: r.messages, Tools: []sdk.ToolDefinition{{Name: "read", Description: "read a file"}}}
	return Event{
		BotID:           s.botID,
		SessionID:       s.session,
		Request:         req,
		Sources:         r.sources,
		Window:          32000,
		Allowance:       24000,
		MaxOutputTokens: 8000,
		Summarizer:      summarizer.summarize,
		Apply: func(pass Pass) (sdk.Request, []Source) {
			messages, sources := splice(req.Messages, r.sources, pass)
			req.Messages, r.sources, r.cut = messages, sources, pass.cut
			return req, sources
		},
	}
}

// replaced is what a pass replaced of the request it read: the sources of
// every message its Order leaves out, rows and summaries alike.
func replaced(sources []Source, pass Pass) []turn.ContextMessageSource {
	kept := make(map[int]bool, len(pass.Order))
	for _, i := range pass.Order {
		kept[i] = true
	}
	var out []turn.ContextMessageSource
	for i, source := range sources {
		if !kept[i] {
			out = append(out, source.Sources...)
		}
	}
	return out
}

// A fold forks the request the run would send: the prefix before the latest
// step, which the verbatim budget keeps, is the run's own, byte for byte, and
// one instruction follows it. The summary replaces the checkpoint's summary
// and the work before the run's input; the input stays, and the checkpoint
// moves past the folded rows.
func TestFoldForksTheRequestBeforeTheStepItKeeps(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("The parser audit found one unsafe cast at parser.go:12.")}}

	passes, err := s.svc.Fold(context.Background(), s.event(r, summarizer))
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v, want one pass", passes, err)
	}
	if len(summarizer.requests) != 1 {
		t.Fatalf("summarizer calls = %d, want 1", len(summarizer.requests))
	}
	fork := summarizer.requests[0]
	if fork.System != "You are the bot." || len(fork.Tools) != 1 || fork.Model != "run-model" || fork.MaxTokens == nil || *fork.MaxTokens > 8000 {
		t.Fatalf("fork request changed what the run sends: system=%q tools=%d model=%q max_tokens=%v", fork.System, len(fork.Tools), fork.Model, fork.MaxTokens)
	}
	if len(fork.Messages) != r.cut+1 || !slices.EqualFunc(fork.Messages[:r.cut], r.messages[:r.cut], messagesEqual) {
		t.Fatalf("fork messages are not the run's prefix plus an instruction: %d messages", len(fork.Messages))
	}
	instruction := messageText(fork.Messages[r.cut])
	for _, want := range []string{instructionMarker, "<kept>", "RUN_INPUT: fix the casts", "ref=" + r.rows[2].ID + ":0"} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("instruction lacks %q:\n%s", want, instruction)
		}
	}
	pass := passes[0]
	folded := replaced(r.sources, pass)
	if pass.More || !slices.Contains(folded, r.sources[0].Sources[0]) {
		t.Fatalf("pass = %+v, want the earlier summary absorbed and nothing left", pass)
	}
	for _, row := range r.rows[:3] {
		if !slices.Contains(folded, row) {
			t.Fatalf("replaced = %v, want the earlier work %s replaced", folded, row.ID)
		}
	}
	for _, row := range r.rows[3:] {
		if slices.Contains(folded, row) {
			t.Fatalf("replaced = %v, the input and the latest step must stay", folded)
		}
	}
	held := s.q.held()
	if len(held) != 1 || held[0].SummaryID != pass.SummaryID {
		t.Fatalf("active checkpoint holds %+v, want only the new summary", held)
	}
	if position := s.q.checkpoints[s.q.active]; position.TurnPosition != s.q.ordered()[2].TurnPosition.Int64 {
		t.Fatalf("checkpoint position %d, want the last folded row %d", position.TurnPosition, s.q.ordered()[2].TurnPosition.Int64)
	}
}

// Runtime context has no rows: it stays where it is, no summary stands for
// it, and the instruction tells the summarizer to leave it out. A closure
// the read path wrote for a call goes with the call.
func TestFoldLeavesRuntimeContextAndTakesClosuresWithTheirCalls(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	memory := sdk.UserMessage("MEMORY_RECALL: the user prefers small patches")
	abandoned := callMessage("call_0")
	closure := resultMessage("call_0", "tool execution interrupted before a response was recorded")
	callRow := s.row()
	r.messages = slices.Insert(r.messages, 1, abandoned, closure)
	r.sources = slices.Insert(r.sources, 1, historySource(callRow), historySource(callRow))
	r.messages = slices.Insert(r.messages, 6, memory)
	r.sources = slices.Insert(r.sources, 6, Source{})
	r.cut += 3
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("Audit done; one cast found.")}}

	passes, err := s.svc.Fold(context.Background(), s.event(r, summarizer))
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v", passes, err)
	}
	instruction := messageText(summarizer.requests[0].Messages[r.cut])
	if !strings.Contains(instruction, "<runtime_context>") || !strings.Contains(instruction, "MEMORY_RECALL") {
		t.Fatalf("instruction does not name the runtime context:\n%s", instruction)
	}
	if folded := replaced(r.sources, passes[0]); !slices.Contains(folded, callRow) {
		t.Fatalf("replaced = %v, want the abandoned call's row", folded)
	}
	messages, _ := splice(r.messages, r.sources, passes[0])
	for _, message := range messages {
		text := messageText(message)
		if strings.Contains(text, "call_0") || message.Role == sdk.MessageRoleTool && strings.Contains(text, "interrupted") {
			t.Fatalf("the spliced request keeps the abandoned call or its closure: %q", text)
		}
	}
	if !slices.ContainsFunc(messages, func(m sdk.Message) bool { return strings.Contains(messageText(m), "MEMORY_RECALL") }) {
		t.Fatal("the spliced request lost the runtime context")
	}
}

// When the request before the step the event keeps does not leave the
// summarizer room to answer in its window, a pass reads less, ending at a
// tool-group boundary, and the next pass forks the spliced request again.
func TestFoldReadsLessWhenThePrefixFillsTheWindow(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	second := s.row()
	secondResult := s.row()
	// The lexer audit takes more than the verbatim budget leaves beside the
	// latest step, so the event keeps only that step.
	r.messages[3] = resultMessage("call_1", strings.Repeat("parser.go:12 unsafe cast of int64 to int32\n", 40))
	r.messages = slices.Insert(r.messages, 4, callMessage("call_3"), resultMessage("call_3", strings.Repeat("lexer.go:40 unsafe cast of uint to int\n", 80)))
	r.sources = slices.Insert(r.sources, 4, historySource(second), historySource(secondResult))
	r.cut += 2
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("The parser part of the audit is summarized."), summaryAnswer("The parser and lexer audits are summarized.")}}
	ev := s.event(r, summarizer)
	// The window holds one long result and an answer, not both results.
	ev.Window = forkTokens(sdk.Request{System: ev.Request.System, Messages: r.messages[:4], Tools: ev.Request.Tools}) + 2200
	ev.Allowance = 4000
	ev.MaxOutputTokens = 4000
	apply, applied, read := ev.Apply, 0, [][]Source{r.sources}
	ev.Apply = func(pass Pass) (sdk.Request, []Source) {
		applied++
		req, sources := apply(pass)
		read = append(read, sources)
		return req, sources
	}

	passes, err := s.svc.Fold(context.Background(), ev)
	if err != nil || len(passes) != 2 || applied != 2 {
		t.Fatalf("passes=%d applied=%d err=%v, want two passes, each spliced into the caller's request", len(passes), applied, err)
	}
	if !passes[0].More || passes[1].More {
		t.Fatalf("more = %v,%v, want the first pass to leave work for the second", passes[0].More, passes[1].More)
	}
	first, next := summarizer.requests[0], summarizer.requests[1]
	if read := len(first.Messages) - 1; read != 4 {
		t.Fatalf("the first pass read %d messages, want the 4 up to the second call", read)
	}
	if !strings.Contains(messageText(next.Messages[0]), "The parser part of the audit is summarized.") {
		t.Fatalf("the second pass did not read the first pass's summary: %q", messageText(next.Messages[0]))
	}
	if next.System != first.System || !slices.EqualFunc(next.Tools, first.Tools, func(a, b sdk.ToolDefinition) bool { return a.Name == b.Name }) || len(next.Tools) != 1 {
		t.Fatalf("the second fork sends system %q and %d tools, want the run's", next.System, len(next.Tools))
	}
	if folded := replaced(read[1], passes[1]); !slices.Contains(folded, turn.ContextMessageSource{Kind: "summary", ID: passes[0].SummaryID}) || !slices.Contains(folded, secondResult) {
		t.Fatalf("second pass replaced %v, want the first summary and the lexer audit", folded)
	}
}

// A fork of a run's own request answers with the output settings the
// request carries, a thinking budget sized for them among them: it asks for
// no output cap the run does not ask for, and for no other one.
func TestFoldKeepsTheOutputSettingsOfARunsRequest(t *testing.T) {
	t.Parallel()
	runCap := 8000
	for _, tc := range []struct {
		name      string
		maxTokens *int
	}{
		{name: "the provider's own cap"},
		{name: "a requested cap", maxTokens: &runCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newFoldSession(t)
			r := s.request()
			summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("Audit summarized.")}}
			ev := s.event(r, summarizer)
			ev.Request.MaxTokens, ev.FixedOutput = tc.maxTokens, true

			if passes, err := s.svc.Fold(context.Background(), ev); err != nil || len(passes) != 1 {
				t.Fatalf("passes=%d err=%v, want one pass", len(passes), err)
			}
			if got := summarizer.requests[0].MaxTokens; got != tc.maxTokens {
				t.Fatalf("fork max_tokens = %v, want the request's own %v", got, tc.maxTokens)
			}
		})
	}
}

// A summarizer whose output is fixed is never asked for less: a pass reads
// only what the window holds beside all of it and leaves the rest to the next
// pass, where one that may answer in less reads up to the cut at once.
func TestFoldReadsWhatTheWindowHoldsBesideAFixedOutput(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	r.messages[3] = resultMessage("call_1", strings.Repeat("parser.go:12 unsafe cast of int64 to int32\n", 600))
	r.messages = slices.Insert(r.messages, 4, callMessage("call_3"), resultMessage("call_3", strings.Repeat("lexer.go:40 unsafe cast of uint to int\n", 600)))
	r.sources = slices.Insert(r.sources, 4, historySource(s.row()), historySource(s.row()))
	ev := s.event(r, &scriptedSummarizer{})
	// A small summary budget, so no window below reads either audit cut.
	ev.Allowance = 4000
	ev.retention = retain(ev, nil)
	whole, err := planFold(ev, nil)
	if err != nil || !whole.final() || ev.cut != r.cut+2 {
		t.Fatalf("plan end=%d cut=%d err=%v, want one read up to the latest step in a roomy window", whole.end, ev.cut, err)
	}
	// The window holds the whole read with half of the output beside it.
	read := append(slices.Clone(whole.fork), sdk.UserMessage(whole.instruction+noToolsReminder))
	ev.Window = forkTokens(sdk.Request{System: ev.Request.System, Messages: read, Tools: ev.Request.Tools}) + instructionNumberSlack + ev.MaxOutputTokens/2

	if flexible, err := planFold(ev, nil); err != nil || !flexible.final() || flexible.maxTokens >= ev.MaxOutputTokens {
		t.Fatalf("plan end=%d max_tokens=%d err=%v, want the whole read with less output", flexible.end, flexible.maxTokens, err)
	}
	ev.FixedOutput = true
	fixed, err := planFold(ev, nil)
	if err != nil || fixed.final() || fixed.end != 4 || fixed.maxTokens != ev.MaxOutputTokens {
		t.Fatalf("plan end=%d max_tokens=%d err=%v, want the read up to the second call with all %d tokens of output", fixed.end, fixed.maxTokens, err, ev.MaxOutputTokens)
	}
}

// A message larger than the window the summarizer reads in is no wall: the
// fork reads its head and tail, and the summary stands for all of it.
func TestFoldReadsAMessageLargerThanItsWindowByItsHeadAndTail(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	huge := "HUGE_HEAD " + strings.Repeat("payload ", 12000) + " HUGE_TAIL"
	r.messages[1] = sdk.UserMessage(huge)
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("The audit request and its findings are summarized.")}}
	ev := s.event(r, summarizer)
	ev.Window, ev.Allowance, ev.MaxOutputTokens = 8000, 6000, 2000
	if tokens := forkTokens(sdk.Request{Messages: r.messages[1:2]}); tokens <= ev.Window {
		t.Fatalf("the message takes %d tokens, want more than the %d-token window", tokens, ev.Window)
	}

	passes, err := s.svc.Fold(context.Background(), ev)
	if err != nil || len(passes) != 1 || !slices.Contains(replaced(r.sources, passes[0]), r.rows[0]) {
		t.Fatalf("passes=%+v err=%v, want one pass that stands for the large message", passes, err)
	}
	fork := summarizer.requests[0]
	read := messageText(fork.Messages[1])
	if forkTokens(fork) > ev.Window || !strings.Contains(read, "HUGE_HEAD") || !strings.Contains(read, "HUGE_TAIL") || !strings.Contains(read, "[memoh pruned]") {
		t.Fatalf("fork = %d tokens, read %d bytes of the message, want its head and tail within the window", forkTokens(fork), len(read))
	}
	if messageText(fork.Messages[0]) != messageText(r.messages[0]) || len(fork.Messages) != r.cut+1 {
		t.Fatalf("the fork changed a message it can read whole")
	}
}

// A call whose arguments are larger than the window, or a result that is, is
// folded the same way: the fork sends their head and tail under the same call,
// so the result still pairs with it, measured as the fork encodes them, where
// markup and quotes grow.
func TestFoldReadsToolPartsLargerThanItsWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, line string
		inResult   bool
	}{
		{name: "markup arguments", line: `<div class="row"><a href="/x?a=1&b=2">item</a></div>`},
		{name: "markup result", line: `<div class="row"><a href="/x?a=1&b=2">item</a></div>`, inResult: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newFoldSession(t)
			r := s.request()
			content := "CONTENT_HEAD " + strings.Repeat(tc.line, 160000/len(tc.line)) + " CONTENT_TAIL"
			if tc.inResult {
				r.messages[3] = sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call_1", ToolName: "read", Result: toolexec.OutputFromValue(map[string]any{"page": content})})
			} else {
				r.messages[2] = sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{
					ToolCallID: "call_1", ToolName: "write", Input: sdk.ParseToolArguments(`{"path":"big.html","content":` + strconv.Quote(content) + `}`),
				}}}
			}
			summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("A large page was handled.")}}
			ev := s.event(r, summarizer)
			ev.Window, ev.Allowance, ev.MaxOutputTokens = 8000, 6000, 2000

			passes, err := s.svc.Fold(context.Background(), ev)
			if folded := replaced(r.sources, passes[0]); err != nil || len(passes) != 1 || !slices.Contains(folded, r.rows[1]) || !slices.Contains(folded, r.rows[2]) {
				t.Fatalf("passes=%+v err=%v, want one pass that stands for the large call and its result", passes, err)
			}
			fork := summarizer.requests[0]
			read := fork.Messages[2].Content[0].(sdk.ToolCallPart).Input.String()
			if tc.inResult {
				read = fork.Messages[3].Content[0].(sdk.ToolResultPart).Result.String()
			}
			if tokens := forkTokens(sdk.Request{Messages: fork.Messages[2:4]}); tokens > forkGroupCap(ev) {
				t.Fatalf("the cut call group takes %d tokens as the fork encodes it, want at most %d", tokens, forkGroupCap(ev))
			}
			if forkTokens(fork) > ev.Window || !strings.Contains(read, "CONTENT_HEAD") || !strings.Contains(read, "CONTENT_TAIL") ||
				fork.Messages[2].Content[0].(sdk.ToolCallPart).ToolCallID != "call_1" || fork.Messages[3].Content[0].(sdk.ToolResultPart).ToolCallID != "call_1" {
				t.Fatalf("fork = %d tokens in a %d-token window, read %d bytes, want the call and result paired with their head and tail", forkTokens(fork), ev.Window, len(read))
			}
		})
	}
}

// A pass reads a call group whole or not at all, so the cap is the group's: a
// call and its result that each fit a fork but not together keep their head
// and tail within a share of it each, and one pass folds both.
func TestFoldReadsACallGroupLargerThanItsWindow(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	content := "CONTENT_HEAD " + strings.Repeat("line of the file ", 600) + " CONTENT_TAIL"
	r.messages[2] = sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{
		ToolCallID: "call_1", ToolName: "write", Input: sdk.ParseToolArguments(`{"path":"big.txt","content":` + strconv.Quote(content) + `}`),
	}}}
	r.messages[3] = resultMessage("call_1", "RESULT_HEAD "+strings.Repeat("parser.go:12 unsafe cast of int64 to int32\n", 300)+" RESULT_TAIL")
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("A large file was written and audited.")}}
	ev := s.event(r, summarizer)
	ev.Window, ev.Allowance, ev.MaxOutputTokens = 8000, 6000, 2000
	planned := ev
	planned.retention = retain(planned, nil)
	capTokens := forkGroupCap(planned)
	call, result := forkTokens(sdk.Request{Messages: r.messages[2:3]}), forkTokens(sdk.Request{Messages: r.messages[3:4]})
	if call > capTokens || result > capTokens || call+result <= capTokens {
		t.Fatalf("call %d and result %d tokens, want each within the %d-token cap and both over it", call, result, capTokens)
	}

	passes, err := s.svc.Fold(context.Background(), ev)
	if err != nil || len(passes) != 1 || len(summarizer.requests) != 1 {
		t.Fatalf("passes=%d err=%v summarizer calls=%d, want one pass from one call", len(passes), err, len(summarizer.requests))
	}
	if folded := replaced(r.sources, passes[0]); !slices.Contains(folded, r.rows[1]) || !slices.Contains(folded, r.rows[2]) {
		t.Fatalf("replaced = %v, want the call and its result folded", folded)
	}
	fork := summarizer.requests[0]
	read := messageText(fork.Messages[2]) + fork.Messages[2].Content[0].(sdk.ToolCallPart).Input.String() + fork.Messages[3].Content[0].(sdk.ToolResultPart).Result.String()
	if group := forkTokens(sdk.Request{Messages: fork.Messages[2:4]}); group > capTokens || forkTokens(fork) > ev.Window {
		t.Fatalf("the group takes %d tokens and the fork %d, want at most %d and %d", group, forkTokens(fork), capTokens, ev.Window)
	}
	for _, want := range []string{"CONTENT_HEAD", "CONTENT_TAIL", "RESULT_HEAD", "RESULT_TAIL"} {
		if !strings.Contains(read, want) {
			t.Fatalf("the fork lost %q of the group", want)
		}
	}
}

// The parts of a group share its cap however its messages split them: a
// message of many results takes more of it than a message of one text, so a
// group that fits once each part keeps its head and tail is read whole.
func TestFoldReadsAGroupOfManyPartsWithinItsCap(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	calls := []sdk.MessagePart{sdk.TextPart{Text: "PLAN_HEAD " + strings.Repeat("plan ", 1600)}}
	var results []sdk.MessagePart
	for i := range 32 {
		id := "call_" + strconv.Itoa(i)
		calls = append(calls, sdk.ToolCallPart{ToolCallID: id, ToolName: "read", Input: sdk.ParseToolArguments(`{}`)})
		results = append(results, sdk.ToolResultPart{ToolCallID: id, ToolName: "read", Result: sdk.TextOutput("RESULT_" + strconv.Itoa(i) + "_HEAD " + strings.Repeat("r", 10000))})
	}
	r.messages[2] = sdk.Message{Role: sdk.MessageRoleAssistant, Content: calls}
	r.messages[3] = sdk.Message{Role: sdk.MessageRoleTool, Content: results}
	ev := s.event(r, &scriptedSummarizer{})
	ev.Window, ev.Allowance, ev.MaxOutputTokens, ev.FixedOutput = 14000, 9000, 5000, true
	ev.retention = retain(ev, nil)

	read, err := forkRead(ev)
	if err != nil {
		t.Fatalf("forkRead: %v, want the group cut within its cap", err)
	}
	if tokens := forkTokens(sdk.Request{Messages: read[2:4]}); tokens > forkGroupCap(ev) {
		t.Fatalf("the group takes %d tokens, want at most %d", tokens, forkGroupCap(ev))
	}
	for i, part := range read[3].Content {
		result := part.(sdk.ToolResultPart)
		if call := read[2].Content[i+1].(sdk.ToolCallPart); call.ToolCallID != result.ToolCallID || !strings.Contains(result.Result.String(), "RESULT_"+strconv.Itoa(i)+"_HEAD") {
			t.Fatalf("result %d = %q under call %s, want its head paired with its call", i, result.Result.String(), call.ToolCallID)
		}
	}
}

// A group is cut until it fits or no part can be cut shorter, however many
// rounds of measuring that takes: one whose many small parts shrink no
// further fits only once its one long part is cut close to the shortest cut
// a part keeps.
func TestCutGroupCutsUntilNoPartCanBeShorter(t *testing.T) {
	t.Parallel()
	calls := []sdk.MessagePart{sdk.TextPart{Text: strings.Repeat("a", 1<<20)}}
	var results []sdk.MessagePart
	for i := range 200 {
		id := "call_" + strconv.Itoa(i)
		calls = append(calls, sdk.ToolCallPart{ToolCallID: id, ToolName: "read", Input: sdk.ParseToolArguments(`{}`)})
		results = append(results, sdk.ToolResultPart{ToolCallID: id, ToolName: "read", Result: sdk.TextOutput("ok")})
	}
	group := []sdk.Message{{Role: sdk.MessageRoleAssistant, Content: calls}, {Role: sdk.MessageRoleTool, Content: results}}
	cutAt := func(bytes int) int {
		limit := contextlimit.ToolOutputLimit{MaxBytes: bytes}
		return forkTokens(sdk.Request{Messages: []sdk.Message{cutParts(group[0], limit), cutParts(group[1], limit)}})
	}

	capTokens := cutAt(1000)
	read, ok := cutGroup(group, capTokens)
	if tokens := forkTokens(sdk.Request{Messages: read}); !ok || tokens > capTokens {
		t.Fatalf("cut=%v to %d tokens, want the group cut within %d", ok, tokens, capTokens)
	}
	if _, ok := cutGroup(group, cutAt(256)-1); ok {
		t.Fatal("a cap below the shortest cut was met")
	}
}

// A cut keeps a group's reasoning as the request carries it: a provider that
// signs reasoning takes a tool loop back only with its reasoning unchanged.
// The reasoning is priced before the parts a cut shortens, and a group whose
// reasoning alone is over the cap does not fit.
func TestCutGroupKeepsReasoningWhole(t *testing.T) {
	t.Parallel()
	thought := sdk.ReasoningPart{ID: "rs_1", Text: "THOUGHT " + strings.Repeat("why ", 400), Format: "anthropic", Model: "claude-sonnet-5", ProviderMetadata: sdk.ProviderMetadata{"anthropic": {"signature": "sig"}}}
	group := []sdk.Message{
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			thought,
			sdk.ToolCallPart{ToolCallID: "call_1", ToolName: "read", Input: sdk.ParseToolArguments(`{}`)},
		}},
		{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{
			sdk.ToolResultPart{ToolCallID: "call_1", ToolName: "read", Result: sdk.TextOutput("RESULT_HEAD " + strings.Repeat("r", 200000))},
		}},
	}
	reasoningTokens := forkTokens(sdk.Request{Messages: []sdk.Message{{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{thought}}}})
	capTokens := reasoningTokens + 2000

	read, ok := cutGroup(group, capTokens)
	if tokens := forkTokens(sdk.Request{Messages: read}); !ok || tokens > capTokens {
		t.Fatalf("cut=%v to %d tokens, want the group cut within %d", ok, tokens, capTokens)
	}
	if got, ok := read[0].Content[0].(sdk.ReasoningPart); !ok || !reflect.DeepEqual(got, thought) {
		t.Fatalf("the cut changed the reasoning: %#v", read[0].Content[0])
	}
	if result := read[1].Content[0].(sdk.ToolResultPart); !strings.Contains(result.Result.String(), "RESULT_HEAD") || len(result.Result.String()) >= 200000 {
		t.Fatalf("the result was not cut to its head and tail: %d bytes", len(result.Result.String()))
	}
	if _, ok := cutGroup(group, reasoningTokens); ok {
		t.Fatal("a group whose reasoning alone fills the cap was cut to fit")
	}
}

// A system prompt the prompt cache moved into the messages is read whole by
// every pass: the fork leaves it as it is and cuts the message it cannot
// read whole to what the window has beside it.
func TestFoldReadsAPromotedSystemPromptWhole(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	system := sdk.Message{Role: sdk.MessageRoleSystem, Content: []sdk.MessagePart{sdk.TextPart{Text: "You are the bot. " + strings.Repeat("Follow the house rules closely. ", 600)}}}
	r.messages[1] = sdk.UserMessage("HUGE_HEAD " + strings.Repeat("payload ", 12000) + " HUGE_TAIL")
	r.messages = append([]sdk.Message{system}, r.messages...)
	r.sources = append([]Source{{}}, r.sources...)
	r.cut++
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("The audit request and its findings are summarized.")}}
	ev := s.event(r, summarizer)
	ev.Request.System = ""
	ev.Window, ev.Allowance, ev.MaxOutputTokens = 12000, 9000, 2000

	passes, err := s.svc.Fold(context.Background(), ev)
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v, want one pass", passes, err)
	}
	fork := summarizer.requests[0]
	if forkTokens(fork) > ev.Window || messageText(fork.Messages[0]) != messageText(system) || !strings.Contains(messageText(fork.Messages[2]), "HUGE_TAIL") {
		t.Fatalf("fork = %d tokens in a %d-token window, want the system prompt whole and the large message cut beside it", forkTokens(fork), ev.Window)
	}
}

// A summarizer that calls a tool is asked once more with the same prefix and
// output, which the window holds with the reminder the instruction gains; a
// second tool call fails the event.
func TestFoldAsksAgainOnceWhenTheSummarizerCallsATool(t *testing.T) {
	t.Parallel()
	toolCall := sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, ToolCalls: []sdk.ToolCall{{ToolCallID: "x", ToolName: "read"}}}
	for _, tc := range []struct {
		name    string
		answers []sdk.ModelResult
		wantErr Reason
	}{
		{name: "answers the second time", answers: []sdk.ModelResult{toolCall, summaryAnswer("Audit summarized.")}},
		{name: "calls a tool twice", answers: []sdk.ModelResult{toolCall, toolCall}, wantErr: ReasonSummarizerFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newFoldSession(t)
			r := s.request()
			summarizer := &scriptedSummarizer{answers: tc.answers}
			ev := s.event(r, summarizer)
			// The window, not the cap, bounds what the fork may answer.
			ev.MaxOutputTokens = 64000
			passes, err := s.svc.Fold(context.Background(), ev)
			if ReasonOf(err) != tc.wantErr || (tc.wantErr == "") != (len(passes) == 1) {
				t.Fatalf("passes=%d err=%v, want reason %q", len(passes), err, tc.wantErr)
			}
			if len(summarizer.requests) != 2 {
				t.Fatalf("summarizer calls = %d, want 2", len(summarizer.requests))
			}
			first, second := summarizer.requests[0], summarizer.requests[1]
			if !slices.EqualFunc(first.Messages[:r.cut], second.Messages[:r.cut], messagesEqual) || !strings.Contains(messageText(second.Messages[r.cut]), noToolsReminder) {
				t.Fatal("the second request changed the prefix or lacks the reminder")
			}
			if *first.MaxTokens >= ev.MaxOutputTokens || *second.MaxTokens != *first.MaxTokens || forkTokens(second)+*second.MaxTokens > ev.Window {
				t.Fatalf("max_tokens %d then %d, the second taking %d tokens of a %d-token window: want the same output, within the window", *first.MaxTokens, *second.MaxTokens, forkTokens(second)+*second.MaxTokens, ev.Window)
			}
		})
	}
}

// A latest step larger than the verbatim budget folds with what is before
// it, in the same pass: the input stays where it is, and the instruction asks
// for where the work stands and names the ref the step's result reads back
// by.
func TestFoldFoldsALatestStepLargerThanTheBudget(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	r.messages[6] = resultMessage("call_2", strings.Repeat("parser.go: patched cast\n", 1200))
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("The audit found casts at parser.go:12; the patch rewrote each of them.")}}
	ev := s.event(r, summarizer)
	if step := forkTokens(sdk.Request{Messages: r.messages[5:]}); step <= verbatimBudget(ev) {
		t.Fatalf("the latest step takes %d tokens, want more than the %d-token budget", step, verbatimBudget(ev))
	}

	passes, err := s.svc.Fold(context.Background(), ev)
	if err != nil || len(passes) != 1 || len(summarizer.requests) != 1 {
		t.Fatalf("passes=%d err=%v summarizer calls=%d, want one pass from one call", len(passes), err, len(summarizer.requests))
	}
	folded := replaced(r.sources, passes[0])
	for _, row := range []turn.ContextMessageSource{r.rows[0], r.rows[4], r.rows[5]} {
		if !slices.Contains(folded, row) {
			t.Fatalf("replaced = %v, want the earlier work and the latest step folded", folded)
		}
	}
	if slices.Contains(folded, r.rows[3]) || passes[0].cut != len(passes[0].Order) {
		t.Fatalf("pass = %+v, want the input kept and nothing after the summary", passes[0])
	}
	fork := summarizer.requests[0]
	if len(fork.Messages) != len(r.messages)+1 {
		t.Fatalf("fork read %d messages, want the whole request and the instruction", len(fork.Messages)-1)
	}
	instruction := messageText(fork.Messages[len(fork.Messages)-1])
	if !strings.Contains(instruction, "End the summary with where the work stands") || !strings.Contains(instruction, "call_2 ref="+r.rows[5].ID+":0") {
		t.Fatalf("instruction does not hand the step over to the summary:\n%s", instruction)
	}
}

// Anchors and call groups share one verbatim budget, anchors first: an input
// that takes nearly all of it stays where it is, and the latest step, small
// as it is, folds with the earlier work.
func TestFoldAnchorsAndStepsShareTheBudget(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("The audit and the patch are summarized.")}}
	ev := s.event(r, summarizer)
	step := forkTokens(sdk.Request{Messages: r.messages[5:]})
	input := "RUN_INPUT: fix the casts the audit found" + strings.Repeat(" detail", 2000)
	for forkTokens(sdk.Request{Messages: []sdk.Message{sdk.UserMessage(input)}}) <= verbatimBudget(ev)-step {
		input += " detail"
	}
	r.messages[4] = sdk.UserMessage(input)
	ev = s.event(r, summarizer)
	if forkTokens(sdk.Request{Messages: r.messages[4:5]}) > verbatimBudget(ev) {
		t.Fatal("the input alone must fit the budget")
	}

	passes, err := s.svc.Fold(context.Background(), ev)
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%d err=%v, want one pass", len(passes), err)
	}
	folded := replaced(r.sources, passes[0])
	if slices.Contains(folded, r.rows[3]) || !slices.Contains(folded, r.rows[4]) || !slices.Contains(folded, r.rows[5]) {
		t.Fatalf("replaced = %v, want the input kept and the latest step folded", folded)
	}
}

// A message no context row holds stays whatever it takes, and takes it from
// the verbatim budget: a run's input before its first step writes its row
// leaves the earlier step no room, so the step folds.
func TestFoldChargesWhatNoRowHoldsToTheBudget(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	r.messages[3] = resultMessage("call_1", strings.Repeat("parser.go:12 unsafe cast of int64 to int32\n", 40))
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("The audit is summarized.")}}
	ev := s.event(r, summarizer)
	input := "RUN_INPUT: fix the casts the audit found"
	for forkTokens(sdk.Request{Messages: []sdk.Message{sdk.UserMessage(input)}}) <= verbatimBudget(ev) {
		input += " detail"
	}
	r.messages, r.sources = append(slices.Clone(r.messages[:4]), sdk.UserMessage(input)), append(slices.Clone(r.sources[:4]), Source{Sources: []turn.ContextMessageSource{{Kind: "history", ID: uuid.NewString()}}, Anchor: true})

	passes, err := s.svc.Fold(context.Background(), s.event(r, summarizer))
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%d err=%v, want one pass", len(passes), err)
	}
	if folded := replaced(r.sources, passes[0]); !slices.Contains(folded, r.rows[1]) || !slices.Contains(folded, r.rows[2]) {
		t.Fatalf("replaced = %v, want the earlier step folded beside the unwritten input", folded)
	}
}

// The input after the newest step, which the next call answers, never folds,
// however little room it leaves: the event fails as protected before it asks
// the summarizer.
func TestFoldKeepsTheInputTheNextCallAnswers(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	r.messages, r.sources, r.cut = r.messages[:5], r.sources[:5], 4
	// Folded, the input would leave a summary room; kept, it leaves none.
	r.messages[4] = sdk.UserMessage("RUN_INPUT: fix the casts the audit found" + strings.Repeat(" in every file", 100))
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("unused")}}
	ev := s.event(r, summarizer)
	ev.Allowance = forkTokens(sdk.Request{System: ev.Request.System, Messages: r.messages[r.cut:], Tools: ev.Request.Tools}) + 10

	_, err := s.svc.Fold(context.Background(), ev)
	if ReasonOf(err) != ReasonProtectedOverflow || len(summarizer.requests) != 0 {
		t.Fatalf("err=%v summarizer calls=%d, want protected_overflow before any call", err, len(summarizer.requests))
	}
}

// A request composed on a checkpoint the session no longer has matches no
// fold onto the active one.
func TestFoldRefusesARequestOfAnotherCheckpoint(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	r.sources[0] = historySource(turn.ContextMessageSource{Kind: "summary", ID: uuid.NewString()})
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("unused")}}

	_, err := s.svc.Fold(context.Background(), s.event(r, summarizer))
	if ReasonOf(err) != ReasonStaleSource || !errors.Is(err, errStaleSource) || len(summarizer.requests) != 0 {
		t.Fatalf("err=%v summarizer calls=%d, want stale_source before any call", err, len(summarizer.requests))
	}
}

// An edit or retry folds onto the checkpoint its replacement keeps: the
// event rewinds before it reads the active checkpoint.
func TestFoldRewindsBeforeTheTurnAReplacementReplaces(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("Audit summarized.")}}
	ev := s.event(r, summarizer)
	ev.ReplacedTurnID = uuid.NewString()

	if _, err := s.svc.Fold(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(s.q.rewinds) != 1 || s.q.rewinds[0].TurnID.String() != ev.ReplacedTurnID {
		t.Fatalf("rewinds = %+v, want one before the replaced turn", s.q.rewinds)
	}
}

func messagesEqual(a, b sdk.Message) bool {
	return a.Role == b.Role && messageText(a) == messageText(b) && len(a.Content) == len(b.Content)
}

// However much room the next request has, a summary may take at most a
// share of the room beside what every request carries, so a fold leaves room
// for the turns after it; the fork's output cap stays above the length it
// asks for.
func TestFoldBoundsTheSummaryBelowTheRoom(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	// The earlier step takes more than a large window's verbatim budget.
	r.messages[3] = resultMessage("call_1", strings.Repeat("parser.go:12 unsafe cast of int64 to int32\n", 3000))
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("Audit summarized.")}}
	ev := s.event(r, summarizer)
	ev.Window, ev.Allowance, ev.MaxOutputTokens = 200000, 168000, 64000

	if _, err := s.svc.Fold(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	fork := summarizer.requests[0]
	if stated := summaryLimitIn(t, fork); stated <= 0 || stated > summaryBudget(ev) || *fork.MaxTokens <= stated {
		t.Fatalf("stated limit %d, max_tokens %d: want at most %d and below the output cap", stated, *fork.MaxTokens, summaryBudget(ev))
	}
}

// inbound adds a context row written from an inbound or self-sent message
// and returns the message's external ID.
func (s *foldSession) inbound(external string) string {
	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	s.q.history = append(s.q.history, candidateRow{ID: id, Role: "user", Content: []byte(`"row"`), ExternalMessageID: pgtype.Text{String: external, Valid: true}})
	return external
}

// A message that renders a source no context row holds cannot be folded:
// the checkpoint has nothing to stand for, so it stays where it is and the
// summary leaves it alone. A self-sent message folds by the row it was
// written to, so the request's own splice removes it.
func TestFoldStandsOnlyForWhatRowsHold(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	r := s.request()
	echo := turn.ContextMessageSource{Kind: "self", ID: s.inbound("echo-1")}
	ghost := turn.ContextMessageSource{Kind: "external", ID: "never-written"}
	r.messages = slices.Insert(r.messages, 1, sdk.UserMessage("the bot said so"+strings.Repeat(" e", 400)), sdk.UserMessage("a message no row holds"))
	r.sources = slices.Insert(r.sources, 1, historySource(echo), historySource(ghost))
	r.cut += 2
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("The parser audit and the bot's note are summarized.")}}

	passes, err := s.svc.Fold(context.Background(), s.event(r, summarizer))
	if err != nil || len(passes) != 1 {
		t.Fatalf("passes=%+v err=%v, want one pass", passes, err)
	}
	if folded := replaced(r.sources, passes[0]); !slices.Contains(folded, echo) || slices.Contains(folded, ghost) {
		t.Fatalf("replaced = %v, want the self-sent message folded by its row and the rowless one left", folded)
	}
	instruction := messageText(summarizer.requests[0].Messages[len(summarizer.requests[0].Messages)-1])
	if !strings.Contains(instruction, "a message no row holds") {
		t.Fatalf("the instruction does not name the message that stays:\n%s", instruction)
	}
	messages, _ := splice(r.messages, r.sources, passes[0])
	if slices.ContainsFunc(messages, func(m sdk.Message) bool { return strings.HasPrefix(messageText(m), "the bot said so") }) ||
		!slices.ContainsFunc(messages, func(m sdk.Message) bool { return messageText(m) == "a message no row holds" }) {
		t.Fatal("the splice did not remove the folded self-sent message and keep the rowless one")
	}
}

// When nothing but summaries is left to fold, an event rewrites them only
// when they do not fit the room the next request has, and the rewrite must
// fit it; summaries that fit are left alone.
func TestFoldRewritesSummariesOnlyToFitTheRoom(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		allowance int
		passes    int
	}{
		{name: "fits", allowance: 24000},
		{name: "over the room", allowance: 1000, passes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newFoldSession(t)
			earlier := strings.Repeat("The earlier work is long. ", 200)
			held := s.q.hold(earlier)
			input := s.row()
			messages := []sdk.Message{SummaryMessage(earlier), sdk.UserMessage("RUN_INPUT"), callMessage("call_1"), resultMessage("call_1", "done")}
			sources := []Source{historySource(turn.ContextMessageSource{Kind: "summary", ID: summaryIDs(held)[0]}), {Sources: []turn.ContextMessageSource{input}, Anchor: true}, historySource(s.row()), historySource(s.row())}
			summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("Earlier work, shortened.")}}
			ev := s.event(request{messages: messages, sources: sources, cut: 2}, summarizer)
			ev.Allowance = tc.allowance

			passes, err := s.svc.Fold(context.Background(), ev)
			if err != nil || len(passes) != tc.passes {
				t.Fatalf("passes=%d err=%v, want %d", len(passes), err, tc.passes)
			}
			if tc.passes == 0 {
				return
			}
			if folded := replaced(sources, passes[0]); !slices.Equal(folded, sources[0].Sources) || passes[0].More {
				t.Fatalf("pass = %+v, want the summary rewritten and nothing else folded", passes[0])
			}
			if limit := summaryLimitIn(t, summarizer.requests[0]); limit > tc.allowance {
				t.Fatalf("the rewrite may take %d tokens, over the %d the request has", limit, tc.allowance)
			}
		})
	}
}

// A residue too small for any summary to replace ends the event once the
// request fits, rather than failing it as ineffective.
func TestFoldLeavesAResidueTooSmallToSummarize(t *testing.T) {
	t.Parallel()
	s := newFoldSession(t)
	held := s.q.hold("Earlier work summarized.")
	input := s.row()
	messages := []sdk.Message{SummaryMessage("Earlier work summarized."), sdk.AssistantMessage("ok"), sdk.UserMessage("RUN_INPUT"), callMessage("call_1"), resultMessage("call_1", "done")}
	sources := []Source{historySource(turn.ContextMessageSource{Kind: "summary", ID: summaryIDs(held)[0]}), historySource(s.row()), {Sources: []turn.ContextMessageSource{input}, Anchor: true}, historySource(s.row()), historySource(s.row())}
	summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("unused")}}

	passes, err := s.svc.Fold(context.Background(), s.event(request{messages: messages, sources: sources, cut: 3}, summarizer))
	if err != nil || len(passes) != 0 || len(summarizer.requests) != 0 {
		t.Fatalf("passes=%d err=%v summarizer calls=%d, want the fitting request left alone", len(passes), err, len(summarizer.requests))
	}
}

// A Discuss run keeps the inputs it answers while they leave a tenth of the
// room for the summary; otherwise anchors before the newest step take the
// verbatim budget, a quarter of the allowance here. Older anchors fold
// either way.
func TestFoldKeepsAnchorsByPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		policy AnchorPolicy
		folded int
	}{
		{name: "capped", policy: AnchorsCapped, folded: 3},
		{name: "inputs first", policy: AnchorsFirst, folded: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newFoldSession(t)
			held := s.q.hold("Earlier talk summarized.")
			messages := []sdk.Message{SummaryMessage("Earlier talk summarized.")}
			sources := []Source{historySource(turn.ContextMessageSource{Kind: "summary", ID: summaryIDs(held)[0]})}
			var inputs []turn.ContextMessageSource
			for i := range 4 {
				input := turn.ContextMessageSource{Kind: "external", ID: s.inbound("input-" + strconv.Itoa(i))}
				inputs = append(inputs, input)
				messages = append(messages, sdk.UserMessage("input "+strconv.Itoa(i)+strings.Repeat(" i", 1200)))
				sources = append(sources, Source{Sources: []turn.ContextMessageSource{input}, Anchor: true})
			}
			messages = append(messages, callMessage("call_9"), resultMessage("call_9", "done"))
			sources = append(sources, historySource(s.row()), historySource(s.row()))
			summarizer := &scriptedSummarizer{answers: []sdk.ModelResult{summaryAnswer("Earlier talk and the oldest inputs summarized.")}}
			ev := s.event(request{messages: messages, sources: sources, cut: len(messages)}, summarizer)
			ev.Request.Tools, ev.Allowance, ev.Window, ev.AnchorPolicy = nil, 3200, 12000, tc.policy

			passes, err := s.svc.Fold(context.Background(), ev)
			if err != nil || len(passes) != 1 {
				t.Fatalf("passes=%d err=%v, want one pass", len(passes), err)
			}
			for i, input := range inputs {
				if folded := slices.Contains(replaced(sources, passes[0]), input); folded != (i < tc.folded) {
					t.Fatalf("input %d folded=%v, want the %d oldest folded", i, folded, tc.folded)
				}
			}
		})
	}
}

// summaryLimitIn is the summary length a fork request's instruction states.
func summaryLimitIn(t *testing.T, fork sdk.Request) int {
	t.Helper()
	instruction := messageText(fork.Messages[len(fork.Messages)-1])
	_, after, ok := strings.Cut(instruction, "must stay under ")
	if !ok {
		t.Fatalf("the instruction states no summary limit:\n%s", instruction)
	}
	limit, err := strconv.Atoi(strings.Fields(after)[0])
	if err != nil {
		t.Fatal(err)
	}
	return limit
}
