package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
)

// newFollowUpTestService wires a Service whose live queue lives in a memory
// backend with one active run, while turn admission is scripted so a
// continuation can start without PostgreSQL.
func newFollowUpTestService(t *testing.T, runner *fakeRunner) (*Service, *scriptedAdmitter, *sessionruntime.MemoryBackend, sessionruntime.Key) {
	t.Helper()
	backend := sessionruntime.NewMemoryBackend()
	service, admitter, key := newFollowUpTestServiceOn(t, runner, backend, backend)
	return service, admitter, backend, key
}

// newFollowUpTestServiceOn seeds the active run on memory and wires the
// manager over queue, which may wrap memory to inject backend failures.
func newFollowUpTestServiceOn(t *testing.T, runner *fakeRunner, memory *sessionruntime.MemoryBackend, queue sessionruntime.Backend) (*Service, *scriptedAdmitter, sessionruntime.Key) {
	t.Helper()
	backend := memory
	key := sessionruntime.Key{BotID: "bot", SessionID: "session"}
	_, _, err := backend.Update(context.Background(), key, func(snapshot sessionruntime.Snapshot, _ bool) (sessionruntime.Snapshot, bool, error) {
		snapshot.BotID, snapshot.SessionID = key.BotID, key.SessionID
		snapshot.CurrentRunView = &sessionruntime.CurrentRunView{
			RunID: "original-run", TurnID: "turn-1", Generation: "gen-1", OwnerID: "owner-1", Status: sessionruntime.RunStatusRunning,
		}
		return snapshot, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := sessionruntime.NewManager(queue, sessionruntime.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	service, admitter := newAdmittedTurnTestService(runner)
	service.sessionManager = manager
	service.allowedTeam = "team1"
	return service, admitter, key
}

func markFollowUpTestRunTerminal(t *testing.T, backend *sessionruntime.MemoryBackend, key sessionruntime.Key) {
	t.Helper()
	_, _, err := backend.Update(context.Background(), key, func(snapshot sessionruntime.Snapshot, _ bool) (sessionruntime.Snapshot, bool, error) {
		if snapshot.CurrentRunView != nil {
			snapshot.CurrentRunView.Status = sessionruntime.RunStatusCompleted
		}
		return snapshot, true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnqueueDeferredTurnStartsFollowUpWithOriginalCommand(t *testing.T) {
	for _, text := range []string{"later", "edited"} {
		t.Run(text, func(t *testing.T) {
			runner := &fakeRunner{chunks: []string{`{"type":"done"}`}}
			service, admitter, backend, key := newFollowUpTestService(t, runner)
			ctx := context.Background()
			cmd := turn.StartTurnCommand{
				TeamID: "team1", Mode: turn.ModeChat, BotID: key.BotID, ThreadID: key.SessionID,
				ChatID: "chat-42", RouteID: "route-7", ReplyTarget: "tg:1", UserID: "user-1",
				Query: "later", UserVisibleText: "later", IdempotencyKey: "msg-1",
				Attachments: []turn.Attachment{{Type: "image", URL: "https://example.invalid/image.png"}},
			}
			if err := service.EnqueueDeferredTurn(ctx, cmd); err != nil {
				t.Fatalf("enqueue deferred turn: %v", err)
			}
			queues, err := service.ListSessionQueues(ctx, key.BotID, key.SessionID)
			if err != nil || len(queues.FollowUp) != 1 || QueuePayloadText(queues.FollowUp[0].Payload) != "later" {
				t.Fatalf("queued follow-up = %#v, %v", queues.FollowUp, err)
			}
			// The same platform message redelivered while still busy is one item.
			if err := service.EnqueueDeferredTurn(ctx, cmd); err != nil {
				t.Fatalf("replay deferred turn: %v", err)
			}
			if queues, err = service.ListSessionQueues(ctx, key.BotID, key.SessionID); err != nil || len(queues.FollowUp) != 1 {
				t.Fatalf("replayed follow-up queue = %#v, %v", queues.FollowUp, err)
			}

			if text == "edited" {
				if _, err := service.UpdateFollowUp(ctx, key.BotID, key.SessionID, string(queues.FollowUp[0].ID), "user-1", "edited"); err != nil {
					t.Fatal(err)
				}
			}

			markFollowUpTestRunTerminal(t, backend, key)
			service.startFollowUp(ctx, sessionruntime.TerminalRun{RunID: "original-run", BotID: key.BotID, SessionID: key.SessionID})

			if runner.gotReq.ReplyTarget != "tg:1" || runner.gotReq.RouteID != "route-7" || runner.gotReq.ChatID != "chat-42" || runner.gotReq.Query != text ||
				!reflect.DeepEqual(runner.gotReq.Attachments, cmd.Attachments) {
				t.Fatalf("continuation lost the original routing: %+v", runner.gotReq)
			}
			admitter.mu.Lock()
			inputs := append([]sessionruntime.AdmitInput(nil), admitter.inputs...)
			admitter.mu.Unlock()
			if len(inputs) != 1 || !strings.HasPrefix(inputs[0].InvocationID, "follow-up:") {
				t.Fatalf("continuation admission = %#v, want a queue-item retry identity", inputs)
			}
			queues, err = service.ListSessionQueues(ctx, key.BotID, key.SessionID)
			if err != nil || len(queues.FollowUp) != 0 {
				t.Fatalf("follow-up still pending after start: %#v, %v", queues.FollowUp, err)
			}
		})
	}
}

func TestEnqueueDeferredTurnWithoutActiveRunReportsNoActiveRun(t *testing.T) {
	backend := sessionruntime.NewMemoryBackend()
	manager := sessionruntime.NewManager(backend, sessionruntime.Options{})
	t.Cleanup(func() { _ = manager.Close() })
	service := &Service{sessionManager: manager}
	err := service.EnqueueDeferredTurn(context.Background(), turn.StartTurnCommand{
		TeamID: "team1", Mode: turn.ModeChat, BotID: "bot", ThreadID: "idle", Query: "hi",
	})
	if !errors.Is(err, sessionruntime.ErrQueueNoActiveRun) {
		t.Fatalf("idle session deferred enqueue error = %v, want %v", err, sessionruntime.ErrQueueNoActiveRun)
	}
}

// A queued command runs as the sender it recorded, so another user with queue
// access must not be able to put words in that sender's mouth: the edit is
// refused and the stored item keeps its original text and sender.
func TestUpdateFollowUpOnlyBySender(t *testing.T) {
	service, _, _, key := newFollowUpTestService(t, &fakeRunner{})
	ctx := context.Background()
	item, err := service.EnqueueFollowUp(ctx, testQueueInput(key.BotID, key.SessionID, "invoke-1", "original"))
	if err != nil {
		t.Fatal(err)
	}
	for _, editor := range []string{"user-2", ""} {
		if _, err := service.UpdateFollowUp(ctx, key.BotID, key.SessionID, string(item.ID), editor, "forged"); !errors.Is(err, ErrQueueItemNotEditable) {
			t.Fatalf("edit by %q error = %v, want %v", editor, err, ErrQueueItemNotEditable)
		}
	}
	queues, err := service.ListSessionQueues(ctx, key.BotID, key.SessionID)
	if err != nil || len(queues.FollowUp) != 1 {
		t.Fatalf("follow-up queue = %#v, %v", queues.FollowUp, err)
	}
	cmd, err := followUpCommand(queues.FollowUp[0])
	if err != nil || cmd.Query != "original" || cmd.UserID != "user-1" {
		t.Fatalf("refused edit changed the item: %+v, %v", cmd, err)
	}

	edited, err := service.UpdateFollowUp(ctx, key.BotID, key.SessionID, string(item.ID), "user-1", "revised")
	if err != nil {
		t.Fatalf("sender edit: %v", err)
	}
	cmd, err = followUpCommand(edited)
	if err != nil || cmd.Query != "revised" || cmd.UserVisibleText != "revised" || cmd.UserID != "user-1" || cmd.SourceChannelIdentityID != "user-1" {
		t.Fatalf("sender edit = %+v, %v", cmd, err)
	}
}

// Steers and follow-ups share the rewrite. An item without a recorded sender
// user — a platform sender with no linked account, or a payload from before
// items carried a command — has nobody who may edit it.
func TestRewriteQueuePayloadTextRequiresRecordedSender(t *testing.T) {
	withSender, err := encodeQueueCommand(turn.StartTurnCommand{TeamID: "team1", BotID: "bot", ThreadID: "session", UserID: "user-1", Query: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	withoutSender, err := encodeQueueCommand(turn.StartTurnCommand{TeamID: "team1", BotID: "bot", ThreadID: "session", SourceChannelIdentityID: "ci-1", Query: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		payload []byte
		editor  string
		ok      bool
	}{
		"sender":            {withSender, "user-1", true},
		"other user":        {withSender, "user-2", false},
		"no editor":         {withSender, "", false},
		"unlinked sender":   {withoutSender, "user-1", false},
		"text-only payload": {[]byte(`{"text":"hi"}`), "user-1", false},
	} {
		_, err := rewriteQueuePayloadText(tc.payload, tc.editor, "new")
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, ErrQueueItemNotEditable)) {
			t.Errorf("%s: error = %v, want ok=%v", name, err, tc.ok)
		}
	}
}

// testQueueInput is what an ingress hands the application for one queued text:
// the team and sender it authenticated, next to the session and the text.
func testQueueInput(botID, sessionID, invocationID, text string) QueueInput {
	return QueueInput{
		TeamID: "team1", BotID: botID, SessionID: sessionID, InvocationID: invocationID,
		UserID: "user-1", SourceChannelIdentityID: "user-1", Text: text,
	}
}

// A hosted runtime serves every team from one process and pins none, so the
// continuation must take the team from the item itself: the ingress recorded
// it at enqueue time together with the sender.
func TestQueuedTextFollowUpReplaysRecordedIdentityWithoutServedTeam(t *testing.T) {
	runner := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	service, admitter, backend, key := newFollowUpTestService(t, runner)
	service.allowedTeam = ""
	ctx := context.Background()
	input := testQueueInput(key.BotID, key.SessionID, "invoke-1", "hello")
	input.TeamID, input.UserID, input.SourceChannelIdentityID = "team-cloud", "user-9", "identity-9"
	item, err := service.EnqueueFollowUp(ctx, input)
	if err != nil {
		t.Fatalf("enqueue follow-up: %v", err)
	}
	if QueuePayloadText(item.Payload) != "hello" {
		t.Fatalf("queued text = %q", QueuePayloadText(item.Payload))
	}
	markFollowUpTestRunTerminal(t, backend, key)
	service.startFollowUp(ctx, sessionruntime.TerminalRun{RunID: "original-run", BotID: key.BotID, SessionID: key.SessionID})

	if runner.gotReq.Query != "hello" || runner.gotReq.UserID != "user-9" || runner.gotReq.SourceChannelIdentityID != "identity-9" ||
		runner.gotReq.BotID != key.BotID || runner.gotReq.ChatID != key.BotID || runner.gotReq.ThreadID != key.SessionID {
		t.Fatalf("continuation lost the recorded identity: %+v", runner.gotReq)
	}
	admitter.mu.Lock()
	inputs := append([]sessionruntime.AdmitInput(nil), admitter.inputs...)
	admitter.mu.Unlock()
	if len(inputs) != 1 || inputs[0].InvocationID != "follow-up:"+string(item.ID) {
		t.Fatalf("continuation admission = %#v", inputs)
	}
	queues, err := service.ListSessionQueues(ctx, key.BotID, key.SessionID)
	if err != nil || len(queues.FollowUp) != 0 {
		t.Fatalf("follow-up still pending after start: %#v, %v", queues.FollowUp, err)
	}
}

func TestQueueInputRequiresRecordedTeam(t *testing.T) {
	service, _, _, key := newFollowUpTestService(t, &fakeRunner{})
	input := testQueueInput(key.BotID, key.SessionID, "invoke-1", "hello")
	input.TeamID = ""
	if _, err := service.EnqueueFollowUp(context.Background(), input); !errors.Is(err, ErrQueueInputIncomplete) {
		t.Fatalf("follow-up without team = %v, want %v", err, ErrQueueInputIncomplete)
	}
	if _, err := service.EnqueueSteer(context.Background(), input); !errors.Is(err, ErrQueueInputIncomplete) {
		t.Fatalf("steer without team = %v, want %v", err, ErrQueueInputIncomplete)
	}
	input.TeamID, input.Text = "team1", "  "
	if _, err := service.EnqueueFollowUp(context.Background(), input); !errors.Is(err, sessionruntime.ErrQueueInvalidReference) {
		t.Fatalf("follow-up without text = %v, want %v", err, sessionruntime.ErrQueueInvalidReference)
	}
}

func TestFollowUpCommandReportsWhatAnItemIsMissing(t *testing.T) {
	item := func(id string, payload []byte) sessionruntime.FollowUpItem {
		return sessionruntime.FollowUpItem{ID: sessionruntime.FollowUpItemID(id), BotID: "bot", SessionID: "session", Payload: payload}
	}
	encode := func(cmd turn.StartTurnCommand) []byte {
		payload, err := encodeQueueCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	cmd, err := followUpCommand(item("f1", encode(turn.StartTurnCommand{TeamID: "team1", BotID: "bot", ThreadID: "session", Query: "hello"})))
	if err != nil || !cmd.NoDefer || cmd.IdempotencyKey != "follow-up:f1" || cmd.TeamID != "team1" || cmd.Query != "hello" {
		t.Fatalf("follow-up command = %+v, %v", cmd, err)
	}
	for _, tc := range []struct {
		name    string
		payload []byte
		want    error
	}{
		{"text-only payload from an earlier release", []byte(`{"text":"hello"}`), errFollowUpPayloadWithoutCommand},
		{"unparseable payload", []byte(`{"text":`), errFollowUpPayloadWithoutCommand},
		{"command for another session", encode(turn.StartTurnCommand{TeamID: "team1", BotID: "bot", ThreadID: "other", Query: "x"}), errFollowUpCommandForeignSession},
		{"command without team", encode(turn.StartTurnCommand{BotID: "bot", ThreadID: "session", Query: "x"}), errFollowUpCommandWithoutTeam},
		{"command without input", encode(turn.StartTurnCommand{TeamID: "team1", BotID: "bot", ThreadID: "session"}), errFollowUpCommandWithoutInput},
	} {
		if _, err := followUpCommand(item("f2", tc.payload)); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// An item the continuation cannot replay is rejected at the boundary that
// claimed it, and the same boundary goes on to the next accepted item. Before
// this, such an item was released back to accepted and reclaimed at every
// terminal boundary without ever starting or being reported.
func TestUnreplayableFollowUpIsRejectedAndDoesNotBlockTheQueue(t *testing.T) {
	runner := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	service, admitter, backend, key := newFollowUpTestService(t, runner)
	ctx := context.Background()
	legacy, err := service.sessionManager.EnqueueFollowUp(ctx, key, "legacy-item", "legacy-invocation", []byte(`{"text":"from an earlier release"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueFollowUp(ctx, testQueueInput(key.BotID, key.SessionID, "invoke-2", "runs after it")); err != nil {
		t.Fatal(err)
	}
	markFollowUpTestRunTerminal(t, backend, key)
	service.startFollowUp(ctx, sessionruntime.TerminalRun{RunID: "original-run", BotID: key.BotID, SessionID: key.SessionID})

	if runner.gotReq.Query != "runs after it" {
		t.Fatalf("second item did not start after the first was rejected: %+v", runner.gotReq)
	}
	admitter.mu.Lock()
	started := len(admitter.inputs)
	admitter.mu.Unlock()
	if started != 1 {
		t.Fatalf("admitted %d runs, want 1", started)
	}
	queues, err := service.ListSessionQueues(ctx, key.BotID, key.SessionID)
	if err != nil || len(queues.FollowUp) != 0 {
		t.Fatalf("pending follow-ups after rejection = %#v, %v", queues.FollowUp, err)
	}
	// The rejected item is terminal: a later boundary must not claim it again.
	if _, _, ok, err := service.sessionManager.ClaimNextFollowUp(ctx, key, "later-run"); err != nil || ok {
		t.Fatalf("rejected item %s was claimable again: ok=%v err=%v", legacy.ID, ok, err)
	}
}

// A team this instance does not serve never becomes served between two
// boundaries, so the item is rejected instead of released for another try.
func TestFollowUpForUnservedTeamIsRejectedNotRetried(t *testing.T) {
	runner := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	service, admitter, backend, key := newFollowUpTestService(t, runner)
	ctx := context.Background()
	foreign := testQueueInput(key.BotID, key.SessionID, "invoke-foreign", "for another team")
	foreign.TeamID = "team-elsewhere"
	if _, err := service.EnqueueFollowUp(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueFollowUp(ctx, testQueueInput(key.BotID, key.SessionID, "invoke-served", "for this team")); err != nil {
		t.Fatal(err)
	}
	markFollowUpTestRunTerminal(t, backend, key)
	service.startFollowUp(ctx, sessionruntime.TerminalRun{RunID: "original-run", BotID: key.BotID, SessionID: key.SessionID})
	// The unserved item is terminal; the served one behind it waits for the
	// next boundary, exactly as any second item does after a started first.
	queues, err := service.ListSessionQueues(ctx, key.BotID, key.SessionID)
	if err != nil || len(queues.FollowUp) != 1 || QueuePayloadText(queues.FollowUp[0].Payload) != "for this team" {
		t.Fatalf("pending after unserved rejection = %#v, %v", queues.FollowUp, err)
	}
	admitter.mu.Lock()
	started := len(admitter.inputs)
	admitter.mu.Unlock()
	if started != 0 || runner.gotReq.Query != "" {
		t.Fatalf("unserved item was admitted: inputs=%d req=%+v", started, runner.gotReq)
	}
	if _, _, ok, err := service.sessionManager.ClaimNextFollowUp(ctx, key, "original-run"); err != nil || !ok {
		t.Fatalf("served item should be claimable by the same boundary: ok=%v err=%v", ok, err)
	}
}

type failingRejectBackend struct {
	*sessionruntime.MemoryBackend
	rejects int
}

func (b *failingRejectBackend) RejectFollowUp(context.Context, sessionruntime.Key, sessionruntime.FollowUpClaimRef, string) error {
	b.rejects++
	return errors.New("injected reject failure")
}

// When the backend cannot record a rejection the claim stays on the item, and
// the next ClaimNextFollowUp for the same run would hand it straight back. The
// starter must stop rather than spin on it.
func TestFollowUpStarterStopsWhenRejectionCannotBeRecorded(t *testing.T) {
	runner := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	memory := sessionruntime.NewMemoryBackend()
	backend := &failingRejectBackend{MemoryBackend: memory}
	service, admitter, key := newFollowUpTestServiceOn(t, runner, memory, backend)
	ctx := context.Background()
	if _, err := service.sessionManager.EnqueueFollowUp(ctx, key, "legacy-item", "legacy-invocation", []byte(`{"text":"no command"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueFollowUp(ctx, testQueueInput(key.BotID, key.SessionID, "invoke-2", "behind it")); err != nil {
		t.Fatal(err)
	}
	markFollowUpTestRunTerminal(t, memory, key)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.startFollowUp(ctx, sessionruntime.TerminalRun{RunID: "original-run", BotID: key.BotID, SessionID: key.SessionID})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("starter kept spinning after the rejection failed")
	}
	if backend.rejects != 1 {
		t.Fatalf("reject attempts = %d, want exactly one before giving up", backend.rejects)
	}
	admitter.mu.Lock()
	started := len(admitter.inputs)
	admitter.mu.Unlock()
	if started != 0 || runner.gotReq.Query != "" {
		t.Fatalf("an item was admitted although the boundary could not move on: inputs=%d req=%+v", started, runner.gotReq)
	}
}

func TestFollowUpStartIsSingleFlightPerSession(t *testing.T) {
	runner := &fakeRunner{chunks: []string{`{"type":"done"}`}}
	service, _, backend, key := newFollowUpTestService(t, runner)
	ctx := context.Background()
	if _, err := service.EnqueueFollowUp(ctx, testQueueInput(key.BotID, key.SessionID, "invoke-1", "queued")); err != nil {
		t.Fatal(err)
	}
	markFollowUpTestRunTerminal(t, backend, key)
	service.followUpStarts.Store(key.String(), &followUpStart{})
	service.startFollowUp(ctx, sessionruntime.TerminalRun{RunID: "original-run", BotID: key.BotID, SessionID: key.SessionID})
	if runner.gotReq.Query != "" {
		t.Fatalf("second starter ran while another was in flight: %+v", runner.gotReq)
	}
	queues, err := service.ListSessionQueues(ctx, key.BotID, key.SessionID)
	if err != nil || len(queues.FollowUp) != 1 || queues.FollowUp[0].Status != sessionruntime.QueueAccepted {
		t.Fatalf("follow-up should remain accepted: %#v, %v", queues.FollowUp, err)
	}
}

func TestFollowUpSchedulingRaces(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pending     int
		duringDrain bool
	}{
		{"enqueue after terminal notification", 1, false},
		{"next terminal before prior drain returns", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				service, admitter, backend, key := newFollowUpTestService(t, &fakeRunner{chunks: []string{`{"type":"done"}`}})
				ctx := context.Background()
				for _, id := range []string{"first", "second"}[:tc.pending] {
					if _, err := service.EnqueueFollowUp(ctx, testQueueInput(key.BotID, key.SessionID, id, "queued")); err != nil {
						t.Fatal(err)
					}
				}
				if tc.duringDrain {
					service.sessionRuntime = &terminalNotifyingAdmitter{scriptedAdmitter: admitter, terminal: func(ctx context.Context, h sessionruntime.RunHandle) {
						service.startFollowUp(ctx, sessionruntime.TerminalRun{RunID: h.RunID, BotID: key.BotID, SessionID: key.SessionID})
					}}
				}
				markFollowUpTestRunTerminal(t, backend, key)
				if tc.duringDrain {
					service.startFollowUp(ctx, sessionruntime.TerminalRun{RunID: "original-run", BotID: key.BotID, SessionID: key.SessionID})
				} else {
					service.kickFollowUpIfIdle(ctx, key.BotID, key.SessionID, "original-run")
				}
				// PendingQueues hides claimed items before StartTurn admits them.
				// Wait for the starter and drain goroutines, not an empty queue,
				// before asserting the continuation's observable result.
				synctest.Wait()
				queues, err := service.ListSessionQueues(ctx, key.BotID, key.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				if len(queues.FollowUp) != 0 {
					t.Fatalf("trigger lost: %d pending follow-ups", len(queues.FollowUp))
				}
				admitter.mu.Lock()
				started := len(admitter.inputs)
				finished := len(admitter.finishes)
				admitter.mu.Unlock()
				if started != tc.pending {
					t.Fatalf("admitted %d runs, want %d", started, tc.pending)
				}
				if finished != tc.pending {
					t.Fatalf("finished %d runs, want %d", finished, tc.pending)
				}
			})
		})
	}
}

type terminalNotifyingAdmitter struct {
	*scriptedAdmitter
	terminal func(context.Context, sessionruntime.RunHandle)
}

func (a *terminalNotifyingAdmitter) FinishRunWithErrorCode(ctx context.Context, handle sessionruntime.RunHandle, status, message string) error {
	if err := a.scriptedAdmitter.FinishRunWithErrorCode(ctx, handle, status, message); err != nil {
		return err
	}
	// Force the valid schedule in which the terminal observer runs before
	// the handle closes and before the first starter has returned from drain.
	a.terminal(ctx, handle)
	return nil
}
