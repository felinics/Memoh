package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func TestChildServersRetireWithoutStoppingParentOrSibling(t *testing.T) {
	starter := &fakeStarter{}
	driver := &Driver{servers: newServerTable(starter.start, slog.Default())}
	t.Cleanup(driver.CloseAll)
	parent := external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "parent", SessionMode: "chat"}
	childA := external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "child-a", SessionMode: "subagent"}
	childB := external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "child-b", SessionMode: "subagent"}
	acquire := func(input external.PromptInput) (recyclable, func()) {
		t.Helper()
		resource, release, err := driver.servers.acquire(t.Context(), promptServerKey(input))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
		return resource, release
	}
	parentServer, _ := acquire(parent)
	a, releaseA := acquire(childA)
	b, _ := acquire(childB)
	if parentServer == a || a == b || parentServer == b {
		t.Fatal("parent and children share a cancellable app-server")
	}
	driver.recycleTurnServer(childA, a)
	releaseA()
	select {
	case <-a.Done():
	default:
		t.Fatal("retired child still depends on an unrelated active reference")
	}
	for _, unaffected := range []recyclable{parentServer, b} {
		select {
		case <-unaffected.Done():
			t.Fatal("retiring child stopped another active conversation")
		default:
		}
	}
	next, _ := acquire(childA)
	if next == a {
		t.Fatal("followup reused retired process")
	}
	parentAgain, _ := acquire(parent)
	bAgain, _ := acquire(childB)
	if parentAgain != parentServer || bAgain != b {
		t.Fatal("child retirement displaced unrelated conversations")
	}
	driver.ResetBotAgent("bot", "agent")
	for _, input := range []external.PromptInput{parent, childA, childB} {
		if driver.servers.peek(promptServerKey(input)) != nil {
			t.Fatal("instance reset left a child app-server reusable")
		}
		botID, agentID := splitServerKey(promptServerKey(input))
		if botID != "bot" || agentID != "agent" {
			t.Fatalf("child scope changed configured instance identity: %q %q", botID, agentID)
		}
	}
}

func TestChildServerStartedForAControlRetiresAfterIt(t *testing.T) {
	starter := &fakeStarter{}
	driver := &Driver{servers: newServerTable(starter.start, slog.Default())}
	t.Cleanup(driver.CloseAll)
	acquire := func(input external.PromptInput) (recyclable, func()) {
		t.Helper()
		resource, release, err := driver.servers.acquire(t.Context(), promptServerKey(input))
		if err != nil {
			t.Fatal(err)
		}
		return resource, release
	}
	alive := func(resource recyclable) bool {
		select {
		case <-resource.Done():
			return false
		default:
			return true
		}
	}
	parent := external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "parent", SessionMode: "chat"}
	child := external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "child", SessionMode: "subagent"}
	shared, release := acquire(parent)
	driver.controlRelease(parent, shared, release)()
	if !alive(shared) {
		t.Fatal("a control stopped the shared server")
	}
	server, release := acquire(child)
	driver.controlRelease(child, server, release)()
	if alive(server) {
		t.Fatal("a control left the child's server running")
	}
	server, releasePrompt := acquire(child)
	for range 2 {
		again, release := acquire(child)
		driver.controlRelease(child, again, release)()
		if again != server || !alive(server) {
			t.Fatal("a control displaced the server a prompt still uses")
		}
	}
	releasePrompt()
	again, release := acquire(child)
	driver.controlRelease(child, again, release)()
	if again != server || alive(server) {
		t.Fatal("the child's server outlived its last user")
	}
}

func TestGoalOfAChildWithoutAThreadStartsNothing(t *testing.T) {
	starter := &fakeStarter{}
	driver := &Driver{servers: newServerTable(starter.start, slog.Default())}
	t.Cleanup(driver.CloseAll)
	_, release, err := driver.servers.acquire(t.Context(), serverKey("bot", "agent"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	goal, err := driver.Goal(t.Context(), external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "child", SessionMode: "subagent"})
	if goal != nil || err != nil || len(starter.started) != 1 {
		t.Fatalf("goal=%v err=%v servers=%d", goal, err, len(starter.started))
	}
}

type stoppingResource struct {
	*fakeResource
	stopErr error
}

func (r *stoppingResource) Stop(context.Context) error {
	if r.stopErr != nil {
		return r.stopErr
	}
	return r.Close()
}

func TestChildPromptCleanupBoundsProcessesAndPreservesResumeAnchor(t *testing.T) {
	var resources []*stoppingResource
	driver := &Driver{servers: newServerTable(func(context.Context, string) (recyclable, error) {
		r := &stoppingResource{fakeResource: newFakeResource(len(resources))}
		resources = append(resources, r)
		return r, nil
	}, slog.Default())}
	t.Cleanup(driver.CloseAll)
	for i := range 20 {
		input := external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: fmt.Sprintf("child-%d", i), SessionMode: "subagent"}
		resource, release, err := driver.servers.acquire(t.Context(), promptServerKey(input))
		if err != nil {
			t.Fatal(err)
		}
		result := external.PromptResult{TurnCompleted: true, AgentTurnID: "native-turn", RuntimeMetadata: map[string]any{metadataThreadIDKey: "native-thread", metadataRolloutPathKey: "/data/native.jsonl"}}
		if err := driver.finishSubagentPrompt(t.Context(), input, resource.(*stoppingResource), &result); err != nil {
			t.Fatal(err)
		}
		release()
		if result.RuntimeMetadata[metadataThreadIDKey] != "native-thread" || result.AgentTurnID != "native-turn" || result.TerminationUnconfirmed {
			t.Fatalf("cleanup lost the followup anchor: %+v", result)
		}
		for _, r := range resources {
			select {
			case <-r.Done():
			default:
				t.Fatal("completed children accumulated live app-server processes")
			}
		}
	}
}

func TestChildInterruptedCleanupReportsUnknownExecution(t *testing.T) {
	resource := &stoppingResource{fakeResource: newFakeResource(1), stopErr: errors.New("missing EXIT")}
	driver := &Driver{servers: newServerTable(func(context.Context, string) (recyclable, error) { return resource, nil }, slog.Default())}
	input := external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "child", SessionMode: "subagent"}
	_, release, err := driver.servers.acquire(t.Context(), promptServerKey(input))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	result := external.PromptResult{Text: "partial", AgentTurnID: "accepted-turn"}
	err = driver.finishSubagentPrompt(t.Context(), input, resource, &result)
	if !errors.Is(err, external.ErrTerminationUnconfirmed) || !result.TerminationUnconfirmed || result.Text != "partial" || result.AgentTurnID != "accepted-turn" {
		t.Fatalf("unknown execution was lost: result=%+v err=%v", result, err)
	}
}

func TestCompletedChildRequiresExitAcknowledgement(t *testing.T) {
	resource := &stoppingResource{fakeResource: newFakeResource(1), stopErr: errors.New("missing EXIT")}
	driver := &Driver{servers: newServerTable(func(context.Context, string) (recyclable, error) { return resource, nil }, slog.Default())}
	input := external.PromptInput{BotID: "bot", BotAgentID: "agent", ThreadID: "child", SessionMode: "subagent"}
	_, release, err := driver.servers.acquire(t.Context(), promptServerKey(input))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	result := external.PromptResult{TurnCompleted: true, Text: "finished report", AgentTurnID: "completed-turn", RuntimeMetadata: map[string]any{metadataThreadIDKey: "child-native"}}
	err = driver.finishSubagentPrompt(t.Context(), input, resource, &result)
	if !errors.Is(err, external.ErrTerminationUnconfirmed) || !result.TerminationUnconfirmed || !result.TurnCompleted || result.Text != "finished report" || result.AgentTurnID != "completed-turn" || result.RuntimeMetadata[metadataThreadIDKey] != "child-native" {
		t.Fatalf("completion hid uncertain process termination or lost its report: result=%+v err=%v", result, err)
	}
}
