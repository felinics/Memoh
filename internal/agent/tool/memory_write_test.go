package tools

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/toolexec"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/mcp"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/settings"
)

const memoryWriteProviderID = "provider-1"

// recordingMemoryProvider records the write requests the tools issue. The
// embedded interface keeps the fake to the methods these tests exercise.
type recordingMemoryProvider struct {
	memprovider.Provider
	added      []memprovider.AddRequest
	updated    []memprovider.UpdateRequest
	deleted    []string
	deleteBots []string
	addErr     error
}

func (*recordingMemoryProvider) Type() string { return "recording" }

func (*recordingMemoryProvider) ListTools(context.Context, mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return nil, nil
}

func (p *recordingMemoryProvider) Add(_ context.Context, req memprovider.AddRequest) (memprovider.SearchResponse, error) {
	p.added = append(p.added, req)
	if p.addErr != nil {
		return memprovider.SearchResponse{}, p.addErr
	}
	return memprovider.SearchResponse{Results: []memprovider.MemoryItem{{ID: "mem-1", Memory: req.Message}}}, nil
}

func (p *recordingMemoryProvider) Update(_ context.Context, req memprovider.UpdateRequest) (memprovider.MemoryItem, error) {
	p.updated = append(p.updated, req)
	return memprovider.MemoryItem{ID: req.MemoryID, Memory: req.Memory}, nil
}

func (p *recordingMemoryProvider) Delete(_ context.Context, botID string, memoryID string) (memprovider.DeleteResponse, error) {
	p.deleted = append(p.deleted, memoryID)
	p.deleteBots = append(p.deleteBots, botID)
	return memprovider.DeleteResponse{}, nil
}

type memoryWriteSettings struct{}

func (memoryWriteSettings) GetBot(context.Context, string) (settings.Settings, error) {
	return settings.Settings{MemoryProviderID: memoryWriteProviderID}, nil
}

type recordingHookService struct {
	events   []string
	decision string
	reason   string
	err      error
}

func (h *recordingHookService) Run(_ context.Context, req hooks.Request, _ hooks.ToolRunner) (hooks.Result, error) {
	h.events = append(h.events, req.Event)
	if req.Event == hooks.EventBeforeMemoryWrite {
		return hooks.Result{Decision: h.decision, Reason: h.reason}, h.err
	}
	return hooks.Result{}, nil
}

func newMemoryWriteProvider(t *testing.T, threads []session.Thread) (*MemoryProvider, *recordingMemoryProvider) {
	t.Helper()
	backing := &recordingMemoryProvider{}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(memoryWriteProviderID, backing)
	return NewMemoryProvider(slog.New(slog.DiscardHandler), registry, memoryWriteSettings{}, fakeHistorySessionLister{sessions: threads}), backing
}

func memoryTool(t *testing.T, p *MemoryProvider, session SessionContext, name string) toolexec.Tool {
	t.Helper()
	tools, err := p.Tools(context.Background(), session)
	if err != nil {
		t.Fatalf("Tools error: %v", err)
	}
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s is not on the memory surface", name)
	return toolexec.Tool{}
}

func TestMemoryWriteToolsAreOnTheSurface(t *testing.T) {
	t.Parallel()
	p, _ := newMemoryWriteProvider(t, nil)
	// The gateway re-exports whatever this returns, so presence here is what
	// makes the write tools reachable from the external agent runtimes.
	for _, name := range []string{ToolCreateMemory().String(), ToolUpdateMemory().String(), ToolDeleteMemory().String()} {
		memoryTool(t, p, SessionContext{BotID: "bot-1"}, name)
	}
}

func TestCreateMemoryKeysProfileToUserWithoutSessionUserID(t *testing.T) {
	t.Parallel()
	// External agent runtimes carry no user id on the tool path; the thread's
	// creator has to stand in, or this write keys on channel_identity while
	// formation keyed the same person on user.
	p, backing := newMemoryWriteProvider(t, []session.Thread{
		{ID: "session-1", BotID: "bot-1", CreatedByUserID: "user-1"},
	})
	tool := memoryTool(t, p, SessionContext{BotID: "bot-1", SessionID: "session-1", ChannelIdentityID: "identity-1"}, ToolCreateMemory().String())

	if _, err := tool.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{
		"memory": "Prefers Chinese for PR descriptions.",
		"layer":  "preference",
		"topic":  "language",
	})); err != nil {
		t.Fatalf("create_memory error: %v", err)
	}
	if len(backing.added) != 1 {
		t.Fatalf("expected one write, got %d", len(backing.added))
	}
	metadata := backing.added[0].Metadata
	if metadata["profile_ref"] != "user:user-1" {
		t.Fatalf("profile_ref = %v, want user:user-1", metadata["profile_ref"])
	}
	if metadata["layer"] != "preference" || metadata["topic"] != "language" {
		t.Fatalf("typed metadata not carried: %v", metadata)
	}
	if ns := backing.added[0].Filters["namespace"]; ns != memprovider.SharedMemoryNamespace {
		t.Fatalf("namespace = %v, want %s", ns, memprovider.SharedMemoryNamespace)
	}
}

func TestCreateMemoryPrefersSessionUserOverThreadCreator(t *testing.T) {
	t.Parallel()
	// The speaker this turn is the subject, which is what formation records.
	p, backing := newMemoryWriteProvider(t, []session.Thread{
		{ID: "session-1", BotID: "bot-1", CreatedByUserID: "owner"},
	})
	tool := memoryTool(t, p, SessionContext{BotID: "bot-1", SessionID: "session-1", UserID: "speaker"}, ToolCreateMemory().String())

	if _, err := tool.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{"memory": "A durable fact."})); err != nil {
		t.Fatalf("create_memory error: %v", err)
	}
	if got := backing.added[0].Metadata["profile_ref"]; got != "user:speaker" {
		t.Fatalf("profile_ref = %v, want user:speaker", got)
	}
}

func TestMemoryWriteHookDenyBlocksTheWrite(t *testing.T) {
	t.Parallel()
	for _, name := range []string{ToolCreateMemory().String(), ToolUpdateMemory().String(), ToolDeleteMemory().String()} {
		t.Run(name, func(t *testing.T) {
			p, backing := newMemoryWriteProvider(t, nil)
			hook := &recordingHookService{decision: hooks.DecisionDeny, reason: "policy"}
			p.hookService = hook
			tool := memoryTool(t, p, SessionContext{BotID: "bot-1", SessionID: "session-1"}, name)

			_, err := tool.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{
				"id": "mem-1", "memory": "should never be stored",
			}))
			if err == nil {
				t.Fatal("a denied memory write must be reported to the agent, not silently dropped")
			}
			if !strings.Contains(err.Error(), "policy") {
				t.Fatalf("denial reason lost: %v", err)
			}
			if len(backing.added)+len(backing.updated)+len(backing.deleted) != 0 {
				t.Fatal("denied write still reached the store")
			}
			for _, event := range hook.events {
				if event == hooks.EventAfterMemoryWrite {
					t.Fatal("AfterMemoryWrite fired for a write that never happened")
				}
			}
		})
	}
}

func TestMemoryWriteRunsBeforeAndAfterHooks(t *testing.T) {
	t.Parallel()
	p, _ := newMemoryWriteProvider(t, nil)
	hook := &recordingHookService{}
	p.hookService = hook
	tool := memoryTool(t, p, SessionContext{BotID: "bot-1", SessionID: "session-1"}, ToolCreateMemory().String())

	if _, err := tool.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{"memory": "A durable fact."})); err != nil {
		t.Fatalf("create_memory error: %v", err)
	}
	want := []string{hooks.EventBeforeMemoryWrite, hooks.EventAfterMemoryWrite}
	if len(hook.events) != len(want) || hook.events[0] != want[0] || hook.events[1] != want[1] {
		t.Fatalf("hook events = %v, want %v", hook.events, want)
	}
}

func TestMemoryWriteSurvivesBrokenHook(t *testing.T) {
	t.Parallel()
	// A hook that errors without denying must not take memory down with it.
	p, backing := newMemoryWriteProvider(t, nil)
	p.hookService = &recordingHookService{err: errors.New("hook exploded")}
	tool := memoryTool(t, p, SessionContext{BotID: "bot-1", SessionID: "session-1"}, ToolCreateMemory().String())

	if _, err := tool.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{"memory": "A durable fact."})); err != nil {
		t.Fatalf("create_memory error: %v", err)
	}
	if len(backing.added) != 1 {
		t.Fatalf("expected the write to proceed, got %d", len(backing.added))
	}
}

func TestUpdateAndDeleteMemoryReachTheStore(t *testing.T) {
	t.Parallel()
	p, backing := newMemoryWriteProvider(t, nil)
	sess := SessionContext{BotID: "bot-1", SessionID: "session-1"}

	update := memoryTool(t, p, sess, ToolUpdateMemory().String())
	if _, err := update.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{
		"id": "mem-1", "memory": "Lives in Shanghai.",
	})); err != nil {
		t.Fatalf("update_memory error: %v", err)
	}
	if len(backing.updated) != 1 || backing.updated[0].BotID != "bot-1" || backing.updated[0].MemoryID != "mem-1" || backing.updated[0].Memory != "Lives in Shanghai." {
		t.Fatalf("update did not reach the store: %+v", backing.updated)
	}

	del := memoryTool(t, p, sess, ToolDeleteMemory().String())
	if _, err := del.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{"id": "mem-1"})); err != nil {
		t.Fatalf("delete_memory error: %v", err)
	}
	if len(backing.deleted) != 1 || backing.deleted[0] != "mem-1" || backing.deleteBots[0] != "bot-1" {
		t.Fatalf("delete did not reach the store: %v", backing.deleted)
	}
}

func TestMemoryWriteRejectsTranscriptSizedBody(t *testing.T) {
	t.Parallel()
	p, backing := newMemoryWriteProvider(t, nil)
	tool := memoryTool(t, p, SessionContext{BotID: "bot-1"}, ToolCreateMemory().String())

	if _, err := tool.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{
		"memory": strings.Repeat("x", maxMemoryToolBodyRunes+1),
	})); err == nil {
		t.Fatal("an oversized body must be rejected, not stored as a transcript")
	}
	if len(backing.added) != 0 {
		t.Fatal("rejected write still reached the store")
	}
}

func TestMemoryWriteIgnoresUnknownLayer(t *testing.T) {
	t.Parallel()
	p, backing := newMemoryWriteProvider(t, nil)
	tool := memoryTool(t, p, SessionContext{BotID: "bot-1"}, ToolCreateMemory().String())

	if _, err := tool.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{
		"memory": "Deploys run on Fridays.", "layer": "not-a-layer",
	})); err != nil {
		t.Fatalf("create_memory error: %v", err)
	}
	if _, ok := backing.added[0].Metadata["layer"]; ok {
		t.Fatalf("unknown layer must be dropped, got %v", backing.added[0].Metadata["layer"])
	}
}

func TestPublicMCPHeadersCannotAdoptPrivateMemoryIdentity(t *testing.T) {
	t.Parallel()
	threads := []session.Thread{
		{ID: "victim-thread", BotID: "bot-1", CreatedByUserID: "victim"},
		{ID: "caller-thread", BotID: "bot-1", CreatedByUserID: "caller"},
	}
	p, backing := newMemoryWriteProvider(t, threads)
	sess := sessionFromMCP(mcp.ToolSessionContext{
		BotID: "bot-1", SessionID: "victim-thread", SessionType: "acp_agent",
		PublicRequest: true, UserID: "caller", ChannelIdentityID: "caller",
	})
	if sess.SessionID != "" || sess.UserID != "caller" {
		t.Fatalf("untrusted routing became authority: %+v", sess)
	}
	tool := memoryTool(t, p, sess, ToolCreateMemory().String())
	if _, err := tool.Execute(&toolexec.ToolExecContext{Context: context.Background()}, toolexec.ArgumentsFromValue(map[string]any{"memory": "caller preference"})); err != nil {
		t.Fatal(err)
	}
	if got := backing.added[0].Metadata["profile_ref"]; got != "user:caller" {
		t.Fatalf("impersonated profile: %v", got)
	}
	_, allowed, err := visibleHistorySessions(context.Background(), fakeHistorySessionLister{sessions: threads}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if historySessionVisible(allowed, "victim-thread") || !historySessionVisible(allowed, "caller-thread") {
		t.Fatalf("history scope = %v", allowed)
	}
	trusted := sessionFromMCP(mcp.ToolSessionContext{BotID: "bot-1", SessionID: "victim-thread"})
	if got := p.resolveActorUserID(context.Background(), trusted); got != "victim" {
		t.Fatalf("trusted runtime lost author: %s", got)
	}
}
