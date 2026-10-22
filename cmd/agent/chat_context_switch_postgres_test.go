package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/application"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/schedule"
)

const (
	loopInput   = "ORIGINAL_INPUT_KEEP_EXACT: finish the numbered effects"
	loopSummary = "Earlier backlog summarized."
	busyInput   = "BUSY_TURN_KEEP"
	foreignRow  = "Background task dep-1 finished."
)

type effectInput struct {
	Index int `json:"index"`
}

type execInput struct {
	Command string `json:"command"`
}

// runEntry is one way a native run starts. The entries that fold are the
// in-run compaction coverage set; the others are declared outside it and must
// still end in a bounded, visible way. extra counts the provider calls a run
// makes before its first effect besides the first one.
type runEntry struct {
	name  string
	folds bool
	extra int
	send  func(s *effectLoop, text string) string
}

var runEntries = []runEntry{
	{name: "web", folds: true, send: (*effectLoop).chat},
	{name: "im", folds: true, send: (*effectLoop).chat},
	{name: "trigger", folds: true, send: (*effectLoop).trigger},
	{name: "follow-up", folds: true, send: (*effectLoop).followUp},
	{name: "approval", folds: true, extra: 1, send: (*effectLoop).decision},
	{name: "ask-user", folds: true, extra: 1, send: (*effectLoop).decision},
	{name: "retry", folds: true, send: (*effectLoop).retry},
	{name: "edit", folds: true, send: (*effectLoop).edit},
	{name: "discuss", folds: true, send: (*effectLoop).discuss},
}

// effectLoop scripts the provider for one native run at a time: the run whose
// input carries marker calls one numbered effect per step for its first steps
// calls, then answers. Any other request is answered at once.
type effectLoop struct {
	f        *backlogFixture
	pipeline *timeline.Pipeline
	mu       sync.Mutex
	marker   string
	steps    int
	calls    int
	start    int
	hold     chan struct{}
	// pendingDecision makes a run pause for approval or user input;
	// foreignAt is the effect during which a row the run never
	// renders is written into the session, and downAt the one after which
	// the summarizer stops answering, downFrom summarizer calls in.
	pendingDecision     bool
	askUser             bool
	decisionAfter       int
	decisionResultBytes int
	foreignAt           int
	downAt              int
	downFrom            int32
	// resultBytes sizes each effect's result; zero keeps the default.
	resultBytes int
	// userID is the account the runs act for; empty sends them for none.
	userID string
	// arrival is a Discuss message delivered during effect arriveAt; arrived
	// holds the segments delivered so, for the caller to render.
	arrival  string
	arriveAt int
	arrived  []timeline.RenderedSegment
	effects  []int
	requests []string
}

type approveExec struct{}

func (approveExec) ToolApprovalPolicy(context.Context, string) (toolapproval.PolicyConfig, error) {
	return toolapproval.PolicyConfig{Enabled: true, Exec: toolapproval.ExecPolicy{Mode: toolapproval.PolicyModeAsk}}, nil
}

type allowEveryone struct{}

func (allowEveryone) HasBotPermission(context.Context, string, string, string) (bool, error) {
	return true, nil
}

func newEffectLoop(t *testing.T, entry runEntry) *effectLoop {
	t.Helper()
	f := newBacklogFixture(t)
	s := &effectLoop{f: f}
	f.stream = s.serve
	f.agent.SetToolProviders([]tools.ToolProvider{s})
	if _, err := f.pool.Exec(f.ctx, `UPDATE models SET config=config || '{"compatibilities":["tool-call"]}'::jsonb WHERE id=$1`, f.chatModel); err != nil {
		t.Fatal(err)
	}
	if entry.name != "discuss" {
		if _, err := f.pool.Exec(f.ctx, `UPDATE bot_sessions SET type='chat', channel_type='local' WHERE id=$1`, f.sessionID); err != nil {
			t.Fatal(err)
		}
	}
	switch entry.name {
	case "im":
		s.pipeline = timeline.NewPipeline(timeline.RenderParams{})
		f.service.SetPipeline(s.pipeline)
	case "approval", "ask-user":
		f.service.SetToolApprovalService(toolapproval.NewService(nil, postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool)), approveExec{}))
		f.service.SetBotPermissionChecker(allowEveryone{})
		if entry.name == "ask-user" {
			s.askUser = true
			// The question schema and usage add overhead beside the same raw tail.
			s.window(9000)
			f.agent.SetToolProviders([]tools.ToolProvider{s, tools.NewAskUserProvider(nil)})
			f.service.SetUserInputService(userinput.NewService(nil, postgresstore.NewQueriesWithPool(f.pool, sqlc.New(f.pool))))
		}
	}
	return s
}

// window sets the context window of the model the runs use.
func (s *effectLoop) window(tokens int) {
	if _, err := s.f.pool.Exec(s.f.ctx, `UPDATE models SET config=config || jsonb_build_object('context_window', $2::int) WHERE id=$1`, s.f.chatModel, tokens); err != nil {
		s.f.t.Fatal(err)
	}
}

func effectResult(index, size int) string {
	return fmt.Sprintf("TOOL_RESULT_%03d ", index) + strings.Repeat("x", size)
}

//nolint:contextcheck // Tool execution, not discovery, owns each invocation's context.
func (s *effectLoop) Tools(context.Context, tools.SessionContext) ([]toolexec.Tool, error) {
	return []toolexec.Tool{
		toolexec.Define("numbered_effect", "Record a numbered effect and return its findings.", func(tc *toolexec.ToolExecContext, input effectInput) (sdk.ToolOutput, error) {
			s.mu.Lock()
			s.effects = append(s.effects, input.Index)
			size, foreign, arrive := cmp.Or(s.resultBytes, 4200), input.Index == s.foreignAt, input.Index == s.arriveAt
			if input.Index == s.downAt {
				s.downFrom = s.f.summaries.Load()
				s.f.failStatus.Store(http.StatusServiceUnavailable)
				s.f.failSummary.Store(true)
			}
			s.mu.Unlock()
			if foreign {
				if err := s.writeForeignRow(context.WithoutCancel(tc.Context)); err != nil {
					return sdk.ToolOutput{}, err
				}
			}
			if arrive {
				segment, err := s.f.deliver(context.WithoutCancel(tc.Context), uuid.NewString(), s.arrival, time.Now().Truncate(time.Millisecond))
				if err != nil {
					return sdk.ToolOutput{}, err
				}
				s.mu.Lock()
				s.arrived = append(s.arrived, segment)
				s.mu.Unlock()
			}
			return toolexec.OutputFromValue(effectResult(input.Index, size)), nil
		}),
		toolexec.Define("exec", "Run a command once it is approved.", func(_ *toolexec.ToolExecContext, _ execInput) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue("DECISION_RESULT_KEEP" + strings.Repeat("x", s.decisionResultBytes)), nil
		}),
	}, nil
}

// writeForeignRow writes into the session the way a background dependency
// notification does: an assistant row with no run, which the running request
// never renders.
func (s *effectLoop) writeForeignRow(ctx context.Context) error {
	f := s.f
	content, err := historyfrag.MarshalStoredModelMessage(turn.ModelMessage{Role: "assistant", Content: turn.NewTextContent(foreignRow)})
	if err != nil {
		return err
	}
	_, err = f.messages.Persist(ctx, messagepkg.PersistInput{
		BotID: f.botID, SessionID: f.sessionID, Role: "assistant", Content: content, DisplayText: foreignRow,
		Metadata: map[string]any{"background_task_id": "dep-1", "background_task_event": "completed"},
	})
	return err
}

func (s *effectLoop) serve(w http.ResponseWriter, body []byte) {
	request := string(body)
	s.mu.Lock()
	s.requests = append(s.requests, request)
	reply := fmt.Sprintf("REPLY_%03d", len(s.requests))
	call, arguments := "", ""
	if s.marker != "" && strings.Contains(request, s.marker) {
		switch {
		case s.pendingDecision && s.calls >= s.decisionAfter:
			s.pendingDecision, call, arguments = false, "exec", `{"command":"ls"}`
			if s.askUser {
				call, arguments = "ask_user", `{"questions":[{"text":"How should we proceed?","kind":"text"}]}`
			}
		case s.calls < s.steps:
			s.calls++
			call, arguments = "numbered_effect", fmt.Sprintf(`{"index":%d}`, s.calls)
		}
	}
	id := fmt.Sprintf("call_%03d", len(s.requests))
	hold := s.hold
	s.mu.Unlock()
	if hold != nil && strings.Contains(request, busyInput) {
		<-hold
	}
	delta, finish := map[string]any{"role": "assistant", "content": reply}, "stop"
	if call != "" {
		delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"index": 0, "id": id, "type": "function", "function": map[string]any{"name": call, "arguments": arguments},
		}}}
		finish = "tool_calls"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, choice := range []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
		chunk, _ := json.Marshal(map[string]any{"id": "loop", "object": "chat.completion.chunk", "choices": []any{choice}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

// run sends text through the entry as a run of steps effects and reports its
// error code with the provider requests the run made.
func (s *effectLoop) run(entry runEntry, text string, steps int) (string, []string) {
	s.mu.Lock()
	s.marker, s.steps, s.calls, s.effects, s.start = text, steps, 0, nil, len(s.requests)
	s.mu.Unlock()
	code := entry.send(s, text)
	s.mu.Lock()
	defer s.mu.Unlock()
	var requests []string
	for _, request := range s.requests[s.start:] {
		if strings.Contains(request, text) {
			requests = append(requests, request)
		}
	}
	return code, requests
}

func (s *effectLoop) command(text string) turn.StartTurnCommand {
	f := s.f
	return turn.StartTurnCommand{TeamID: f.cfg.TeamID, Mode: turn.ModeChat, BotID: f.botID, ChatID: f.botID, ThreadID: f.sessionID, UserID: s.userID, Query: text, CurrentChannel: "local"}
}

// chat runs one turn from the web UI, or from an IM channel whose message
// reaches the run through the timeline pipeline, and reports its error code.
func (s *effectLoop) chat(text string) string {
	cmd := s.command(text)
	if s.pipeline != nil {
		cmd.ExternalMessageID, cmd.EventID = s.push(text)
	}
	return s.startTurn(cmd)
}

// push delivers text to the IM pipeline and returns its message id.
func (s *effectLoop) push(text string) (string, string) {
	id, at := uuid.NewString(), time.Now().UnixMilli()
	eventID, event, err := s.f.cursor.PersistEvent(s.f.ctx, s.f.botID, s.f.sessionID, timeline.MessageEvent{
		SessionID: s.f.sessionID, MessageID: id, ReceivedAtMs: at, TimestampSec: at / 1000,
		Content: []timeline.ContentNode{{Type: "text", Text: text}}, Conversation: timeline.ConversationMeta{Channel: "telegram", ConversationType: "private"},
	})
	if err != nil {
		s.f.t.Fatal(err)
	}
	s.pipeline.PushEvent(s.f.sessionID, event)
	return id, eventID
}

func (s *effectLoop) startTurn(cmd turn.StartTurnCommand) string {
	f := s.f
	f.t.Helper()
	handle, err := f.service.StartTurn(f.ctx, cmd)
	if err != nil {
		f.t.Fatal(err)
	}
	for range handle.Events() {
	}
	if err := <-handle.Errs(); err != nil {
		return string(apperror.CodeOf(err))
	}
	return ""
}

// trigger fires a schedule whose command is text.
func (s *effectLoop) trigger(text string) string {
	f := s.f
	_, err := f.service.TriggerSchedule(f.ctx, f.botID, schedule.TriggerPayload{FireID: uuid.NewString(), ID: uuid.NewString(), Name: "effects", Command: text, OwnerUserID: f.ownerUserID, SessionID: f.sessionID}, "")
	if err != nil {
		return string(apperror.CodeOf(err))
	}
	return ""
}

// followUp queues text behind a run in progress. The queued turn starts when
// that run ends and is driven by the server.
func (s *effectLoop) followUp(text string) string {
	f := s.f
	f.t.Helper()
	var runs int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM session_runs WHERE session_id=$1`, f.sessionID).Scan(&runs); err != nil {
		f.t.Fatal(err)
	}
	hold := make(chan struct{})
	s.mu.Lock()
	s.hold = hold
	s.mu.Unlock()
	busy, err := f.service.StartTurn(f.ctx, s.command(busyInput))
	if err != nil {
		f.t.Fatal(err)
	}
	cmd := s.command(text)
	if _, err := f.service.StartTurn(f.ctx, cmd); !errors.Is(err, turn.ErrSessionBusy) {
		f.t.Fatalf("a turn was admitted while another runs: %v", err)
	}
	if err := f.service.EnqueueDeferredTurn(f.ctx, cmd); err != nil {
		f.t.Fatal(err)
	}
	close(hold)
	for range busy.Events() {
	}
	if err := <-busy.Errs(); err != nil {
		f.t.Fatal(err)
	}
	return s.lastRunCode(runs + 2)
}

// decision pauses a Web run for approval or user input, then resumes it
// through the corresponding decision API.
func (s *effectLoop) decision(text string) string {
	f := s.f
	f.t.Helper()
	s.mu.Lock()
	s.pendingDecision = s.steps > 0
	approve := s.pendingDecision
	s.mu.Unlock()
	var runs int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM session_runs WHERE session_id=$1`, f.sessionID).Scan(&runs); err != nil {
		f.t.Fatal(err)
	}
	if code := s.chat(text); code != "" || !approve {
		return code
	}
	var id, question string
	if s.askUser {
		if err := f.pool.QueryRow(f.ctx, `SELECT id::text, ui_payload_json->'questions'->0->>'id' FROM user_input_requests WHERE session_id=$1 AND status='pending'`, f.sessionID).Scan(&id, &question); err != nil {
			f.t.Fatal(err)
		}
	} else if err := f.pool.QueryRow(f.ctx, `SELECT id::text FROM tool_approval_requests WHERE session_id=$1 AND status='pending'`, f.sessionID).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	events := make(chan json.RawMessage)
	go func() {
		for range events {
		}
	}()
	var err error
	if s.askUser {
		err = f.service.RespondUserInput(f.ctx, turn.UserInputResponse{
			BotID: f.botID, ThreadID: f.sessionID, ActorUserID: f.ownerUserID, UserInputID: id,
			Answers: []turn.QuestionAnswer{{QuestionID: question, Text: "DECISION_RESULT_KEEP" + strings.Repeat("x", s.decisionResultBytes)}},
		}, events)
	} else {
		err = f.service.RespondToolApproval(f.ctx, turn.ToolApprovalResponse{BotID: f.botID, ThreadID: f.sessionID, ActorUserID: f.ownerUserID, ApprovalID: id, Decision: "approve"}, events)
	}
	close(events)
	if err != nil {
		return string(apperror.CodeOf(err))
	}
	return s.lastRunCode(runs + 1)
}

// lastRunCode waits until the session has runs runs and the latest one ended,
// and reports its error code.
func (s *effectLoop) lastRunCode(runs int) string {
	f := s.f
	f.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var count int
		var state, code string
		if err := f.pool.QueryRow(f.ctx, `SELECT count(*) OVER (), state, coalesce(error_code, '') FROM session_runs WHERE session_id=$1 ORDER BY turn_position DESC LIMIT 1`, f.sessionID).Scan(&count, &state, &code); err != nil {
			f.t.Fatal(err)
		}
		if count >= runs && !ledger.State(state).Active() {
			return code
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("the run did not finish: runs=%d state=%q", count, state)
		}
	}
}

// retry answers text from the web UI, then retries that turn the way the
// Web client does.
func (s *effectLoop) retry(text string) string {
	return s.replace(text, s.retryLatest)
}

// edit answers a draft from the web UI, then edits that turn into text.
func (s *effectLoop) edit(text string) string {
	return s.replace("DRAFT_BEFORE_EDIT", s.editLatest(text))
}

func (s *effectLoop) retryLatest(ctx context.Context, admission sessionruntime.Admission, target string, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) (application.RunOutcome, error) {
	f, position := s.f, admission.TurnPosition
	return f.service.RetryLatestMessageWS(ctx, application.RetryLatestMessageInput{
		BotID: f.botID, SessionID: f.sessionID, RunID: admission.RunID, TurnID: admission.TurnID, TurnPosition: &position,
		TargetTurnID: target, ActorChannelIdentityID: f.ownerUserID, ActorUserID: f.ownerUserID, RunHandle: admission.Handle,
	}, eventCh, abortCh)
}

func (s *effectLoop) editLatest(text string) replaceTurn {
	return func(ctx context.Context, admission sessionruntime.Admission, target string, eventCh chan<- application.WSStreamEvent, abortCh <-chan struct{}) (application.RunOutcome, error) {
		f, position := s.f, admission.TurnPosition
		return f.service.EditLatestMessageWS(ctx, application.EditLatestMessageInput{
			BotID: f.botID, SessionID: f.sessionID, RunID: admission.RunID, TurnID: admission.TurnID, TurnPosition: &position,
			TargetTurnID: target, Text: text, ActorChannelIdentityID: f.ownerUserID, ActorUserID: f.ownerUserID, RunHandle: admission.Handle,
		}, eventCh, abortCh)
	}
}

type replaceTurn func(context.Context, sessionruntime.Admission, string, chan<- application.WSStreamEvent, <-chan struct{}) (application.RunOutcome, error)

// replace answers first from the web UI, then replaces that turn through
// start.
func (s *effectLoop) replace(first string, start replaceTurn) string {
	f := s.f
	f.t.Helper()
	s.mu.Lock()
	steps := s.steps
	s.steps = 0
	s.mu.Unlock()
	if code := s.chat(first); code != "" {
		f.t.Fatalf("the turn to replace failed with %s", code)
	}
	s.mu.Lock()
	s.steps, s.calls, s.start = steps, 0, len(s.requests)
	s.mu.Unlock()
	return s.replaceLatest(start)
}

// replaceLatest replaces the latest turn through start: the new run is
// admitted, streamed over the WS surface and finished in the session runtime
// by the caller.
func (s *effectLoop) replaceLatest(start replaceTurn) string {
	f := s.f
	f.t.Helper()
	var targetTurnID string
	if err := f.pool.QueryRow(f.ctx, `SELECT turn_id FROM bot_history_messages WHERE session_id=$1 AND turn_id IS NOT NULL ORDER BY created_at DESC, id DESC LIMIT 1`, f.sessionID).Scan(&targetTurnID); err != nil {
		f.t.Fatal(err)
	}
	ctx, cancelCause := context.WithCancelCause(f.ctx)
	defer cancelCause(context.Canceled)
	abortCh := make(chan struct{}, 1)
	admission, err := f.manager.Admit(ctx, sessionruntime.AdmitInput{
		BotID: f.botID, SessionID: f.sessionID, InvocationID: uuid.NewString(), Payload: []byte(`{"kind":"retry_message"}`),
		Execution: sessionruntime.Execution{
			Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			},
			AbortCh: abortCh, Cancel: func() { cancelCause(context.Canceled) }, OwnershipCancel: cancelCause,
		},
	})
	if err != nil || !admission.Started {
		f.t.Fatalf("admit replacement: started=%v err=%v", admission.Started, err)
	}
	handle := admission.Handle
	ctx = runtimefence.WithContext(ctx, runtimefence.Fence{BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken})
	eventCh := make(chan application.WSStreamEvent, 256)
	forwarded := make(chan struct{})
	go func() {
		defer close(forwarded)
		for payload := range eventCh {
			var event native.StreamEvent
			if json.Unmarshal(payload, &event) == nil {
				_, _ = f.manager.HandleAgentEvent(f.ctx, handle, event)
			}
		}
	}()
	outcome, runErr := start(ctx, admission, targetTurnID, eventCh, abortCh)
	close(eventCh)
	<-forwarded
	switch {
	case runErr == nil:
	case errors.Is(runErr, context.Canceled):
		outcome = application.RunOutcome{}
	case outcome.Status == sessionruntime.RunStatusErrored && outcome.ErrorCode() != "":
	default:
		outcome = application.RunOutcome{Status: sessionruntime.RunStatusErrored, Cause: runErr}
	}
	if _, err := f.manager.FinishRunWithErrorCode(context.WithoutCancel(f.ctx), handle, outcome.Status, outcome.ErrorCode()); err != nil {
		f.t.Fatal(err)
	}
	return outcome.ErrorCode()
}

// discuss delivers text as an unanswered Discuss message, received now so
// the steps that answer it follow it, and lets the driver answer it.
func (s *effectLoop) discuss(text string) string {
	s.f.appendMessageAt(uuid.NewString(), text, time.Now().Truncate(time.Millisecond))
	return s.f.trigger()
}

// inRunScenario is one headline case of in-run compaction, run in a fresh
// session through one entry.
type inRunScenario struct {
	name string
	run  func(t *testing.T, entry runEntry, s *effectLoop)
}

var inRunScenarios = []inRunScenario{
	{name: "prefix-full", run: prefixFullScenario},
	{name: "long-loop", run: longLoopScenario},
	{name: "decision-after-progress", run: decisionAfterProgressScenario},
	{name: "decision-full-summary", run: decisionFullSummaryScenario},
	{name: "decision-keeps-prompt", run: decisionKeepsPromptScenario},
}

// TestPostgresInRunCompactionByEntry runs every headline scenario through
// every entry that folds; the entries outside the coverage set run the long
// loop only, to show they end bounded.
func TestPostgresInRunCompactionByEntry(t *testing.T) {
	for _, entry := range runEntries {
		for _, scenario := range inRunScenarios {
			if strings.HasPrefix(scenario.name, "decision-") && entry.name != "approval" && entry.name != "ask-user" {
				continue
			}
			if !entry.folds && scenario.name != "long-loop" {
				continue
			}
			t.Run(entry.name+"/"+scenario.name, func(t *testing.T) {
				scenario.run(t, entry, newEffectLoop(t, entry))
			})
		}
	}
}

// Replacing a turn that compacted in place leaves its summary behind: the
// replacement's first step hides the turn and moves the session to a new
// compaction epoch, so the attempt renders the history before the turn raw
// and folds it, with its own steps, into a summary of its own. Every effect
// of the attempt runs once, no summary of the current epoch covers a row of
// the replaced turn, and the next turn renders the attempt's summary without
// compacting.
func TestPostgresReplacingACompactedTurnFoldsAgain(t *testing.T) {
	const steps = 14
	web := runEntries[0]
	for name, replace := range map[string]func(*effectLoop) replaceTurn{
		"retry": func(s *effectLoop) replaceTurn { return s.retryLatest },
		"edit":  func(s *effectLoop) replaceTurn { return s.editLatest(loopInput) },
	} {
		t.Run(name, func(t *testing.T) {
			s := newEffectLoop(t, web)
			f := s.f
			if code, _ := s.run(web, "EARLIER_TURN_KEEP", 0); code != "" {
				t.Fatalf("earlier turn failed with %s", code)
			}
			if code, _ := s.run(web, loopInput, steps); code != "" || f.summaries.Load() == 0 {
				t.Fatalf("the turn to replace: code=%q summary_calls=%d, want it answered after compacting in place", code, f.summaries.Load())
			}
			epoch := func() (epoch int64) {
				if err := f.pool.QueryRow(f.ctx, `SELECT compaction_epoch FROM bot_sessions WHERE id=$1`, f.sessionID).Scan(&epoch); err != nil {
					t.Fatal(err)
				}
				return epoch
			}
			before, summaries := epoch(), f.summaries.Load()
			s.mu.Lock()
			s.marker, s.steps, s.calls, s.effects, s.start = loopInput, steps, 0, nil, len(s.requests)
			s.mu.Unlock()
			want := make([]int, steps)
			for i := range want {
				want[i] = i + 1
			}
			if code := s.replaceLatest(replace(s)); code != "" || !slices.Equal(s.effects, want) || f.summaries.Load() == summaries {
				t.Fatalf("replacement: code=%q effects=%v summary_calls=%d, want every effect once on a summary of its own", code, s.effects, f.summaries.Load()-summaries)
			}
			var active, coveredHidden int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(DISTINCT c.id), count(h.id) FILTER (WHERE NOT h.turn_visible)
				FROM bot_history_message_compacts c JOIN bot_sessions s ON s.id=c.session_id AND s.compaction_epoch=c.compaction_epoch
				LEFT JOIN bot_history_messages h ON h.compact_id=c.id
				WHERE c.session_id=$1 AND c.status='ok' AND c.superseded_by IS NULL`, f.sessionID).Scan(&active, &coveredHidden); err != nil {
				t.Fatal(err)
			}
			if epoch() == before || active != 1 || coveredHidden != 0 {
				t.Fatalf("epoch moved=%v active summaries=%d covering %d hidden rows, want the replaced turn's summary retired and one summary covering none of it", epoch() != before, active, coveredHidden)
			}
			s.window(16000)
			summaries = f.summaries.Load()
			code, requests := s.run(web, "NEXT_ROUND_QUESTION", 0)
			if code != "" || len(requests) != 1 || f.summaries.Load() != summaries ||
				strings.Count(requests[0], loopSummary) != 1 || strings.Count(requests[0], loopInput) != 1 {
				t.Fatalf("next turn: code=%q provider_calls=%d summary_calls=%d summaries=%d inputs=%d, want one call rendering the attempt's summary and input once",
					code, len(requests), f.summaries.Load()-summaries, strings.Count(requests[0], loopSummary), strings.Count(requests[0], loopInput))
			}
		})
	}
}

func decisionAfterProgressScenario(t *testing.T, entry runEntry, s *effectLoop) {
	s.decisionAfter, s.decisionResultBytes = 2, 9000
	code, requests := s.run(entry, loopInput, 4)
	if code != "" || !slices.Equal(s.effects, []int{1, 2, 3, 4}) || len(requests) != 6 {
		t.Fatalf("resume: code=%q effects=%v calls=%d, want all four effects completed once", code, s.effects, len(requests))
	}
	if s.f.summaries.Load() == 0 || strings.Count(requests[3], loopInput) != 1 || !strings.Contains(requests[3], "DECISION_RESULT_KEEP"+strings.Repeat("x", 9000)) {
		t.Fatal("resumed request must compact earlier progress and retain its input and approved result")
	}
}

// A run resumed by a decision keeps the system prompt of its first request:
// the continuation acts for the account the run was admitted for, not for
// nobody, so a user timezone the bot does not override survives the decision
// and every request of the run keeps the same prefix.
func decisionKeepsPromptScenario(t *testing.T, entry runEntry, s *effectLoop) {
	f := s.f
	if _, err := f.pool.Exec(f.ctx, `UPDATE users SET timezone='Asia/Shanghai' WHERE id=$1`, f.ownerUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE bots SET timezone=NULL WHERE id=$1`, f.botID); err != nil {
		t.Fatal(err)
	}
	// A server always records each run's admitted intent; it signs it with the
	// JWT secret, which the fixture otherwise leaves unset.
	f.service.SetResumeSecret("decision-keeps-prompt")
	s.userID, s.decisionAfter = f.ownerUserID, 1
	code, requests := s.run(entry, loopInput, 2)
	if code != "" || !slices.Equal(s.effects, []int{1, 2}) || len(requests) < 4 {
		t.Fatalf("resume: code=%q effects=%v requests=%d, want both effects around the decision", code, s.effects, len(requests))
	}
	system := requestMessages(t, requests[0])[0]
	if !strings.Contains(system, "Timezone: Asia/Shanghai") {
		t.Fatalf("first request does not carry the requester's timezone: %.200s", system)
	}
	for i, request := range requests[1:] {
		if got := requestMessages(t, request)[0]; got != system {
			t.Fatalf("request %d rewrote the system prompt the run started with", i+2)
		}
	}
}

// A resumed run whose summary takes all the room the summarizer is told it
// has still fits: the input the run keeps beside the summary is priced
// before that room is stated.
func decisionFullSummaryScenario(t *testing.T, entry runEntry, s *effectLoop) {
	s.f.longSummary.Store(true)
	decisionAfterProgressScenario(t, entry, s)
}

// requestMessages is each message of a provider request body, as sent.
func requestMessages(t *testing.T, request string) []string {
	t.Helper()
	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(request), &body); err != nil {
		t.Fatal(err)
	}
	messages := make([]string, len(body.Messages))
	for i, message := range body.Messages {
		messages[i] = string(message)
	}
	return messages
}

// summaryContents is every message of a provider request body that carries
// the scripted summary, in order.
func summaryContents(t *testing.T, request string) []string {
	t.Helper()
	var summaries []string
	for _, message := range requestMessages(t, request) {
		if strings.Contains(message, loopSummary) {
			summaries = append(summaries, message)
		}
	}
	return summaries
}

// firstKept is the oldest effect result a request still carries; the ones
// before it are gone from the request.
func firstKept(request string, upTo int) int {
	first := upTo + 1
	for index := upTo; index >= 1 && strings.Contains(request, fmt.Sprintf("TOOL_RESULT_%03d ", index)); index-- {
		first = index
	}
	return first
}

// markersInOrder reports whether each marker occurs in request after the one
// before it.
func markersInOrder(request string, markers ...string) bool {
	at := 0
	for _, marker := range markers {
		next := strings.Index(request[at:], marker)
		if next < 0 {
			return false
		}
		at += next
	}
	return true
}

// When the history a run starts with fills the request, the first step that
// does not fit folds that history into a summary and the run goes on: the
// effect runs once, the request keeps every message the fold does not cover
// exactly as it was sent, with the input and the step's result verbatim, and
// the next turn replays the same summary without compacting.
func prefixFullScenario(t *testing.T, entry runEntry, s *effectLoop) {
	f := s.f
	for i := range 3 {
		if code, _ := s.run(entry, fmt.Sprintf("PREFIX_TURN_%d ", i)+strings.Repeat("p", 2000), 0); code != "" {
			t.Fatalf("prefix turn %d failed with %s", i, code)
		}
	}
	const passive = "ambient group message"
	if s.pipeline != nil {
		s.push(passive)
	}
	summaries := f.summaries.Load()
	s.resultBytes = 12000
	if s.askUser {
		s.resultBytes = 9000
	}
	code, requests := s.run(entry, loopInput, 1)
	if code != "" || !slices.Equal(s.effects, []int{1}) || len(requests) != 2+entry.extra {
		t.Fatalf("code=%q effects=%v provider_calls=%d summary_calls=%d, want the run answered after one effect", code, s.effects, len(requests), f.summaries.Load()-summaries)
	}
	before, last := requests[len(requests)-2], requests[len(requests)-1]
	summary := summaryContents(t, last)
	if strings.Contains(before, loopSummary) || f.summaries.Load()-summaries != 1 || len(summary) != 1 || strings.Count(last, loopInput) != 1 ||
		!strings.Contains(last, "TOOL_RESULT_001 ") || strings.Contains(last, "PREFIX_TURN_0 ") || strings.Contains(last, "[tool result pruned") {
		t.Fatalf("summary_calls=%d summaries=%q input=%d result=%v raw_prefix=%v, want the earlier turns folded in one call at the step that did not fit, and the input and result raw",
			f.summaries.Load()-summaries, summary, strings.Count(last, loopInput), strings.Contains(last, "TOOL_RESULT_001 "), strings.Contains(last, "PREFIX_TURN_0 "))
	}
	sent := requestMessages(t, before)
	for _, message := range requestMessages(t, last) {
		at := slices.Index(sent, message)
		switch {
		case message == summary[0]:
		case at >= 0:
			sent = sent[at+1:]
		case !strings.Contains(message, "TOOL_RESULT_001 ") && !strings.Contains(message, `"name":"numbered_effect"`):
			t.Fatalf("the fold changed or reordered a message it did not cover: %.200s", message)
		}
	}
	if s.pipeline != nil && strings.Count(last, passive) != 1 {
		t.Fatal("the fold dropped a pipeline message it did not cover")
	}

	summaries = f.summaries.Load()
	code, requests = s.run(entry, "NEXT_ROUND_QUESTION", 0)
	if code != "" || len(requests) != 1 || f.summaries.Load() != summaries || len(s.effects) != 0 {
		t.Fatalf("next turn: code=%q provider_calls=%d summary_calls=%d effects=%v, want one call without compacting", code, len(requests), f.summaries.Load()-summaries, s.effects)
	}
	next := requests[0]
	summaryFirst := func(request string) bool {
		return strings.Index(request, loopSummary) < strings.Index(request, loopInput)
	}
	if replayed := summaryContents(t, next); !slices.Equal(replayed, summary) || summaryFirst(next) != summaryFirst(last) || !markersInOrder(next, loopInput, "TOOL_RESULT_001 ", "NEXT_ROUND_QUESTION") {
		t.Fatalf("next turn replays the summary as %q, the run sent %q at the same place before the input: %v", replayed, summary, summaryFirst(last))
	}
}

// A tool loop that outgrows the model window keeps going on one rolling
// summary in every entry of the coverage set: each effect runs once, each
// event folds everything before the latest step in one summarizer call that
// is told the run's input, requests between events only grow, the input and
// the latest results stay verbatim, a row written into the session by someone else is left alone,
// and the next turn renders the summary the run sent at the same place
// without compacting again. When the summarizer is down the
// loop ends visibly with budget_unsatisfied after one retry, keeps the steps
// it committed, and the next turn is answered once the summarizer is back.
// The entries outside the set never fold: they end bounded, without
// replaying an effect, and the next turn is answered.
func longLoopScenario(t *testing.T, entry runEntry, s *effectLoop) {
	const steps = 14
	f := s.f
	if code, _ := s.run(entry, "EARLIER_TURN_KEEP", 0); code != "" {
		t.Fatalf("earlier turn failed with %s", code)
	}
	summaries := f.summaries.Load()
	s.foreignAt = 3
	var summarized []string
	f.onSummary = func(body []byte) { summarized = append(summarized, string(body)) }
	code, requests := s.run(entry, loopInput, steps)
	f.onSummary = nil
	s.foreignAt = 0
	if !entry.folds {
		if f.summaries.Load() != summaries || code != "context.budget_unsatisfied" || len(s.effects) == 0 || !slices.IsSorted(s.effects) || len(slices.Compact(slices.Clone(s.effects))) != len(s.effects) || len(s.effects) >= steps {
			t.Fatalf("outside the coverage set: code=%q summary_calls=%d effects=%v, want no fold, a bounded end and no replayed effect", code, f.summaries.Load()-summaries, s.effects)
		}
		var previous []string
		for i, request := range requests {
			messages := requestMessages(t, request)
			if strings.Contains(request, "[tool result pruned") || len(messages) < len(previous) || !slices.Equal(previous, messages[:len(previous)]) {
				t.Fatalf("request %d changed previously sent messages before failing closed", i+1)
			}
			previous = messages
		}
		t.Logf("code=%q provider_calls=%d effects=%d first_kept_in_last_request=%d", code, len(requests), len(s.effects), firstKept(requests[len(requests)-1], steps))
		code, next := s.run(entry, "NEXT_ROUND_QUESTION", 0)
		if code != "" {
			t.Fatalf("next turn after the loop failed with %s", code)
		}
		if len(s.effects) != 0 || len(next) != 1 {
			t.Fatalf("next turn: effects=%v requests=%d, want one answer without replaying effects", s.effects, len(next))
		}
		if entry.name == "discuss" && (f.summaries.Load() == summaries || strings.Count(next[0], loopInput) != 1 || strings.Count(next[0], "NEXT_ROUND_QUESTION") != 1) {
			t.Fatal("Discuss must compact completed steps while retaining both unanswered inputs verbatim")
		}
		return
	}
	want := make([]int, steps)
	for i := range want {
		want[i] = i + 1
	}
	if code != "" || !slices.Equal(s.effects, want) || len(requests) != steps+1+entry.extra {
		t.Fatalf("code=%q effects=%v provider_calls=%d summary_calls=%d, want each effect once in order and one call per step", code, s.effects, len(requests), f.summaries.Load()-summaries)
	}
	requests = requests[entry.extra:]
	events, first := 0, 1
	var previous []string
	for i, request := range requests {
		kept := firstKept(request, i)
		for index := 1; index < kept; index++ {
			if strings.Contains(request, fmt.Sprintf("TOOL_RESULT_%03d ", index)) {
				t.Fatalf("request %d still carries folded result %d", i+1, index)
			}
		}
		summary := strings.Count(request, loopSummary)
		if strings.Count(request, loopInput) != 1 || summary > 1 || (i > 0 && kept > i) || kept < first || strings.Contains(request, "[tool result pruned") || strings.Contains(request, foreignRow) ||
			(summary == 1 && !markersInOrder(request, loopInput, loopSummary, fmt.Sprintf("TOOL_RESULT_%03d ", kept))) {
			t.Fatalf("request %d: input=%d summaries=%d first kept=%d (before %d), want the input once, at most one summary after it, the latest results raw, and nothing the run did not write",
				i+1, strings.Count(request, loopInput), summary, kept, first)
		}
		messages := requestMessages(t, request)
		if kept == first && i > 0 && (len(messages) < len(previous) || !slices.Equal(previous, messages[:len(previous)])) {
			t.Fatalf("request %d is not request %d with the step between them appended", i+1, i)
		}
		if kept > first {
			if kept != i {
				t.Fatalf("request %d keeps results from %d raw, want an event to fold everything before the latest step", i+1, kept)
			}
			events++
		}
		first, previous = kept, messages
	}
	if events < 2 || int(f.summaries.Load()-summaries) != events {
		t.Fatalf("events=%d summary_calls=%d, want the loop to fold at least twice, one summarizer call each", events, f.summaries.Load()-summaries)
	}
	for i, body := range summarized {
		if !strings.Contains(body, loopInput) {
			t.Fatalf("summarizer call %d was not told the run's input", i+1)
		}
	}
	var running, toolRows, foldedRows, foreign int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE state <> 'completed') FROM session_runs WHERE session_id=$1`, f.sessionID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE h.role='tool' AND h.content::text LIKE '%TOOL_RESULT_%'), count(*) FILTER (WHERE h.role='tool' AND h.content::text LIKE '%TOOL_RESULT_%' AND c.status='ok'), count(*) FILTER (WHERE h.content::text LIKE $2 AND h.compact_id IS NULL) FROM bot_history_messages h LEFT JOIN bot_history_message_compacts c ON c.id=h.compact_id WHERE h.session_id=$1`, f.sessionID, "%"+foreignRow+"%").Scan(&toolRows, &foldedRows, &foreign); err != nil {
		t.Fatal(err)
	}
	if running != 0 || toolRows != steps || foldedRows != first-1 || foreign != 1 || f.activeSummaries() != 1 {
		t.Fatalf("unfinished_runs=%d tool_rows=%d folded=%d raw_foreign_rows=%d active_summaries=%d, want every run completed, the %d folded steps under one summary and the foreign row raw", running, toolRows, foldedRows, foreign, f.activeSummaries(), first-1)
	}
	sent := summaryContents(t, requests[len(requests)-1])

	// The run may have ended at its allowance; the next turn gets room for
	// its question, so what it renders is the replay, not another fold.
	s.window(16000)
	summaries = f.summaries.Load()
	code, requests = s.run(entry, "NEXT_ROUND_QUESTION", 0)
	s.window(8000)
	if code != "" || len(requests) != 1 || f.summaries.Load() != summaries {
		t.Fatalf("next turn: code=%q provider_calls=%d summary_calls=%d, want one answered call without compacting", code, len(requests), f.summaries.Load()-summaries)
	}
	next := requests[0]
	if !slices.Equal(summaryContents(t, next), sent) || firstKept(next, steps) != first || len(s.effects) != 0 ||
		!markersInOrder(next, loopInput, loopSummary, fmt.Sprintf("TOOL_RESULT_%03d ", first), "NEXT_ROUND_QUESTION") || strings.Contains(next, "EARLIER_TURN_KEEP") {
		t.Fatalf("next turn: summaries=%q first kept=%d effects=%v, want the summary the run sent %q after the input, the results it kept, then the question", summaryContents(t, next), firstKept(next, steps), s.effects, sent)
	}

	s.downAt = 1
	code, requests = s.run(entry, "SUMMARIZER_DOWN_INPUT", steps)
	if code != "context.budget_unsatisfied" || f.summaries.Load()-s.downFrom != 2 || len(s.effects) == 0 || len(s.effects) >= steps || !slices.IsSorted(s.effects) || len(slices.Compact(slices.Clone(s.effects))) != len(s.effects) {
		t.Fatalf("summarizer down: code=%q summary_attempts=%d provider_calls=%d effects=%v, want budget_unsatisfied after the summarizer was retried once, with the effects so far run once", code, f.summaries.Load()-s.downFrom, len(requests), s.effects)
	}
	var reasons, failedRows int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM context_lifecycles, jsonb_array_elements(snapshot->'mutations') m WHERE session_id=$1 AND m->>'kind'='context_switch' AND m->>'detail' LIKE '%reason=summarizer_failed%'`, f.sessionID).Scan(&reasons); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM bot_history_messages WHERE session_id=$1 AND role='tool' AND content::text LIKE '%TOOL_RESULT_%' AND created_at >= (SELECT max(created_at) FROM bot_history_messages WHERE session_id=$1 AND role='user' AND content::text LIKE '%SUMMARIZER_DOWN_INPUT%')`, f.sessionID).Scan(&failedRows); err != nil {
		t.Fatal(err)
	}
	if reasons != 1 || failedRows != len(s.effects) {
		t.Fatalf("summarizer down: %d events recorded summarizer_failed and %d tool rows follow the input, want the failed event and the %d committed effects kept", reasons, failedRows, len(s.effects))
	}
	f.failSummary.Store(false)
	summaries = f.summaries.Load()
	if code, requests := s.run(entry, "AFTER_SUMMARIZER_BACK", 0); code != "" || len(requests) != 1 || f.summaries.Load() == summaries || len(s.effects) != 0 {
		t.Fatalf("summarizer back: code=%q provider_calls=%d summary_calls=%d effects=%v, want the turn answered on a fresh summary without replaying an effect", code, len(requests), f.summaries.Load()-summaries, s.effects)
	}
}
