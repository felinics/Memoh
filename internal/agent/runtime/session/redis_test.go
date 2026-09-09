package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
)

var runtimeBackendPrefixSequence atomic.Uint64

type delayedCommandSubscriptionBackend struct {
	DistributedBackend
	delay    time.Duration
	observed chan<- Command
}

type dropCommandResultSubscriptionBackend struct {
	DistributedBackend
	dropped chan<- Command
}

type dropNextRuntimePublishBackend struct {
	DistributedBackend
	dropNext atomic.Bool
}

func (b *dropNextRuntimePublishBackend) arm() {
	b.dropNext.Store(true)
}

func (b *dropNextRuntimePublishBackend) Publish(ctx context.Context, event Event) error {
	if event.Type == EventRuntimeDelta && b.dropNext.CompareAndSwap(true, false) {
		return nil
	}
	return b.DistributedBackend.Publish(ctx, event)
}

func (b dropCommandResultSubscriptionBackend) SubscribeCommands(ctx context.Context, ownerID string) (CommandSubscription, error) {
	sub, err := b.DistributedBackend.SubscribeCommands(ctx, ownerID)
	if err != nil {
		return CommandSubscription{}, err
	}
	forwardCtx, cancel := context.WithCancel(ctx)
	commands := make(chan Command, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(commands)
		for {
			select {
			case <-forwardCtx.Done():
				return
			case command, ok := <-sub.C:
				if !ok {
					return
				}
				if command.Type == CommandResult {
					select {
					case b.dropped <- command:
					default:
					}
					continue
				}
				select {
				case commands <- command:
				case <-forwardCtx.Done():
					return
				}
			}
		}
	}()
	return CommandSubscription{
		C: commands,
		Close: func() {
			cancel()
			sub.Close()
			<-done
		},
	}, nil
}

func (b delayedCommandSubscriptionBackend) SubscribeCommands(ctx context.Context, ownerID string) (CommandSubscription, error) {
	sub, err := b.DistributedBackend.SubscribeCommands(ctx, ownerID)
	if err != nil {
		return CommandSubscription{}, err
	}
	forwardCtx, cancel := context.WithCancel(ctx)
	commands := make(chan Command, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(commands)
		for {
			select {
			case <-forwardCtx.Done():
				return
			case command, ok := <-sub.C:
				if !ok {
					return
				}
				select {
				case b.observed <- command:
				default:
				}
				timer := time.NewTimer(b.delay)
				select {
				case <-timer.C:
				case <-forwardCtx.Done():
					timer.Stop()
					return
				}
				select {
				case commands <- command:
				case <-forwardCtx.Done():
					return
				}
			}
		}
	}()
	return CommandSubscription{
		C: commands,
		Close: func() {
			cancel()
			sub.Close()
			<-done
		},
	}, nil
}

func uniqueRuntimeBackendPrefix(scope string) string {
	return fmt.Sprintf("memoh:test:session_runtime:%s:%d:%d:", scope, time.Now().UnixNano(), runtimeBackendPrefixSequence.Add(1))
}

func redisRuntimeBackendSuite(redisURL string) distributedRuntimeBackendContractSuite {
	newRedisBackend := func(t *testing.T, prefix string) DistributedBackend {
		t.Helper()
		backend, err := NewRedisBackend(context.Background(), RedisOptions{
			URL:       redisURL,
			KeyPrefix: prefix,
			StateTTL:  time.Minute,
		})
		if err != nil {
			t.Fatalf("redis backend: %v", err)
		}
		return backend
	}
	return distributedRuntimeBackendContractSuite{
		newBackend: func(t *testing.T) DistributedBackend {
			t.Helper()
			return newRedisBackend(t, uniqueRuntimeBackendPrefix("backend"))
		},
		newSharedBackends: func(t *testing.T, count int) []DistributedBackend {
			t.Helper()
			prefix := uniqueRuntimeBackendPrefix("shared")
			backends := make([]DistributedBackend, count)
			for i := range backends {
				backends[i] = newRedisBackend(t, prefix)
			}
			return backends
		},
	}
}

func TestRedisValkeyRuntimeManagerContractOptional(t *testing.T) {
	targets := []struct {
		name string
		url  string
	}{
		{name: "redis", url: os.Getenv("MEMOH_TEST_REDIS_URL")},
		{name: "valkey", url: os.Getenv("MEMOH_TEST_VALKEY_URL")},
	}
	var ran bool
	for _, target := range targets {
		target := target
		if target.url == "" {
			continue
		}
		ran = true
		t.Run(target.name, func(t *testing.T) {
			suite := redisRuntimeBackendSuite(target.url)
			runCommonRuntimeManagerContract(t, suite.common())
			runDistributedRuntimeManagerContract(t, suite)
			runRedisSubscriptionReconnectContract(t, target.url)
			runRedisSubscriptionCloseBarrierContract(t, target.url)
			runRedisBackendCloseSubscriptionContract(t, target.url)
			runRedisExpiredActiveResponseTransportContract(t, target.url)
			runRedisDurableCommandResultContract(t, target.url)
			runRedisBoundedCommandWorkersContract(t, target.url)
			runRedisDuplicateCommandSaturationContract(t, target.url)
			runRedisLeaseIndexContract(t, target.url)
		})
	}
	if !ran {
		if os.Getenv("MEMOH_TEST_DISTRIBUTED_REQUIRED") == "1" {
			t.Fatal("distributed session runtime contracts are required, but neither MEMOH_TEST_REDIS_URL nor MEMOH_TEST_VALKEY_URL is set")
		}
		t.Skip("set MEMOH_TEST_REDIS_URL and/or MEMOH_TEST_VALKEY_URL to run Redis and/or Valkey session runtime contracts")
	}
}

func runRedisSubscriptionCloseBarrierContract(t *testing.T, redisURL string) {
	t.Helper()

	backend, err := NewRedisBackend(context.Background(), RedisOptions{
		URL: redisURL, KeyPrefix: uniqueRuntimeBackendPrefix("subscription-close-barrier"), StateTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("redis backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	runtimeSub, err := backend.Subscribe(context.Background(), Key{BotID: testBotID, SessionID: "session-close-barrier"})
	if err != nil {
		t.Fatalf("subscribe runtime: %v", err)
	}
	commandSub, err := backend.SubscribeCommands(context.Background(), "owner-close-barrier")
	if err != nil {
		runtimeSub.Close()
		t.Fatalf("subscribe commands: %v", err)
	}

	runtimeSub.Close()
	requireClosedChannel(t, "runtime subscription", runtimeSub.C)
	backend.subscriptionsMu.Lock()
	remaining := len(backend.subscriptions)
	backend.subscriptionsMu.Unlock()
	if remaining != 1 {
		t.Fatalf("runtime subscription Close returned before unregister: remaining subscriptions = %d, want 1", remaining)
	}

	commandSub.Close()
	requireClosedChannel(t, "command subscription", commandSub.C)
	backend.subscriptionsMu.Lock()
	remaining = len(backend.subscriptions)
	backend.subscriptionsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("command subscription Close returned before unregister: remaining subscriptions = %d, want 0", remaining)
	}
}

func runRedisBackendCloseSubscriptionContract(t *testing.T, redisURL string) {
	t.Helper()

	backend, err := NewRedisBackend(context.Background(), RedisOptions{
		URL: redisURL, KeyPrefix: uniqueRuntimeBackendPrefix("close-subscriptions"), StateTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("redis backend: %v", err)
	}
	runtimeSub, err := backend.Subscribe(context.Background(), Key{BotID: testBotID, SessionID: "session-close"})
	if err != nil {
		t.Fatalf("subscribe runtime: %v", err)
	}
	commandSub, err := backend.SubscribeCommands(context.Background(), "owner-close")
	if err != nil {
		runtimeSub.Close()
		_ = backend.Close()
		t.Fatalf("subscribe commands: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- backend.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close backend: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend Close did not wait for subscriptions to stop")
	}
	for event := range runtimeSub.C {
		if event.Type == EventRuntimeDropped {
			t.Fatalf("orderly backend Close emitted recovery marker: %#v", event)
		}
	}
	requireClosedChannel(t, "command subscription", commandSub.C)
}

func requireClosedChannel[T any](t *testing.T, name string, ch <-chan T) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("%s remained open", name)
		}
	}
}

func runRedisDurableCommandResultContract(t *testing.T, redisURL string) {
	t.Helper()

	prefix := uniqueRuntimeBackendPrefix("durable-command-result")
	newBackend := func() *RedisBackend {
		backend, err := NewRedisBackend(context.Background(), RedisOptions{
			URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute,
		})
		if err != nil {
			t.Fatalf("redis backend: %v", err)
		}
		return backend
	}
	owner := testRuntimeManagerWithOptions(t, newBackend(), Options{
		OwnerID: "durable-result-owner", StateTTL: time.Minute,
		OwnerLeaseTTL: time.Second, CommandAckTTL: 750 * time.Millisecond, CommandWorkerLimit: 1,
	})
	dropped := make(chan Command, 1)
	remoteBackend := dropCommandResultSubscriptionBackend{DistributedBackend: newBackend(), dropped: dropped}
	remote := testRuntimeManagerWithOptions(t, remoteBackend, Options{
		OwnerID: "durable-result-remote", StateTTL: time.Minute,
		OwnerLeaseTTL: time.Second, CommandAckTTL: 750 * time.Millisecond,
	})
	handled := make(chan Command, 8)
	owner.SetCommandHandler(func(_ context.Context, command Command) error {
		handled <- command
		return nil
	})
	const (
		sessionID  = "session-durable-result"
		runID      = "stream-durable-result"
		approvalID = "approval-durable-result"
	)
	if err := owner.StartRun(context.Background(), testBotID, sessionID, runID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, sessionID, runID), native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-durable-result", ApprovalID: approvalID, Status: "pending",
	}); err != nil {
		t.Fatalf("record approval: %v", err)
	}

	handledResult, err := remote.DispatchActiveCommand(context.Background(), testBotID, sessionID, CommandToolApprovalResponse, approvalID, []byte(`{"decision":"approve"}`))
	if err != nil || !handledResult {
		t.Fatalf("dispatch with dropped pubsub result = handled:%v err:%v", handledResult, err)
	}
	receiveTestResult(t, "owner command handler", handled)
	result := receiveTestResult(t, "dropped pubsub command result", dropped)
	stored, ok, err := remoteBackend.LoadCommandResult(context.Background(), result.ID)
	if err != nil || !ok || stored.ID != result.ID || stored.Error != "" {
		t.Fatalf("durable command result = ok:%v err:%v result:%#v", ok, err, stored)
	}
	if stored.PayloadHash != activeCommandPayloadHash(CommandToolApprovalResponse, []byte(`{"decision":"approve"}`)) {
		t.Fatalf("durable command payload hash = %q", stored.PayloadHash)
	}
	if err := remote.Close(); err != nil {
		t.Fatalf("close original requester: %v", err)
	}
	restarted := testRuntimeManagerWithOptions(t, newBackend(), Options{
		OwnerID: "durable-result-restarted", StateTTL: time.Minute,
		OwnerLeaseTTL: time.Second, CommandAckTTL: 750 * time.Millisecond,
	})
	handledResult, err = restarted.DispatchActiveCommand(context.Background(), testBotID, sessionID, CommandToolApprovalResponse, approvalID, []byte(`{"decision":"approve"}`))
	if err != nil || !handledResult {
		t.Fatalf("dispatch after requester restart = handled:%v err:%v", handledResult, err)
	}
	const barrierApprovalID = "approval-durable-result-barrier"
	if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, sessionID, runID), native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-durable-result-barrier", ApprovalID: barrierApprovalID, Status: "pending",
	}); err != nil {
		t.Fatalf("record barrier approval: %v", err)
	}
	if handledResult, err = restarted.DispatchActiveCommand(context.Background(), testBotID, sessionID, CommandToolApprovalResponse, barrierApprovalID, []byte(`{"decision":"approve"}`)); err != nil || !handledResult {
		t.Fatalf("dispatch barrier command = handled:%v err:%v", handledResult, err)
	}
	if barrier := receiveTestResult(t, "barrier command handler", handled); barrier.TargetID != barrierApprovalID {
		t.Fatalf("stable retry executed before barrier: %#v", barrier)
	}
	handledResult, err = restarted.DispatchActiveCommand(context.Background(), testBotID, sessionID, CommandToolApprovalResponse, approvalID, []byte(`{"decision":"reject"}`))
	if !handledResult || !errors.Is(err, ErrCommandPayloadConflict) {
		t.Fatalf("conflicting stable retry = handled:%v err:%v, want payload conflict", handledResult, err)
	}

	const localApprovalID = "approval-durable-result-local-owner"
	if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, sessionID, runID), native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-durable-result-local-owner", ApprovalID: localApprovalID, Status: "pending",
	}); err != nil {
		t.Fatalf("record owner-local approval: %v", err)
	}
	for attempt := range 2 {
		handledResult, err = owner.DispatchActiveCommand(context.Background(), testBotID, sessionID, CommandToolApprovalResponse, localApprovalID, []byte(`{"decision":"approve"}`))
		if err != nil || !handledResult {
			t.Fatalf("owner-local dispatch %d = handled:%v err:%v", attempt, handledResult, err)
		}
		if attempt == 0 {
			receiveTestResult(t, "owner-local command handler", handled)
		}
	}
	const localBarrierApprovalID = "approval-durable-result-local-barrier"
	if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, sessionID, runID), native.StreamEvent{
		Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-durable-result-local-barrier", ApprovalID: localBarrierApprovalID, Status: "pending",
	}); err != nil {
		t.Fatalf("record owner-local barrier approval: %v", err)
	}
	if handledResult, err = owner.DispatchActiveCommand(context.Background(), testBotID, sessionID, CommandToolApprovalResponse, localBarrierApprovalID, []byte(`{"decision":"approve"}`)); err != nil || !handledResult {
		t.Fatalf("dispatch owner-local barrier = handled:%v err:%v", handledResult, err)
	}
	if barrier := receiveTestResult(t, "owner-local barrier command handler", handled); barrier.TargetID != localBarrierApprovalID {
		t.Fatalf("owner-local stable retry executed before barrier: %#v", barrier)
	}
	handledResult, err = owner.DispatchActiveCommand(context.Background(), testBotID, sessionID, CommandToolApprovalResponse, localApprovalID, []byte(`{"decision":"reject"}`))
	if !handledResult || !errors.Is(err, ErrCommandPayloadConflict) {
		t.Fatalf("owner-local conflicting retry = handled:%v err:%v, want payload conflict", handledResult, err)
	}
}

func runRedisBoundedCommandWorkersContract(t *testing.T, redisURL string) {
	t.Helper()

	prefix := uniqueRuntimeBackendPrefix("bounded-command-workers")
	newBackend := func() *RedisBackend {
		backend, err := NewRedisBackend(context.Background(), RedisOptions{
			URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute,
		})
		if err != nil {
			t.Fatalf("redis backend: %v", err)
		}
		return backend
	}
	owner := testRuntimeManagerWithOptions(t, newBackend(), Options{
		OwnerID: "bounded-worker-owner", StateTTL: time.Minute, OwnerLeaseTTL: 5 * time.Second,
		CommandAckTTL: 2 * time.Second, CommandWorkerLimit: 1,
	})
	remote := testRuntimeManagerWithOptions(t, newBackend(), Options{
		OwnerID: "bounded-worker-remote", StateTTL: time.Minute,
		OwnerLeaseTTL: 5 * time.Second, CommandAckTTL: 2 * time.Second,
	})

	const commandCount = 8
	for i := range commandCount {
		sessionID := fmt.Sprintf("session-bounded-worker-%d", i)
		runID := fmt.Sprintf("stream-bounded-worker-%d", i)
		approvalID := fmt.Sprintf("approval-bounded-worker-%d", i)
		if err := owner.StartRun(context.Background(), testBotID, sessionID, runID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
			t.Fatalf("start run %d: %v", i, err)
		}
		if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, sessionID, runID), native.StreamEvent{
			Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: fmt.Sprintf("call-bounded-worker-%d", i), ApprovalID: approvalID, Status: "pending",
		}); err != nil {
			t.Fatalf("record approval %d: %v", i, err)
		}
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var active atomic.Int64
	var maxActive atomic.Int64
	owner.SetCommandHandler(func(ctx context.Context, _ Command) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maxActive.Load()
			if current <= observed || maxActive.CompareAndSwap(observed, current) {
				break
			}
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	type dispatchResult struct {
		index   int
		handled bool
		err     error
	}
	results := make(chan dispatchResult, commandCount)
	for i := range commandCount {
		i := i
		go func() {
			handled, err := remote.DispatchActiveCommand(
				context.Background(), testBotID, fmt.Sprintf("session-bounded-worker-%d", i),
				CommandToolApprovalResponse, fmt.Sprintf("approval-bounded-worker-%d", i), []byte(`{"decision":"approve"}`),
			)
			results <- dispatchResult{index: i, handled: handled, err: err}
		}()
	}
	receiveTestResult(t, "first bounded command handler", entered)

	completed := make([]dispatchResult, 0, commandCount)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for len(completed) < commandCount {
		select {
		case result := <-results:
			completed = append(completed, result)
			if errors.Is(result.err, ErrCommandBusy) {
				releaseOnce.Do(func() { close(release) })
			}
		case <-deadline.C:
			releaseOnce.Do(func() { close(release) })
			t.Fatalf("timed out waiting for bounded command results; completed=%d", len(completed))
		}
	}
	busy := 0
	busyIndex := -1
	for _, result := range completed {
		if !result.handled {
			t.Fatalf("bounded command was not handled: %#v", result)
		}
		if errors.Is(result.err, ErrCommandBusy) {
			busy++
			if busyIndex < 0 {
				busyIndex = result.index
			}
			continue
		}
		if result.err != nil {
			t.Fatalf("bounded command result: %v", result.err)
		}
	}
	if busy == 0 {
		t.Fatal("command worker saturation did not return ErrCommandBusy")
	}
	if got := maxActive.Load(); got > 1 {
		t.Fatalf("maximum concurrent command handlers = %d, want <= 1", got)
	}
	if busyIndex < 0 {
		t.Fatal("missing busy command index")
	}
	handled, err := remote.DispatchActiveCommand(
		context.Background(), testBotID, fmt.Sprintf("session-bounded-worker-%d", busyIndex),
		CommandToolApprovalResponse, fmt.Sprintf("approval-bounded-worker-%d", busyIndex), []byte(`{"decision":"approve"}`),
	)
	if err != nil || !handled {
		t.Fatalf("retry after command capacity recovered = handled:%v err:%v", handled, err)
	}
}

func runRedisDuplicateCommandSaturationContract(t *testing.T, redisURL string) {
	t.Helper()

	prefix := uniqueRuntimeBackendPrefix("duplicate-command-saturation")
	newBackend := func() *RedisBackend {
		backend, err := NewRedisBackend(context.Background(), RedisOptions{
			URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute,
		})
		if err != nil {
			t.Fatalf("redis backend: %v", err)
		}
		return backend
	}
	owner := testRuntimeManagerWithOptions(t, newBackend(), Options{
		OwnerID: "duplicate-saturation-owner", StateTTL: time.Minute,
		OwnerLeaseTTL: 5 * time.Second, CommandAckTTL: 2 * time.Second, CommandWorkerLimit: 1,
	})
	remote := testRuntimeManagerWithOptions(t, newBackend(), Options{
		OwnerID: "duplicate-saturation-remote", StateTTL: time.Minute,
		OwnerLeaseTTL: 5 * time.Second, CommandAckTTL: 2 * time.Second,
	})

	const (
		sessionA  = "session-duplicate-saturation-a"
		streamA   = "stream-duplicate-saturation-a"
		approvalA = "approval-duplicate-saturation-a"
		sessionB  = "session-duplicate-saturation-b"
		streamB   = "stream-duplicate-saturation-b"
		approvalB = "approval-duplicate-saturation-b"
	)
	startApproval := func(sessionID, runID, approvalID string) {
		t.Helper()
		if err := owner.StartRun(context.Background(), testBotID, sessionID, runID, make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
			t.Fatalf("start %s: %v", runID, err)
		}
		if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, sessionID, runID), native.StreamEvent{
			Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-" + approvalID, ApprovalID: approvalID, Status: "pending",
		}); err != nil {
			t.Fatalf("record %s: %v", approvalID, err)
		}
	}
	startApproval(sessionA, streamA, approvalA)
	startApproval(sessionB, streamB, approvalB)

	enteredA := make(chan struct{})
	releaseA := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseA) }) })
	var callsA atomic.Int64
	var callsB atomic.Int64
	owner.SetCommandHandler(func(ctx context.Context, command Command) error {
		switch command.TargetID {
		case approvalA:
			if callsA.Add(1) == 1 {
				close(enteredA)
			}
			select {
			case <-releaseA:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		case approvalB:
			callsB.Add(1)
		}
		return nil
	})

	type dispatchResult struct {
		name    string
		handled bool
		err     error
	}
	results := make(chan dispatchResult, 3)
	dispatch := func(name, sessionID, approvalID string) {
		go func() {
			handled, err := remote.DispatchActiveCommand(context.Background(), testBotID, sessionID, CommandToolApprovalResponse, approvalID, []byte(`{"decision":"approve"}`))
			results <- dispatchResult{name: name, handled: handled, err: err}
		}()
	}
	dispatch("first-a", sessionA, approvalA)
	receiveTestResult(t, "first duplicate-saturation handler", enteredA)

	snapshotB, err := owner.Snapshot(context.Background(), testBotID, sessionB)
	if err != nil || snapshotB.CurrentRunView == nil {
		t.Fatalf("load queued run B: %v %#v", err, snapshotB.CurrentRunView)
	}
	commandBID := activeCommandID(testBotID, sessionB, snapshotB.CurrentRunView, CommandToolApprovalResponse, approvalB)
	dispatch("queued-b", sessionB, approvalB)
	deadline := time.Now().Add(time.Second)
	for {
		owner.mu.Lock()
		_, admitted := owner.admittedCommands[commandBID]
		owner.mu.Unlock()
		if admitted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second command was not queued behind the blocked worker")
		}
		time.Sleep(time.Millisecond)
	}

	dispatch("duplicate-a", sessionA, approvalA)
	snapshotA, err := owner.Snapshot(context.Background(), testBotID, sessionA)
	if err != nil || snapshotA.CurrentRunView == nil {
		t.Fatalf("load active run A: %v %#v", err, snapshotA.CurrentRunView)
	}
	commandAID := activeCommandID(testBotID, sessionA, snapshotA.CurrentRunView, CommandToolApprovalResponse, approvalA)
	deadline = time.Now().Add(time.Second)
	for {
		remote.mu.Lock()
		waiters := len(remote.pendingCommands[commandAID])
		remote.mu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("duplicate requester waiters = %d, want 2", waiters)
		}
		time.Sleep(time.Millisecond)
	}

	releaseOnce.Do(func() { close(releaseA) })
	for range 3 {
		result := receiveTestResult(t, "duplicate-saturation dispatch", results)
		if !result.handled || result.err != nil {
			t.Fatalf("%s = handled:%v err:%v", result.name, result.handled, result.err)
		}
	}
	if got := callsA.Load(); got != 1 {
		t.Fatalf("duplicate A handler calls = %d, want 1", got)
	}
	if got := callsB.Load(); got != 1 {
		t.Fatalf("queued B handler calls = %d, want 1", got)
	}
	stored, ok, err := remote.distributed.LoadCommandResult(context.Background(), commandAID)
	if err != nil || !ok || stored.Error != "" {
		t.Fatalf("stable command result after saturation = ok:%v err:%v result:%#v", ok, err, stored)
	}
}

func runRedisExpiredActiveResponseTransportContract(t *testing.T, redisURL string) {
	t.Helper()

	const (
		commandTTL   = 75 * time.Millisecond
		commandDelay = 125 * time.Millisecond
	)
	prefix := uniqueRuntimeBackendPrefix("expired-command-transport")
	newBackend := func() *RedisBackend {
		backend, err := NewRedisBackend(context.Background(), RedisOptions{
			URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute,
		})
		if err != nil {
			t.Fatalf("redis backend: %v", err)
		}
		return backend
	}
	observed := make(chan Command, 4)
	owner := testRuntimeManagerWithOptions(t, delayedCommandSubscriptionBackend{
		DistributedBackend: newBackend(), delay: commandDelay, observed: observed,
	}, Options{
		OwnerID: "expired-command-owner", StateTTL: time.Minute,
		OwnerLeaseTTL: time.Second, CommandAckTTL: commandTTL,
	})
	remote := testRuntimeManagerWithOptions(t, newBackend(), Options{
		OwnerID: "expired-command-remote", StateTTL: time.Minute,
		OwnerLeaseTTL: time.Second, CommandAckTTL: commandTTL,
	})
	handledCommands := make(chan Command, 2)
	owner.SetCommandHandler(func(_ context.Context, command Command) error {
		handledCommands <- command
		return nil
	})
	if err := owner.StartRun(context.Background(), testBotID, "session-expired-command", "stream-expired-command", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
		t.Fatalf("start run: %v", err)
	}
	for _, event := range []native.StreamEvent{
		{Type: native.EventToolApprovalRequest, ToolName: "exec", ToolCallID: "call-expired-approval", ApprovalID: "approval-expired", Status: "pending"},
		{Type: native.EventUserInputRequest, ToolName: "ask_user", ToolCallID: "call-expired-input", UserInputID: "input-expired", Status: "pending"},
	} {
		if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, "session-expired-command", "stream-expired-command"), event); err != nil {
			t.Fatalf("record response target: %v", err)
		}
	}

	seenCommandIDs := make(map[string]string)
	for _, request := range []struct {
		commandType string
		targetID    string
	}{
		{commandType: CommandToolApprovalResponse, targetID: "approval-expired"},
		{commandType: CommandUserInputResponse, targetID: "input-expired"},
	} {
		type dispatchResult struct {
			handled bool
			err     error
		}
		dispatchDone := make(chan dispatchResult, 1)
		go func() {
			handled, err := remote.DispatchActiveCommand(context.Background(), testBotID, "session-expired-command", request.commandType, request.targetID, []byte(`{"ok":true}`))
			dispatchDone <- dispatchResult{handled: handled, err: err}
		}()
		var command Command
		for {
			command = receiveTestResult(t, "serialized runtime command", observed)
			if command.Type == request.commandType && command.TargetID == request.targetID {
				break
			}
			if seenCommandIDs[command.TargetID] != command.ID {
				t.Fatalf("transported command = %#v", command)
			}
		}
		seenCommandIDs[request.targetID] = command.ID
		if command.CreatedAt.IsZero() || command.ExpiresAt.IsZero() || !command.ExpiresAt.After(command.CreatedAt) {
			t.Fatalf("transported command expiry = created:%s expires:%s", command.CreatedAt, command.ExpiresAt)
		}
		result := receiveTestResult(t, "expired command dispatch", dispatchDone)
		if !result.handled || result.err == nil {
			t.Fatalf("expired command dispatch = handled:%v err:%v, want timeout", result.handled, result.err)
		}
	}
	time.Sleep(2 * commandDelay)
	select {
	case command := <-handledCommands:
		t.Fatalf("expired command reached handler: %#v", command)
	default:
	}
}

func runRedisSubscriptionReconnectContract(t *testing.T, redisURL string) {
	t.Helper()

	prefix := uniqueRuntimeBackendPrefix("reconnect")
	clientName := fmt.Sprintf("memoh-runtime-reconnect-%d", runtimeBackendPrefixSequence.Add(1))
	parsedURL, err := url.Parse(redisURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	query := parsedURL.Query()
	query.Set("client_name", clientName)
	parsedURL.RawQuery = query.Encode()
	newBackend := func() *RedisBackend {
		backend, err := NewRedisBackend(context.Background(), RedisOptions{URL: parsedURL.String(), KeyPrefix: prefix, StateTTL: time.Minute})
		if err != nil {
			t.Fatalf("redis backend: %v", err)
		}
		return backend
	}
	ownerTransport := newBackend()
	ownerBackend := &dropNextRuntimePublishBackend{DistributedBackend: ownerTransport}
	remoteBackend := newBackend()
	owner := NewManager(ownerBackend, Options{OwnerID: "owner-reconnect", StateTTL: time.Minute, OwnerLeaseTTL: time.Second, CommandAckTTL: time.Second})
	remote := NewManager(remoteBackend, Options{OwnerID: "remote-reconnect", StateTTL: time.Minute, OwnerLeaseTTL: time.Second, CommandAckTTL: time.Second})
	if err := owner.Start(context.Background()); err != nil {
		t.Fatalf("start owner manager: %v", err)
	}
	if err := remote.Start(context.Background()); err != nil {
		t.Fatalf("start remote manager: %v", err)
	}
	t.Cleanup(func() {
		_ = remote.Close()
		_ = owner.Close()
	})
	sub, err := remote.Subscribe(context.Background(), testBotID, "session-reconnect")
	if err != nil {
		t.Fatalf("subscribe runtime: %v", err)
	}
	t.Cleanup(sub.Close)
	injectCh := make(chan turn.InjectMessage, 1)
	if err := owner.StartRun(context.Background(), testBotID, "session-reconnect", "stream-reconnect", make(chan struct{}, 1), func() {}, injectCh); err != nil {
		t.Fatalf("start reconnect run: %v", err)
	}
	active := waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Delta != nil && event.Delta.CurrentRunView != nil &&
			event.Delta.CurrentRunView.RunID == "stream-reconnect" &&
			event.Delta.CurrentRunView.Status == RunStatusRunning
	})

	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	admin := redis.NewClient(redisOptions)
	t.Cleanup(func() { _ = admin.Close() })
	killRedisPubSubClientsByName(t, admin, clientName)
	ownerBackend.arm()
	if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, "session-reconnect", "stream-reconnect"), native.StreamEvent{Type: native.EventTextDelta, Delta: "during disconnect"}); err != nil {
		t.Fatalf("handle event during Pub/Sub disconnect: %v", err)
	}
	checkpoint := waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Type == EventRuntimeSnapshot && event.Seq >= active.Seq+1
	})
	if checkpoint.Seq != active.Seq+1 || checkpoint.Snapshot == nil || checkpoint.Snapshot.CurrentRunView == nil ||
		len(checkpoint.Snapshot.CurrentRunView.Messages) != 1 || checkpoint.Snapshot.CurrentRunView.Messages[0].Content != "during disconnect" {
		t.Fatalf("disconnect recovery checkpoint = %#v", checkpoint)
	}
	waitRedisSubscriptions(t, admin,
		ownerTransport.commandChannel("owner-reconnect"),
		remoteBackend.commandChannel("remote-reconnect"),
		remoteBackend.sessionChannel(Key{BotID: testBotID, SessionID: "session-reconnect"}),
	)

	if _, err := owner.HandleAgentEvent(context.Background(), requireRunHandle(t, owner, testBotID, "session-reconnect", "stream-reconnect"), native.StreamEvent{Type: native.EventTextDelta, Delta: "after reconnect"}); err != nil {
		t.Fatalf("handle event after reconnect: %v", err)
	}
	next := waitRuntimeEvent(t, sub.C, func(event Event) bool {
		return event.Delta != nil && len(event.Delta.MessageAppends) == 1 && event.Delta.MessageAppends[0].Content == "after reconnect"
	})
	if next.Type != EventRuntimeDelta || next.Seq != checkpoint.Seq+1 {
		t.Fatalf("post-reconnect event = %#v, want continuous delta after seq %d", next, checkpoint.Seq)
	}
	if _, err := remote.Steer(context.Background(), testBotID, "session-reconnect", "stream-reconnect", "steer after reconnect"); err != nil {
		t.Fatalf("steer after reconnect: %v", err)
	}
	select {
	case injected := <-injectCh:
		if injected.Text != "steer after reconnect" {
			t.Fatalf("injected text = %q", injected.Text)
		}
		if injected.Applied != nil {
			injected.Applied()
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote steer was not delivered after Pub/Sub reconnect")
	}
}

func killRedisPubSubClientsByName(t *testing.T, client *redis.Client, clientName string) {
	t.Helper()
	clientList, err := client.Do(context.Background(), "CLIENT", "LIST", "TYPE", "pubsub").Text()
	if err != nil {
		t.Fatalf("list pubsub clients: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(clientList, "\n") {
		fields := make(map[string]string)
		for _, field := range strings.Fields(line) {
			key, value, ok := strings.Cut(field, "=")
			if ok {
				fields[key] = value
			}
		}
		if fields["name"] == clientName && fields["id"] != "" {
			ids = append(ids, fields["id"])
		}
	}
	if len(ids) == 0 {
		t.Fatalf("no pubsub clients found for %q", clientName)
	}
	for _, id := range ids {
		if _, err := client.ClientKillByFilter(context.Background(), "ID", id, "SKIPME", "yes").Result(); err != nil {
			t.Fatalf("kill pubsub client %s for %q: %v", id, clientName, err)
		}
	}
}

func waitRedisSubscriptions(t *testing.T, client *redis.Client, channels ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		counts, err := client.PubSubNumSub(context.Background(), channels...).Result()
		if err != nil {
			t.Fatalf("read pubsub subscription counts: %v", err)
		}
		ready := true
		for _, channel := range channels {
			if counts[channel] == 0 {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("pubsub channels were not restored: %v", channels)
}
