package acp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

func warmInstanceRuntime(botAgentID string) *runtimeHandle {
	return &runtimeHandle{
		id:                    newRuntimeID(),
		botID:                 "bot-1",
		botAgentID:            botAgentID,
		agentID:               acpprofile.AgentACPID,
		projectPath:           "/data",
		runtimeOwnerAccountID: "user-1",
		session:               &client.Session{},
		status:                stateIdle,
		lastActive:            time.Now(),
	}
}

// Every generic instance shares the "acp" profile id, so only the instance id
// tells a Hermes process from a Grok one.
func TestWarmRuntimeServesOnlyItsOwnInstance(t *testing.T) {
	pool := newSessionPool(nil, nil, nil, fakeSessionGetter{session: SessionDescriptor{
		BotID:      "bot-1",
		BotAgentID: "agent-grok",
		IsACP:      true,
	}})
	hermes := warmInstanceRuntime("agent-hermes")
	grok := warmInstanceRuntime("agent-grok")
	injectRuntime(pool, hermes)
	injectRuntime(pool, grok)

	err := pool.BindRuntime(context.Background(), "bot-1", hermes.id, "grok-session", acpprofile.AgentACPID, "/data", "user-1")
	if !errors.Is(err, ErrRuntimeBindRejected) {
		t.Fatalf("binding another instance's runtime: error = %v, want ErrRuntimeBindRejected", err)
	}
	if err := pool.BindRuntime(context.Background(), "bot-1", grok.id, "grok-session", acpprofile.AgentACPID, "/data", "user-1"); err != nil {
		t.Fatalf("binding the session's own instance runtime: error = %v", err)
	}
}

func TestCloseBotAgentRuntimesLeavesOtherInstancesRunning(t *testing.T) {
	pool := newSessionPool(nil, nil, nil)
	hermes := warmInstanceRuntime("agent-hermes")
	grok := warmInstanceRuntime("agent-grok")
	unbound := warmInstanceRuntime("")
	for _, h := range []*runtimeHandle{hermes, grok, unbound} {
		injectRuntime(pool, h)
	}

	if err := pool.CloseBotAgentRuntimes("bot-1", "agent-hermes"); err != nil {
		t.Fatalf("CloseBotAgentRuntimes() error = %v", err)
	}

	pool.mu.RLock()
	_, hermesAlive := pool.runtimes[hermes.id]
	_, grokAlive := pool.runtimes[grok.id]
	_, unboundAlive := pool.runtimes[unbound.id]
	pool.mu.RUnlock()
	if hermesAlive {
		t.Fatal("the reconfigured instance's runtime must be closed")
	}
	if !grokAlive {
		t.Fatal("another instance's runtime must keep running")
	}
	// A provider-only session may be running as the reconfigured instance.
	if unboundAlive {
		t.Fatal("a runtime that names no instance must be closed too")
	}
}
