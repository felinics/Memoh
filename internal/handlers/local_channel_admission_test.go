package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/application"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const (
	wsAdmissionBotID     = "11111111-1111-1111-1111-111111111111"
	wsAdmissionSessionID = "22222222-2222-2222-2222-222222222222"
)

// wsTerminalWrite is one release of the session's active slot.
type wsTerminalWrite struct {
	handle  sessionruntime.RunHandle
	status  string
	message string
}

// stubWSTurnAdmitter stands in for the session runtime manager. The handler
// depends on the two-method admission interface rather than the manager, so the
// entry point is exercisable without PostgreSQL, Redis, or a live backend.
type stubWSTurnAdmitter struct {
	admission sessionruntime.Admission
	admitErr  error

	mu       sync.Mutex
	admitted []sessionruntime.AdmitInput
	// terminal reports each terminal write, so a test can wait for the release
	// that happens on the run's goroutine instead of sleeping for it.
	terminal chan wsTerminalWrite
}

type wsLifecycleQueries struct {
	dbstore.Queries
	params []sqlc.CreateContextLifecycleParams
}

func (q *wsLifecycleQueries) CreateContextLifecycle(
	_ context.Context,
	arg sqlc.CreateContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	q.params = append(q.params, arg)
	return sqlc.ContextLifecycle{
		RunID:     arg.RunID,
		BotID:     arg.BotID,
		SessionID: arg.SessionID,
		Status:    arg.Status,
		ErrorCode: arg.ErrorCode,
		Snapshot:  arg.Snapshot,
	}, nil
}

func (*wsLifecycleQueries) GetContextLifecycleByRunID(
	context.Context,
	pgtype.UUID,
) (sqlc.ContextLifecycle, error) {
	return sqlc.ContextLifecycle{}, pgx.ErrNoRows
}

func newStubWSTurnAdmitter() *stubWSTurnAdmitter {
	return &stubWSTurnAdmitter{terminal: make(chan wsTerminalWrite, 4)}
}

func (a *stubWSTurnAdmitter) Admit(_ context.Context, in sessionruntime.AdmitInput) (sessionruntime.Admission, error) {
	a.mu.Lock()
	a.admitted = append(a.admitted, in)
	a.mu.Unlock()
	if a.admitErr != nil {
		return sessionruntime.Admission{}, a.admitErr
	}
	return a.admission, nil
}

func (a *stubWSTurnAdmitter) FinishRun(_ context.Context, handle sessionruntime.RunHandle, status, message string) error {
	select {
	case a.terminal <- wsTerminalWrite{handle: handle, status: status, message: message}:
	default:
	}
	return nil
}

func (a *stubWSTurnAdmitter) submissions() []sessionruntime.AdmitInput {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]sessionruntime.AdmitInput(nil), a.admitted...)
}

func (a *stubWSTurnAdmitter) awaitTerminalWrite(t *testing.T) wsTerminalWrite {
	t.Helper()
	select {
	case write := <-a.terminal:
		return write
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the run's terminal write")
		return wsTerminalWrite{}
	}
}

// startedWSAdmission is an admission that this process owns and must execute.
func startedWSAdmission() sessionruntime.Admission {
	return sessionruntime.Admission{
		RunID:        "run-1",
		TurnID:       "turn-1",
		TurnPosition: 7,
		State:        "running",
		Started:      true,
		Cursor:       sessionruntime.Cursor{Epoch: "epoch-1", Seq: 12},
		Handle: sessionruntime.RunHandle{
			BotID:        wsAdmissionBotID,
			SessionID:    wsAdmissionSessionID,
			RunID:        "run-1",
			TurnID:       "turn-1",
			Generation:   "generation-1",
			FencingToken: 3,
		},
	}
}

func wsAdmissionTestRef() wsTurnRef {
	return wsTurn("invocation-1", wsAdmissionSessionID)
}

func wsAdmissionTestSubmission() []byte {
	return wsSubmission{Kind: "message", SessionID: wsAdmissionSessionID, Text: "hi"}.encode()
}

// admitWSTestTurn runs one admission and returns both halves of its outcome:
// what the handler decided, and what the client was told. The writer's queue is
// the wire those send helpers actually reach.
func admitWSTestTurn(t *testing.T, handler *LocalChannelHandler, ref wsTurnRef, submission []byte, builders ...wsRunAdmissionBuilder) (wsRunAdmission, bool, []map[string]any) {
	t.Helper()

	writer := &wsWriter{ch: make(chan []byte, 4), stop: make(chan struct{}), done: make(chan struct{})}
	// Cause-carrying like the real stream context, so the admission registers the
	// same revocation the handler would hand it.
	ctx, cancelCause := context.WithCancelCause(context.Background())
	cancel := func() { cancelCause(context.Canceled) }
	defer cancel()

	var builder wsRunAdmissionBuilder
	if len(builders) > 0 {
		builder = builders[0]
	}
	admission, ok := handler.admitWSTurn(ctx, writer, wsAdmissionBotID, ref, submission, builder, make(chan struct{}, 1), cancel, cancelCause)

	events := make([]map[string]any, 0, len(writer.ch))
	for len(writer.ch) > 0 {
		var event map[string]any
		if err := json.Unmarshal(<-writer.ch, &event); err != nil {
			t.Fatalf("unmarshal ws event: %v", err)
		}
		events = append(events, event)
	}
	return admission, ok, events
}

// The run id, the turn it belongs to, and the position it became observable at
// all come from admission, and the handler has to carry all three: the ledger
// decides the first two and the live reservation decides the third, so nothing
// downstream can reconstruct them.
func TestAdmitWSTurnCarriesRunTurnAndCursor(t *testing.T) {
	t.Parallel()

	runtime := newStubWSTurnAdmitter()
	runtime.admission = startedWSAdmission()
	handler := &LocalChannelHandler{logger: slog.Default(), sessionRuntime: runtime}

	admission, ok, events := admitWSTestTurn(t, handler, wsAdmissionTestRef(), wsAdmissionTestSubmission())
	if !ok || !admission.Execute {
		t.Fatalf("admission = %+v, ok = %v, want an executable run", admission, ok)
	}
	if admission.RunID != "run-1" || admission.Handle.FencingToken != 3 {
		t.Fatalf("admission = %+v, want the ledger's run and its fencing token", admission)
	}
	if admission.Accepted.TurnID != "turn-1" {
		t.Fatalf("accepted turn = %q, want turn-1", admission.Accepted.TurnID)
	}
	if admission.Accepted.Cursor != (sessionruntime.Cursor{Epoch: "epoch-1", Seq: 12}) {
		t.Fatalf("accepted cursor = %+v, want the activation cursor", admission.Accepted.Cursor)
	}
	if admission.Accepted.Duplicate {
		t.Fatalf("a first admission is not a duplicate: %+v", admission.Accepted)
	}
	// Acceptance is published once the run is registered, not here: a run the
	// connection cannot register must not have been announced already.
	if len(events) != 0 {
		t.Fatalf("admission wrote %#v, want nothing on the wire yet", events)
	}
}

func TestAdmitWSTurnBuildsNormalMessageRequestUserTurn(t *testing.T) {
	t.Parallel()

	runtime := newStubWSTurnAdmitter()
	runtime.admission = startedWSAdmission()
	handler := &LocalChannelHandler{logger: slog.Default(), sessionRuntime: runtime}
	timestamp := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	messageAdmission := &wsMessageAdmission{
		handler: handler,
		botID:   wsAdmissionBotID,
		request: chatview.UITurn{
			Role:              "user",
			Text:              "hello",
			UserMessageKind:   "text",
			Timestamp:         timestamp,
			Platform:          "local",
			SenderUserID:      "user-1",
			ExternalMessageID: "invocation-1",
		},
		attachments: []turn.Attachment{{
			Type:   "file",
			Name:   "notes.txt",
			Mime:   "text/plain",
			Base64: "not-runtime-state",
		}},
	}

	_, ok, _ := admitWSTestTurn(t, handler, wsAdmissionTestRef(), wsAdmissionTestSubmission(),
		messageAdmission.build)
	if !ok {
		t.Fatal("admission failed")
	}

	admitted := runtime.submissions()
	if len(admitted) != 1 || admitted[0].Execution.Admission == nil {
		t.Fatalf("admitted = %#v, want one request user turn builder", admitted)
	}
	view, err := admitted[0].Execution.Admission(context.Background(), startedWSAdmission().Handle)
	if err != nil {
		t.Fatalf("build admission view: %v", err)
	}
	if view.RequestUserTurn == nil {
		t.Fatalf("request user turn = %#v", view.RequestUserTurn)
	}
	requestTurn := view.RequestUserTurn
	if requestTurn.TurnID != "turn-1" || requestTurn.Role != "user" || requestTurn.Text != "hello" || requestTurn.UserMessageKind != "text" ||
		requestTurn.Timestamp != timestamp || requestTurn.Platform != "local" ||
		requestTurn.SenderUserID != "user-1" || requestTurn.ExternalMessageID != "invocation-1" {
		t.Fatalf("request user turn = %#v", requestTurn)
	}
	if len(requestTurn.Attachments) != 1 || requestTurn.Attachments[0].Name != "notes.txt" ||
		requestTurn.Attachments[0].Mime != "text/plain" {
		t.Fatalf("request user turn attachments = %#v", requestTurn.Attachments)
	}
	prepared := messageAdmission.preparedAttachments()
	if len(prepared) != 1 || prepared[0].Base64 != "not-runtime-state" {
		t.Fatalf("prepared execution attachments = %#v", prepared)
	}
}

func TestAdmitWSTurnPublishesReplacementOperation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		admission       *wsReplacementAdmission
		wantKind        string
		wantReplaceFrom string
		wantReplacement bool
	}{
		{
			name: "retry",
			admission: &wsReplacementAdmission{
				kind: sessionruntime.RunOperationRetry,
				prepareAnchor: func(context.Context) (string, error) {
					return "assistant-first", nil
				},
			},
			wantKind:        sessionruntime.RunOperationRetry,
			wantReplaceFrom: "assistant-first",
		},
		{
			name: "edit",
			admission: &wsReplacementAdmission{
				kind: sessionruntime.RunOperationEdit,
				prepareAnchor: func(context.Context) (string, error) {
					return "user-request", nil
				},
				replacementUserTurn: &chatview.UITurn{Role: "user", Text: "edited"},
				attachmentsPrepared: true,
			},
			wantKind:        sessionruntime.RunOperationEdit,
			wantReplaceFrom: "user-request",
			wantReplacement: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runtime := newStubWSTurnAdmitter()
			runtime.admission = startedWSAdmission()
			handler := &LocalChannelHandler{logger: slog.Default(), sessionRuntime: runtime}

			_, ok, _ := admitWSTestTurn(t, handler, wsAdmissionTestRef(), wsAdmissionTestSubmission(), test.admission.build)
			if !ok {
				t.Fatal("admission failed")
			}
			admitted := runtime.submissions()
			if len(admitted) != 1 || admitted[0].Execution.Admission == nil {
				t.Fatalf("admitted = %#v, want one replacement operation builder", admitted)
			}
			view, err := admitted[0].Execution.Admission(context.Background(), startedWSAdmission().Handle)
			if err != nil {
				t.Fatalf("build admission view: %v", err)
			}
			if view.Operation == nil {
				t.Fatal("replacement operation is nil")
			}
			if view.Operation.Kind != test.wantKind || view.Operation.ReplaceFromMessageID != test.wantReplaceFrom {
				t.Fatalf("replacement operation = %#v", view.Operation)
			}
			if test.wantReplacement {
				if view.Operation.ReplacementUserTurn == nil ||
					view.Operation.ReplacementUserTurn.TurnID != "turn-1" ||
					view.Operation.ReplacementUserTurn.Text != "edited" {
					t.Fatalf("replacement user turn = %#v", view.Operation.ReplacementUserTurn)
				}
			} else if view.Operation.ReplacementUserTurn != nil {
				t.Fatalf("replacement user turn = %#v, want nil", view.Operation.ReplacementUserTurn)
			}
		})
	}
}

// The wire form is the contract. turn_id, epoch and seq use the names the
// subscription frames use, so a client that also observes the session orders
// acceptance against those frames with one comparison instead of two
// vocabularies (SR-OBS-002, SR-OBS-003).
func TestSendWSRunAcceptedCarriesTurnAndCursor(t *testing.T) {
	t.Parallel()

	admission := startedWSAdmission()
	accepted := decodeWSTestEvent(t, func(writer *wsWriter) {
		sendWSRunAccepted(writer, wsAdmissionTestRef().withRun(admission.RunID), wsRunAcceptance{
			TurnID: admission.TurnID,
			Cursor: admission.Cursor,
		})
	})
	if accepted["type"] != "run_accepted" || accepted["run_id"] != "run-1" || accepted["invocation_id"] != "invocation-1" {
		t.Fatalf("run_accepted = %#v", accepted)
	}
	if accepted["turn_id"] != "turn-1" {
		t.Fatalf("run_accepted turn_id = %#v, want turn-1", accepted["turn_id"])
	}
	if accepted["epoch"] != "epoch-1" || accepted["seq"] != float64(12) {
		t.Fatalf("run_accepted cursor = %#v, want epoch-1 at seq 12", accepted)
	}
	if _, present := accepted["duplicate"]; present {
		t.Fatalf("a first acceptance marked itself duplicate: %#v", accepted)
	}
}

// A replay names the turn the original submission was admitted under, because
// every subscriber of that run is shown the same turn. It reports no cursor: the
// call reserved nothing, so it produced no position, and where the session
// stands now is what a subscription's snapshot answers.
func TestAdmitWSTurnReplayNamesTheTurnWithoutACursor(t *testing.T) {
	t.Parallel()

	runtime := newStubWSTurnAdmitter()
	runtime.admission = sessionruntime.Admission{
		RunID:   "run-1",
		TurnID:  "turn-1",
		State:   "running",
		Replay:  true,
		Started: false,
	}
	handler := &LocalChannelHandler{logger: slog.Default(), sessionRuntime: runtime}

	admission, ok, events := admitWSTestTurn(t, handler, wsAdmissionTestRef(), wsAdmissionTestSubmission())
	if !ok || admission.Execute {
		t.Fatalf("admission = %+v, ok = %v, want an answered replay that executes nothing", admission, ok)
	}
	if admission.Handle.FencingToken != 0 {
		t.Fatalf("replay claims ownership: %+v", admission.Handle)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one acceptance", events)
	}
	accepted := events[0]
	if accepted["type"] != "run_accepted" || accepted["run_id"] != "run-1" || accepted["duplicate"] != true {
		t.Fatalf("replay acceptance = %#v", accepted)
	}
	if accepted["turn_id"] != "turn-1" {
		t.Fatalf("replay acceptance turn_id = %#v, want turn-1", accepted["turn_id"])
	}
	if _, present := accepted["epoch"]; present {
		t.Fatalf("replay reported a cursor it did not produce: %#v", accepted)
	}
	if _, present := accepted["seq"]; present {
		t.Fatalf("replay reported a cursor it did not produce: %#v", accepted)
	}
}

// What the client submitted is what gets fingerprinted, and the run must be
// interruptible once it exists: an admission without abort plumbing is a turn
// nobody can stop.
func TestAdmitWSTurnSubmitsTheClientsIdentityAndAbortPlumbing(t *testing.T) {
	t.Parallel()

	runtime := newStubWSTurnAdmitter()
	runtime.admission = startedWSAdmission()
	handler := &LocalChannelHandler{logger: slog.Default(), sessionRuntime: runtime}

	submission := wsAdmissionTestSubmission()
	if _, ok, _ := admitWSTestTurn(t, handler, wsAdmissionTestRef(), submission); !ok {
		t.Fatal("admission failed")
	}

	submitted := runtime.submissions()
	if len(submitted) != 1 {
		t.Fatalf("admit calls = %d, want 1", len(submitted))
	}
	in := submitted[0]
	if in.BotID != wsAdmissionBotID || in.SessionID != wsAdmissionSessionID || in.InvocationID != "invocation-1" {
		t.Fatalf("admit input = %+v, want the connection's bot, session and invocation", in)
	}
	if string(in.Payload) != string(submission) {
		t.Fatalf("admit payload = %q, want the canonical submission %q", in.Payload, submission)
	}
	if in.Execution.Admission == nil || in.Execution.AbortCh == nil || in.Execution.Cancel == nil {
		t.Fatalf("execution plumbing = %+v, want a builder, an abort channel and a cancel", in.Execution)
	}
}

// A refusal is not a run. The client is given the stable code it branches on —
// one of these is worth retrying unchanged, the other never is — and no run id,
// because none exists.
func TestAdmitWSTurnRejectionsCarryStableCodes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want apperror.Code
	}{
		{name: "busy", err: sessionruntime.ErrSessionBusy, want: apperror.CodeSessionBusy},
		{name: "conflict", err: sessionruntime.ErrInvocationConflict, want: apperror.CodeSessionInvocationConflict},
	} {
		runtime := newStubWSTurnAdmitter()
		runtime.admitErr = tc.err
		handler := &LocalChannelHandler{logger: slog.Default(), sessionRuntime: runtime}

		admission, ok, events := admitWSTestTurn(t, handler, wsAdmissionTestRef(), wsAdmissionTestSubmission())
		if ok || admission.Execute {
			t.Fatalf("%s: admission = %+v, ok = %v, want a refusal", tc.name, admission, ok)
		}
		if len(events) != 1 {
			t.Fatalf("%s: events = %#v, want one rejection", tc.name, events)
		}
		rejected := events[0]
		if rejected["type"] != "run_rejected" || rejected["code"] != string(tc.want) {
			t.Fatalf("%s: rejection = %#v, want run_rejected %s", tc.name, rejected, tc.want)
		}
		if _, present := rejected["run_id"]; present {
			t.Fatalf("%s: rejection named a run: %#v", tc.name, rejected)
		}
	}
}

// Without durable admission there is no single-active-run guarantee to fall back
// on, so the turn is refused rather than run unprotected.
func TestAdmitWSTurnRequiresConfiguredRuntime(t *testing.T) {
	t.Parallel()

	handler := &LocalChannelHandler{logger: slog.Default()}
	admission, ok, events := admitWSTestTurn(t, handler, wsAdmissionTestRef(), wsAdmissionTestSubmission())
	if ok || admission.Execute {
		t.Fatalf("admission = %+v, ok = %v, want a refusal", admission, ok)
	}
	if len(events) != 1 || events[0]["type"] != "error" {
		t.Fatalf("events = %#v, want one error", events)
	}
}

// The terminal write is how the session's single active slot is released. Only
// the stable code reaches the run's published state; the error itself is a
// private diagnostic that stays in the log.
//
// The status it carries is only ever what this process can actually testify to.
// A run that returns cleanly, or returns a bare cancellation, is reported with
// no status at all: an abort routed in from another connection or another server
// unblocks the runner and cancels its context, so both of those are exactly what
// a stopped run looks like from here. Naming them would overwrite the intent
// already recorded against the run — which is how a routed abort used to
// finalize as a successful turn (SR-CTL-001).
func TestFinishWSRunReleasesTheSlotUnderTheAdmittedToken(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		runErr      error
		wantStatus  string
		wantMessage string
	}{
		{name: "clean return leaves the outcome to the manager", wantStatus: ""},
		{
			name:       "bare cancellation blames nothing",
			runErr:     context.Canceled,
			wantStatus: "",
		},
		{
			name:        "errored",
			runErr:      apperror.New(apperror.CodeSessionBusy, nil),
			wantStatus:  sessionruntime.RunStatusErrored,
			wantMessage: string(apperror.CodeSessionBusy),
		},
		{
			name:       "errored without a catalog code",
			runErr:     errors.New("model exploded"),
			wantStatus: sessionruntime.RunStatusErrored,
		},
	} {
		runtime := newStubWSTurnAdmitter()
		handler := &LocalChannelHandler{logger: slog.Default(), sessionRuntime: runtime}
		admitted := startedWSAdmission()

		handler.finishWSRun(context.Background(), wsRunAdmission{RunID: admitted.RunID, Handle: admitted.Handle}, tc.runErr)

		write := runtime.awaitTerminalWrite(t)
		if write.status != tc.wantStatus || write.message != tc.wantMessage {
			t.Fatalf("%s: terminal write = %+v, want %s %q", tc.name, write, tc.wantStatus, tc.wantMessage)
		}
		if write.handle.FencingToken != admitted.Handle.FencingToken || write.handle.RunID != admitted.RunID {
			t.Fatalf("%s: terminal write handle = %+v, want the admitted one", tc.name, write.handle)
		}
	}
}

// A stream that never owned the run owns no terminal transition either. Writing
// one would either fail the fence or, worse, end a turn another owner is still
// executing.
func TestFinishWSRunSkipsRunsThisProcessDoesNotOwn(t *testing.T) {
	t.Parallel()

	runtime := newStubWSTurnAdmitter()
	handler := &LocalChannelHandler{logger: slog.Default(), sessionRuntime: runtime}

	handler.finishWSRun(context.Background(), wsRunAdmission{RunID: "run-1"}, nil)

	select {
	case write := <-runtime.terminal:
		t.Fatalf("unowned run wrote a terminal state: %+v", write)
	default:
	}
}

func TestFinishWSRunPersistsPreContextFailureAfterFencedFinish(t *testing.T) {
	const runID = "33333333-3333-4333-8333-333333333333"
	queries := &wsLifecycleQueries{}
	agentService := application.NewService(
		slog.New(slog.DiscardHandler),
		nil,
		queries,
		nil,
		nil,
		nil,
		nil,
		time.UTC,
		time.Second,
	)
	runtime := newStubWSTurnAdmitter()
	handler := &LocalChannelHandler{
		agentService:   agentService,
		logger:         slog.Default(),
		sessionRuntime: runtime,
	}
	admitted := startedWSAdmission()
	admitted.RunID = runID
	admitted.Handle.RunID = runID
	runErr := errors.New("resolve failed before context assembly")

	handler.finishWSRun(
		context.Background(),
		wsRunAdmission{RunID: admitted.RunID, Handle: admitted.Handle},
		runErr,
	)

	write := runtime.awaitTerminalWrite(t)
	if write.status != sessionruntime.RunStatusErrored {
		t.Fatalf("terminal status = %q, want %q", write.status, sessionruntime.RunStatusErrored)
	}
	if len(queries.params) != 1 {
		t.Fatalf("CreateContextLifecycle calls = %d, want 1", len(queries.params))
	}
	row := queries.params[0]
	if row.Status != "failed_provider" {
		t.Fatalf("context lifecycle status = %q, want failed_provider", row.Status)
	}
	if bytes.Contains(row.Snapshot, []byte("resolve failed")) {
		t.Fatalf("content-light snapshot leaked private error: %s", row.Snapshot)
	}
	var snapshot struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
		t.Fatalf("decode lifecycle snapshot: %v", err)
	}
	if snapshot.Version != 1 {
		t.Fatalf("snapshot version = %d, want 1", snapshot.Version)
	}
}

// The end-to-end shape of one accepted turn: admission names the run and its
// turn, the client is told both plus the cursor the run became observable at,
// and the run's end releases the session under the token admission handed out.
func TestStartWSStreamPublishesAdmittedTurnAndReleasesTheSession(t *testing.T) {
	t.Parallel()

	runtime := newStubWSTurnAdmitter()
	runtime.admission = startedWSAdmission()
	handler := &LocalChannelHandler{logger: slog.Default(), sessionRuntime: runtime}

	writer := &wsWriter{ch: make(chan []byte, 16), stop: make(chan struct{}), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ref, started := handler.startWSStream(ctx, ctx, writer, wsAdmissionBotID, wsAdmissionTestRef(), "test", wsAdmissionTestSubmission(), nil,
		nil,
		func(context.Context, wsTurnRef, wsAdmittedTurn, chan<- application.WSStreamEvent, <-chan struct{}) error {
			return nil
		})
	if !started || ref.RunID != "run-1" {
		t.Fatalf("stream started = %v, ref = %+v, want the admitted run", started, ref)
	}

	var accepted map[string]any
	select {
	case data := <-writer.ch:
		if err := json.Unmarshal(data, &accepted); err != nil {
			t.Fatalf("unmarshal acceptance: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for run_accepted")
	}
	if accepted["type"] != "run_accepted" || accepted["run_id"] != "run-1" {
		t.Fatalf("first event = %#v, want run_accepted naming the admitted run", accepted)
	}
	if accepted["turn_id"] != "turn-1" || accepted["epoch"] != "epoch-1" || accepted["seq"] != float64(12) {
		t.Fatalf("run_accepted = %#v, want the admitted turn and cursor", accepted)
	}

	// The runner returned cleanly, which this process cannot tell apart from a
	// run something else stopped, so it releases the slot without naming an
	// outcome. What is asserted here is that the release happened at all and
	// carried the admitted fence.
	write := runtime.awaitTerminalWrite(t)
	if write.status != "" || write.handle.FencingToken != 3 {
		t.Fatalf("terminal write = %+v, want an unnamed outcome fenced by the admitted token", write)
	}
}
