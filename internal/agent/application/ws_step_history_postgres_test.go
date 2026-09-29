package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

// These tests drive a Web chat turn the way the WebSocket handler does: the
// run is admitted by the real session runtime against the PostgreSQL ledger,
// the stream runs fenced so every completed step is committed to history as it
// lands, events are published to the manager as the forwarder publishes them,
// and the terminal write uses the outcome the runner returned. Each case pins
// what a client reads back after the run: the session_runs row, the history
// rows, and the turns the history endpoint builds from them.

const wsStepHistoryPartialText = "PARTIAL_WS_STEP_TEXT"

// wsStepHistoryLoopText is the chunk wsStepHistoryTextLoop repeats; it is long
// enough for one chunk to count toward the text loop guard's streak.
var wsStepHistoryLoopText = strings.Repeat("abcd", 64)

type wsStepHistoryModel string

const (
	wsStepHistorySuccess wsStepHistoryModel = "success"
	// wsStepHistoryAuthFailure rejects the request before any output.
	wsStepHistoryAuthFailure wsStepHistoryModel = "auth_failure"
	// wsStepHistoryBrokenStream streams some text, then the connection fails
	// with an error the runtime does not retry.
	wsStepHistoryBrokenStream wsStepHistoryModel = "broken_stream"
	// wsStepHistoryBlock streams some text, then waits until the request is
	// cancelled.
	wsStepHistoryBlock wsStepHistoryModel = "block"
	// wsStepHistoryToolThenBrokenStream answers the first call with a tool
	// call, which commits a step, and breaks the second call's stream after
	// some text.
	wsStepHistoryToolThenBrokenStream wsStepHistoryModel = "tool_then_broken_stream"
	// wsStepHistoryContextBudget fails the run before the model is called:
	// the context view cannot fit the window, which the runtime reports with
	// the code context.budget_unsatisfied.
	wsStepHistoryContextBudget wsStepHistoryModel = "context_budget"
	// wsStepHistoryTextLoop repeats the same text until the runtime's loop
	// detection aborts the run. The abort follows no error event, and its
	// cause carries no catalogued code.
	wsStepHistoryTextLoop wsStepHistoryModel = "text_loop"
)

// wsStepHistoryTransport answers streaming model requests with the scripted
// model behavior and passes every other request to the test provider.
type wsStepHistoryTransport struct {
	base  http.RoundTripper
	mode  wsStepHistoryModel
	calls atomic.Int32
}

func (t *wsStepHistoryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return nil, err
	}
	var request struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &request)
	if !request.Stream {
		clone := r.Clone(r.Context())
		clone.Body = io.NopCloser(strings.NewReader(string(body)))
		clone.ContentLength = int64(len(body))
		return t.base.RoundTrip(clone)
	}
	call := t.calls.Add(1)
	textChunk := fmt.Sprintf("data: {\"id\":\"chatcmpl-ws-step\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", wsStepHistoryPartialText)
	stream := func(tail io.Reader) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(io.MultiReader(strings.NewReader(textChunk), tail)),
			Request:    r,
		}
	}
	switch t.mode {
	case wsStepHistoryToolThenBrokenStream:
		if call == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"id\":\"chatcmpl-ws-step\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_ws_step\",\"type\":\"function\",\"function\":{\"name\":\"missing_tool\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n" +
						"data: {\"id\":\"chatcmpl-ws-step\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")),
				Request: r,
			}, nil
		}
		return stream(failingReader{err: errors.New("upstream stream broke")}), nil
	case wsStepHistoryAuthFailure:
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid api key"}}`)),
			Request:    r,
		}, nil
	case wsStepHistoryBrokenStream:
		return stream(failingReader{err: errors.New("upstream stream broke")}), nil
	case wsStepHistoryBlock:
		return stream(blockingReader{ctx: r.Context()}), nil
	case wsStepHistoryTextLoop:
		loopChunk := fmt.Sprintf("data: {\"id\":\"chatcmpl-ws-step\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", wsStepHistoryLoopText)
		return stream(io.MultiReader(strings.NewReader(strings.Repeat(loopChunk, 4)), blockingReader{ctx: r.Context()})), nil
	default:
		return stream(strings.NewReader("data: {\"id\":\"chatcmpl-ws-step\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")), nil
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type blockingReader struct{ ctx context.Context }

func (r blockingReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

// loopDetectionQueries turns on the bot's loop detection.
type loopDetectionQueries struct{ noDecisionQueries }

func (loopDetectionQueries) GetBotByID(context.Context, pgtype.UUID) (dbsqlc.GetBotByIDRow, error) {
	return dbsqlc.GetBotByIDRow{Metadata: []byte(`{"features":{"loop_detection":{"enabled":true}}}`)}, nil
}

// wsStepHistoryRun is what a client can read back after one run.
type wsStepHistoryRun struct {
	// events are the types of the events the client was sent.
	events []string
	// failureFrames are the error and agent_abort events the client was
	// sent, each as its type and the code it carried.
	failureFrames []string
	// text is the text the client was streamed.
	text string
	// sessionRun is session_runs state / error_code / error_message.
	sessionRun [3]string
	// history is one line per history row: role, assistant text, and the
	// failure metadata the row carries.
	history []string
	// turns is one line per turn the history endpoint returns: role and the
	// messages it renders.
	turns []string
}

type wsStepHistoryHarness struct {
	// unfenced runs the stream without a runtime fence, so its steps are not
	// committed as they land and the turn is written once, when it ends.
	unfenced  bool
	pool      *pgxpool.Pool
	botID     string
	sessionID string
	service   *Service
	manager   *sessionruntime.Manager
	messages  *messagepkg.DBService
}

func newWSStepHistoryHarness(t *testing.T, mode wsStepHistoryModel) wsStepHistoryHarness {
	t.Helper()
	ctx := context.Background()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)

	// History rows reference the model that produced them; the model itself
	// is resolved by the fixture's fake queries.
	if _, err := pool.Exec(ctx, `
		INSERT INTO providers (id, name) VALUES ($1, 'ws-step-history-provider')
		ON CONFLICT DO NOTHING
	`, directLifecycleProviderID); err != nil {
		t.Fatalf("create provider fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO models (id, model_id, provider_id) VALUES ($1, 'direct-lifecycle-model', $2)
		ON CONFLICT DO NOTHING
	`, directLifecycleModelID, directLifecycleProviderID); err != nil {
		t.Fatalf("create model fixture: %v", err)
	}
	queries := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID:       "owner-ws-step-history",
		StateTTL:      time.Minute,
		OwnerLeaseTTL: time.Minute,
		Ledger:        ledger.NewPostgres(dbsqlc.New(pool), pool),
		Fence:         runtimefence.NewActivator(queries),
	})
	t.Cleanup(func() { _ = manager.Close() })

	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	service := fixture.service
	messages := messagepkg.NewService(service.logger, queries)
	service.messageService = messages
	service.streamHTTPClient = &http.Client{Transport: &wsStepHistoryTransport{base: service.streamHTTPClient.Transport, mode: mode}}
	if mode == wsStepHistoryContextBudget {
		service.agent = native.New(native.Deps{
			Logger: service.logger,
			ContextViewApplier: func(context.Context, native.RunConfig) (native.RunConfig, error) {
				return native.RunConfig{}, contextfrag.ErrBudgetUnsatisfied
			},
		})
	}
	service.SetSessionRuntime(manager)
	// The real manager asks for the run's pending decisions when it finishes.
	service.queries = noDecisionQueries{service.queries.(*directLifecycleQueries)}
	if mode == wsStepHistoryTextLoop {
		service.queries = loopDetectionQueries{service.queries.(noDecisionQueries)}
	}
	return wsStepHistoryHarness{pool: pool, botID: botID, sessionID: sessionID, service: service, manager: manager, messages: messages}
}

// run admits one Web chat turn and runs it to its terminal write. stop, when
// set, is called once the first text reaches the client and is expected to
// stop the run.
func (h wsStepHistoryHarness) run(t *testing.T, stop func(ctx context.Context, runID string)) wsStepHistoryRun {
	t.Helper()
	baseCtx := context.Background()
	streamCtx, streamCancelCause := context.WithCancelCause(baseCtx)
	streamCancel := func() { streamCancelCause(context.Canceled) }
	defer streamCancel()
	abortCh := make(chan struct{}, 1)
	admission, err := h.manager.Admit(streamCtx, sessionruntime.AdmitInput{
		BotID:        h.botID,
		SessionID:    h.sessionID,
		InvocationID: uuid.NewString(),
		Payload:      []byte(`{"kind":"message","text":"` + directLifecyclePrompt + `"}`),
		Execution: sessionruntime.Execution{
			Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			},
			AbortCh:         abortCh,
			Cancel:          streamCancel,
			OwnershipCancel: streamCancelCause,
		},
	})
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if !admission.Started {
		t.Fatal("Admit() did not start the run")
	}
	handle := admission.Handle
	if !h.unfenced {
		streamCtx = runtimefence.WithContext(streamCtx, runtimefence.Fence{
			BotID: handle.BotID, SessionID: handle.SessionID, Token: handle.FencingToken,
		})
	}
	position := admission.TurnPosition
	req := ChatRequest{
		BotID:             h.botID,
		ChatID:            h.botID,
		ThreadID:          h.sessionID,
		RunID:             admission.RunID,
		TurnID:            admission.TurnID,
		TurnPosition:      &position,
		Query:             directLifecyclePrompt,
		RawQuery:          directLifecyclePrompt,
		UserVisibleText:   directLifecyclePrompt,
		CurrentChannel:    "local",
		RunHandle:         handle,
		InjectCh:          make(chan turn.InjectMessage),
		QueueSteerEnabled: true,
	}

	eventCh := make(chan WSStreamEvent, 64)
	forwarded := make(chan struct{})
	var stopOnce sync.Once
	var events, failureFrames []string
	var text strings.Builder
	go func() {
		defer close(forwarded)
		for payload := range eventCh {
			var event native.StreamEvent
			if err := json.Unmarshal(payload, &event); err != nil {
				continue
			}
			events = append(events, string(event.Type))
			if event.Type == native.EventTextDelta {
				text.WriteString(event.Delta)
			}
			if event.Type == native.EventError || event.Type == native.EventAgentAbort {
				failureFrames = append(failureFrames, string(event.Type)+" "+event.Code)
			}
			_, _ = h.manager.HandleAgentEvent(baseCtx, handle, event)
			if stop != nil && event.Type == native.EventTextDelta {
				stopOnce.Do(func() { go stop(baseCtx, admission.RunID) })
			}
		}
	}()
	outcomeCh := make(chan RunOutcome, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(eventCh)
		outcome, err := h.service.StreamChatWS(streamCtx, req, eventCh, abortCh)
		outcomeCh <- outcome
		errCh <- err
	}()
	var outcome RunOutcome
	var runErr error
	select {
	case outcome = <-outcomeCh:
		runErr = <-errCh
	case <-time.After(20 * time.Second):
		t.Fatal("WS turn did not finish")
	}
	<-forwarded
	// The handler's terminal write: a bare cancellation stays unnamed, a
	// delivered failure names the run, any other error fails it.
	switch {
	case runErr == nil:
	case errors.Is(runErr, context.Canceled):
		outcome = RunOutcome{}
	case outcome.Status == sessionruntime.RunStatusErrored && outcome.ErrorCode() != "":
	default:
		outcome = RunOutcome{Status: sessionruntime.RunStatusErrored, Cause: runErr}
	}
	if _, err := h.manager.FinishRunWithErrorCode(context.WithoutCancel(baseCtx), handle, outcome.Status, outcome.ErrorCode()); err != nil {
		t.Fatalf("FinishRunWithErrorCode() error = %v", err)
	}
	out := h.read(t, admission.RunID)
	out.events = events
	out.failureFrames = failureFrames
	out.text = text.String()
	return out
}

func (h wsStepHistoryHarness) read(t *testing.T, runID string) wsStepHistoryRun {
	t.Helper()
	ctx := context.Background()
	var out wsStepHistoryRun
	if err := h.pool.QueryRow(ctx, `
		SELECT state, COALESCE(error_code, ''), COALESCE(error_message, '')
		FROM session_runs WHERE run_id = $1
	`, runID).Scan(&out.sessionRun[0], &out.sessionRun[1], &out.sessionRun[2]); err != nil {
		t.Fatalf("read session_runs: %v", err)
	}
	rows, err := h.messages.ListBySession(ctx, h.sessionID)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	for _, row := range rows {
		// The user row stores the model-facing text; the turns below show
		// what the user sees.
		line := row.Role
		if row.Role != "user" {
			line += " " + strconv.Quote(historyRowText(row))
		}
		if row.Metadata[messagepkg.AgentStepInterruptedMetadataKey] == true {
			line += " interrupted"
		}
		if code, _ := row.Metadata[messagepkg.HistoryErrorCodeMetadataKey].(string); code != "" {
			line += " error_code=" + code
		}
		out.history = append(out.history, line)
	}
	uiRows, err := h.messages.ListLatestUIBySession(ctx, h.sessionID, 30)
	if err != nil {
		t.Fatalf("list UI history: %v", err)
	}
	// The history endpoint reads newest first and reverses before converting.
	slices.Reverse(uiRows)
	for _, uiTurn := range chatview.ConvertMessagesToUITurns(uiRows) {
		var line strings.Builder
		line.WriteString(uiTurn.Role)
		if uiTurn.Text != "" {
			line.WriteString(" " + strconv.Quote(uiTurn.Text))
		}
		for _, message := range uiTurn.Messages {
			line.WriteString(" [" + string(message.Type))
			if message.Code != "" {
				line.WriteString(" " + message.Code)
			}
			if message.Type == chatview.UIMessageText {
				line.WriteString(" " + strconv.Quote(message.Content))
			}
			line.WriteString("]")
		}
		out.turns = append(out.turns, line.String())
	}
	return out
}

func historyRowText(row messagepkg.Message) string {
	var content struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(row.Content, &content); err != nil {
		return string(row.Content)
	}
	var text string
	if err := json.Unmarshal(content.Content, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content.Content, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			if part.Type == "text" {
				b.WriteString(part.Text)
			}
		}
		return b.String()
	}
	return string(content.Content)
}

func assertWSStepHistory(t *testing.T, got wsStepHistoryRun, wantRun [3]string, wantHistory, wantTurns []string) {
	t.Helper()
	if got.sessionRun != wantRun {
		t.Errorf("session_runs = %q, want %q", got.sessionRun, wantRun)
	}
	assertStrings(t, "history rows", got.history, wantHistory)
	assertStrings(t, "history turns", got.turns, wantTurns)
}

func TestPostgresWSStepHistorySuccess(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistorySuccess)
	got := h.run(t, nil)
	assertWSStepHistory(t, got,
		[3]string{"completed", "", ""},
		[]string{
			`user`,
			`assistant "` + wsStepHistoryPartialText + `"`,
		},
		[]string{
			`user "` + directLifecyclePrompt + `"`,
			`assistant [text "` + wsStepHistoryPartialText + `"]`,
		},
	)
}

func TestPostgresWSStepHistoryStop(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistoryBlock)
	got := h.run(t, func(ctx context.Context, runID string) {
		if _, err := h.manager.AbortControl(ctx, h.botID, h.sessionID, runID, "stop-1"); err != nil {
			t.Errorf("AbortControl() error = %v", err)
		}
	})
	assertWSStepHistory(t, got,
		[3]string{"aborted", "", ""},
		[]string{
			`user`,
			`assistant "` + wsStepHistoryPartialText + `" interrupted`,
		},
		[]string{
			`user "` + directLifecyclePrompt + `"`,
			`assistant [text "` + wsStepHistoryPartialText + `"]`,
		},
	)
}

// A run that fails before any step commits still records the send: the user
// message and an empty assistant row carrying the run's failure code, the
// rows persistTurnFailure writes for a turn without step commits. The row's
// code is the one session_runs records.
func TestPostgresWSStepHistoryFailureWithoutSteps(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistoryAuthFailure)
	got := h.run(t, nil)
	assertWSStepHistory(t, got,
		[3]string{"failed", "agent.provider_auth_failed", ""},
		[]string{
			`user`,
			`assistant "" interrupted error_code=agent.provider_auth_failed`,
		},
		[]string{
			`user "` + directLifecyclePrompt + `"`,
			`assistant [error agent.provider_auth_failed]`,
		},
	)
}

// Every failure of an admitted send is written to history, whatever its code:
// the user message and an empty assistant row carrying the code session_runs
// records.
func TestPostgresWSStepHistoryFailureWithAnyCode(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistoryContextBudget)
	got := h.run(t, nil)
	assertOrdered(t, got.events, "error")
	assertWSStepHistory(t, got,
		[3]string{"failed", "context.budget_unsatisfied", ""},
		[]string{
			`user`,
			`assistant "" interrupted error_code=context.budget_unsatisfied`,
		},
		[]string{
			`user "` + directLifecyclePrompt + `"`,
			`assistant [error context.budget_unsatisfied]`,
		},
	)
}

// A run without step commits writes its turn when it ends, and records a
// failure the same way: the user message and a row with the run's code.
func TestPostgresWSStepHistoryUnfencedFailure(t *testing.T) {
	for _, tc := range []struct {
		mode wsStepHistoryModel
		code string
	}{
		{wsStepHistoryAuthFailure, "agent.provider_auth_failed"},
		{wsStepHistoryContextBudget, "context.budget_unsatisfied"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			h := newWSStepHistoryHarness(t, tc.mode)
			h.unfenced = true
			got := h.run(t, nil)
			assertWSStepHistory(t, got,
				[3]string{"failed", tc.code, ""},
				[]string{
					`user`,
					`assistant "" interrupted error_code=` + tc.code,
				},
				[]string{
					`user "` + directLifecyclePrompt + `"`,
					`assistant [error ` + tc.code + `]`,
				},
			)
		})
	}
}

// A run the user stops has not failed, so without step commits it writes no
// failure row either.
func TestPostgresWSStepHistoryUnfencedStop(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistoryBlock)
	h.unfenced = true
	got := h.run(t, func(ctx context.Context, runID string) {
		if _, err := h.manager.AbortControl(ctx, h.botID, h.sessionID, runID, "stop-1"); err != nil {
			t.Errorf("AbortControl() error = %v", err)
		}
	})
	assertWSStepHistory(t, got,
		[3]string{"aborted", "", ""},
		nil,
		nil,
	)
}

// A run that ends in an abort nobody asked for has failed, even though its
// cause carries no catalogued code: the run is named
// agent.response_interrupted when its terminal is declared, and the live
// frame, session_runs and history all carry that code. With step commits the
// streamed text is kept as an interrupted step with the code; without them
// the turn records the user message and a row with the code.
func TestPostgresWSStepHistoryUncodedFailure(t *testing.T) {
	const code = "agent.response_interrupted"
	t.Run("steps", func(t *testing.T) {
		h := newWSStepHistoryHarness(t, wsStepHistoryTextLoop)
		got := h.run(t, nil)
		assertStrings(t, "failure frames", got.failureFrames, []string{"agent_abort " + code})
		if !strings.HasPrefix(got.text, wsStepHistoryPartialText+wsStepHistoryLoopText) {
			t.Fatalf("streamed text = %q, want the partial text and the repeated chunk", got.text)
		}
		assertWSStepHistory(t, got,
			[3]string{"failed", code, ""},
			[]string{
				`user`,
				`assistant "` + got.text + `" interrupted error_code=` + code,
			},
			[]string{
				`user "` + directLifecyclePrompt + `"`,
				`assistant [text "` + got.text + `"] [error ` + code + `]`,
			},
		)
	})
	t.Run("unfenced", func(t *testing.T) {
		h := newWSStepHistoryHarness(t, wsStepHistoryTextLoop)
		h.unfenced = true
		got := h.run(t, nil)
		assertStrings(t, "failure frames", got.failureFrames, []string{"agent_abort " + code})
		assertWSStepHistory(t, got,
			[3]string{"failed", code, ""},
			[]string{
				`user`,
				`assistant "" interrupted error_code=` + code,
			},
			[]string{
				`user "` + directLifecyclePrompt + `"`,
				`assistant [error ` + code + `]`,
			},
		)
	})
}

// Text streamed by a model call that then fails is in no committed step. It
// is written as an interrupted step that carries the run's failure code, the
// same code session_runs records.
func TestPostgresWSStepHistoryFailureAfterPartialOutput(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistoryBrokenStream)
	got := h.run(t, nil)
	assertOrdered(t, got.events, "text_delta", "error", "agent_abort")
	assertWSStepHistory(t, got,
		[3]string{"failed", "agent.response_interrupted", ""},
		[]string{
			`user`,
			`assistant "` + wsStepHistoryPartialText + `" interrupted error_code=agent.response_interrupted`,
		},
		[]string{
			`user "` + directLifecyclePrompt + `"`,
			`assistant [text "` + wsStepHistoryPartialText + `"] [error agent.response_interrupted]`,
		},
	)
}

// A failure after a committed step keeps that step and adds the text the
// failed model call streamed as an interrupted step with the run's code.
func TestPostgresWSStepHistoryFailureAfterCommittedStep(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistoryToolThenBrokenStream)
	got := h.run(t, nil)
	assertOrdered(t, got.events, "tool_call_start", "step_end", "text_delta", "error", "agent_abort")
	assertWSStepHistory(t, got,
		[3]string{"failed", "agent.response_interrupted", ""},
		[]string{
			`user`,
			`assistant ""`,
			`tool ""`,
			`assistant "` + wsStepHistoryPartialText + `" interrupted error_code=agent.response_interrupted`,
		},
		[]string{
			`user "` + directLifecyclePrompt + `"`,
			`assistant [tool] [text "` + wsStepHistoryPartialText + `"] [error agent.response_interrupted]`,
		},
	)
}

// An idle timeout cancels the model call, so the runtime checkpoints its text
// itself before the run ends; that checkpoint is the run's only row for the
// call and no second one is written.
func TestPostgresWSStepHistoryIdleTimeout(t *testing.T) {
	h := newWSStepHistoryHarness(t, wsStepHistoryBlock)
	h.service.streamIdleTimeout = 300 * time.Millisecond
	h.service.streamIdleTimeoutMax = 300 * time.Millisecond
	got := h.run(t, nil)
	assertWSStepHistory(t, got,
		[3]string{"failed", "agent.response_timeout", ""},
		[]string{
			`user`,
			`assistant "` + wsStepHistoryPartialText + `" interrupted`,
		},
		[]string{
			`user "` + directLifecyclePrompt + `"`,
			`assistant [text "` + wsStepHistoryPartialText + `"]`,
		},
	)
}

// The text a failed run keeps is the text of the model call that failed: a
// retried call is regenerated from the last committed step, and a step end
// hands the text to that step's commit.
func TestUncommittedStepTextStartsOverAtRetryAndStepEnd(t *testing.T) {
	for _, boundary := range []native.StreamEventType{native.EventRetry, native.EventStepEnd} {
		var text uncommittedStepText
		for _, event := range []native.StreamEvent{
			{Type: native.EventTextDelta, Delta: "before"},
			{Type: boundary},
			{Type: native.EventTextDelta, Delta: "part"},
			{Type: native.EventTextDelta, Delta: "ial"},
		} {
			text.observe(event)
		}
		if got := text.String(); got != "partial" {
			t.Fatalf("uncommitted text after %s = %q, want %q", boundary, got, "partial")
		}
	}
}
