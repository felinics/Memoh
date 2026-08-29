package sessionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

const (
	testBotID     = "bot-runtime"
	testSessionID = "session-runtime"
	testRunID     = "stream-runtime"
)

func requireRunHandle(t *testing.T, manager *Manager, botID, sessionID, runID string) RunHandle {
	t.Helper()
	ref, ok, err := manager.RunRef(context.Background(), botID, sessionID, runID)
	if err != nil || !ok {
		t.Fatalf("load run handle for %q = ok:%v err:%v", runID, ok, err)
	}
	if ref.BotID != botID || ref.SessionID != sessionID {
		t.Fatalf("run handle scope = %s/%s, want %s/%s", ref.BotID, ref.SessionID, botID, sessionID)
	}
	return RunHandle{BotID: ref.BotID, SessionID: ref.SessionID, RunID: ref.RunID, Generation: ref.Generation}
}

type runtimeBackendContractSuite struct {
	newBackend        func(t *testing.T) Backend
	newSharedBackends func(t *testing.T, count int) []Backend
}

type overflowObservingBackend struct {
	Backend
	overflow chan Event
}

func (b *overflowObservingBackend) Subscribe(ctx context.Context, key Key) (Subscription, error) {
	sub, err := b.Backend.Subscribe(ctx, key)
	if err != nil {
		return Subscription{}, err
	}
	forwardCtx, cancel := context.WithCancel(ctx)
	events := make(chan Event)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		defer sub.Close()
		for {
			select {
			case <-forwardCtx.Done():
				return
			case event, ok := <-sub.C:
				if !ok {
					return
				}
				select {
				case events <- event:
					if event.Type == EventRuntimeDropped && event.Message == "runtime subscriber buffer overflow" {
						select {
						case b.overflow <- event:
						default:
						}
					}
				case <-forwardCtx.Done():
					return
				}
			}
		}
	}()
	var closeOnce sync.Once
	return Subscription{
		C: events,
		Close: func() {
			closeOnce.Do(cancel)
			<-done
		},
	}, nil
}

type blockingCommandResultLoadBackend struct {
	DistributedBackend
	started chan struct{}
	once    sync.Once
}

type closeErrorBackend struct {
	Backend
	err error
}

func (b closeErrorBackend) Close() error {
	return b.err
}

type gatedCommandResultLoadBackend struct {
	DistributedBackend
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingCommandResultLoadBackend) LoadCommandResult(ctx context.Context, _ string) (Command, bool, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return Command{}, false, ctx.Err()
}

func (b *gatedCommandResultLoadBackend) LoadCommandResult(ctx context.Context, commandID string) (Command, bool, error) {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return Command{}, false, ctx.Err()
	}
	return b.DistributedBackend.LoadCommandResult(ctx, commandID)
}

func TestRuntimeCommandResultPollingHonorsAcknowledgementDeadline(t *testing.T) {
	backend := &blockingCommandResultLoadBackend{started: make(chan struct{})}
	manager := NewManager(backend, Options{CommandAckTTL: 40 * time.Millisecond})
	request := Command{ID: "command-deadline", PayloadHash: commandPayloadHash([]byte(`{"decision":"approve"}`))}

	startedAt := time.Now()
	err := manager.waitCommandResult(context.Background(), request, make(chan error), manager.commandTimeout())
	elapsed := time.Since(startedAt)
	if err == nil || !strings.Contains(err.Error(), "not acknowledged") {
		t.Fatalf("wait error = %v, want acknowledgement timeout", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("blocked result lookup exceeded acknowledgement deadline: %s", elapsed)
	}
	select {
	case <-backend.started:
	default:
		t.Fatal("result polling did not reach the backend")
	}
}

func TestRuntimeCommandAdmissionDeduplicatesBeforeWorkerScheduling(t *testing.T) {
	manager := NewManager(NewMemoryBackend(), Options{})
	cmd := Command{Type: CommandToolApprovalResponse, ID: "stable-command-id"}
	if !manager.admitCommand(cmd) {
		t.Fatal("first command was not admitted")
	}
	if manager.admitCommand(cmd) {
		t.Fatal("duplicate command was admitted while the original was queued")
	}
	manager.releaseCommandAdmission(cmd)
	if !manager.admitCommand(cmd) {
		t.Fatal("command was not admitted after the original completed")
	}
}

type distributedRuntimeBackendContractSuite struct {
	newBackend        func(t *testing.T) DistributedBackend
	newSharedBackends func(t *testing.T, count int) []DistributedBackend
}

func (s distributedRuntimeBackendContractSuite) common() runtimeBackendContractSuite {
	return runtimeBackendContractSuite{
		newBackend: func(t *testing.T) Backend {
			return s.newBackend(t)
		},
		newSharedBackends: func(t *testing.T, count int) []Backend {
			distributed := s.newSharedBackends(t, count)
			backends := make([]Backend, len(distributed))
			for i := range distributed {
				backends[i] = distributed[i]
			}
			return backends
		},
	}
}

func memoryRuntimeBackendSuite() runtimeBackendContractSuite {
	return runtimeBackendContractSuite{
		newBackend: func(t *testing.T) Backend {
			t.Helper()
			return NewMemoryBackend()
		},
		newSharedBackends: func(t *testing.T, count int) []Backend {
			t.Helper()
			backend := NewMemoryBackend()
			backends := make([]Backend, count)
			for i := range backends {
				backends[i] = backend
			}
			return backends
		},
	}
}

// The convenience overloads below used to be exported Manager methods. Only
// tests ever called them — production admits through Admit — so they live here
// now, next to the tests that need the shorthand, instead of widening the
// package's public surface with eight ways to say one thing.

func (m *Manager) startRunWithAdmission(ctx context.Context, botID, sessionID, runID string, admission RunAdmissionView, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- turn.InjectMessage) error {
	_, err := m.StartRunWithAdmissionBuilderHandle(ctx, botID, sessionID, runID, func(context.Context, RunHandle) (RunAdmissionView, error) {
		return admission, nil
	}, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) startRunWithOperation(ctx context.Context, botID, sessionID, runID string, operation *RunOperationView, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- turn.InjectMessage) error {
	return m.startRunWithAdmission(ctx, botID, sessionID, runID, RunAdmissionView{Operation: operation}, abortCh, cancel, injectCh)
}

func (m *Manager) startRunWithAdmissionBuilder(ctx context.Context, botID, sessionID, runID string, builder func(context.Context) (RunAdmissionView, error), abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- turn.InjectMessage) error {
	_, err := m.StartRunWithAdmissionBuilderHandle(ctx, botID, sessionID, runID, func(ctx context.Context, _ RunHandle) (RunAdmissionView, error) {
		return builder(ctx)
	}, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) startRunWithOperationBuilder(ctx context.Context, botID, sessionID, runID string, builder func(context.Context) (*RunOperationView, error), abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- turn.InjectMessage) error {
	_, err := m.StartRunWithAdmissionBuilderHandle(ctx, botID, sessionID, runID, func(ctx context.Context, _ RunHandle) (RunAdmissionView, error) {
		operation, err := builder(ctx)
		return RunAdmissionView{Operation: operation}, err
	}, abortCh, cancel, injectCh)
	return err
}

func (m *Manager) startRunWithAdmissionBuilderAndOwnership(ctx context.Context, botID, sessionID, runID string, builder func(context.Context, RunHandle) (RunAdmissionView, error), ownershipCancel context.CancelCauseFunc, abortCh chan<- struct{}, cancel context.CancelFunc, injectCh chan<- turn.InjectMessage) error {
	_, err := m.StartRunWithAdmissionBuilderAndOwnershipHandle(ctx, botID, sessionID, runID, builder, ownershipCancel, abortCh, cancel, injectCh)
	return err
}

func testRuntimeManager(t *testing.T, backend Backend, ownerID string) *Manager {
	t.Helper()
	manager := NewManager(backend, Options{
		OwnerID:       ownerID,
		StateTTL:      time.Hour,
		OwnerLeaseTTL: 2 * time.Second,
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})
	return manager
}

func testRuntimeManagerWithOptions(t *testing.T, backend Backend, opts Options) *Manager {
	t.Helper()
	manager := NewManager(backend, opts)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})
	return manager
}

type dropCommandBackend struct {
	DistributedBackend
	published chan Command
}

type dropFirstCommandBackend struct {
	DistributedBackend
	publishes atomic.Int32
}

func (b *dropFirstCommandBackend) PublishCommand(ctx context.Context, ownerID string, command Command) error {
	if b.publishes.Add(1) == 1 {
		return nil
	}
	return b.DistributedBackend.PublishCommand(ctx, ownerID, command)
}

func (b dropCommandBackend) PublishCommand(_ context.Context, _ string, command Command) error {
	if b.published != nil {
		b.published <- command
	}
	return nil
}

type gatedCommandSubscribeBackend struct {
	DistributedBackend
	entered  chan struct{}
	release  chan struct{}
	commands chan Command
	closed   chan struct{}
	once     sync.Once
}

type contextBlockingCommandSubscribeBackend struct {
	DistributedBackend
	entered chan struct{}
}

type contextBlockingHealthBackend struct {
	DistributedBackend
	checked chan struct{}
}

func (b *contextBlockingHealthBackend) CheckHealth(ctx context.Context) error {
	close(b.checked)
	<-ctx.Done()
	return ctx.Err()
}

func (*contextBlockingHealthBackend) SubscribeCommands(context.Context, string) (CommandSubscription, error) {
	panic("command subscription must not start before health check succeeds")
}

func (b *contextBlockingCommandSubscribeBackend) SubscribeCommands(ctx context.Context, _ string) (CommandSubscription, error) {
	close(b.entered)
	<-ctx.Done()
	return CommandSubscription{}, ctx.Err()
}

func (b *gatedCommandSubscribeBackend) SubscribeCommands(context.Context, string) (CommandSubscription, error) {
	close(b.entered)
	<-b.release
	return CommandSubscription{C: b.commands, Close: b.closeSubscription}, nil
}

func (*gatedCommandSubscribeBackend) Close() error {
	return nil
}

func (b *gatedCommandSubscribeBackend) closeSubscription() {
	b.once.Do(func() {
		close(b.commands)
		close(b.closed)
	})
}

type startRunGateBackend struct {
	DistributedBackend
	claimed chan struct{}
	release chan struct{}
	once    sync.Once
}

type gatedReleaseCloseBackend struct {
	DistributedBackend
	releaseStarted chan struct{}
	allowRelease   chan struct{}
	closed         chan struct{}
	releaseOnce    sync.Once
	closeOnce      sync.Once
	closeCalls     atomic.Int32
}

func (b *gatedReleaseCloseBackend) ReleaseRun(ctx context.Context, key Key, ref RunRef, update ActiveRunUpdate) (Snapshot, bool, error) {
	b.releaseOnce.Do(func() { close(b.releaseStarted) })
	select {
	case <-b.allowRelease:
	case <-ctx.Done():
		return Snapshot{}, false, ctx.Err()
	}
	return b.DistributedBackend.ReleaseRun(ctx, key, ref, update)
}

func (b *gatedReleaseCloseBackend) Close() error {
	b.closeCalls.Add(1)
	b.closeOnce.Do(func() { close(b.closed) })
	return b.DistributedBackend.Close()
}

type preClaimGateBackend struct {
	DistributedBackend
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type activationGateBackend struct {
	Backend
	calls   atomic.Int64
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type delayedOwnershipValidationBackend struct {
	DistributedBackend
	validated chan struct{}
	release   chan struct{}
}

type delayedRunRefCleanupBackend struct {
	DistributedBackend
	targetGeneration string
	deleted          chan struct{}
	release          chan struct{}
	once             sync.Once
}

func (b *delayedRunRefCleanupBackend) DeleteRunRef(ctx context.Context, ref RunRef) (bool, error) {
	deleted, err := b.DistributedBackend.DeleteRunRef(ctx, ref)
	if ref.Generation != b.targetGeneration {
		return deleted, err
	}
	b.once.Do(func() { close(b.deleted) })
	select {
	case <-b.release:
		return deleted, err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

type blockedLeaseRenewalBackend struct {
	DistributedBackend
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type uninterruptibleLeaseRenewalBackend struct {
	DistributedBackend
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockedLeaseRenewalBackend) RenewLease(ctx context.Context, key Key, runID, ownerID, generation string, now, expiresAt time.Time) error {
	if b.calls.Add(1) == 1 {
		return b.DistributedBackend.RenewLease(ctx, key, runID, ownerID, generation, now, expiresAt)
	}
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return b.DistributedBackend.RenewLease(ctx, key, runID, ownerID, generation, now, expiresAt)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *uninterruptibleLeaseRenewalBackend) RenewLease(ctx context.Context, key Key, runID, ownerID, generation string, now, expiresAt time.Time) error {
	if b.calls.Add(1) == 1 {
		return b.DistributedBackend.RenewLease(ctx, key, runID, ownerID, generation, now, expiresAt)
	}
	b.once.Do(func() { close(b.started) })
	<-b.release
	return ctx.Err()
}

func (b *delayedOwnershipValidationBackend) ValidateRunOwnership(ctx context.Context, key Key, ref RunRef) error {
	if err := b.DistributedBackend.ValidateRunOwnership(ctx, key, ref); err != nil {
		return err
	}
	close(b.validated)
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type toggledUpdateFailureBackend struct {
	DistributedBackend
	failing atomic.Bool
}

type toggledNowFailureBackend struct {
	DistributedBackend
	failing atomic.Bool
}

type countCommandPublishBackend struct {
	DistributedBackend
	publishes atomic.Int64
}

type delayedNowBackend struct {
	Backend
	delay time.Duration
}

func (b delayedNowBackend) Now(ctx context.Context) (time.Time, error) {
	now, err := b.Backend.Now(ctx)
	if err != nil {
		return time.Time{}, err
	}
	timer := time.NewTimer(b.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return now, nil
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	}
}

func (b *toggledNowFailureBackend) Now(ctx context.Context) (time.Time, error) {
	if b.failing.Load() {
		return time.Time{}, context.DeadlineExceeded
	}
	return b.DistributedBackend.Now(ctx)
}

func (b *countCommandPublishBackend) PublishCommand(ctx context.Context, ownerID string, command Command) error {
	b.publishes.Add(1)
	return b.DistributedBackend.PublishCommand(ctx, ownerID, command)
}

func (b *toggledUpdateFailureBackend) Update(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error) {
	if b.failing.Load() {
		return Snapshot{}, false, errors.New("injected persistent update failure")
	}
	return b.DistributedBackend.Update(ctx, key, update)
}

func (b *toggledUpdateFailureBackend) UpdateActiveRun(ctx context.Context, key Key, runID, generation string, update ActiveRunUpdate) (Snapshot, bool, error) {
	if b.failing.Load() {
		return Snapshot{}, false, errors.New("injected persistent update failure")
	}
	return b.DistributedBackend.UpdateActiveRun(ctx, key, runID, generation, update)
}

func (b *toggledUpdateFailureBackend) ReleaseRun(ctx context.Context, key Key, ref RunRef, update ActiveRunUpdate) (Snapshot, bool, error) {
	if b.failing.Load() {
		return Snapshot{}, false, errors.New("injected persistent update failure")
	}
	return b.DistributedBackend.ReleaseRun(ctx, key, ref, update)
}

func (b *startRunGateBackend) StartRun(ctx context.Context, key Key, ref RunRef, update SnapshotUpdate) (Snapshot, bool, error) {
	snapshot, changed, err := b.DistributedBackend.StartRun(ctx, key, ref, update)
	b.once.Do(func() { close(b.claimed) })
	select {
	case <-b.release:
		return snapshot, changed, err
	case <-ctx.Done():
		return Snapshot{}, false, ctx.Err()
	}
}

func (b *preClaimGateBackend) StartRun(ctx context.Context, key Key, ref RunRef, update SnapshotUpdate) (Snapshot, bool, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
		return b.DistributedBackend.StartRun(ctx, key, ref, update)
	case <-ctx.Done():
		return Snapshot{}, false, ctx.Err()
	}
}

func (b *activationGateBackend) Update(ctx context.Context, key Key, update SnapshotUpdate) (Snapshot, bool, error) {
	if b.calls.Add(1) == 2 {
		b.once.Do(func() { close(b.entered) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return Snapshot{}, false, ctx.Err()
		}
	}
	return b.Backend.Update(ctx, key, update)
}

func richRuntimeAgentScript() []native.StreamEvent {
	return []native.StreamEvent{
		{Type: native.EventAgentStart},
		{Type: native.EventReasoningDelta, Delta: "I need to inspect the workspace."},
		{Type: native.EventTextDelta, Delta: "I will check the current state."},
		{Type: native.EventToolCallStart, ToolName: "exec", ToolCallID: "call-exec", Input: map[string]any{"command": "pwd"}},
		{Type: native.EventToolCallProgress, ToolName: "exec", ToolCallID: "call-exec", Progress: "queued"},
		{Type: native.EventToolCallEnd, ToolName: "exec", ToolCallID: "call-exec", Result: map[string]any{"stdout": "/workspace\n"}},
		{Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-approval", Input: map[string]any{"command": "rm -rf build"}, ApprovalID: "approval-1", ShortID: 7, Status: "pending"},
		{Type: native.EventUserInputRequest, ToolName: "ask_user", ToolCallID: "call-ask", Input: map[string]any{"questions": []any{map[string]any{"text": "Continue?", "kind": "single_select"}}}, UserInputID: "input-1", ShortID: 8, Status: "pending", Metadata: map[string]any{"ui_payload": map[string]any{"version": 2, "questions": []any{map[string]any{"id": "q1", "text": "Continue?", "kind": "single_select"}}}}},
	}
}

func TestMemoryRuntimeManagerContract(t *testing.T) {
	t.Parallel()
	runCommonRuntimeManagerContract(t, memoryRuntimeBackendSuite())
}

func TestActiveCommandContextRequiresAbsoluteExpiry(t *testing.T) {
	t.Parallel()

	manager := NewManager(NewMemoryBackend(), Options{CommandAckTTL: time.Second})
	ctx, cancel, err := manager.activeCommandContext(context.Background(), Command{CreatedAt: time.Now().UTC()})
	cancel()
	if ctx != nil || !errors.Is(err, ErrCommandExpired) {
		t.Fatalf("command context = (%v, %v), want ErrCommandExpired", ctx, err)
	}
}

func TestActiveCommandPayloadHashUsesResponseSemantics(t *testing.T) {
	t.Parallel()

	approvedByFirstActor := []byte(`{"Decision":"approve","Reason":" ok ","ActorUserID":"user-a"}`)
	approvedBySecondActor := []byte(`{"decision":"approved","reason":"ok","actor_user_id":"user-b"}`)
	if first, second := activeCommandPayloadHash(CommandToolApprovalResponse, approvedByFirstActor), activeCommandPayloadHash(CommandToolApprovalResponse, approvedBySecondActor); first != second {
		t.Fatalf("equivalent approval hashes differ: %q != %q", first, second)
	}
	if approved, rejected := activeCommandPayloadHash(CommandToolApprovalResponse, approvedByFirstActor), activeCommandPayloadHash(CommandToolApprovalResponse, []byte(`{"decision":"reject","reason":"ok"}`)); approved == rejected {
		t.Fatal("conflicting approval responses share a payload hash")
	}

	answersA := []byte(`{"Answers":[{"QuestionID":"q2","Text":" second "},{"QuestionID":"q1","OptionIDs":["o1"]}],"ActorUserID":"user-a"}`)
	answersB := []byte(`{"answers":[{"question_id":"q1","option_ids":["o1"]},{"question_id":"q2","text":"second"}],"actor_user_id":"user-b"}`)
	if first, second := activeCommandPayloadHash(CommandUserInputResponse, answersA), activeCommandPayloadHash(CommandUserInputResponse, answersB); first != second {
		t.Fatalf("equivalent user-input hashes differ: %q != %q", first, second)
	}
	answersWithIgnoredFields := []byte(`{"answers":[{"question_id":"q1","option_ids":["o1"]},{"question_id":"q2","text":"second"}],"reason":"ignored","text_answer":"ignored"}`)
	if first, second := activeCommandPayloadHash(CommandUserInputResponse, answersB), activeCommandPayloadHash(CommandUserInputResponse, answersWithIgnoredFields); first != second {
		t.Fatalf("structured answer hash includes ignored fields: %q != %q", first, second)
	}
	canceledA := []byte(`{"canceled":true,"reason":"later","answers":[{"question_id":"q1","text":"ignored"}]}`)
	canceledB := []byte(`{"Canceled":true,"Reason":" later ","TextAnswer":"ignored"}`)
	if first, second := activeCommandPayloadHash(CommandUserInputResponse, canceledA), activeCommandPayloadHash(CommandUserInputResponse, canceledB); first != second {
		t.Fatalf("cancel hash includes ignored answer fields: %q != %q", first, second)
	}
}

// Rows decided by pre-option_id binaries keep their persisted payload hashes,
// so the canonical form of a payload without the new fields must stay
// byte-identical to what those binaries computed. Zero values (empty
// option_id, skipped=false) must not change a legacy hash; only real
// selections and real skips may.
func TestActiveCommandPayloadHashKeepsLegacyCanonicalForm(t *testing.T) {
	t.Parallel()

	legacyApproval := activeCommandPayloadHash(CommandToolApprovalResponse, []byte(`{"decision":"approve"}`))
	if want := commandPayloadHash([]byte(`{"decision":"approved","reason":""}`)); legacyApproval != want {
		t.Fatalf("legacy approval canonical form changed: %q != %q", legacyApproval, want)
	}
	if zeroOption := activeCommandPayloadHash(CommandToolApprovalResponse, []byte(`{"decision":"approve","option_id":""}`)); zeroOption != legacyApproval {
		t.Fatalf("empty option_id changed the legacy hash: %q != %q", zeroOption, legacyApproval)
	}
	if blankOption := activeCommandPayloadHash(CommandToolApprovalResponse, []byte(`{"decision":"approve","option_id":"  "}`)); blankOption != legacyApproval {
		t.Fatalf("whitespace option_id changed the legacy hash: %q != %q", blankOption, legacyApproval)
	}
	if selected := activeCommandPayloadHash(CommandToolApprovalResponse, []byte(`{"decision":"approve","option_id":"allow-once"}`)); selected == legacyApproval {
		t.Fatal("a selected option must change the payload hash")
	}

	legacyAnswer := activeCommandPayloadHash(CommandUserInputResponse, []byte(`{"answers":[{"question_id":"q1","option_ids":["o1"]}]}`))
	if want := commandPayloadHash([]byte(`{"answers":[{"custom_text":"","option_ids":["o1"],"question_id":"q1","text":""}],"canceled":false}`)); legacyAnswer != want {
		t.Fatalf("legacy answer canonical form changed: %q != %q", legacyAnswer, want)
	}
	if zeroSkip := activeCommandPayloadHash(CommandUserInputResponse, []byte(`{"answers":[{"question_id":"q1","option_ids":["o1"],"skipped":false}]}`)); zeroSkip != legacyAnswer {
		t.Fatalf("skipped=false changed the legacy hash: %q != %q", zeroSkip, legacyAnswer)
	}
	if skipped := activeCommandPayloadHash(CommandUserInputResponse, []byte(`{"answers":[{"question_id":"q1","option_ids":["o1"],"skipped":true}]}`)); skipped == legacyAnswer {
		t.Fatal("a real skip must change the payload hash")
	}
}

func TestActiveCommandContextAccountsForBackendTimeLatency(t *testing.T) {
	t.Parallel()

	backend := delayedNowBackend{Backend: NewMemoryBackend(), delay: 40 * time.Millisecond}
	manager := NewManager(backend, Options{CommandAckTTL: time.Second})
	createdAt := time.Now().UTC()
	ctx, cancel, err := manager.activeCommandContext(context.Background(), Command{
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(25 * time.Millisecond),
	})
	cancel()
	if ctx != nil || !errors.Is(err, ErrCommandExpired) {
		t.Fatalf("delayed command context = (%v, %v), want ErrCommandExpired", ctx, err)
	}
}

func waitRuntimeEvent(t *testing.T, events <-chan Event, pred func(Event) bool) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("runtime subscription closed")
			}
			if pred(event) {
				return event
			}
		case <-deadline:
			t.Fatal("timed out waiting for runtime event")
		}
	}
}

func TestRuntimeManagerRejectsRunAfterClose(t *testing.T) {
	manager := NewManager(NewMemoryBackend(), Options{})
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	err := manager.StartRun(
		context.Background(),
		testBotID,
		testSessionID,
		"stream-after-close",
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("start run after close error = %v, want ErrManagerClosed", err)
	}
	if ctrl := manager.localControl("stream-after-close"); ctrl != nil {
		t.Fatalf("closed manager retained run control: %#v", ctrl)
	}
}

func TestRuntimeManagerCloseReturnsStoredResultAfterCompletion(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("backend close failed")
	manager := NewManager(closeErrorBackend{Backend: NewMemoryBackend(), err: closeErr}, Options{})
	if err := manager.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("first Close error = %v, want %v", err, closeErr)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.CloseContext(canceled); !errors.Is(err, closeErr) {
		t.Fatalf("completed CloseContext error = %v, want stored %v", err, closeErr)
	}
}

func receiveTestResult[T any](t *testing.T, label string, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(5 * time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", label)
		return zero
	}
}

func TestRuntimeManagerDoesNotActivateAdmissionAfterClose(t *testing.T) {
	backend := NewMemoryBackend()
	manager := NewManager(backend, Options{})
	builderStarted := make(chan struct{})
	releaseBuilder := make(chan struct{})
	startDone := make(chan error, 1)
	go func() {
		_, err := manager.StartRunWithAdmissionBuilderHandle(
			context.Background(),
			testBotID,
			"session-close-during-admission",
			"stream-close-during-admission",
			func(context.Context, RunHandle) (RunAdmissionView, error) {
				close(builderStarted)
				<-releaseBuilder
				return RunAdmissionView{}, nil
			},
			make(chan struct{}, 1),
			func() {},
			make(chan turn.InjectMessage, 1),
		)
		startDone <- err
	}()
	<-builderStarted

	shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
	cancelShutdown()
	if err := manager.CloseContext(shutdownCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close manager error = %v, want context canceled", err)
	}
	close(releaseBuilder)
	if err := receiveTestResult(t, "closed admission", startDone); !errors.Is(err, ErrManagerClosed) && !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("closed admission error = %v, want manager closed or ownership lost", err)
	}
	snapshot, ok, err := backend.Load(context.Background(), Key{BotID: testBotID, SessionID: "session-close-during-admission"})
	if err != nil || !ok {
		t.Fatalf("load closed admission = ok:%v err:%v", ok, err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status == RunStatusRunning {
		t.Fatalf("closed admission snapshot = %#v, must not activate", snapshot.CurrentRunView)
	}
}

func TestRuntimeManagerDoesNotStartCommandLoopAfterClose(t *testing.T) {
	backend := &gatedCommandSubscribeBackend{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		commands: make(chan Command),
		closed:   make(chan struct{}),
	}
	t.Cleanup(backend.closeSubscription)
	manager := NewManager(backend, Options{OwnerID: "owner-start-close-race"})
	t.Cleanup(func() { _ = manager.Close() })
	startDone := make(chan error, 1)
	go func() {
		startDone <- manager.Start(context.Background())
	}()
	<-backend.entered
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	close(backend.release)
	if err := receiveTestResult(t, "manager start after close", startDone); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("start after close error = %v, want ErrManagerClosed", err)
	}
	receiveTestResult(t, "late command subscription close", backend.closed)
}

func TestRuntimeManagerStartHonorsStartupDeadlineDuringCommandSubscribe(t *testing.T) {
	t.Parallel()

	backend := &contextBlockingCommandSubscribeBackend{entered: make(chan struct{})}
	manager := NewManager(backend, Options{OwnerID: "owner-start-deadline"})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := manager.Start(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("start ignored startup deadline for %s", elapsed)
	}
}

func TestRuntimeManagerStartHonorsStartupDeadlineDuringHealthCheck(t *testing.T) {
	t.Parallel()

	backend := &contextBlockingHealthBackend{checked: make(chan struct{})}
	manager := NewManager(backend, Options{OwnerID: "owner-health-deadline"})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := manager.Start(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start error = %v, want context deadline exceeded", err)
	}
	select {
	case <-backend.checked:
	default:
		t.Fatal("startup health check was not called")
	}
}

func TestRuntimeManagerCloseStopsSubscriptions(t *testing.T) {
	t.Parallel()

	manager := NewManager(NewMemoryBackend(), Options{})
	sub, err := manager.Subscribe(context.Background(), testBotID, "session-close-subscription")
	if err != nil {
		t.Fatalf("subscribe runtime: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	for {
		select {
		case _, ok := <-sub.C:
			if !ok {
				if _, err := manager.Subscribe(context.Background(), testBotID, "session-after-close"); !errors.Is(err, ErrManagerClosed) {
					t.Fatalf("subscribe after manager close error = %v, want ErrManagerClosed", err)
				}
				return
			}
		default:
			t.Fatal("manager Close returned before runtime subscription channel closed")
		}
	}
}

func TestRuntimeManagerDoesNotTerminalizeAdmissionRejectedByClose(t *testing.T) {
	backend := &activationGateBackend{
		Backend: NewMemoryBackend(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager := NewManager(backend, Options{})
	startDone := make(chan error, 1)
	go func() {
		startDone <- manager.StartRun(
			context.Background(), testBotID, "session-close-activation", "stream-close-activation",
			make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1),
		)
	}()
	<-backend.entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	deadline := time.After(time.Second)
	for !manager.isClosed() {
		select {
		case <-deadline:
			t.Fatal("manager close did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(backend.release)
	if err := receiveTestResult(t, "activation rejected by close", startDone); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("start run error = %v, want ErrManagerClosed", err)
	}
	if err := receiveTestResult(t, "manager close after activation rejection", closeDone); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	snapshot, ok, err := backend.Load(context.Background(), Key{BotID: testBotID, SessionID: "session-close-activation"})
	if err != nil || !ok {
		t.Fatalf("load rejected activation = ok:%v err:%v", ok, err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusAdmitting {
		t.Fatalf("rejected activation = %#v, want admitting reservation", snapshot.CurrentRunView)
	}
}

func testGateCleanup(t *testing.T, cancel context.CancelFunc, gate chan struct{}) func() {
	t.Helper()
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(func() {
		cancel()
		release()
	})
	return release
}

func runManagerAbortAcknowledgesReservedRunBeforeTerminalCompletion(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	backend := &startRunGateBackend{
		DistributedBackend: suite.newBackend(t),
		claimed:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "owner-start-abort",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: 60 * time.Millisecond,
		CommandAckTTL: 50 * time.Millisecond,
	})
	runCtx, cancelRun := context.WithCancel(context.Background())
	releaseGate := testGateCleanup(t, cancelRun, backend.release)

	startErr := make(chan error, 1)
	go func() {
		startErr <- manager.StartRun(
			runCtx,
			testBotID,
			testSessionID,
			testRunID,
			make(chan struct{}, 1),
			func() {},
			make(chan turn.InjectMessage, 1),
		)
	}()
	receiveTestResult(t, "reserved run claim", backend.claimed)
	handle := manager.localControl(testRunID).handle()

	type abortResult struct {
		ok  bool
		err error
	}
	aborted := make(chan abortResult, 1)
	go func() {
		ok, err := manager.Abort(runCtx, testBotID, testSessionID, testRunID)
		aborted <- abortResult{ok: ok, err: err}
	}()
	result := receiveTestResult(t, "reserved run abort", aborted)
	if result.err != nil || !result.ok {
		t.Fatalf("abort = ok:%v err:%v", result.ok, result.err)
	}
	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || (snapshot.CurrentRunView.Status != RunStatusAborting && snapshot.CurrentRunView.Status != RunStatusAborted) {
		t.Fatalf("current run = %#v, want aborting or already-aborted admission", snapshot.CurrentRunView)
	}

	releaseGate()
	if err := receiveTestResult(t, "aborted run admission", startErr); err == nil {
		t.Fatal("start run unexpectedly activated after abort")
	}
	if err := manager.FinishRun(context.Background(), handle, RunStatusAborted, ""); err != nil {
		t.Fatalf("finish aborted run: %v", err)
	}
	snapshot, err = manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil || snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusAborted {
		t.Fatalf("terminal snapshot = %#v err:%v", snapshot.CurrentRunView, err)
	}
}

func runManagerDoesNotActivateAdmissionAfterClaimLeaseExpires(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	const leaseTTL = 500 * time.Millisecond
	backend := &startRunGateBackend{
		DistributedBackend: suite.newBackend(t),
		claimed:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "owner-expired-admission",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: leaseTTL,
		CommandAckTTL: 30 * time.Millisecond,
	})
	runCtx, cancelRun := context.WithCancel(context.Background())
	releaseGate := testGateCleanup(t, cancelRun, backend.release)
	builderCalled := make(chan struct{}, 1)
	startErr := make(chan error, 1)
	go func() {
		startErr <- manager.startRunWithAdmissionBuilder(
			runCtx,
			testBotID,
			testSessionID,
			"stream-expired-admission",
			func(context.Context) (RunAdmissionView, error) {
				builderCalled <- struct{}{}
				return RunAdmissionView{}, nil
			},
			make(chan struct{}, 1),
			func() {},
			make(chan turn.InjectMessage, 1),
		)
	}()
	receiveTestResult(t, "expiring run claim", backend.claimed)
	time.Sleep(leaseTTL + leaseTTL/2)
	releaseGate()
	if err := receiveTestResult(t, "expired run admission", startErr); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("expired admission error = %v, want ErrRunOwnershipLost", err)
	}
	select {
	case <-builderCalled:
		t.Fatal("expired admission executed its builder")
	default:
	}
	if manager.localControl("stream-expired-admission") != nil {
		t.Fatal("expired admission retained local control")
	}
}

func runManagerDoesNotRetryFinishAfterOwnershipLoss(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	const leaseTTL = 500 * time.Millisecond
	manager := testRuntimeManagerWithOptions(t, suite.newBackend(t), Options{
		OwnerID:       "owner-expired-finish",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: leaseTTL,
		CommandAckTTL: 30 * time.Millisecond,
	})
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, "stream-expired-finish", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	_ = manager.stopLeaseRenewalContext(context.Background(), manager.localControl("stream-expired-finish"))
	time.Sleep(leaseTTL + leaseTTL/2)
	if err := manager.FinishRun(context.Background(), requireRunHandle(t, manager, testBotID, testSessionID, "stream-expired-finish"), RunStatusCompleted, ""); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("finish expired run error = %v, want ErrRunOwnershipLost", err)
	}
	if manager.localControl("stream-expired-finish") != nil {
		t.Fatal("ownership-lost finish retained a retrying local control")
	}
}

func runManagerCloseWaitsForStartRunAndCancelsControl(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	backend := &startRunGateBackend{
		DistributedBackend: suite.newBackend(t),
		claimed:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	manager := NewManager(backend, Options{
		OwnerID:       "owner-start-close",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: time.Second,
	})
	closeDone := make(chan struct{})
	var closeOnce sync.Once
	var closeErr error
	startManagerClose := func() {
		closeOnce.Do(func() {
			go func() {
				closeErr = manager.Close()
				close(closeDone)
			}()
		})
	}
	t.Cleanup(func() {
		startManagerClose()
		receiveTestResult(t, "manager cleanup", closeDone)
		if closeErr != nil {
			t.Errorf("close manager during cleanup: %v", closeErr)
		}
	})
	runCtx, cancelRun := context.WithCancel(context.Background())
	releaseGate := testGateCleanup(t, cancelRun, backend.release)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start manager: %v", err)
	}

	canceled := make(chan struct{}, 1)
	startErr := make(chan error, 1)
	go func() {
		startErr <- manager.StartRun(
			runCtx,
			testBotID,
			testSessionID,
			testRunID,
			make(chan struct{}, 1),
			func() { canceled <- struct{}{} },
			make(chan turn.InjectMessage, 1),
		)
	}()
	receiveTestResult(t, "run claim before manager close", backend.claimed)

	startManagerClose()
	select {
	case <-closeDone:
		t.Fatalf("close completed before StartRun initialized: %v", closeErr)
	case <-time.After(25 * time.Millisecond):
	}

	releaseGate()
	if err := receiveTestResult(t, "run admission before manager close", startErr); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("start run error = %v, want ErrManagerClosed", err)
	}
	receiveTestResult(t, "manager close", closeDone)
	if closeErr != nil {
		t.Fatalf("close manager: %v", closeErr)
	}
	receiveTestResult(t, "active run cancellation", canceled)
}

func runManagerConcurrentCloseWaitsForRunRelease(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backend := &gatedReleaseCloseBackend{
		DistributedBackend: suite.newBackend(t),
		releaseStarted:     make(chan struct{}),
		allowRelease:       make(chan struct{}),
		closed:             make(chan struct{}),
	}
	manager := NewManager(backend, Options{
		OwnerID:       "owner-concurrent-close",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: time.Second,
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	if err := manager.StartRun(
		context.Background(), testBotID, "session-concurrent-close", "stream-concurrent-close",
		make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1),
	); err != nil {
		t.Fatalf("start run: %v", err)
	}

	closeResults := make(chan error, 2)
	go func() { closeResults <- manager.Close() }()
	go func() { closeResults <- manager.CloseContext(context.Background()) }()
	receiveTestResult(t, "run release during concurrent close", backend.releaseStarted)
	select {
	case <-backend.closed:
		t.Fatal("backend closed before active run release completed")
	default:
	}
	close(backend.allowRelease)
	for range 2 {
		if err := receiveTestResult(t, "concurrent manager close", closeResults); err != nil {
			t.Fatalf("close manager: %v", err)
		}
	}
	if calls := backend.closeCalls.Load(); calls != 1 {
		t.Fatalf("backend Close calls = %d, want 1", calls)
	}
}

func TestMemoryBackendExpiresSnapshots(t *testing.T) {
	backend := NewMemoryBackendWithTTL(30 * time.Millisecond)
	t.Cleanup(func() { _ = backend.Close() })
	key := Key{BotID: testBotID, SessionID: "session-memory-ttl"}
	if _, _, err := backend.Update(context.Background(), key, func(snapshot Snapshot, _ bool) (Snapshot, bool, error) {
		snapshot.BotID = key.BotID
		snapshot.SessionID = key.SessionID
		snapshot.Seq = 1
		return snapshot, true, nil
	}); err != nil {
		t.Fatalf("start memory run: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, ok, err := backend.Load(context.Background(), key); err != nil || ok {
		t.Fatalf("expired snapshot = ok:%v err:%v", ok, err)
	}
}

func TestLocalRuntimeDoesNotUseOwnerLeases(t *testing.T) {
	t.Parallel()

	manager := testRuntimeManagerWithOptions(t, NewMemoryBackendWithTTL(20*time.Millisecond), Options{
		OwnerID:       "must-not-leak-into-local-state",
		StateTTL:      20 * time.Millisecond,
		OwnerLeaseTTL: 20 * time.Millisecond,
	})
	if err := manager.StartRun(context.Background(), testBotID, "session-local-no-lease", "stream-local-no-lease", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start local run: %v", err)
	}
	ctrl := manager.localControl("stream-local-no-lease")
	if ctrl == nil {
		t.Fatal("local run control missing")
	}
	if ctrl.leaseStop != nil || ctrl.leaseDone != nil || !ctrl.leaseValidUntil.IsZero() {
		t.Fatalf("local control unexpectedly has lease state: %#v", ctrl)
	}

	time.Sleep(3 * 20 * time.Millisecond)
	handle := requireRunHandle(t, manager, testBotID, "session-local-no-lease", "stream-local-no-lease")
	if err := manager.ValidateRunOwnership(context.Background(), handle); err != nil {
		t.Fatalf("local ownership expired after lease TTL: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "still running"}); err != nil {
		t.Fatalf("handle local event after lease TTL: %v", err)
	}
	snapshot, err := manager.Snapshot(context.Background(), testBotID, "session-local-no-lease")
	if err != nil {
		t.Fatalf("load local snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusRunning {
		t.Fatalf("local run = %#v, want running", snapshot.CurrentRunView)
	}
	if snapshot.CurrentRunView.OwnerID != "" || snapshot.CurrentRunView.OwnerLeaseExpiresAt != nil {
		t.Fatalf("local run leaked distributed ownership: %#v", snapshot.CurrentRunView)
	}
	assertRuntimeBlock(t, snapshot.CurrentRunView.Messages, chatview.UIMessageText, "", "still running")
}

func runCommonRuntimeManagerContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()
	t.Run("snapshots rich active run and publishes deltas", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerSnapshotsRichActiveRunContract(t, suite)
	})
	t.Run("shares validated replacement operation snapshots", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerSharesReplacementOperationContract(t, suite)
	})
	t.Run("shares canonical request user turn snapshots", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerSharesRequestUserTurnContract(t, suite)
	})
	t.Run("keeps queued steer valid past command acknowledgement timeout", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerKeepsQueuedSteerPastAckTimeoutContract(t, suite)
	})
	t.Run("rejects queued steer when the run finishes", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerRejectsQueuedSteerOnFinishContract(t, suite)
	})
	t.Run("rejects queued steer when the agent sends a terminal event", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerRejectsQueuedSteerOnAgentTerminalContract(t, suite)
	})
	t.Run("recovers subscriber buffer overflow", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerSignalsSubscriberOverflowContract(t, suite)
	})
	t.Run("keeps errored streams errored when abort terminal follows", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerKeepsErroredStreamErroredAfterAbortContract(t, suite)
	})
	t.Run("keeps errored streams errored when end terminal follows", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerKeepsErroredStreamErroredAfterEndContract(t, suite)
	})
	t.Run("clears a retried error when the recovered stream ends", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerClearsRetriedErrorOnCleanEndContract(t, suite)
	})
	t.Run("fences delayed owner mutations after stream id reuse", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerFencesDelayedOwnerMutationsContract(t, suite)
	})
	t.Run("serializes concurrent snapshot updates", func(t *testing.T) {
		t.Parallel()
		runRuntimeBackendSerializesConcurrentSnapshotUpdatesContract(t, suite)
	})
	t.Run("scopes identical stream ids by bot and session", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerScopesIdenticalRunIDsContract(t, suite)
	})
	t.Run("detects a gap in the first delta after subscription hydration", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerDetectsFirstDeltaGapContract(t, suite)
	})
}

func runRuntimeManagerScopesIdenticalRunIDsContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()
	manager := testRuntimeManager(t, suite.newBackend(t), "owner-scoped-streams")
	const runID = "shared-caller-run-id"
	abortChannels := make(map[string]chan struct{})
	for _, sessionID := range []string{"session-scope-a", "session-scope-b"} {
		abortCh := make(chan struct{}, 1)
		abortChannels[sessionID] = abortCh
		if err := manager.StartRun(context.Background(), testBotID, sessionID, runID, abortCh, func() {}, make(chan turn.InjectMessage, 1)); err != nil {
			t.Fatalf("start %s: %v", sessionID, err)
		}
		snapshot, err := manager.Snapshot(context.Background(), testBotID, sessionID)
		if err != nil || snapshot.CurrentRunView == nil || snapshot.CurrentRunView.RunID != runID {
			t.Fatalf("snapshot %s = %#v, err %v", sessionID, snapshot.CurrentRunView, err)
		}
	}
	for _, sessionID := range []string{"session-scope-a", "session-scope-b"} {
		aborted, err := manager.Abort(context.Background(), testBotID, sessionID, runID)
		if err != nil || !aborted {
			t.Fatalf("abort %s = %v, err %v", sessionID, aborted, err)
		}
		select {
		case <-abortChannels[sessionID]:
		case <-time.After(time.Second):
			t.Fatalf("abort %s was not routed to its scoped run", sessionID)
		}
	}
	for _, sessionID := range []string{"session-scope-a", "session-scope-b"} {
		handle := requireRunHandle(t, manager, testBotID, sessionID, runID)
		if err := manager.FinishRun(context.Background(), handle, RunStatusAborted, ""); err != nil {
			t.Fatalf("finish %s: %v", sessionID, err)
		}
	}
}

func runRuntimeManagerDetectsFirstDeltaGapContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()
	backend := suite.newBackend(t)
	manager := testRuntimeManager(t, backend, "owner-first-gap")
	const sessionID = "session-first-gap"
	if err := manager.StartRun(context.Background(), testBotID, sessionID, "stream-first-gap", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	sub, err := manager.Subscribe(context.Background(), testBotID, sessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	baseline := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeSnapshot })
	if baseline.Snapshot == nil || baseline.Seq == 0 {
		t.Fatalf("baseline = %#v", baseline)
	}
	checkpointSeq := baseline.Seq + 2
	if _, _, err := backend.Update(context.Background(), Key{BotID: testBotID, SessionID: sessionID}, func(snapshot Snapshot, ok bool) (Snapshot, bool, error) {
		if !ok {
			t.Fatal("runtime snapshot disappeared before gap update")
		}
		snapshot.Seq = checkpointSeq
		snapshot.UpdatedAt = time.Now().UTC()
		return snapshot, true, nil
	}); err != nil {
		t.Fatalf("commit gap checkpoint: %v", err)
	}
	if err := backend.Publish(context.Background(), Event{
		Type: EventRuntimeDelta, BotID: testBotID, SessionID: sessionID,
		Epoch: baseline.Epoch, Seq: checkpointSeq,
		Delta: &RuntimeDelta{MessageAppends: []RuntimeMessageAppend{{ID: 0, Type: chatview.UIMessageText, Content: "gap"}}},
	}); err != nil {
		t.Fatalf("publish gap: %v", err)
	}
	recovered := waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Type == EventRuntimeSnapshot || event.Type == EventRuntimeDelta || event.Type == EventRuntimeDropped
	})
	if recovered.Type != EventRuntimeSnapshot || recovered.Snapshot == nil || recovered.Seq != checkpointSeq {
		t.Fatalf("gap recovery event = %#v", recovered)
	}
}

func TestRuntimeManagerDropsGapWhenSnapshotIsBehind(t *testing.T) {
	backend := NewMemoryBackend()
	manager := testRuntimeManager(t, backend, "observer-uncovered-gap")
	const sessionID = "session-uncovered-gap"
	if err := manager.StartRun(context.Background(), testBotID, sessionID, "stream-uncovered-gap", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	sub, err := manager.Subscribe(context.Background(), testBotID, sessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	baseline := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeSnapshot })
	if err := backend.Publish(context.Background(), Event{
		Type: EventRuntimeDelta, BotID: testBotID, SessionID: sessionID,
		Epoch: baseline.Epoch, Seq: baseline.Seq + 2, Delta: &RuntimeDelta{},
	}); err != nil {
		t.Fatalf("publish uncovered gap: %v", err)
	}
	dropped := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeDropped })
	if !strings.Contains(dropped.Message, "snapshot is behind observed event") {
		t.Fatalf("dropped event = %#v", dropped)
	}
}

func runRuntimeManagerFencesDelayedOwnerMutationsContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()
	var generation atomic.Int64
	manager := testRuntimeManagerWithOptions(t, suite.newBackend(t), Options{
		OwnerID:       "owner-delayed-mutation",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: time.Second,
		RunGenerationGenerator: func() string {
			return fmt.Sprintf("generation-%d", generation.Add(1))
		},
	})

	oldInject := make(chan turn.InjectMessage, 1)
	oldHandle, err := manager.StartRunHandle(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, oldInject)
	if err != nil {
		t.Fatalf("start first generation: %v", err)
	}
	if _, err := manager.Steer(context.Background(), testBotID, testSessionID, testRunID, "old generation steer"); err != nil {
		t.Fatalf("steer first generation: %v", err)
	}
	var delayedApplied func()
	select {
	case injected := <-oldInject:
		delayedApplied = injected.Applied
	case <-time.After(time.Second):
		t.Fatal("first generation steer was not delivered")
	}
	if delayedApplied == nil {
		t.Fatal("first generation steer has no applied callback")
	}
	if err := manager.FinishRun(context.Background(), oldHandle, RunStatusCompleted, ""); err != nil {
		t.Fatalf("finish first generation: %v", err)
	}
	newAbort := make(chan struct{}, 1)
	newInject := make(chan turn.InjectMessage, 1)
	newHandle, err := manager.StartRunHandle(context.Background(), testBotID, testSessionID, testRunID, newAbort, func() {}, newInject)
	if err != nil {
		t.Fatalf("start second generation: %v", err)
	}
	if oldHandle.Generation == newHandle.Generation {
		t.Fatalf("generation reused: %q", oldHandle.Generation)
	}
	if ok, err := manager.AbortRun(context.Background(), oldHandle); ok || !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("stale abort = ok:%v err:%v, want ErrRunOwnershipLost", ok, err)
	}
	if _, err := manager.SteerRun(context.Background(), oldHandle, "late steer command"); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("stale steer error = %v, want ErrRunOwnershipLost", err)
	}
	select {
	case <-newAbort:
		t.Fatal("stale abort signaled the new generation")
	case injected := <-newInject:
		t.Fatalf("stale steer reached the new generation: %#v", injected)
	default:
	}

	if _, err := manager.HandleAgentEvent(context.Background(), oldHandle, native.StreamEvent{Type: native.EventTextDelta, Delta: "late output"}); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("late event error = %v, want ErrRunOwnershipLost", err)
	}
	if err := manager.FinishRun(context.Background(), oldHandle, RunStatusErrored, "late finish"); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("late finish error = %v, want ErrRunOwnershipLost", err)
	}
	delayedApplied()
	if manager.localControlForHandle(newHandle) == nil {
		t.Fatal("late mutation removed the current run control")
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("load current generation: %v", err)
	}
	if !runMatchesHandle(snapshot.CurrentRunView, newHandle) || snapshot.CurrentRunView.Status != RunStatusRunning || len(snapshot.CurrentRunView.Messages) != 0 || snapshot.CurrentRunView.Error != "" {
		t.Fatalf("current generation changed by stale callback: %#v", snapshot.CurrentRunView)
	}
}

func runDistributedRuntimeManagerContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	t.Run("does not prepare rejected run operations", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerDoesNotBuildRejectedOperationContract(t, suite)
	})
	t.Run("routes abort and steer across managers", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerRoutesAbortAndSteerAcrossManagersContract(t, suite)
	})
	t.Run("routes abort past a stale local generation", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerRoutesAbortPastStaleLocalGeneration(t, suite)
	})
	t.Run("acknowledges abort after rapid run replacement", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerAcknowledgesAbortAfterRunReplacement(t, suite)
	})
	t.Run("routes active response commands to the run owner", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerRoutesActiveResponsesAcrossManagersContract(t, suite)
	})
	t.Run("preserves remote command deadline errors", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerPreservesRemoteCommandDeadlineError(t, suite)
	})
	t.Run("releases pending routed commands when the manager closes", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerReleasesPendingCommandOnClose(t, suite)
	})
	t.Run("releases owned runs when the manager closes", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerReleasesOwnedRunOnClose(t, suite)
	})
	t.Run("does not block command results behind slow handlers", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerDoesNotBlockCommandResultsBehindSlowHandlers(t, suite)
	})
	t.Run("preserves ownership lookup deadline errors", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerPreservesOwnershipDeadlineError(t, suite)
	})
	t.Run("acknowledges an applied response that finishes the run", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerAcknowledgesAppliedResponseAfterFinish(t, suite)
	})
	t.Run("reconciles a response after owner control is lost", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerReconcilesResponseAfterOwnerControlLoss(t, suite)
	})
	t.Run("keeps command routing alive after startup context cancellation", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerCommandRoutingOutlivesStartContext(t, suite)
	})
	t.Run("cancels active response handlers when the run finishes", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerCancelsActiveResponseOnFinish(t, suite)
	})
	t.Run("cancels active response handlers when the manager closes", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerCancelsActiveResponseOnClose(t, suite)
	})
	t.Run("bounds active response handlers by command expiry", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerExpiresActiveResponseHandlers(t, suite)
	})
	t.Run("aborts a run while operation admission is blocked", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerAbortsBlockedAdmissionContract(t, suite)
	})
	t.Run("marks expired owner lease lost", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerMarksExpiredOwnerLeaseLostContract(t, suite)
	})
	t.Run("rejects expired owner lease revival", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerRejectsExpiredLeaseRevivalContract(t, suite)
	})
	t.Run("fences stale owner events after lease takeover", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerFencesStaleOwnerEventsContract(t, suite)
	})
	t.Run("cancels local execution when lease ownership is lost", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerCancelsExecutionAfterOwnershipLossContract(t, suite)
	})
	t.Run("keeps terminal hook authority for a user abort", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerKeepsHookAuthorityForUserAbort(t, suite)
	})
	t.Run("renews idle owner lease until owner stops", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerRenewsIdleOwnerLeaseContract(t, suite)
	})
	t.Run("notifies attached subscribers when owner lease expires", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerNotifiesLeaseLostContract(t, suite)
	})
	t.Run("reconciles snapshots whose publish was missed", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerReconcilesMissedPublishContract(t, suite)
	})
	t.Run("does not acknowledge dropped runtime commands", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerDroppedCommandAckContract(t, suite)
	})
	t.Run("retries a dropped runtime command with the same durable identity", func(t *testing.T) {
		t.Parallel()
		runRuntimeManagerRetriesDroppedCommandContract(t, suite)
	})
	t.Run("acknowledges abort while run admission is reserved", func(t *testing.T) {
		runManagerAbortAcknowledgesReservedRunBeforeTerminalCompletion(t, suite)
	})
	t.Run("records abort before the distributed claim", func(t *testing.T) {
		runManagerAbortBeforeClaim(t, suite)
	})
	t.Run("does not activate admission after claim lease expires", func(t *testing.T) {
		runManagerDoesNotActivateAdmissionAfterClaimLeaseExpires(t, suite)
	})
	t.Run("does not retry finish after ownership loss", func(t *testing.T) {
		runManagerDoesNotRetryFinishAfterOwnershipLoss(t, suite)
	})
	t.Run("close waits for run admission and cancels control", func(t *testing.T) {
		runManagerCloseWaitsForStartRunAndCancelsControl(t, suite)
	})
	t.Run("concurrent close waits for run release", func(t *testing.T) {
		runManagerConcurrentCloseWaitsForRunRelease(t, suite)
	})
	t.Run("retries terminal update before dropping owner route", func(t *testing.T) {
		runRuntimeManagerRetriesTerminalUpdateBeforeDroppingOwnerRoute(t, suite)
	})
	t.Run("abort command does not republish stale self reference", func(t *testing.T) {
		runRuntimeManagerAbortCommandDoesNotRepublishStaleSelfReference(t, suite)
	})
	t.Run("scopes stream reference cleanup to one run generation", func(t *testing.T) {
		runRuntimeManagerScopesRunRefCleanupToGeneration(t, suite)
	})
	t.Run("rejects delayed commands from an older run generation", func(t *testing.T) {
		runRuntimeManagerRejectsDelayedOldGenerationCommand(t, suite)
	})
	t.Run("rejects ownership validation that returns after the local lease deadline", func(t *testing.T) {
		runRuntimeManagerRejectsValidationAfterLocalLeaseDeadline(t, suite)
	})
	t.Run("lease watchdog revokes ownership while renewal is blocked", func(t *testing.T) {
		runRuntimeManagerLeaseWatchdogRevokesBlockedRenewal(t, suite)
	})
	t.Run("close deadline bounds an uninterruptible lease renewal", func(t *testing.T) {
		runRuntimeManagerCloseDeadlineBoundsLeaseRenewal(t, suite)
	})
	t.Run("expired snapshot cleanup cannot cancel a reused stream generation", func(t *testing.T) {
		runRuntimeManagerExpiredCleanupScopesLocalControlToGeneration(t, suite)
	})
}

func runRuntimeManagerReconcilesResponseAfterOwnerControlLoss(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "response-reconcile-owner")
	restarted := testRuntimeManager(t, backends[1], "response-reconcile-owner")
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, "stream-response-reconcile", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start response run: %v", err)
	}
	handle := requireRunHandle(t, owner, testBotID, testSessionID, "stream-response-reconcile")
	if _, err := owner.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-response-reconcile",
		ApprovalID: "approval-response-reconcile", Status: "pending",
	}); err != nil {
		t.Fatalf("record response target: %v", err)
	}
	owner.forgetLocalControlForHandle(context.Background(), handle)
	var reconciled atomic.Int64
	restarted.SetCommandReconciler(func(_ context.Context, command Command) (bool, error) {
		reconciled.Add(1)
		if command.TargetID != "approval-response-reconcile" || command.Generation != handle.Generation {
			t.Fatalf("reconcile command = %#v", command)
		}
		return true, nil
	})
	handled, err := restarted.DispatchActiveCommand(
		context.Background(), testBotID, testSessionID, CommandToolApprovalResponse,
		"approval-response-reconcile", []byte(`{"decision":"approve"}`),
	)
	if err != nil || !handled {
		t.Fatalf("reconciled response = handled:%v err:%v", handled, err)
	}
	if reconciled.Load() != 1 {
		t.Fatalf("reconciler calls = %d, want 1", reconciled.Load())
	}
}

func runManagerAbortBeforeClaim(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	backend := &preClaimGateBackend{
		DistributedBackend: suite.newBackend(t),
		entered:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	manager := testRuntimeManager(t, backend, "owner-pre-claim-abort")
	startErr := make(chan error, 1)
	builderCalled := make(chan struct{}, 1)
	go func() {
		_, err := manager.StartRunWithAdmissionBuilderHandle(
			context.Background(), testBotID, testSessionID, "stream-pre-claim-abort",
			func(context.Context, RunHandle) (RunAdmissionView, error) {
				builderCalled <- struct{}{}
				return RunAdmissionView{}, nil
			},
			make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1),
		)
		startErr <- err
	}()
	receiveTestResult(t, "pre-claim gate", backend.entered)
	aborted, err := manager.Abort(context.Background(), testBotID, testSessionID, "stream-pre-claim-abort")
	if err != nil || !aborted {
		t.Fatalf("pre-claim abort = ok:%v err:%v", aborted, err)
	}
	if err := receiveTestResult(t, "pre-claim start", startErr); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-claim start error = %v, want context canceled", err)
	}
	close(backend.release)
	select {
	case <-builderCalled:
		t.Fatal("pre-claim abort allowed admission builder to run")
	default:
	}
	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("pre-claim abort snapshot: %v", err)
	}
	if snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Status != RunStatusAborted {
		t.Fatalf("pre-claim abort snapshot = %#v, want no claim or aborted", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerRoutesAbortPastStaleLocalGeneration(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	backends := suite.newSharedBackends(t, 2)
	caller := testRuntimeManager(t, backends[0], "owner-stale-local-caller")
	owner := testRuntimeManager(t, backends[1], "owner-current-generation")
	abortCh := make(chan struct{}, 1)
	handle, err := owner.StartRunHandle(
		context.Background(),
		testBotID,
		testSessionID,
		"stream-reused-generation",
		abortCh,
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start current run: %v", err)
	}
	ready := make(chan struct{})
	close(ready)
	caller.mu.Lock()
	caller.controls[scopedRunControlKey(testBotID, testSessionID, handle.RunID)] = &runControl{
		botID: testBotID, sessionID: testSessionID, runID: handle.RunID,
		generation: "stale-generation", ready: ready,
	}
	caller.mu.Unlock()

	ok, err := caller.AbortRun(context.Background(), handle)
	if err != nil || !ok {
		t.Fatalf("route abort past stale local generation = ok:%v err:%v", ok, err)
	}
	select {
	case <-abortCh:
	case <-time.After(time.Second):
		t.Fatal("current run owner did not receive routed abort")
	}
}

func runRuntimeManagerAcknowledgesAbortAfterRunReplacement(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "owner-abort-replacement")
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	gated := &gatedCommandResultLoadBackend{
		DistributedBackend: backends[1],
		started:            loadStarted,
		release:            releaseLoad,
	}
	droppedResult := make(chan Command, 1)
	remote := testRuntimeManager(t, dropCommandResultSubscriptionBackend{
		DistributedBackend: gated,
		dropped:            droppedResult,
	}, "remote-abort-replacement")

	abortCh := make(chan struct{}, 1)
	handle, err := owner.StartRunHandle(
		context.Background(),
		testBotID,
		testSessionID,
		"stream-abort-replacement",
		abortCh,
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start aborted run: %v", err)
	}

	type abortResult struct {
		ok  bool
		err error
	}
	resultCh := make(chan abortResult, 1)
	go func() {
		ok, abortErr := remote.AbortRun(context.Background(), handle)
		resultCh <- abortResult{ok: ok, err: abortErr}
	}()
	receiveTestResult(t, "owner abort signal", abortCh)
	resultCommand := receiveTestResult(t, "dropped abort result", droppedResult)
	if resultCommand.Type != CommandResult || resultCommand.ID == "" {
		t.Fatalf("abort result command = %#v", resultCommand)
	}
	receiveTestResult(t, "abort result polling", loadStarted)

	if err := owner.FinishRun(context.Background(), handle, RunStatusAborted, ""); err != nil {
		t.Fatalf("finish aborted run: %v", err)
	}
	replacement, err := owner.StartRunHandle(
		context.Background(),
		testBotID,
		testSessionID,
		"stream-after-abort",
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start replacement run: %v", err)
	}
	close(releaseLoad)

	result := receiveTestResult(t, "remote abort acknowledgement", resultCh)
	if result.err != nil || !result.ok {
		t.Fatalf("remote abort after replacement = ok:%v err:%v", result.ok, result.err)
	}
	snapshot, err := remote.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("load replacement snapshot: %v", err)
	}
	if !runMatchesHandle(snapshot.CurrentRunView, replacement) {
		t.Fatalf("replacement run changed by abort acknowledgement: %#v", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerCloseDeadlineBoundsLeaseRenewal(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	const leaseTTL = time.Second
	backend := &uninterruptibleLeaseRenewalBackend{
		DistributedBackend: suite.newBackend(t),
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	releaseBackend := sync.OnceFunc(func() { close(backend.release) })
	t.Cleanup(releaseBackend)
	manager := NewManager(backend, Options{
		OwnerID:       "owner-close-deadline",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: leaseTTL,
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, "stream-close-deadline", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	select {
	case <-backend.started:
	case <-time.After(leaseTTL):
		t.Fatal("lease renewal did not start")
	}
	ctrl := manager.localControl("stream-close-deadline")
	if ctrl == nil {
		t.Fatal("local run control is missing")
	}
	ctrl.leaseLifecycleMu.Lock()
	leaseDone := ctrl.leaseDone
	ctrl.leaseLifecycleMu.Unlock()
	if leaseDone == nil {
		t.Fatal("lease renewal completion channel is missing")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	err := manager.CloseContext(closeCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("close ignored its deadline for %s", elapsed)
	}
	releaseBackend()
	select {
	case <-leaseDone:
	case <-time.After(time.Second):
		t.Fatal("lease renewal worker did not exit after backend release")
	}
}

func runRuntimeManagerLeaseWatchdogRevokesBlockedRenewal(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	const leaseTTL = 120 * time.Millisecond
	backend := &blockedLeaseRenewalBackend{
		DistributedBackend: suite.newBackend(t),
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	releaseBackend := sync.OnceFunc(func() { close(backend.release) })
	t.Cleanup(func() { backend.once.Do(func() { close(backend.started) }); releaseBackend() })
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "owner-blocked-renewal",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: leaseTTL,
	})
	abortCh := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	authorityCtx, revokeAuthority := context.WithCancelCause(context.Background())
	handle, err := manager.StartRunWithAdmissionBuilderAndOwnershipHandle(
		context.Background(), testBotID, testSessionID, "stream-blocked-renewal",
		func(context.Context, RunHandle) (RunAdmissionView, error) { return RunAdmissionView{}, nil },
		revokeAuthority,
		abortCh,
		func() {
			select {
			case canceled <- struct{}{}:
			default:
			}
		},
		make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("lease renewal did not block")
	}
	select {
	case <-authorityCtx.Done():
		if !errors.Is(context.Cause(authorityCtx), ErrRunOwnershipLost) {
			t.Fatalf("authority cancellation = %v, want ErrRunOwnershipLost", context.Cause(authorityCtx))
		}
	case <-time.After(2 * leaseTTL):
		t.Fatal("lease watchdog did not revoke ownership at the local deadline")
	}
	select {
	case <-abortCh:
	case <-time.After(time.Second):
		t.Fatal("lease watchdog did not signal abort")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("lease watchdog did not cancel agent execution")
	}
	if err := manager.ValidateRunOwnership(context.Background(), handle); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("ownership validation error = %v, want ErrRunOwnershipLost", err)
	}
}

func runRuntimeManagerExpiredCleanupScopesLocalControlToGeneration(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	const leaseTTL = 80 * time.Millisecond
	backend := &delayedRunRefCleanupBackend{
		DistributedBackend: suite.newBackend(t),
		deleted:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	releaseBackend := sync.OnceFunc(func() { close(backend.release) })
	t.Cleanup(func() { backend.once.Do(func() { close(backend.deleted) }); releaseBackend() })
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "owner-expired-cleanup-generation",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: leaseTTL,
	})
	oldHandle, err := manager.StartRunHandle(context.Background(), testBotID, testSessionID, "stream-reused-after-expiry", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1))
	if err != nil {
		t.Fatalf("start old generation: %v", err)
	}
	backend.targetGeneration = oldHandle.Generation
	_ = manager.stopLeaseRenewalContext(context.Background(), manager.localControlForHandle(oldHandle))
	time.Sleep(leaseTTL + 30*time.Millisecond)

	snapshotDone := make(chan error, 1)
	go func() {
		_, snapshotErr := manager.Snapshot(context.Background(), testBotID, testSessionID)
		snapshotDone <- snapshotErr
	}()
	select {
	case <-backend.deleted:
	case <-time.After(time.Second):
		t.Fatal("expired stream reference cleanup did not start")
	}
	manager.forgetLocalControlForHandle(context.Background(), oldHandle)
	newAbort := make(chan struct{}, 1)
	newCanceled := make(chan struct{}, 1)
	newHandle, err := manager.StartRunHandle(context.Background(), testBotID, testSessionID, "stream-reused-after-expiry", newAbort, func() {
		select {
		case newCanceled <- struct{}{}:
		default:
		}
	}, make(chan turn.InjectMessage, 1))
	if err != nil {
		t.Fatalf("start reused generation: %v", err)
	}
	if newHandle.Generation == oldHandle.Generation {
		t.Fatalf("generation was reused: %q", newHandle.Generation)
	}
	releaseBackend()
	select {
	case err := <-snapshotDone:
		if err != nil {
			t.Fatalf("reconcile expired snapshot: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expired snapshot cleanup did not finish")
	}
	select {
	case <-newAbort:
		t.Fatal("expired cleanup aborted the reused generation")
	case <-newCanceled:
		t.Fatal("expired cleanup canceled the reused generation")
	case <-time.After(30 * time.Millisecond):
	}
	if err := manager.ValidateRunOwnership(context.Background(), newHandle); err != nil {
		t.Fatalf("validate reused generation: %v", err)
	}
}

func runRuntimeManagerRejectsValidationAfterLocalLeaseDeadline(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	const leaseTTL = 120 * time.Millisecond
	backend := &delayedOwnershipValidationBackend{
		DistributedBackend: suite.newBackend(t),
		validated:          make(chan struct{}),
		release:            make(chan struct{}),
	}
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "owner-delayed-validation",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: leaseTTL,
	})
	handle, err := manager.StartRunHandle(context.Background(), testBotID, testSessionID, "stream-delayed-validation", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1))
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	_ = manager.stopLeaseRenewalContext(context.Background(), manager.localControlForHandle(handle))

	result := make(chan error, 1)
	go func() {
		result <- manager.ValidateRunOwnership(context.Background(), handle)
	}()
	receiveTestResult(t, "atomic ownership validation", backend.validated)
	time.Sleep(leaseTTL + 20*time.Millisecond)
	close(backend.release)
	if err := receiveTestResult(t, "delayed ownership validation", result); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("delayed ownership validation error = %v, want ErrRunOwnershipLost", err)
	}
}

func runRuntimeManagerScopesRunRefCleanupToGeneration(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backend := suite.newBackend(t)
	var generation atomic.Int64
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "owner-generation-cleanup",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: time.Second,
		RunGenerationGenerator: func() string {
			return fmt.Sprintf("generation-%d", generation.Add(1))
		},
	})
	const runID = "stream-generation-cleanup"
	key := Key{BotID: testBotID, SessionID: testSessionID}
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, runID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start first generation: %v", err)
	}
	oldRef, ok, err := backend.LoadRunRef(context.Background(), key, runID)
	if err != nil || !ok {
		t.Fatalf("load first generation ref = ok:%v err:%v", ok, err)
	}
	if err := manager.FinishRun(context.Background(), requireRunHandle(t, manager, testBotID, testSessionID, runID), RunStatusCompleted, ""); err != nil {
		t.Fatalf("finish first generation: %v", err)
	}
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, runID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start second generation: %v", err)
	}
	newRef, ok, err := backend.LoadRunRef(context.Background(), key, runID)
	if err != nil || !ok {
		t.Fatalf("load second generation ref = ok:%v err:%v", ok, err)
	}
	if oldRef.Generation == newRef.Generation {
		t.Fatalf("run generation was reused: %q", oldRef.Generation)
	}
	deleted, err := backend.DeleteRunRef(context.Background(), oldRef)
	if err != nil {
		t.Fatalf("delete stale generation ref: %v", err)
	}
	if deleted {
		t.Fatal("stale generation cleanup deleted the current stream reference")
	}
	currentRef, ok, err := backend.LoadRunRef(context.Background(), key, runID)
	if err != nil || !ok || currentRef != newRef {
		t.Fatalf("current stream ref after stale cleanup = (%#v, %v, %v), want %#v", currentRef, ok, err, newRef)
	}
}

func runRuntimeManagerRejectsDelayedOldGenerationCommand(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backend := suite.newBackend(t)
	var generation atomic.Int64
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "owner-generation-command",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: 200 * time.Millisecond,
		RunGenerationGenerator: func() string {
			return fmt.Sprintf("generation-%d", generation.Add(1))
		},
	})
	const runID = "stream-generation-command"
	key := Key{BotID: testBotID, SessionID: testSessionID}
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, runID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start first generation: %v", err)
	}
	oldRef, ok, err := backend.LoadRunRef(context.Background(), key, runID)
	if err != nil || !ok {
		t.Fatalf("load first generation ref = ok:%v err:%v", ok, err)
	}
	if err := manager.FinishRun(context.Background(), requireRunHandle(t, manager, testBotID, testSessionID, runID), RunStatusCompleted, ""); err != nil {
		t.Fatalf("finish first generation: %v", err)
	}
	newAbort := make(chan struct{}, 1)
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, runID, newAbort, func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start second generation: %v", err)
	}
	now, err := backend.Now(context.Background())
	if err != nil {
		t.Fatalf("load backend time: %v", err)
	}
	if err := backend.PublishCommand(context.Background(), oldRef.OwnerID, Command{
		Type:       CommandAbort,
		BotID:      oldRef.BotID,
		SessionID:  oldRef.SessionID,
		RunID:      oldRef.RunID,
		Generation: oldRef.Generation,
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Second),
	}); err != nil {
		t.Fatalf("publish delayed old-generation command: %v", err)
	}
	select {
	case <-newAbort:
		t.Fatal("old-generation command aborted the current run")
	case <-time.After(250 * time.Millisecond):
	}
	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("load current generation snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Generation == oldRef.Generation || snapshot.CurrentRunView.Status != RunStatusRunning {
		t.Fatalf("current run after delayed command = %#v", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerRejectsExpiredLeaseRevivalContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	const leaseTTL = 100 * time.Millisecond
	backend := suite.newBackend(t)
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "owner-expired-revival",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: leaseTTL,
		CommandAckTTL: 30 * time.Millisecond,
	})
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, "stream-expired-revival", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	ctrl := manager.localControl("stream-expired-revival")
	if ctrl == nil {
		t.Fatal("local run control is missing")
	}
	_ = manager.stopLeaseRenewalContext(context.Background(), ctrl)

	initial, ok, err := backend.Load(context.Background(), Key{BotID: testBotID, SessionID: testSessionID})
	if err != nil || !ok || initial.CurrentRunView == nil {
		t.Fatalf("load initial lease: snapshot=%#v err=%v", initial, err)
	}
	deadline := *initial.CurrentRunView.OwnerLeaseExpiresAt
	for {
		now, nowErr := backend.Now(context.Background())
		if nowErr != nil {
			t.Fatalf("load backend time: %v", nowErr)
		}
		if !now.Before(deadline.Add(10 * time.Millisecond)) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := backend.RenewLease(
		context.Background(),
		Key{BotID: testBotID, SessionID: testSessionID},
		"stream-expired-revival",
		"owner-expired-revival",
		initial.CurrentRunView.Generation,
		deadline.Add(-time.Millisecond),
		deadline.Add(leaseTTL),
	); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("renew expired lease error = %v, want ErrRunOwnershipLost", err)
	}
	if _, ok, err := backend.LoadRunRef(context.Background(), Key{BotID: testBotID, SessionID: testSessionID}, "stream-expired-revival"); err != nil || ok {
		t.Fatalf("expired stream ref = ok:%v err:%v, want absent", ok, err)
	}
	if err := manager.ValidateRunOwnership(context.Background(), RunHandle{BotID: testBotID, SessionID: testSessionID, RunID: "stream-expired-revival", Generation: initial.CurrentRunView.Generation}); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("validate expired ownership error = %v, want ErrRunOwnershipLost", err)
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot expired run: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusLost {
		t.Fatalf("expired current run = %#v, want lost", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerKeepsQueuedSteerPastAckTimeoutContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	const commandAckTTL = 500 * time.Millisecond
	manager := testRuntimeManagerWithOptions(t, suite.newBackend(t), Options{
		OwnerID:       "owner-queued-steer",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: commandAckTTL,
	})
	injectCh := make(chan turn.InjectMessage, 1)
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, injectCh); err != nil {
		t.Fatalf("start run: %v", err)
	}
	steer, err := manager.Steer(context.Background(), testBotID, testSessionID, testRunID, "wait for the next model step")
	if err != nil {
		t.Fatalf("steer: %v", err)
	}

	queued := waitRuntimeSnapshot(t, manager, testBotID, testSessionID, func(snapshot Snapshot) bool {
		return snapshot.CurrentRunView != nil &&
			snapshot.CurrentRunView.Steer != nil &&
			snapshot.CurrentRunView.Steer.ID == steer.ID &&
			snapshot.CurrentRunView.Steer.Status == SteerStatusQueued
	})
	if queued.CurrentRunView == nil || queued.CurrentRunView.Steer == nil {
		t.Fatal("queued steer is missing")
	}
	time.Sleep(2 * commandAckTTL)
	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot queued steer after acknowledgement timeout: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Steer == nil || snapshot.CurrentRunView.Steer.ID != steer.ID || snapshot.CurrentRunView.Steer.Status != SteerStatusQueued {
		t.Fatalf("steer after acknowledgement timeout = %#v, want queued", snapshot.CurrentRunView)
	}

	select {
	case injected := <-injectCh:
		if injected.Applied == nil {
			t.Fatal("queued steer is missing its applied acknowledgement")
		}
		injected.Applied()
	case <-time.After(time.Second):
		t.Fatal("queued steer was not delivered to the agent")
	}
	waitRuntimeSnapshot(t, manager, testBotID, testSessionID, func(snapshot Snapshot) bool {
		return snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Steer != nil && snapshot.CurrentRunView.Steer.Status == SteerStatusApplied
	})
}

func runRuntimeManagerCancelsExecutionAfterOwnershipLossContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backend := suite.newBackend(t)
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "owner-before-takeover",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: 90 * time.Millisecond,
		CommandAckTTL: 30 * time.Millisecond,
	})
	abortCh := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	injectCh := make(chan turn.InjectMessage, 1)
	authorityCtx, revokeAuthority := context.WithCancelCause(context.Background())
	handle, err := manager.StartRunWithAdmissionBuilderAndOwnershipHandle(context.Background(), testBotID, testSessionID, testRunID, func(context.Context, RunHandle) (RunAdmissionView, error) {
		return RunAdmissionView{}, nil
	}, revokeAuthority, abortCh, func() {
		select {
		case canceled <- struct{}{}:
		default:
		}
	}, injectCh)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	if _, changed, err := backend.Update(context.Background(), Key{BotID: testBotID, SessionID: testSessionID}, func(snapshot Snapshot, ok bool) (Snapshot, bool, error) {
		if !ok || snapshot.CurrentRunView == nil {
			return snapshot, false, errors.New("active run is missing")
		}
		snapshot.CurrentRunView.OwnerID = "owner-after-takeover"
		return snapshot, true, nil
	}); err != nil || !changed {
		t.Fatalf("replace runtime owner = changed:%v err:%v", changed, err)
	}

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("ownership loss did not cancel local execution")
	}
	select {
	case <-abortCh:
	case <-time.After(time.Second):
		t.Fatal("ownership loss did not signal the agent abort channel")
	}
	if err := manager.ValidateRunOwnership(context.Background(), handle); !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("ownership validation error = %v, want ErrRunOwnershipLost", err)
	}
	select {
	case <-authorityCtx.Done():
		if !errors.Is(context.Cause(authorityCtx), ErrRunOwnershipLost) {
			t.Fatalf("hook authority cause = %v, want ErrRunOwnershipLost", context.Cause(authorityCtx))
		}
	case <-time.After(time.Second):
		t.Fatal("ownership loss did not revoke terminal hook authority")
	}
	closeExecutionInjectChannel(t, injectCh)
}

func runRuntimeManagerKeepsHookAuthorityForUserAbort(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "owner-user-abort-authority")
	remote := testRuntimeManager(t, backends[1], "remote-user-abort-authority")
	authorityCtx, revokeAuthority := context.WithCancelCause(context.Background())
	if err := owner.startRunWithAdmissionBuilderAndOwnership(context.Background(), testBotID, testSessionID, testRunID, func(context.Context, RunHandle) (RunAdmissionView, error) {
		return RunAdmissionView{}, nil
	}, revokeAuthority, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	if ok, err := remote.Abort(context.Background(), testBotID, testSessionID, testRunID); err != nil || !ok {
		t.Fatalf("remote abort = ok:%v err:%v", ok, err)
	}
	select {
	case <-authorityCtx.Done():
		t.Fatalf("user abort revoked terminal hook authority: %v", context.Cause(authorityCtx))
	default:
	}
	if err := owner.FinishRun(context.Background(), requireRunHandle(t, owner, testBotID, testSessionID, testRunID), RunStatusAborted, ""); err != nil {
		t.Fatalf("finish aborted run: %v", err)
	}
	select {
	case <-authorityCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("finished run did not release terminal hook authority")
	}
}

func runRuntimeManagerAbortsBlockedAdmissionContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "owner-admitting-abort")
	remote := testRuntimeManager(t, backends[1], "remote-admitting-abort")
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	builderStarted := make(chan struct{})
	startDone := make(chan error, 1)
	go func() {
		startDone <- owner.startRunWithOperationBuilder(
			streamCtx,
			testBotID,
			testSessionID,
			"stream-admitting-abort",
			func(ctx context.Context) (*RunOperationView, error) {
				close(builderStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			},
			make(chan struct{}, 1),
			streamCancel,
			make(chan turn.InjectMessage, 1),
		)
	}()
	select {
	case <-builderStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("operation builder did not start")
	}

	aborted, err := remote.Abort(context.Background(), testBotID, testSessionID, "stream-admitting-abort")
	if err != nil || !aborted {
		t.Fatalf("remote abort during admission = ok:%v err:%v", aborted, err)
	}
	if err := <-startDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("admitting start error = %v, want context canceled", err)
	}
	snapshot, err := remote.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot after admission abort: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusAborted {
		t.Fatalf("admission abort snapshot = %#v", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerRetriesTerminalUpdateBeforeDroppingOwnerRoute(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	backend := &toggledUpdateFailureBackend{DistributedBackend: suite.newBackend(t)}
	manager := testRuntimeManager(t, backend, "owner-finish-retry")
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, "stream-finish-retry", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	const secondSessionID = "session-finish-retry-second"
	if err := manager.StartRun(context.Background(), testBotID, secondSessionID, "stream-finish-retry", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start duplicate-id run: %v", err)
	}
	firstHandle := requireRunHandle(t, manager, testBotID, testSessionID, "stream-finish-retry")

	backend.failing.Store(true)
	if err := manager.FinishRun(context.Background(), firstHandle, RunStatusErrored, "stream failed"); err == nil {
		t.Fatal("terminal update unexpectedly succeeded")
	}
	if manager.localControlForHandle(firstHandle) == nil {
		t.Fatal("terminal update failure dropped local owner control")
	}
	if _, ok, err := manager.RunRef(context.Background(), testBotID, testSessionID, "stream-finish-retry"); err != nil || !ok {
		t.Fatalf("terminal update failure stream ref = ok:%v err:%v", ok, err)
	}

	backend.failing.Store(false)
	snapshot := waitRuntimeSnapshot(t, manager, testBotID, testSessionID, func(snapshot Snapshot) bool {
		return snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Status == RunStatusErrored
	})
	if snapshot.CurrentRunView.Error != "stream failed" {
		t.Fatalf("terminal retry error = %q", snapshot.CurrentRunView.Error)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, ok, err := manager.RunRef(context.Background(), testBotID, testSessionID, "stream-finish-retry")
		if err == nil && !ok && manager.localControlForHandle(firstHandle) == nil {
			secondHandle := requireRunHandle(t, manager, testBotID, secondSessionID, "stream-finish-retry")
			if err := manager.FinishRun(context.Background(), secondHandle, RunStatusCompleted, ""); err != nil {
				t.Fatalf("finish duplicate-id run: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("terminal retry did not clean owner route")
}

func runRuntimeManagerDoesNotBuildRejectedOperationContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "owner-admitted")
	contender := testRuntimeManager(t, backends[1], "owner-rejected")
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, "stream-admitted", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start admitted run: %v", err)
	}

	var built atomic.Bool
	err := contender.startRunWithOperationBuilder(
		context.Background(),
		testBotID,
		testSessionID,
		"stream-rejected",
		func(context.Context) (*RunOperationView, error) {
			built.Store(true)
			return &RunOperationView{Kind: RunOperationRetry, ReplaceFromMessageID: "assistant-old"}, nil
		},
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if err == nil {
		t.Fatal("competing run admission succeeded")
	}
	if built.Load() {
		t.Fatal("operation builder ran before cross-server admission")
	}
}

func TestRuntimeManagerPublishesAdmittingCheckpoint(t *testing.T) {
	backend := NewMemoryBackend()
	manager := testRuntimeManager(t, backend, "owner-admitting")
	sub, err := manager.Subscribe(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	builderStarted := make(chan struct{})
	releaseBuilder := make(chan struct{})
	startDone := make(chan error, 1)
	go func() {
		startDone <- manager.startRunWithOperationBuilder(
			context.Background(),
			testBotID,
			testSessionID,
			"stream-admitting",
			func(context.Context) (*RunOperationView, error) {
				close(builderStarted)
				<-releaseBuilder
				return &RunOperationView{Kind: RunOperationRetry, ReplaceFromMessageID: "assistant-old"}, nil
			},
			make(chan struct{}, 1),
			func() {},
			make(chan turn.InjectMessage, 1),
		)
	}()
	<-builderStarted

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot reserved run: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusAdmitting {
		t.Fatalf("reserved run = %#v, want admitting", snapshot.CurrentRunView)
	}
	event := waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Delta != nil && event.Delta.CurrentRunView != nil && event.Delta.CurrentRunView.Status == RunStatusAdmitting
	})
	if event.Snapshot != nil || event.Seq != snapshot.Seq || event.Delta.CurrentRunView.Generation == "" {
		t.Fatalf("admitting checkpoint = %#v", event)
	}

	close(releaseBuilder)
	if err := <-startDone; err != nil {
		t.Fatalf("activate run: %v", err)
	}
	event = waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Delta != nil && event.Delta.CurrentRunView != nil && event.Delta.CurrentRunView.Status == RunStatusRunning
	})
	if event.Delta == nil || event.Delta.CurrentRunView == nil || event.Delta.CurrentRunView.Status != RunStatusRunning || event.Snapshot != nil {
		t.Fatalf("activated event = %#v", event)
	}
}

func TestRuntimeManagerBuilderFailureReleasesReservation(t *testing.T) {
	backend := NewMemoryBackend()
	manager := testRuntimeManager(t, backend, "owner-builder-failure")
	sub, err := manager.Subscribe(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	err = manager.startRunWithOperationBuilder(
		context.Background(),
		testBotID,
		testSessionID,
		"stream-builder-failure",
		func(context.Context) (*RunOperationView, error) { return nil, errors.New("prepare operation failed") },
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if err == nil || !strings.Contains(err.Error(), "prepare operation failed") {
		t.Fatalf("builder error = %v", err)
	}
	if _, ok, err := manager.RunRef(context.Background(), testBotID, testSessionID, "stream-builder-failure"); err != nil || ok {
		t.Fatalf("reservation stream ref = ok:%v err:%v", ok, err)
	}
	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusErrored {
		t.Fatalf("failed admission snapshot = %#v", snapshot.CurrentRunView)
	}
	event := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Seq == snapshot.Seq })
	if event.Delta == nil || event.Delta.CurrentRunView == nil || event.Delta.CurrentRunView.Status != RunStatusErrored {
		t.Fatalf("failed admission event = %#v, want self-contained terminal run", event)
	}
}

func runRuntimeManagerRejectsQueuedSteerOnFinishContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	manager := testRuntimeManager(t, suite.newBackend(t), "owner-finished-steer")
	injectCh := make(chan turn.InjectMessage, 1)
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, injectCh); err != nil {
		t.Fatalf("start run: %v", err)
	}
	steer, err := manager.Steer(context.Background(), testBotID, testSessionID, testRunID, "adjust before finish")
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	waitRuntimeSnapshot(t, manager, testBotID, testSessionID, func(snapshot Snapshot) bool {
		return snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Steer != nil &&
			snapshot.CurrentRunView.Steer.ID == steer.ID && snapshot.CurrentRunView.Steer.Status == SteerStatusQueued
	})
	if err := manager.FinishRun(context.Background(), requireRunHandle(t, manager, testBotID, testSessionID, testRunID), RunStatusCompleted, ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Steer == nil || snapshot.CurrentRunView.Steer.Status != SteerStatusRejected {
		t.Fatalf("finished steer = %#v, want rejected", snapshot.CurrentRunView)
	}
	if snapshot.CurrentRunView.Steer.Error != steerRunFinishedError {
		t.Fatalf("finished steer error = %q", snapshot.CurrentRunView.Steer.Error)
	}
	select {
	case injected := <-injectCh:
		if injected.Applied != nil {
			injected.Applied()
		}
	default:
		t.Fatal("queued steer was not delivered")
	}
	snapshot, err = manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot after late apply: %v", err)
	}
	if snapshot.CurrentRunView.Steer.Status != SteerStatusRejected {
		t.Fatalf("late apply changed terminal steer = %#v", snapshot.CurrentRunView.Steer)
	}
}

func runRuntimeManagerRejectsQueuedSteerOnAgentTerminalContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	for _, tc := range []struct {
		name       string
		event      native.StreamEvent
		wantStatus string
	}{
		{name: "end", event: native.StreamEvent{Type: native.EventAgentEnd}, wantStatus: RunStatusCompleted},
		{name: "abort", event: native.StreamEvent{Type: native.EventAgentAbort}, wantStatus: RunStatusAborted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := testRuntimeManager(t, suite.newBackend(t), "owner-agent-terminal-steer-"+tc.name)
			sub, err := manager.Subscribe(context.Background(), testBotID, testSessionID)
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			defer sub.Close()
			injectCh := make(chan turn.InjectMessage, 1)
			handle, err := manager.StartRunHandle(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, injectCh)
			if err != nil {
				t.Fatalf("start run: %v", err)
			}
			steer, err := manager.SteerRun(context.Background(), handle, "adjust before agent "+tc.name)
			if err != nil {
				t.Fatalf("steer: %v", err)
			}
			select {
			case <-injectCh:
			case <-time.After(time.Second):
				t.Fatal("queued steer was not delivered")
			}
			waitRuntimeSnapshot(t, manager, testBotID, testSessionID, func(snapshot Snapshot) bool {
				return snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Steer != nil &&
					snapshot.CurrentRunView.Steer.ID == steer.ID && snapshot.CurrentRunView.Steer.Status == SteerStatusQueued
			})
			if _, err := manager.HandleAgentEvent(context.Background(), handle, tc.event); err != nil {
				t.Fatalf("handle agent %s: %v", tc.name, err)
			}

			snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Steer == nil || snapshot.CurrentRunView.Steer.Status != SteerStatusRejected {
				t.Fatalf("agent-terminal steer = %#v, want rejected", snapshot.CurrentRunView)
			}
			if snapshot.CurrentRunView.Steer.Error != steerRunFinishedError {
				t.Fatalf("agent-terminal steer error = %q", snapshot.CurrentRunView.Steer.Error)
			}
			event := waitRuntimeEvent(t, sub.C, func(event Event) bool {
				return event.Delta != nil && event.Delta.Run != nil && event.Delta.Run.Status != nil && *event.Delta.Run.Status == tc.wantStatus
			})
			if event.Delta.Run.Steer == nil || event.Delta.Run.Steer.Status != SteerStatusRejected {
				t.Fatalf("agent-terminal delta = %#v, want rejected steer", event.Delta)
			}
		})
	}
}

func runRuntimeManagerSharesReplacementOperationContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "owner-operation")
	observer := testRuntimeManager(t, backends[1], "observer-operation")
	replacement := &chatview.UITurn{
		Role:      "user",
		Text:      "edited prompt",
		Timestamp: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
	}
	operation := &RunOperationView{
		Kind:                 RunOperationEdit,
		ReplaceFromMessageID: "user-old",
		ReplacementUserTurn:  replacement,
	}
	if err := owner.startRunWithOperation(
		context.Background(),
		testBotID,
		testSessionID,
		testRunID,
		operation,
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	); err != nil {
		t.Fatalf("start replacement run: %v", err)
	}

	snapshot, err := observer.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("observer snapshot: %v", err)
	}
	got := snapshot.CurrentRunView
	if got == nil || got.Operation == nil {
		t.Fatalf("current run operation = %#v", got)
	}
	if got.Operation.Kind != RunOperationEdit || got.Operation.ReplaceFromMessageID != "user-old" {
		t.Fatalf("operation = %#v", got.Operation)
	}
	if got.Operation.ReplacementUserTurn == nil || got.Operation.ReplacementUserTurn.Text != "edited prompt" {
		t.Fatalf("replacement user turn = %#v", got.Operation.ReplacementUserTurn)
	}
}

func runRuntimeManagerSharesRequestUserTurnContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "owner-request-turn")
	observer := testRuntimeManager(t, backends[1], "observer-request-turn")
	requestTurn := &chatview.UITurn{
		Role:              "user",
		Text:              "inspect the workspace",
		Attachments:       []chatview.UIAttachment{{Type: "file", Name: "notes.txt", ContentHash: "sha256:notes"}},
		Timestamp:         time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
		Platform:          "local",
		SenderUserID:      "user-request-turn",
		ExternalMessageID: testRunID,
	}
	if err := owner.startRunWithAdmission(
		context.Background(),
		testBotID,
		testSessionID,
		testRunID,
		RunAdmissionView{RequestUserTurn: requestTurn},
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	); err != nil {
		t.Fatalf("start run with request user turn: %v", err)
	}

	snapshot, err := observer.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("observer snapshot: %v", err)
	}
	got := snapshot.CurrentRunView
	if got == nil || got.RequestUserTurn == nil {
		t.Fatalf("current run request user turn = %#v", got)
	}
	if got.RequestUserTurn.Text != requestTurn.Text || got.RequestUserTurn.ExternalMessageID != testRunID {
		t.Fatalf("request user turn = %#v", got.RequestUserTurn)
	}
	if len(got.RequestUserTurn.Attachments) != 1 || got.RequestUserTurn.Attachments[0].ContentHash != "sha256:notes" {
		t.Fatalf("request user turn attachments = %#v", got.RequestUserTurn.Attachments)
	}

	requestTurn.Text = "mutated by caller"
	requestTurn.Attachments[0].Name = "mutated.txt"
	if got.RequestUserTurn.Text != "inspect the workspace" || got.RequestUserTurn.Attachments[0].Name != "notes.txt" {
		t.Fatalf("runtime request user turn aliases caller state: %#v", got.RequestUserTurn)
	}
}

func TestRuntimeManagerRejectsNonUserRequestTurn(t *testing.T) {
	t.Parallel()

	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-invalid-request-turn")
	err := manager.startRunWithAdmission(
		context.Background(),
		testBotID,
		testSessionID,
		"stream-invalid-request-turn",
		RunAdmissionView{RequestUserTurn: &chatview.UITurn{Role: "assistant", Text: "invalid"}},
		make(chan struct{}, 1),
		func() {},
		make(chan turn.InjectMessage, 1),
	)
	if err == nil || !strings.Contains(err.Error(), "must have role user") {
		t.Fatalf("start run error = %v, want invalid request user turn", err)
	}
	snapshot, snapshotErr := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if snapshotErr != nil {
		t.Fatalf("load errored snapshot: %v", snapshotErr)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusErrored {
		t.Fatalf("invalid admission snapshot = %#v", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerSnapshotsRichActiveRunContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	manager := testRuntimeManager(t, suite.newBackend(t), "owner-1")
	sub, err := manager.Subscribe(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	abortCh := make(chan struct{}, 1)
	injectCh := make(chan turn.InjectMessage, 1)
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, testRunID, abortCh, func() {}, injectCh); err != nil {
		t.Fatalf("start run: %v", err)
	}
	for _, event := range richRuntimeAgentScript() {
		if _, err := manager.HandleAgentEvent(context.Background(), requireRunHandle(t, manager, testBotID, testSessionID, testRunID), event); err != nil {
			t.Fatalf("handle event %s: %v", event.Type, err)
		}
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.RunID != testRunID || snapshot.CurrentRunView.Status != RunStatusWaitingDecision {
		t.Fatalf("current run = %#v", snapshot.CurrentRunView)
	}
	if snapshot.Epoch == "" || snapshot.CurrentRunView.Generation == "" {
		t.Fatalf("runtime identity is incomplete: epoch=%q generation=%q", snapshot.Epoch, snapshot.CurrentRunView.Generation)
	}
	assertRuntimeBlock(t, snapshot.CurrentRunView.Messages, chatview.UIMessageReasoning, "", "I need to inspect the workspace.")
	assertRuntimeBlock(t, snapshot.CurrentRunView.Messages, chatview.UIMessageText, "", "I will check the current state.")
	assertRuntimeBlock(t, snapshot.CurrentRunView.Messages, chatview.UIMessageTool, "call-exec", "")
	assertRuntimeBlock(t, snapshot.CurrentRunView.Messages, chatview.UIMessageTool, "call-approval", "")
	assertRuntimeBlock(t, snapshot.CurrentRunView.Messages, chatview.UIMessageTool, "call-ask", "")

	var sawDelta bool
	deadline := time.After(2 * time.Second)
	for !sawDelta {
		select {
		case event := <-sub.C:
			sawDelta = event.Type == EventRuntimeDelta && event.Epoch == snapshot.Epoch && event.Seq == snapshot.Seq && event.Delta != nil && event.Snapshot == nil
		case <-deadline:
			t.Fatal("timed out waiting for runtime delta")
		}
	}
}

func TestRuntimeManagerPublishesBoundedTextAppendPayloads(t *testing.T) {
	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-bounded-delta")
	sub, err := manager.Subscribe(context.Background(), testBotID, "session-bounded-delta")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	if err := manager.StartRun(context.Background(), testBotID, "session-bounded-delta", "stream-bounded-delta", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	_ = waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Type == EventRuntimeDelta && event.Delta != nil && event.Delta.CurrentRunView != nil
	})

	chunk := strings.Repeat("x", 1024)
	var lastWireSize int
	for i := 0; i < 32; i++ {
		if _, err := manager.HandleAgentEvent(context.Background(), requireRunHandle(t, manager, testBotID, "session-bounded-delta", "stream-bounded-delta"), native.StreamEvent{
			Type:  native.EventTextDelta,
			Delta: chunk,
		}); err != nil {
			t.Fatalf("handle text delta %d: %v", i, err)
		}
		event := waitRuntimeEvent(t, sub.C, func(event Event) bool {
			return event.Type == EventRuntimeDelta && event.Delta != nil && len(event.Delta.MessageAppends) == 1
		})
		if event.Snapshot != nil || event.Delta.MessageAppends[0].Content != chunk {
			t.Fatalf("text delta %d = %#v", i, event)
		}
		wire, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal text delta %d: %v", i, err)
		}
		lastWireSize = len(wire)
		if lastWireSize > len(chunk)+512 {
			t.Fatalf("text delta %d wire size = %d, want bounded near chunk size %d", i, lastWireSize, len(chunk))
		}
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, "session-bounded-delta")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshotWire, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if lastWireSize*10 >= len(snapshotWire) {
		t.Fatalf("last delta size = %d, snapshot size = %d; delta appears to carry accumulated output", lastWireSize, len(snapshotWire))
	}
}

func TestRuntimeManagerRehydratesSnapshotWhenSequenceEpochResets(t *testing.T) {
	backend := NewMemoryBackend()
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "observer-sequence-reset",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: 100 * time.Millisecond,
	})
	key := Key{BotID: testBotID, SessionID: "session-sequence-reset"}
	firstUpdatedAt := time.Now().UTC()
	if _, _, err := backend.Update(context.Background(), key, func(Snapshot, bool) (Snapshot, bool, error) {
		return Snapshot{BotID: key.BotID, SessionID: key.SessionID, Epoch: "epoch-1", Seq: 100, UpdatedAt: firstUpdatedAt}, true, nil
	}); err != nil {
		t.Fatalf("seed first epoch: %v", err)
	}
	sub, err := manager.Subscribe(context.Background(), key.BotID, key.SessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	_ = waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Type == EventRuntimeSnapshot && event.Seq == 100
	})

	secondUpdatedAt := firstUpdatedAt.Add(time.Second)
	if _, _, err := backend.Update(context.Background(), key, func(Snapshot, bool) (Snapshot, bool, error) {
		return Snapshot{BotID: key.BotID, SessionID: key.SessionID, Epoch: "epoch-2", Seq: 2, UpdatedAt: secondUpdatedAt}, true, nil
	}); err != nil {
		t.Fatalf("seed second epoch: %v", err)
	}
	if err := backend.Publish(context.Background(), Event{
		Type:      EventRuntimeDelta,
		BotID:     key.BotID,
		SessionID: key.SessionID,
		Epoch:     "epoch-2",
		Seq:       2,
		UpdatedAt: &secondUpdatedAt,
		Delta:     &RuntimeDelta{},
	}); err != nil {
		t.Fatalf("publish second epoch delta: %v", err)
	}
	event := waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Type == EventRuntimeSnapshot && event.Seq == 2
	})
	if event.Epoch != "epoch-2" || event.Snapshot == nil || event.Delta != nil || event.Snapshot.Epoch != "epoch-2" || event.Snapshot.Seq != 2 {
		t.Fatalf("sequence reset event = %#v", event)
	}
}

func TestRuntimeManagerDropsEpochlessEventsAfterEpochIsEstablished(t *testing.T) {
	backend := NewMemoryBackend()
	manager := testRuntimeManager(t, backend, "observer-missing-epoch")
	key := Key{BotID: testBotID, SessionID: "session-missing-epoch"}
	now := time.Now().UTC()
	if _, _, err := backend.Update(context.Background(), key, func(Snapshot, bool) (Snapshot, bool, error) {
		return Snapshot{BotID: key.BotID, SessionID: key.SessionID, Epoch: "epoch-1", Seq: 1, UpdatedAt: now}, true, nil
	}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	sub, err := manager.Subscribe(context.Background(), key.BotID, key.SessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	_ = waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Type == EventRuntimeSnapshot && event.Epoch == "epoch-1"
	})
	if err := backend.Publish(context.Background(), Event{
		Type: EventRuntimeDelta, BotID: key.BotID, SessionID: key.SessionID, Seq: 2, Delta: &RuntimeDelta{},
	}); err != nil {
		t.Fatalf("publish epochless event: %v", err)
	}
	event := waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Type == EventRuntimeDropped
	})
	if event.Epoch != "epoch-1" || event.Seq != 1 {
		t.Fatalf("dropped event cursor = epoch:%q seq:%d", event.Epoch, event.Seq)
	}
	if !strings.Contains(event.Message, "runtime event is missing epoch") {
		t.Fatalf("dropped event = %#v", event)
	}
}

func runRuntimeManagerRoutesAbortAndSteerAcrossManagersContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "owner-1")
	remote := testRuntimeManager(t, backends[1], "owner-2")
	abortCh := make(chan struct{}, 1)
	injectCh := make(chan turn.InjectMessage, 1)
	canceled := make(chan struct{}, 1)
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, testRunID, abortCh, func() { canceled <- struct{}{} }, injectCh); err != nil {
		t.Fatalf("start run: %v", err)
	}

	if ok, err := remote.Abort(context.Background(), testBotID, testSessionID, testRunID); err != nil || !ok {
		if err != nil {
			t.Fatalf("remote abort: %v", err)
		}
		t.Fatal("remote abort returned false")
	}
	select {
	case <-abortCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for remote abort")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancel")
	}
	snapshot := waitRuntimeSnapshot(t, owner, testBotID, testSessionID, func(s Snapshot) bool {
		return s.CurrentRunView != nil && s.CurrentRunView.Status == RunStatusAborting
	})
	if snapshot.CurrentRunView.Error != "" {
		t.Fatalf("abort error = %q, want empty", snapshot.CurrentRunView.Error)
	}
	if err := owner.FinishRun(context.Background(), requireRunHandle(t, owner, testBotID, testSessionID, testRunID), RunStatusAborted, ""); err != nil {
		t.Fatalf("finish aborted run: %v", err)
	}
	snapshot, err := owner.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot after errored finish: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusAborted {
		t.Fatalf("abort status changed after errored finish: %#v", snapshot.CurrentRunView)
	}

	steerInjectCh := make(chan turn.InjectMessage, 1)
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, "stream-steer", make(chan struct{}, 1), func() {}, steerInjectCh); err != nil {
		t.Fatalf("start steer run: %v", err)
	}
	steer, err := remote.Steer(context.Background(), testBotID, testSessionID, "stream-steer", "adjust course")
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	if steer.Status != SteerStatusPending {
		t.Fatalf("initial steer = %#v", steer)
	}
	select {
	case injected := <-steerInjectCh:
		if injected.Text != "adjust course" {
			t.Fatalf("injected text = %q", injected.Text)
		}
		pending, err := remote.Snapshot(context.Background(), testBotID, testSessionID)
		if err != nil {
			t.Fatalf("snapshot pending steer: %v", err)
		}
		if pending.CurrentRunView == nil || pending.CurrentRunView.Steer == nil || pending.CurrentRunView.Steer.Status != SteerStatusQueued {
			t.Fatalf("steer was not acknowledged as queued before agent consumption: %#v", pending.CurrentRunView)
		}
		if _, err := remote.Steer(context.Background(), testBotID, testSessionID, "stream-steer", "overlapping adjustment"); err == nil {
			t.Fatal("concurrent steer should be rejected while the first command is pending")
		}
		if injected.Applied == nil {
			t.Fatal("steer injection is missing its agent-consumption acknowledgement")
		}
		injected.Applied()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for steer injection")
	}

	snapshot = waitRuntimeSnapshot(t, remote, testBotID, testSessionID, func(s Snapshot) bool {
		return s.CurrentRunView != nil && s.CurrentRunView.Steer != nil && s.CurrentRunView.Steer.Status == SteerStatusApplied
	})
	if snapshot.CurrentRunView.Steer.ID == "" {
		t.Fatalf("steer state = %#v", snapshot.CurrentRunView.Steer)
	}
}

func runRuntimeManagerRoutesActiveResponsesAcrossManagersContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "response-owner")
	remote := testRuntimeManager(t, backends[1], "response-remote")
	commands := make(chan Command, 2)
	owner.SetCommandHandler(func(_ context.Context, command Command) error {
		commands <- command
		return nil
	})
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start response run: %v", err)
	}
	for _, event := range []native.StreamEvent{
		{Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-approval", ApprovalID: "approval-1", Status: "pending"},
		{Type: native.EventUserInputRequest, ToolName: "ask_user", ToolCallID: "call-input", UserInputID: "input-1", Status: "pending"},
	} {
		if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, testSessionID, testRunID), event); err != nil {
			t.Fatalf("record response target: %v", err)
		}
	}

	for _, request := range []struct {
		commandType string
		targetID    string
	}{
		{commandType: CommandToolApprovalResponse, targetID: "approval-1"},
		{commandType: CommandUserInputResponse, targetID: "input-1"},
	} {
		handled, err := remote.DispatchActiveCommand(context.Background(), testBotID, testSessionID, request.commandType, request.targetID, []byte(`{"ok":true}`))
		if err != nil || !handled {
			t.Fatalf("dispatch %s = handled:%v err:%v", request.commandType, handled, err)
		}
		select {
		case command := <-commands:
			if command.Type != request.commandType || command.TargetID != request.targetID || command.RunID != testRunID {
				t.Fatalf("routed command = %#v", command)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s", request.commandType)
		}
	}
	if handled, err := remote.DispatchActiveCommand(context.Background(), testBotID, testSessionID, CommandToolApprovalResponse, "approval-old", nil); err != nil || handled {
		t.Fatalf("unrelated target = handled:%v err:%v", handled, err)
	}
}

func runRuntimeManagerPreservesRemoteCommandDeadlineError(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "deadline-owner")
	remote := testRuntimeManager(t, backends[1], "deadline-remote")
	owner.SetCommandHandler(func(context.Context, Command) error {
		return context.DeadlineExceeded
	})
	if err := owner.StartRun(context.Background(), testBotID, "session-deadline", "stream-deadline", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start deadline run: %v", err)
	}
	if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, "session-deadline", "stream-deadline"), native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-deadline",
		ApprovalID: "approval-deadline", Status: "pending",
	}); err != nil {
		t.Fatalf("record deadline approval: %v", err)
	}

	handled, err := remote.DispatchActiveCommand(context.Background(), testBotID, "session-deadline", CommandToolApprovalResponse, "approval-deadline", []byte(`{"action":"approve"}`))
	if !handled || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("remote deadline result = handled:%v err:%v, want context.DeadlineExceeded", handled, err)
	}
}

func runRuntimeManagerPreservesOwnershipDeadlineError(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	ownerBackend := &toggledNowFailureBackend{DistributedBackend: backends[0]}
	owner := testRuntimeManager(t, ownerBackend, "ownership-deadline-owner")
	remote := testRuntimeManager(t, backends[1], "ownership-deadline-remote")
	var handlerCalls atomic.Int64
	owner.SetCommandHandler(func(context.Context, Command) error {
		handlerCalls.Add(1)
		return nil
	})
	if err := owner.StartRun(context.Background(), testBotID, "session-ownership-deadline", "stream-ownership-deadline", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start ownership deadline run: %v", err)
	}
	if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, "session-ownership-deadline", "stream-ownership-deadline"), native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-ownership-deadline",
		ApprovalID: "approval-ownership-deadline", Status: "pending",
	}); err != nil {
		t.Fatalf("record ownership deadline approval: %v", err)
	}
	ownerBackend.failing.Store(true)

	handled, err := remote.DispatchActiveCommand(context.Background(), testBotID, "session-ownership-deadline", CommandToolApprovalResponse, "approval-ownership-deadline", []byte(`{"action":"approve"}`))
	if !handled || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ownership deadline result = handled:%v err:%v, want context.DeadlineExceeded", handled, err)
	}
	if calls := handlerCalls.Load(); calls != 0 {
		t.Fatalf("command handler calls = %d, want zero after ownership lookup failure", calls)
	}
}

func runRuntimeManagerAcknowledgesAppliedResponseAfterFinish(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "response-applied-owner")
	remote := testRuntimeManager(t, backends[1], "response-applied-remote")
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start response run: %v", err)
	}
	handle := requireRunHandle(t, owner, testBotID, testSessionID, testRunID)
	owner.SetCommandHandler(func(ctx context.Context, _ Command) error {
		return owner.FinishRun(context.WithoutCancel(ctx), handle, RunStatusCompleted, "")
	})
	if _, err := owner.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-applied",
		ApprovalID: "approval-applied", Status: "pending",
	}); err != nil {
		t.Fatalf("record approval request: %v", err)
	}

	handled, err := remote.DispatchActiveCommand(context.Background(), testBotID, testSessionID, CommandToolApprovalResponse, "approval-applied", []byte(`{"action":"approve"}`))
	if err != nil || !handled {
		t.Fatalf("applied response = handled:%v err:%v, want acknowledged success", handled, err)
	}
	snapshot, err := remote.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusCompleted {
		t.Fatalf("applied response run = %#v, want completed", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerCommandRoutingOutlivesStartContext(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := NewManager(backends[0], Options{
		OwnerID: "startup-context-owner", StateTTL: time.Hour,
		OwnerLeaseTTL: time.Second, CommandAckTTL: time.Second,
	})
	startCtx, cancelStart := context.WithCancel(context.Background())
	if err := owner.Start(startCtx); err != nil {
		t.Fatalf("start owner manager: %v", err)
	}
	cancelStart()
	t.Cleanup(func() { _ = owner.Close() })
	remote := testRuntimeManager(t, backends[1], "startup-context-remote")
	abortCh := make(chan struct{}, 1)
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, testRunID, abortCh, func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run after startup context cancellation: %v", err)
	}

	if ok, err := remote.Abort(context.Background(), testBotID, testSessionID, testRunID); err != nil || !ok {
		t.Fatalf("remote abort after startup context cancellation = ok:%v err:%v", ok, err)
	}
	receiveTestResult(t, "abort routed after startup context cancellation", abortCh)
}

func runRuntimeManagerCancelsActiveResponseOnFinish(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "response-finish-owner")
	remote := testRuntimeManager(t, backends[1], "response-finish-remote")
	handlerStarted := make(chan struct{})
	handlerCanceled := make(chan error, 1)
	owner.SetCommandHandler(func(ctx context.Context, _ Command) error {
		close(handlerStarted)
		<-ctx.Done()
		handlerCanceled <- ctx.Err()
		return ctx.Err()
	})
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start response run: %v", err)
	}
	handle := requireRunHandle(t, owner, testBotID, testSessionID, testRunID)
	if _, err := owner.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-finish",
		ApprovalID: "approval-finish", Status: "pending",
	}); err != nil {
		t.Fatalf("record approval request: %v", err)
	}

	type dispatchResult struct {
		handled bool
		err     error
	}
	dispatchDone := make(chan dispatchResult, 1)
	go func() {
		handled, err := remote.DispatchActiveCommand(context.Background(), testBotID, testSessionID, CommandToolApprovalResponse, "approval-finish", []byte(`{"action":"approve"}`))
		dispatchDone <- dispatchResult{handled: handled, err: err}
	}()
	receiveTestResult(t, "active response handler start", handlerStarted)
	if err := owner.FinishRun(context.Background(), handle, RunStatusCompleted, ""); err != nil {
		t.Fatalf("finish run with active response: %v", err)
	}
	if err := receiveTestResult(t, "active response handler cancellation", handlerCanceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler cancellation error = %v, want context.Canceled", err)
	}
	result := receiveTestResult(t, "active response acknowledgement", dispatchDone)
	if !result.handled || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("dispatch after finish = handled:%v err:%v, want handler cancellation", result.handled, result.err)
	}
}

func runRuntimeManagerExpiresActiveResponseHandlers(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	const commandTTL = 200 * time.Millisecond
	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManagerWithOptions(t, backends[0], Options{
		OwnerID: "response-expiry-owner", StateTTL: time.Hour,
		OwnerLeaseTTL: time.Second, CommandAckTTL: commandTTL,
	})
	remote := testRuntimeManagerWithOptions(t, backends[1], Options{
		OwnerID: "response-expiry-remote", StateTTL: time.Hour,
		OwnerLeaseTTL: time.Second, CommandAckTTL: commandTTL,
	})
	handlerResults := make(chan error, 2)
	owner.SetCommandHandler(func(ctx context.Context, _ Command) error {
		<-ctx.Done()
		handlerResults <- ctx.Err()
		return ctx.Err()
	})
	if err := owner.StartRun(context.Background(), testBotID, "session-response-expiry", "stream-response-expiry", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start response run: %v", err)
	}
	for _, event := range []native.StreamEvent{
		{Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-expiry-approval", ApprovalID: "approval-expiry", Status: "pending"},
		{Type: native.EventUserInputRequest, ToolName: "ask_user", ToolCallID: "call-expiry-input", UserInputID: "input-expiry", Status: "pending"},
	} {
		if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, "session-response-expiry", "stream-response-expiry"), event); err != nil {
			t.Fatalf("record response target: %v", err)
		}
	}
	for _, request := range []struct {
		commandType string
		targetID    string
	}{
		{commandType: CommandToolApprovalResponse, targetID: "approval-expiry"},
		{commandType: CommandUserInputResponse, targetID: "input-expiry"},
	} {
		handled, err := remote.DispatchActiveCommand(context.Background(), testBotID, "session-response-expiry", request.commandType, request.targetID, []byte(`{"ok":true}`))
		if !handled || err == nil {
			t.Fatalf("expiring %s dispatch = handled:%v err:%v, want deadline error", request.commandType, handled, err)
		}
		if handlerErr := receiveTestResult(t, "active response handler expiry", handlerResults); !errors.Is(handlerErr, context.DeadlineExceeded) {
			t.Fatalf("handler expiry error = %v, want context deadline exceeded", handlerErr)
		}
	}
}

func runRuntimeManagerCancelsActiveResponseOnClose(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	manager := NewManager(suite.newBackend(t), Options{
		OwnerID: "response-close-owner", StateTTL: time.Hour,
		OwnerLeaseTTL: time.Second, CommandAckTTL: time.Second,
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start manager: %v", err)
	}
	handlerStarted := make(chan struct{})
	handlerCanceled := make(chan error, 1)
	manager.SetCommandHandler(func(ctx context.Context, _ Command) error {
		close(handlerStarted)
		<-ctx.Done()
		handlerCanceled <- ctx.Err()
		return ctx.Err()
	})
	injectCh := make(chan turn.InjectMessage, 1)
	if err := manager.StartRun(context.Background(), testBotID, "session-response-close", "stream-response-close", make(chan struct{}, 1), func() {}, injectCh); err != nil {
		t.Fatalf("start response run: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), requireRunHandle(t, manager, testBotID, "session-response-close", "stream-response-close"), native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-close",
		ApprovalID: "approval-close", Status: "pending",
	}); err != nil {
		t.Fatalf("record approval request: %v", err)
	}

	type dispatchResult struct {
		handled bool
		err     error
	}
	dispatchDone := make(chan dispatchResult, 1)
	go func() {
		handled, err := manager.DispatchActiveCommand(context.Background(), testBotID, "session-response-close", CommandToolApprovalResponse, "approval-close", []byte(`{"action":"approve"}`))
		dispatchDone <- dispatchResult{handled: handled, err: err}
	}()
	receiveTestResult(t, "active response handler start", handlerStarted)
	if err := manager.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	if err := receiveTestResult(t, "active response handler cancellation", handlerCanceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler cancellation error = %v, want context.Canceled", err)
	}
	result := receiveTestResult(t, "active response after close", dispatchDone)
	if !result.handled || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("dispatch after close = handled:%v err:%v, want handler cancellation", result.handled, result.err)
	}
	closeExecutionInjectChannel(t, injectCh)
}

func runRuntimeManagerAbortCommandDoesNotRepublishStaleSelfReference(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	backend := &countCommandPublishBackend{DistributedBackend: suite.newBackend(t)}
	manager := testRuntimeManager(t, backend, "stale-self-owner")
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start stale self run: %v", err)
	}
	ctrl := manager.localControl(testRunID)
	manager.removeLocalControl(testRunID, ctrl)
	createdAt := time.Now().UTC()
	manager.applyCommand(context.Background(), Command{
		Type:      CommandAbort,
		BotID:     testBotID,
		SessionID: testSessionID,
		RunID:     testRunID,
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(time.Second),
	})
	if got := backend.publishes.Load(); got != 0 {
		t.Fatalf("stale self abort republished %d command(s)", got)
	}
}

func runRuntimeManagerMarksExpiredOwnerLeaseLostContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backend := suite.newBackend(t)
	manager := testRuntimeManager(t, backend, "owner-1")
	expired := time.Now().Add(-time.Minute).UTC()
	seed := Snapshot{
		BotID:     testBotID,
		SessionID: testSessionID,
		Seq:       3,
		CurrentRunView: &CurrentRunView{
			RunID:               testRunID,
			Status:              RunStatusRunning,
			OwnerID:             "dead-owner",
			OwnerLeaseExpiresAt: &expired,
			StartedAt:           expired,
			UpdatedAt:           expired,
		},
	}
	if _, _, err := backend.Update(context.Background(), Key{BotID: testBotID, SessionID: testSessionID}, func(Snapshot, bool) (Snapshot, bool, error) {
		return seed, true, nil
	}); err != nil {
		t.Fatalf("save expired snapshot: %v", err)
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusLost {
		t.Fatalf("current run = %#v, want lost", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerFencesStaleOwnerEventsContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	oldOwner := testRuntimeManagerWithOptions(t, backends[0], Options{
		OwnerID:       "owner-stale",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: 60 * time.Millisecond,
		CommandAckTTL: 50 * time.Millisecond,
	})
	newOwner := testRuntimeManagerWithOptions(t, backends[1], Options{
		OwnerID:       "owner-current",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: 60 * time.Millisecond,
		CommandAckTTL: 50 * time.Millisecond,
	})
	if err := oldOwner.StartRun(context.Background(), testBotID, testSessionID, "stream-stale", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start stale owner run: %v", err)
	}
	staleControl := oldOwner.localControl("stream-stale")
	_ = oldOwner.stopLeaseRenewalContext(context.Background(), staleControl)
	time.Sleep(80 * time.Millisecond)

	lost, err := newOwner.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("mark old owner lost: %v", err)
	}
	if lost.CurrentRunView == nil || lost.CurrentRunView.Status != RunStatusLost {
		t.Fatalf("expired run = %#v, want lost", lost.CurrentRunView)
	}
	if err := newOwner.StartRun(context.Background(), testBotID, testSessionID, "stream-current", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start replacement run: %v", err)
	}

	messages, err := oldOwner.HandleAgentEvent(context.Background(), requireRunHandle(t, oldOwner, testBotID, testSessionID, "stream-stale"), native.StreamEvent{
		Type:  native.EventTextDelta,
		Delta: "late stale output",
	})
	if !errors.Is(err, ErrRunOwnershipLost) {
		t.Fatalf("handle stale event error = %v, want ErrRunOwnershipLost", err)
	}
	if len(messages) != 0 {
		t.Fatalf("stale event produced UI messages: %#v", messages)
	}
	snapshot, err := newOwner.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot replacement run: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.RunID != "stream-current" || snapshot.CurrentRunView.OwnerID != "owner-current" {
		t.Fatalf("stale owner replaced current run: %#v", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerRenewsIdleOwnerLeaseContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManagerWithOptions(t, backends[0], Options{
		OwnerID:       "owner-lease",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: 200 * time.Millisecond,
		CommandAckTTL: 50 * time.Millisecond,
	})
	remote := testRuntimeManagerWithOptions(t, backends[1], Options{
		OwnerID:       "remote-lease",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: 200 * time.Millisecond,
		CommandAckTTL: 50 * time.Millisecond,
	})
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	first, err := remote.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	if first.CurrentRunView == nil {
		t.Fatal("initial current run missing")
	}
	firstLease := *first.CurrentRunView.OwnerLeaseExpiresAt

	time.Sleep(3 * 200 * time.Millisecond)
	snapshot, err := remote.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot after idle lease renewal: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusRunning {
		t.Fatalf("idle current run = %#v, want running", snapshot.CurrentRunView)
	}
	if snapshot.CurrentRunView.OwnerLeaseExpiresAt == nil || !snapshot.CurrentRunView.OwnerLeaseExpiresAt.After(firstLease) {
		t.Fatalf("owner lease was not renewed: first=%s current=%s", firstLease, snapshot.CurrentRunView.OwnerLeaseExpiresAt)
	}

	owner.forgetLocalControl(context.Background(), testRunID)
	time.Sleep(2 * 200 * time.Millisecond)
	snapshot, err = remote.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot after owner stop: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusLost {
		t.Fatalf("stopped owner current run = %#v, want lost", snapshot.CurrentRunView)
	}
	if _, ok, err := remote.RunRef(context.Background(), testBotID, testSessionID, testRunID); err != nil || ok {
		if err != nil {
			t.Fatalf("stream ref after lost: %v", err)
		}
		t.Fatal("stream ref should be removed after owner lease is lost")
	}
}

func runRuntimeManagerNotifiesLeaseLostContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()
	const leaseTTL = 500 * time.Millisecond

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManagerWithOptions(t, backends[0], Options{
		OwnerID:       "owner-notify-lost",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: leaseTTL,
		CommandAckTTL: 50 * time.Millisecond,
	})
	observer := testRuntimeManagerWithOptions(t, backends[1], Options{
		OwnerID:       "observer-notify-lost",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: leaseTTL,
		CommandAckTTL: 50 * time.Millisecond,
	})
	sub, err := observer.Subscribe(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("subscribe observer: %v", err)
	}
	defer sub.Close()
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	_ = owner.stopLeaseRenewalContext(context.Background(), owner.localControl(testRunID))

	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-sub.C:
			if event.Snapshot != nil && event.Snapshot.CurrentRunView != nil && event.Snapshot.CurrentRunView.Status == RunStatusLost {
				return
			}
		case <-deadline:
			t.Fatal("attached subscriber did not receive owner lease loss")
		}
	}
}

func runRuntimeManagerReconcilesMissedPublishContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backend := suite.newBackend(t)
	manager := testRuntimeManagerWithOptions(t, backend, Options{
		OwnerID:       "observer-missed-publish",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: 150 * time.Millisecond,
		CommandAckTTL: 50 * time.Millisecond,
	})
	sub, err := manager.Subscribe(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	now := time.Now().UTC()
	seed := Snapshot{
		BotID:     testBotID,
		SessionID: testSessionID,
		Seq:       7,
		UpdatedAt: now,
		CurrentRunView: &CurrentRunView{
			RunID:     "stream-missed-publish",
			Status:    RunStatusCompleted,
			OwnerID:   "owner-missed-publish",
			StartedAt: now,
			UpdatedAt: now,
			Messages:  []chatview.UIMessage{},
		},
	}
	if _, _, err := backend.Update(context.Background(), Key{BotID: testBotID, SessionID: testSessionID}, func(Snapshot, bool) (Snapshot, bool, error) {
		return seed, true, nil
	}); err != nil {
		t.Fatalf("commit snapshot without publish: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-sub.C:
			if event.Type == EventRuntimeSnapshot && event.Snapshot != nil && event.Snapshot.Seq == seed.Seq {
				return
			}
		case <-deadline:
			t.Fatal("subscription did not reconcile committed snapshot after missed publish")
		}
	}
}

func runRuntimeManagerSignalsSubscriberOverflowContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	backend := &overflowObservingBackend{
		Backend:  suite.newBackend(t),
		overflow: make(chan Event, 1),
	}
	manager := testRuntimeManager(t, backend, "observer-overflow")
	sub, err := manager.Subscribe(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()
	baseline := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Type == EventRuntimeSnapshot })
	checkpointSeq := baseline.Seq + 160
	for seq := baseline.Seq + 1; seq <= checkpointSeq; seq++ {
		updatedAt := time.Now().UTC()
		if _, _, err := backend.Update(context.Background(), Key{BotID: testBotID, SessionID: testSessionID}, func(snapshot Snapshot, ok bool) (Snapshot, bool, error) {
			if !ok {
				t.Fatal("runtime snapshot disappeared before overflow recovery")
			}
			snapshot.Seq = seq
			snapshot.UpdatedAt = updatedAt
			return snapshot, true, nil
		}); err != nil {
			t.Fatalf("commit overflow update %d: %v", seq, err)
		}
		if err := backend.Publish(context.Background(), Event{
			Type: EventRuntimeDelta, BotID: testBotID, SessionID: testSessionID,
			Epoch: baseline.Epoch, Seq: seq, UpdatedAt: &updatedAt, Delta: &RuntimeDelta{},
		}); err != nil {
			t.Fatalf("publish overflow update %d: %v", seq, err)
		}
	}
	var recovered Event
	overflowObserved := false
	deadline := time.After(5 * time.Second)
	for recovered.Snapshot == nil || !overflowObserved {
		select {
		case event, ok := <-sub.C:
			if !ok {
				t.Fatal("runtime subscription closed during overflow recovery")
			}
			if event.Type == EventRuntimeSnapshot && event.Snapshot != nil && event.Seq >= checkpointSeq {
				recovered = event
			}
		case <-backend.overflow:
			overflowObserved = true
		case <-deadline:
			t.Fatal("subscription did not observe and recover a real backend queue overflow")
		}
	}
	if recovered.Type != EventRuntimeSnapshot || recovered.Snapshot == nil || recovered.Seq != checkpointSeq {
		t.Fatalf("overflow recovery event = %#v", recovered)
	}

	nextSeq := checkpointSeq
	for range 128 {
		nextSeq++
		updatedAt := time.Now().UTC()
		if _, _, err := backend.Update(context.Background(), Key{BotID: testBotID, SessionID: testSessionID}, func(snapshot Snapshot, ok bool) (Snapshot, bool, error) {
			if !ok {
				t.Fatal("runtime snapshot disappeared after overflow recovery")
			}
			snapshot.Seq = nextSeq
			snapshot.UpdatedAt = updatedAt
			return snapshot, true, nil
		}); err != nil {
			t.Fatalf("commit post-overflow delta %d: %v", nextSeq, err)
		}
		if err := backend.Publish(context.Background(), Event{
			Type: EventRuntimeDelta, BotID: testBotID, SessionID: testSessionID,
			Epoch: baseline.Epoch, Seq: nextSeq, UpdatedAt: &updatedAt, Delta: &RuntimeDelta{},
		}); err != nil {
			t.Fatalf("publish post-overflow delta %d: %v", nextSeq, err)
		}
		next := waitRuntimeEvent(t, sub.C, func(event Event) bool { return event.Seq >= nextSeq })
		if next.Type == EventRuntimeDelta && next.Seq == nextSeq {
			return
		}
	}
	t.Fatal("subscription never resumed continuous delta delivery after overflow recovery")
}

func runRuntimeManagerDroppedCommandAckContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManagerWithOptions(t, backends[0], Options{
		OwnerID:       "owner-command",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: 50 * time.Millisecond,
	})
	remote := testRuntimeManagerWithOptions(t, dropCommandBackend{DistributedBackend: backends[1]}, Options{
		OwnerID:       "remote-command",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: 50 * time.Millisecond,
	})
	injectCh := make(chan turn.InjectMessage, 1)
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, injectCh); err != nil {
		t.Fatalf("start run: %v", err)
	}

	if ok, err := remote.Abort(context.Background(), testBotID, testSessionID, testRunID); err == nil || ok {
		t.Fatalf("dropped abort = ok:%v err:%v, want acknowledgement error", ok, err)
	}
	snapshot, err := remote.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot after dropped abort: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusRunning {
		t.Fatalf("dropped abort status = %#v, want still running", snapshot.CurrentRunView)
	}

	steer, err := remote.Steer(context.Background(), testBotID, testSessionID, testRunID, "adjust course")
	if err != nil {
		t.Fatalf("dropped steer initial publish: %v", err)
	}
	if steer.Status != SteerStatusPending {
		t.Fatalf("initial dropped steer = %#v", steer)
	}
	snapshot = waitRuntimeSnapshot(t, remote, testBotID, testSessionID, func(s Snapshot) bool {
		return s.CurrentRunView != nil &&
			s.CurrentRunView.Steer != nil &&
			s.CurrentRunView.Steer.ID == steer.ID &&
			s.CurrentRunView.Steer.Status == SteerStatusRejected
	})
	if snapshot.CurrentRunView.Steer.Error != "runtime steer command was not acknowledged" {
		t.Fatalf("dropped steer state = %#v", snapshot.CurrentRunView.Steer)
	}

	owner.applyCommand(context.Background(), Command{
		Type:      CommandSteer,
		BotID:     testBotID,
		SessionID: testSessionID,
		RunID:     testRunID,
		SteerID:   steer.ID,
		Text:      "late adjust course",
		CreatedAt: time.Now().UTC().Add(-time.Second),
	})
	select {
	case injected := <-injectCh:
		t.Fatalf("late rejected steer was injected: %#v", injected)
	default:
	}
	snapshot, err = remote.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot after late steer command: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Steer == nil || snapshot.CurrentRunView.Steer.Status != SteerStatusRejected {
		t.Fatalf("late steer command state = %#v, want rejected", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerRetriesDroppedCommandContract(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManagerWithOptions(t, backends[0], Options{
		OwnerID: "owner-command-retry", StateTTL: time.Hour, OwnerLeaseTTL: time.Second, CommandAckTTL: 500 * time.Millisecond,
	})
	retryingBackend := &dropFirstCommandBackend{DistributedBackend: backends[1]}
	remote := testRuntimeManagerWithOptions(t, retryingBackend, Options{
		OwnerID: "remote-command-retry", StateTTL: time.Hour, OwnerLeaseTTL: time.Second, CommandAckTTL: 500 * time.Millisecond,
	})
	abortCh := make(chan struct{}, 1)
	if err := owner.StartRun(context.Background(), testBotID, testSessionID, "stream-command-retry", abortCh, func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start owner run: %v", err)
	}

	aborted, err := remote.Abort(context.Background(), testBotID, testSessionID, "stream-command-retry")
	if err != nil || !aborted {
		t.Fatalf("retry dropped abort = %v, err %v", aborted, err)
	}
	select {
	case <-abortCh:
	case <-time.After(time.Second):
		t.Fatal("retried runtime command did not reach owner")
	}
	if got := retryingBackend.publishes.Load(); got < 2 {
		t.Fatalf("runtime command publishes = %d, want at least 2", got)
	}
	select {
	case <-abortCh:
		t.Fatal("retried runtime command executed more than once")
	case <-time.After(200 * time.Millisecond):
	}
}

func runRuntimeManagerReleasesPendingCommandOnClose(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManager(t, backends[0], "owner-pending-close")
	published := make(chan Command, 1)
	remote := testRuntimeManagerWithOptions(t, dropCommandBackend{DistributedBackend: backends[1], published: published}, Options{
		OwnerID:       "remote-pending-close",
		StateTTL:      time.Hour,
		OwnerLeaseTTL: time.Second,
		CommandAckTTL: time.Hour,
	})
	handle, err := owner.StartRunHandle(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1))
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := owner.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-pending-close",
		ApprovalID: "approval-pending-close", Status: "pending",
	}); err != nil {
		t.Fatalf("publish approval request: %v", err)
	}

	dispatchCtx, cancelDispatch := context.WithCancel(context.Background())
	defer cancelDispatch()
	type dispatchResult struct {
		handled bool
		err     error
	}
	dispatchDone := make(chan dispatchResult, 1)
	go func() {
		handled, dispatchErr := remote.DispatchActiveCommand(
			dispatchCtx, testBotID, testSessionID, CommandToolApprovalResponse,
			"approval-pending-close", json.RawMessage(`{"status":"approved"}`),
		)
		dispatchDone <- dispatchResult{handled: handled, err: dispatchErr}
	}()
	receiveTestResult(t, "dropped routed command publish", published)
	if err := remote.Close(); err != nil {
		t.Fatalf("close remote manager: %v", err)
	}
	remote.mu.Lock()
	pendingCount := len(remote.pendingCommands)
	remote.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("pending commands after close = %d, want 0", pendingCount)
	}
	result := receiveTestResult(t, "pending routed command close", dispatchDone)
	if !result.handled || !errors.Is(result.err, ErrManagerClosed) {
		t.Fatalf("dispatch after close = handled:%v err:%v, want ErrManagerClosed", result.handled, result.err)
	}
}

func runRuntimeManagerReleasesOwnedRunOnClose(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	owner := testRuntimeManagerWithOptions(t, backends[0], Options{
		OwnerID: "owner-graceful-release", StateTTL: time.Hour, OwnerLeaseTTL: 30 * time.Second,
	})
	remote := testRuntimeManagerWithOptions(t, backends[1], Options{
		OwnerID: "remote-graceful-release", StateTTL: time.Hour, OwnerLeaseTTL: 30 * time.Second,
	})
	if err := owner.StartRun(context.Background(), testBotID, "session-graceful-release", "stream-graceful-release", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start owned run: %v", err)
	}

	if err := owner.CloseContext(context.Background()); err != nil {
		t.Fatalf("close owner: %v", err)
	}
	snapshot, err := remote.Snapshot(context.Background(), testBotID, "session-graceful-release")
	if err != nil {
		t.Fatalf("load released snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusLost || snapshot.CurrentRunView.Error != runtimeOwnerShutdownError || snapshot.CurrentRunView.OwnerLeaseExpiresAt != nil {
		t.Fatalf("released snapshot = %#v", snapshot.CurrentRunView)
	}
	if _, ok, err := backends[1].LoadRunRef(context.Background(), Key{BotID: testBotID, SessionID: "session-graceful-release"}, "stream-graceful-release"); err != nil || ok {
		t.Fatalf("released stream ref = ok:%v err:%v", ok, err)
	}
	if err := remote.StartRun(context.Background(), testBotID, "session-graceful-release", "stream-after-graceful-release", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start replacement run immediately after close: %v", err)
	}
}

func runRuntimeManagerDoesNotBlockCommandResultsBehindSlowHandlers(t *testing.T, suite distributedRuntimeBackendContractSuite) {
	t.Helper()

	backends := suite.newSharedBackends(t, 2)
	const ackTTL = 750 * time.Millisecond
	ownerA := testRuntimeManagerWithOptions(t, backends[0], Options{
		OwnerID: "owner-command-hol-a", StateTTL: time.Hour, OwnerLeaseTTL: 5 * time.Second, CommandAckTTL: ackTTL,
	})
	ownerB := testRuntimeManagerWithOptions(t, backends[1], Options{
		OwnerID: "owner-command-hol-b", StateTTL: time.Hour, OwnerLeaseTTL: 5 * time.Second, CommandAckTTL: 5 * time.Second,
	})

	blockedHandlerEntered := make(chan struct{})
	releaseBlockedHandler := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(releaseBlockedHandler) }) }
	t.Cleanup(releaseHandler)
	ownerA.SetCommandHandler(func(ctx context.Context, _ Command) error {
		close(blockedHandlerEntered)
		select {
		case <-releaseBlockedHandler:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	ownerB.SetCommandHandler(func(context.Context, Command) error { return nil })

	startApprovalRun := func(manager *Manager, sessionID, runID, approvalID string) {
		t.Helper()
		if err := manager.StartRun(context.Background(), testBotID, sessionID, runID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
			t.Fatalf("start %s: %v", runID, err)
		}
		handle := requireRunHandle(t, manager, testBotID, sessionID, runID)
		if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
			Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-" + approvalID, ApprovalID: approvalID, Status: "pending",
		}); err != nil {
			t.Fatalf("record approval %s: %v", approvalID, err)
		}
	}
	startApprovalRun(ownerA, "session-command-hol-a", "stream-command-hol-a", "approval-command-hol-a")
	startApprovalRun(ownerB, "session-command-hol-b", "stream-command-hol-b", "approval-command-hol-b")

	blockedDispatchDone := make(chan error, 1)
	go func() {
		_, err := ownerB.DispatchActiveCommand(context.Background(), testBotID, "session-command-hol-a", CommandToolApprovalResponse, "approval-command-hol-a", []byte(`{"decision":"approve"}`))
		blockedDispatchDone <- err
	}()
	receiveTestResult(t, "blocked owner command handler", blockedHandlerEntered)

	handled, err := ownerA.DispatchActiveCommand(context.Background(), testBotID, "session-command-hol-b", CommandToolApprovalResponse, "approval-command-hol-b", []byte(`{"decision":"approve"}`))
	if err != nil || !handled {
		t.Fatalf("unrelated command behind blocked handler = handled:%v err:%v", handled, err)
	}
	releaseHandler()
	if err := receiveTestResult(t, "blocked command completion", blockedDispatchDone); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked command completion: %v", err)
	}
}

func runRuntimeManagerKeepsErroredStreamErroredAfterAbortContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	manager := testRuntimeManager(t, suite.newBackend(t), "owner-error")
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	handle := requireRunHandle(t, manager, testBotID, testSessionID, testRunID)
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type:  native.EventError,
		Error: "runtime interrupted",
	}); err != nil {
		t.Fatalf("handle error event: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventAgentAbort,
	}); err != nil {
		t.Fatalf("handle abort terminal event: %v", err)
	}
	if err := manager.FinishRun(context.Background(), handle, "", ""); err != nil {
		t.Fatalf("finish errored run: %v", err)
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusErrored {
		t.Fatalf("current run = %#v, want errored", snapshot.CurrentRunView)
	}
	if snapshot.CurrentRunView.Error != "runtime interrupted" {
		t.Fatalf("error = %q, want runtime interrupted", snapshot.CurrentRunView.Error)
	}
}

func TestRuntimeManagerPublishesStableStreamErrorCode(t *testing.T) {
	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-coded-error")
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	handle := requireRunHandle(t, manager, testBotID, testSessionID, testRunID)
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventError, Code: "agent.response_timeout", Error: "public timeout",
	}); err != nil {
		t.Fatalf("handle error event: %v", err)
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CurrentRunView == nil {
		t.Fatal("current run is nil")
	}
	if snapshot.CurrentRunView.ErrorCode != "agent.response_timeout" || snapshot.CurrentRunView.Error != "public timeout" {
		t.Fatalf("runtime error = code:%q detail:%q", snapshot.CurrentRunView.ErrorCode, snapshot.CurrentRunView.Error)
	}
}

func runRuntimeManagerKeepsErroredStreamErroredAfterEndContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	manager := testRuntimeManager(t, suite.newBackend(t), "owner-error-end")
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	handle := requireRunHandle(t, manager, testBotID, testSessionID, testRunID)
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type:  native.EventError,
		Error: "provider failed",
	}); err != nil {
		t.Fatalf("handle error event: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventAgentEnd,
	}); err != nil {
		t.Fatalf("handle end terminal event: %v", err)
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusErrored || snapshot.CurrentRunView.Error != "provider failed" {
		t.Fatalf("current run = %#v, want errored provider failure", snapshot.CurrentRunView)
	}
}

func runRuntimeManagerClearsRetriedErrorOnCleanEndContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	manager := testRuntimeManager(t, suite.newBackend(t), "owner-retry-recovered")
	if err := manager.StartRun(context.Background(), testBotID, testSessionID, testRunID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	handle := requireRunHandle(t, manager, testBotID, testSessionID, testRunID)
	// The native stream publishes the mid-stream failure before it retries, so
	// a run that recovers must not stay errored once it reaches a clean end.
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type:  native.EventError,
		Error: "provider hung up",
	}); err != nil {
		t.Fatalf("handle error event: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type:       native.EventRetry,
		Attempt:    1,
		MaxAttempt: 3,
		RetryError: "provider hung up",
	}); err != nil {
		t.Fatalf("handle retry event: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventAgentEnd,
	}); err != nil {
		t.Fatalf("handle end terminal event: %v", err)
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != RunStatusCompleted {
		t.Fatalf("current run = %#v, want completed", snapshot.CurrentRunView)
	}
	if snapshot.CurrentRunView.Error != "" {
		t.Fatalf("error = %q, want the retried failure cleared", snapshot.CurrentRunView.Error)
	}
}

func runRuntimeBackendSerializesConcurrentSnapshotUpdatesContract(t *testing.T, suite runtimeBackendContractSuite) {
	t.Helper()

	key := Key{BotID: testBotID, SessionID: "session-concurrent"}
	backends := suite.newSharedBackends(t, 4)
	for _, backend := range backends {
		backend := backend
		t.Cleanup(func() { _ = backend.Close() })
	}
	const updates = 40
	var wg sync.WaitGroup
	errCh := make(chan error, updates)
	for i := 0; i < updates; i++ {
		i := i
		backend := backends[i%len(backends)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := backend.Update(context.Background(), key, func(snapshot Snapshot, ok bool) (Snapshot, bool, error) {
				if !ok {
					snapshot = Snapshot{BotID: key.BotID, SessionID: key.SessionID}
				}
				snapshot.Seq++
				snapshot.UpdatedAt = time.Now().UTC()
				// Appending to the cumulative message list, not just bumping
				// Seq, is what makes a lost update visible: a scalar increment
				// can be re-derived, a dropped append cannot.
				if snapshot.CurrentRunView == nil {
					snapshot.CurrentRunView = &CurrentRunView{RunID: testRunID}
				}
				snapshot.CurrentRunView.Messages = append(snapshot.CurrentRunView.Messages, chatview.UIMessage{
					ID:      i,
					Type:    chatview.UIMessageText,
					Content: fmt.Sprintf("update-%02d", i),
				})
				return snapshot, true, nil
			})
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	snapshot, ok, err := backends[0].Load(context.Background(), key)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !ok {
		t.Fatal("snapshot missing after concurrent updates")
	}
	if snapshot.Seq != updates {
		t.Fatalf("seq = %d, want %d", snapshot.Seq, updates)
	}
	if snapshot.CurrentRunView == nil {
		t.Fatal("current run view is missing after concurrent updates")
	}
	if got := len(snapshot.CurrentRunView.Messages); got != updates {
		t.Fatalf("messages len = %d, want %d", got, updates)
	}
}

func assertRuntimeBlock(t *testing.T, messages []chatview.UIMessage, kind chatview.UIMessageType, toolCallID, content string) {
	t.Helper()
	for _, message := range messages {
		if message.Type != kind {
			continue
		}
		if toolCallID != "" && message.ToolCallID != toolCallID {
			continue
		}
		if content != "" && message.Content != content {
			continue
		}
		return
	}
	data, _ := json.Marshal(messages)
	t.Fatalf("missing runtime block kind=%s tool=%q content=%q in %s", kind, toolCallID, content, data)
}

func waitRuntimeSnapshot(t *testing.T, manager *Manager, botID, sessionID string, pred func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := manager.Snapshot(context.Background(), botID, sessionID)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if pred(snapshot) {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, _ := manager.Snapshot(context.Background(), botID, sessionID)
	t.Fatalf("timed out waiting for snapshot, last=%#v", snapshot)
	return Snapshot{}
}
