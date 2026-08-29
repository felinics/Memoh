package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/agent/background"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
)

type fakeSpawnAgent struct {
	block            chan struct{}
	failFor          map[string]string
	contextLifecycle *contextfrag.LifecycleSnapshot

	mu    sync.Mutex
	calls []SpawnRunConfig
}

func (f *fakeSpawnAgent) Generate(ctx context.Context, cfg SpawnRunConfig) (*SpawnResult, error) {
	return f.GenerateWithWatchdog(ctx, cfg, func() {})
}

func (f *fakeSpawnAgent) GenerateWithWatchdog(ctx context.Context, cfg SpawnRunConfig, _ func()) (*SpawnResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cfg)
	f.mu.Unlock()

	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if msg, ok := f.failFor[cfg.Query]; ok {
		return nil, errors.New(msg)
	}
	return &SpawnResult{
		Text: "report for " + cfg.Query,
		Messages: []sdk.Message{{
			Role:    sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{sdk.TextPart{Text: "report for " + cfg.Query}},
		}},
		ContextLifecycle: f.contextLifecycle,
	}, nil
}

func (f *fakeSpawnAgent) queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, call := range f.calls {
		out = append(out, call.Query)
	}
	return out
}

func (f *fakeSpawnAgent) callAt(i int) (SpawnRunConfig, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.calls) {
		return SpawnRunConfig{}, false
	}
	return f.calls[i], true
}

type fakeAgentSessionService struct {
	mu       sync.Mutex
	next     int
	sessions []sessionpkg.Thread
	configs  map[string]sessionpkg.SubagentConfig
	contexts map[string][]sessionpkg.SubagentForkContextMessage
}

func (s *fakeAgentSessionService) Create(_ context.Context, input sessionpkg.CreateInput) (sessionpkg.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	now := time.Unix(int64(s.next), 0).UTC()
	sess := sessionpkg.Thread{
		ID:              "child_" + strconv.Itoa(s.next),
		BotID:           input.BotID,
		Type:            input.Type,
		Title:           input.Title,
		Metadata:        input.Metadata,
		ParentThreadID:  input.ParentThreadID,
		CreatedByUserID: input.CreatedByUserID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	s.sessions = append(s.sessions, sess)
	return sess, nil
}

func (s *fakeAgentSessionService) CreateSubagent(ctx context.Context, input sessionpkg.CreateSubagentInput) (sessionpkg.Thread, sessionpkg.SubagentConfig, error) {
	sess, err := s.Create(ctx, input.Thread)
	if err != nil {
		return sessionpkg.Thread{}, sessionpkg.SubagentConfig{}, err
	}
	config := sessionpkg.SubagentConfig{
		ThreadID:     sess.ID,
		ModelUUID:    input.ModelUUID,
		ModelID:      input.ModelID,
		ProviderName: input.ProviderName,
		Forked:       input.Forked,
	}
	s.mu.Lock()
	if s.configs == nil {
		s.configs = make(map[string]sessionpkg.SubagentConfig)
	}
	s.configs[sess.ID] = config
	if s.contexts == nil {
		s.contexts = make(map[string][]sessionpkg.SubagentForkContextMessage)
	}
	for _, message := range input.ForkContext {
		s.contexts[sess.ID] = append(s.contexts[sess.ID], sessionpkg.SubagentForkContextMessage{
			SourceMessageID: message.SourceMessageID,
			Role:            message.Role,
			Message:         append(json.RawMessage(nil), message.Message...),
		})
	}
	s.mu.Unlock()
	return sess, config, nil
}

func (s *fakeAgentSessionService) ListSubagentForkContext(_ context.Context, sessionID string) ([]sessionpkg.SubagentForkContextMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	contextMessages := s.contexts[sessionID]
	out := make([]sessionpkg.SubagentForkContextMessage, 0, len(contextMessages))
	for _, message := range contextMessages {
		out = append(out, sessionpkg.SubagentForkContextMessage{
			SourceMessageID: message.SourceMessageID,
			Role:            message.Role,
			Message:         append(json.RawMessage(nil), message.Message...),
		})
	}
	return out, nil
}

func (s *fakeAgentSessionService) GetSubagentConfig(_ context.Context, sessionID string) (sessionpkg.SubagentConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	config, ok := s.configs[sessionID]
	if !ok {
		return sessionpkg.SubagentConfig{}, pgx.ErrNoRows
	}
	return config, nil
}

func (s *fakeAgentSessionService) ListSubagentsByParent(_ context.Context, parentSessionID string) ([]sessionpkg.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sessionpkg.Thread
	for _, sess := range s.sessions {
		if sess.ParentThreadID == parentSessionID {
			if _, ok := s.configs[sess.ID]; !ok {
				continue
			}
			out = append(out, sess)
		}
	}
	return out, nil
}

func (s *fakeAgentSessionService) byAgent(parentSessionID, agentID string) (sessionpkg.Thread, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.ParentThreadID == parentSessionID && sess.Metadata["agent_id"] == agentID {
			return sess, true
		}
	}
	return sessionpkg.Thread{}, false
}

type fakeAgentMessageService struct {
	mu       sync.Mutex
	messages map[string][]messagepkg.Message
	inputs   []messagepkg.PersistInput
}

func newFakeAgentMessageService() *fakeAgentMessageService {
	return &fakeAgentMessageService{messages: make(map[string][]messagepkg.Message)}
}

func (s *fakeAgentMessageService) Persist(_ context.Context, input messagepkg.PersistInput) (messagepkg.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, input)
	msg := messagepkg.Message{
		ID:        "msg_" + strconv.Itoa(len(s.messages[input.SessionID])+1),
		BotID:     input.BotID,
		SessionID: input.SessionID,
		Role:      input.Role,
		Content:   input.Content,
		Metadata:  input.Metadata,
		Usage:     input.Usage,
		CreatedAt: time.Now().UTC(),
	}
	s.messages[input.SessionID] = append(s.messages[input.SessionID], msg)
	return msg, nil
}

func (s *fakeAgentMessageService) persistInputs() []messagepkg.PersistInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messagepkg.PersistInput(nil), s.inputs...)
}

func (s *fakeAgentMessageService) ListBySession(_ context.Context, sessionID string) ([]messagepkg.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messagepkg.Message(nil), s.messages[sessionID]...), nil
}

func (*fakeAgentMessageService) List(context.Context, string) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListSince(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListActiveSince(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListLatest(context.Context, string, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListBefore(context.Context, string, time.Time, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListActiveSinceBySession(context.Context, string, time.Time) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListActiveSinceBySessionWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListActiveSinceWithinBytes(context.Context, string, time.Time, int64) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) MeasureActiveBySession(context.Context, string, time.Time) (messagepkg.ActiveMessagesMeasure, error) {
	return messagepkg.ActiveMessagesMeasure{}, nil
}

func (*fakeAgentMessageService) ListLatestBySession(context.Context, string, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListBeforeBySession(context.Context, string, time.Time, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) ListBeforeMessageBySession(context.Context, string, string, int32) ([]messagepkg.Message, error) {
	return nil, nil
}

func (*fakeAgentMessageService) LocateByExternalIDBySession(context.Context, string, string, int32, int32) (messagepkg.LocateResult, error) {
	return messagepkg.LocateResult{}, nil
}

func (s *fakeAgentMessageService) GetByIDBySession(_ context.Context, sessionID string, messageID string) (messagepkg.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, msg := range s.messages[sessionID] {
		if msg.ID == messageID {
			return msg, nil
		}
	}
	return messagepkg.Message{}, nil
}

func (s *fakeAgentMessageService) ListVisibleFromBySession(_ context.Context, sessionID string, messageID string) ([]messagepkg.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.messages[sessionID]
	for i, msg := range msgs {
		if msg.ID == messageID {
			return append([]messagepkg.Message(nil), msgs[i:]...), nil
		}
	}
	return nil, nil
}

func (*fakeAgentMessageService) GetVisibleTurnByMessage(context.Context, string, string) (messagepkg.HistoryTurn, error) {
	return messagepkg.HistoryTurn{}, nil
}

func (*fakeAgentMessageService) GetLatestVisibleTurnBySession(context.Context, string) (messagepkg.HistoryTurn, error) {
	return messagepkg.HistoryTurn{}, nil
}

func (*fakeAgentMessageService) ReplaceTurn(context.Context, string, string, string, *int64, string, string, string) (messagepkg.HistoryTurn, error) {
	return messagepkg.HistoryTurn{}, nil
}

func (*fakeAgentMessageService) DeleteByIDs(context.Context, []string) error {
	return nil
}

func (*fakeAgentMessageService) DeleteByBot(context.Context, string) error {
	return nil
}

func (*fakeAgentMessageService) DeleteBySession(context.Context, string) error {
	return nil
}

func (*fakeAgentMessageService) LinkAssets(context.Context, string, []messagepkg.AssetRef) error {
	return nil
}

func newAgentControlProvider(t *testing.T, agent *fakeSpawnAgent) (*SpawnProvider, *background.Manager, *fakeAgentSessionService, *fakeAgentMessageService) {
	t.Helper()
	return newAgentControlProviderWithAdmitter(t, agent, &fakeSubagentAdmitter{})
}

func newAgentControlProviderWithAdmitter(t *testing.T, agent *fakeSpawnAgent, admitter *fakeSubagentAdmitter) (*SpawnProvider, *background.Manager, *fakeAgentSessionService, *fakeAgentMessageService) {
	t.Helper()
	mgr := background.New(nil)
	sessionSvc := &fakeAgentSessionService{}
	messageSvc := newFakeAgentMessageService()
	p := NewSpawnProvider(nil, nil, nil, nil, nil, mgr)
	p.sessionService = sessionSvc
	p.SetAgent(agent)
	p.SetMessageService(messageSvc)
	p.SetSubagentAdmitter(admitter)
	p.modelResolver = func(context.Context, SessionContext, string, string, string) (resolvedSubagentModel, error) {
		return resolvedSubagentModel{
			Model:            &sdk.Model{},
			UUID:             "00000000-0000-0000-0000-000000000123",
			ModelID:          "test-model",
			ProviderName:     "test-provider",
			SupportsToolCall: true,
		}, nil
	}
	return p, mgr, sessionSvc, messageSvc
}

func executeAgentTool(t *testing.T, p *SpawnProvider, session SessionContext, name string, args map[string]any) (any, error) {
	t.Helper()
	tools, err := p.Tools(context.Background(), session)
	if err != nil {
		t.Fatalf("Tools failed: %v", err)
	}
	for _, tool := range tools {
		if tool.Name == name {
			return tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, args)
		}
	}
	t.Fatalf("tool %q not found in %v", name, toolNames(tools))
	return nil, nil
}

func toolNames(tools []sdk.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func waitUntil(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	m, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", value)
	}
	return m
}

func TestAgentControlToolsExposeSingleAgentSurface(t *testing.T) {
	p, _, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	tools, err := p.Tools(context.Background(), session)
	if err != nil {
		t.Fatalf("Tools failed: %v", err)
	}
	got := toolNames(tools)
	want := []string{"spawn_agent", "send_message", "list_agents", "list_models"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected tools: got %v want %v", got, want)
	}

	subagentTools, err := p.Tools(context.Background(), SessionContext{BotID: "bot1", SessionID: "child", IsSubagent: true})
	if err != nil {
		t.Fatalf("subagent Tools failed: %v", err)
	}
	if len(subagentTools) != 0 {
		t.Fatalf("subagent should not see agent control tools, got %v", toolNames(subagentTools))
	}
}

func TestAgentControlToolSchemasDoNotReferenceSiblingTools(t *testing.T) {
	p, _, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	tools, err := p.Tools(context.Background(), SessionContext{BotID: "bot1", SessionID: "parent1"})
	if err != nil {
		t.Fatalf("Tools failed: %v", err)
	}
	for _, tool := range tools {
		raw, err := json.Marshal(tool.Parameters)
		if err != nil {
			t.Fatalf("marshal %s schema: %v", tool.Name, err)
		}
		schema := string(raw)
		if tool.Name == ToolSendMessage().String() {
			for _, absent := range []string{ToolSpawnAgent().String(), ToolListAgents().String()} {
				if strings.Contains(schema, absent) {
					t.Fatalf("%s schema references sibling tool %s:\n%s", tool.Name, absent, schema)
				}
			}
		}
	}
}

func TestSpawnAgentSessionInheritsParentUserIdentity(t *testing.T) {
	agent := &fakeSpawnAgent{}
	p, _, sessions, _ := newAgentControlProvider(t, agent)
	location := time.FixedZone("UTC+8", 8*60*60)
	session := SessionContext{
		BotID:               "bot1",
		ChatID:              "chat1",
		SessionID:           "parent1",
		UserID:              "user1",
		ChannelIdentityID:   "identity1",
		CurrentPlatform:     "telegram",
		ReplyTarget:         "chat-target",
		ConversationType:    "group",
		WorkspaceTargetID:   "workspace-1",
		WorkspaceTargetKind: "remote",
		WorkspaceTargetName: "Build machine",
		TimezoneLocation:    location,
		Skills: map[string]SkillDetail{
			"review": {Description: "Review code", Content: "instructions", Path: "/skills/review"},
		},
	}

	res := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{"id": "worker", "task": "alpha"}))
	rec, ok := sessions.byAgent("parent1", "worker")
	if !ok {
		t.Fatalf("expected child session for worker, result=%v", res)
	}
	if rec.CreatedByUserID != "user1" {
		t.Fatalf("expected child session creator to inherit parent user, got %q", rec.CreatedByUserID)
	}
	call, ok := agent.callAt(0)
	if !ok {
		t.Fatal("expected subagent call")
	}
	if call.Identity.UserID != "user1" || call.Identity.ReplyTarget != "chat-target" || call.Identity.WorkspaceTargetID != "workspace-1" {
		t.Fatalf("subagent identity was not fully inherited: %+v", call.Identity)
	}
	if call.Identity.TimezoneLocation != location || call.Skills["review"].Path != "/skills/review" {
		t.Fatalf("subagent timezone or skills were not inherited: identity=%+v skills=%+v", call.Identity, call.Skills)
	}
}

func TestSpawnAgentPropagatesContextBudgetAndToolExchangePolicy(t *testing.T) {
	agent := &fakeSpawnAgent{}
	p, _, _, _ := newAgentControlProvider(t, agent)
	policy := &contextfrag.ToolExchangePolicy{MinMessages: 10}
	session := SessionContext{
		BotID:                     "bot1",
		SessionID:                 "parent1",
		ContextBudgetMaxTokens:    128000,
		ContextToolExchangePolicy: policy,
	}

	mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{"task": "alpha"})

	call, ok := agent.callAt(0)
	if !ok {
		t.Fatal("expected spawn_agent call")
	}
	if call.ContextBudgetMaxTokens != 128000 {
		t.Fatalf("ContextBudgetMaxTokens = %d, want 128000", call.ContextBudgetMaxTokens)
	}
	if call.ContextToolExchangePolicy != policy {
		t.Fatalf("ContextToolExchangePolicy = %p, want same pointer %p", call.ContextToolExchangePolicy, policy)
	}
}

func TestSpawnAgentUsesResolvedModelContextBudgetOverParent(t *testing.T) {
	agent := &fakeSpawnAgent{}
	p, _, _, _ := newAgentControlProvider(t, agent)
	p.modelResolver = func(context.Context, SessionContext, string, string, string) (resolvedSubagentModel, error) {
		return resolvedSubagentModel{Model: &sdk.Model{}, ModelID: "model-2", ContextBudgetMaxTokens: 64000}, nil
	}
	session := SessionContext{
		BotID:                  "bot1",
		SessionID:              "parent1",
		ContextBudgetMaxTokens: 128000,
	}

	mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{"task": "alpha"})

	call, ok := agent.callAt(0)
	if !ok {
		t.Fatal("expected spawn_agent call")
	}
	if call.ContextBudgetMaxTokens != 64000 {
		t.Fatalf("ContextBudgetMaxTokens = %d, want 64000 (the resolved subagent model's own context window, not the parent's 128000)", call.ContextBudgetMaxTokens)
	}
}

func TestSpawnAgentIDsAndDuplicateValidation(t *testing.T) {
	p, _, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	res := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{"task": "alpha"}))
	if res["agent_id"] != "agent_1" || res["status"] != "completed" {
		t.Fatalf("unexpected auto id result: %v", res)
	}

	res = asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{"id": " Research_One ", "task": "beta"}))
	if res["agent_id"] != "research_one" {
		t.Fatalf("expected normalized custom id, got %v", res)
	}

	if _, err := executeAgentTool(t, p, session, "spawn_agent", map[string]any{"id": "research_one", "task": "again"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected duplicate id error, got %v", err)
	} else if strings.Contains(err.Error(), ToolSendMessage().String()) {
		t.Fatalf("duplicate id error should not name sibling tools that may be unavailable, got %v", err)
	}
	if _, err := executeAgentTool(t, p, session, "spawn_agent", map[string]any{"id": "1bad", "task": "bad"}); err == nil || !strings.Contains(err.Error(), "invalid agent id") {
		t.Fatalf("expected invalid id error, got %v", err)
	}
}

func mustExecuteAgentTool(t *testing.T, p *SpawnProvider, session SessionContext, name string, args map[string]any) any {
	t.Helper()
	res, err := executeAgentTool(t, p, session, name, args)
	if err != nil {
		t.Fatalf("%s failed: %v", name, err)
	}
	return res
}

func TestSendMessageReusesSessionAndHistory(t *testing.T) {
	agent := &fakeSpawnAgent{}
	p, _, sessions, messages := newAgentControlProvider(t, agent)
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	first := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{"id": "worker", "task": "first"}))
	second := asMap(t, mustExecuteAgentTool(t, p, session, "send_message", map[string]any{"id": "worker", "message": "second"}))
	if first["session_id"] == "" || first["session_id"] != second["session_id"] {
		t.Fatalf("expected send_message to reuse child session, first=%v second=%v", first, second)
	}

	call, ok := agent.callAt(1)
	if !ok {
		t.Fatal("expected second agent call")
	}
	if call.Identity.SessionID != first["session_id"] {
		t.Fatalf("expected reused identity session, got %q want %q", call.Identity.SessionID, first["session_id"])
	}
	if len(call.Messages) < 2 {
		t.Fatalf("expected persisted history loaded into second call, got %d messages", len(call.Messages))
	}
	rec, ok := sessions.byAgent("parent1", "worker")
	if !ok || rec.Metadata["agent_control_version"] != agentControlVersion {
		t.Fatalf("expected persisted agent metadata, got %+v", rec)
	}
	stored, _ := messages.ListBySession(context.Background(), first["session_id"].(string))
	if len(stored) != 4 {
		raw, _ := json.Marshal(stored)
		t.Fatalf("expected two user+assistant turns persisted, got %d: %s", len(stored), raw)
	}
}

func TestSpawnAgentRecordsPinnedModelOnSessionMetadata(t *testing.T) {
	agent := &fakeSpawnAgent{}
	p, _, sessions, _ := newAgentControlProvider(t, agent)
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{"id": "worker", "task": "first"})

	rec, ok := sessions.byAgent("parent1", "worker")
	if !ok {
		t.Fatal("spawned session not recorded")
	}
	// A client that lets a human talk to this subagent directly reads the model
	// off the session; without it the composer would offer the parent bot's
	// default and silently move the agent onto another model.
	if rec.Metadata["model_uuid"] != "00000000-0000-0000-0000-000000000123" {
		t.Fatalf("session metadata lost the pinned model uuid: %+v", rec.Metadata)
	}
	if rec.Metadata["model_id"] != "test-model" || rec.Metadata["model_provider"] != "test-provider" {
		t.Fatalf("session metadata lost the pinned model identity: %+v", rec.Metadata)
	}
}

func TestForkedSubagentKeepsInvisibleParentSnapshotAcrossFollowUps(t *testing.T) {
	agent := &fakeSpawnAgent{}
	p, _, _, messages := newAgentControlProvider(t, agent)
	parentMessages := []sdk.Message{
		sdk.UserMessage("parent question"),
		sdk.AssistantMessage("parent working context"),
	}
	session := SessionContext{
		BotID:       "bot1",
		SessionID:   "parent1",
		ForkContext: NewMessageSnapshot(parentMessages),
	}

	first := asMap(t, mustExecuteAgentTool(t, p, session, ToolSpawnAgent().String(), map[string]any{
		"id":   "worker",
		"task": "first child task",
		"fork": true,
	}))
	if first["fork"] != true {
		t.Fatalf("expected fork metadata in result, got %v", first)
	}
	firstCall, ok := agent.callAt(0)
	if !ok || !reflect.DeepEqual(firstCall.Messages, parentMessages) {
		t.Fatalf("expected only invisible parent prefix before first child query, got %+v", firstCall.Messages)
	}
	stored, _ := messages.ListBySession(context.Background(), first["session_id"].(string))
	if len(stored) != 2 {
		t.Fatalf("fork prefix must not be copied into visible child history, got %d stored messages", len(stored))
	}

	mustExecuteAgentTool(t, p, session, ToolSendMessage().String(), map[string]any{
		"id":      "worker",
		"message": "second child task",
	})
	secondCall, ok := agent.callAt(1)
	if !ok || len(secondCall.Messages) != 4 {
		t.Fatalf("expected parent prefix plus first child turn, got %+v", secondCall.Messages)
	}
	if !reflect.DeepEqual(secondCall.Messages[:2], parentMessages) {
		t.Fatalf("follow-up lost immutable parent prefix: %+v", secondCall.Messages)
	}
}

func TestNonForkedSubagentDoesNotInheritAvailableParentSnapshot(t *testing.T) {
	agent := &fakeSpawnAgent{}
	p, _, _, _ := newAgentControlProvider(t, agent)
	session := SessionContext{
		BotID:       "bot1",
		SessionID:   "parent1",
		ForkContext: NewMessageSnapshot([]sdk.Message{sdk.UserMessage("parent-only")}),
	}

	mustExecuteAgentTool(t, p, session, ToolSpawnAgent().String(), map[string]any{
		"id":   "worker",
		"task": "isolated child task",
	})
	call, ok := agent.callAt(0)
	if !ok || len(call.Messages) != 0 {
		t.Fatalf("fork=false should not inherit parent context, got %+v", call.Messages)
	}
}

func TestSpawnAgentPersistsLifecycleOnFinalAssistantAndAssociatesItsID(t *testing.T) {
	snapshot := &contextfrag.LifecycleSnapshot{
		Version: 1,
		Counts:  contextfrag.ManifestCounts{Fragments: 3, Messages: 2},
	}
	admitter := &fakeSubagentAdmitter{}
	p, _, sessions, messages := newAgentControlProviderWithAdmitter(
		t,
		&fakeSpawnAgent{contextLifecycle: snapshot},
		admitter,
	)

	mustExecuteAgentTool(t, p, SessionContext{BotID: "bot1", SessionID: "parent1"}, "spawn_agent", map[string]any{
		"id":   "worker",
		"task": "audit context",
	})
	child, ok := sessions.byAgent("parent1", "worker")
	if !ok {
		t.Fatal("expected child session")
	}
	stored, err := messages.ListBySession(context.Background(), child.ID)
	if err != nil {
		t.Fatalf("ListBySession error: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("persisted messages = %d, want user and assistant", len(stored))
	}
	metadataSnapshot, ok := stored[1].Metadata[contextfrag.MetadataContextLifecycleKey].(contextfrag.LifecycleSnapshot)
	if !ok || metadataSnapshot.Counts.Fragments != snapshot.Counts.Fragments {
		t.Fatalf("assistant metadata lifecycle = %#v, want content-light snapshot", stored[1].Metadata)
	}
	terminals := admitter.terminals()
	if len(terminals) != 1 || terminals[0].contextLifecycle == nil {
		t.Fatalf("terminal records = %#v, want one lifecycle snapshot", terminals)
	}
	if got := terminals[0].contextLifecycle.AssistantMessageID; got != stored[1].ID {
		t.Fatalf("assistant message association = %q, want %q", got, stored[1].ID)
	}
}

func TestBusyAgentQueuesAndRunsFIFO(t *testing.T) {
	block := make(chan struct{})
	agent := &fakeSpawnAgent{block: block}
	p, mgr, _, _ := newAgentControlProvider(t, agent)
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	first := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":                "worker",
		"task":              "first",
		"run_in_background": true,
	}))
	if first["status"] != "background_started" {
		t.Fatalf("expected background_started, got %v", first)
	}
	if msg, _ := first["message"].(string); !strings.Contains(msg, "wait_until") || !strings.Contains(msg, "get_background_status") {
		t.Fatalf("background start message should guide wait/status flow, got %q", msg)
	}
	second := asMap(t, mustExecuteAgentTool(t, p, session, "send_message", map[string]any{"id": "worker", "message": "second"}))
	third := asMap(t, mustExecuteAgentTool(t, p, session, "send_message", map[string]any{"id": "worker", "message": "third"}))
	if second["status"] != "queued" || second["queue_position"] != 1 {
		t.Fatalf("expected second queued at position 1, got %v", second)
	}
	if third["status"] != "queued" || third["queue_position"] != 2 {
		t.Fatalf("expected third queued at position 2, got %v", third)
	}

	close(block)
	waitUntil(t, 2*time.Second, func() bool {
		return reflect.DeepEqual(agent.queries(), []string{"first", "second", "third"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, _, err := mgr.WaitForSessionTask(ctx, session.BotID, session.SessionID, third["task_id"].(string), 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if snap.Status != background.TaskCompleted {
		t.Fatalf("expected third task completed, got %+v", snap)
	}
}

func TestQueuedSpawnAgentUsesExecutionModelContextBudget(t *testing.T) {
	block := make(chan struct{})
	agent := &fakeSpawnAgent{block: block}
	p, _, _, _ := newAgentControlProvider(t, agent)
	var resolverMu sync.Mutex
	resolverBudgets := []int{128000, 128000, 128000, 64000, 32000}
	resolverCall := 0
	p.modelResolver = func(context.Context, SessionContext, string, string, string) (resolvedSubagentModel, error) {
		resolverMu.Lock()
		defer resolverMu.Unlock()
		if resolverCall >= len(resolverBudgets) {
			return resolvedSubagentModel{}, errors.New("unexpected model resolution")
		}
		budget := resolverBudgets[resolverCall]
		resolverCall++
		return resolvedSubagentModel{
			Model:                  &sdk.Model{},
			ModelID:                "model-2",
			ProviderName:           "provider-2",
			ContextBudgetMaxTokens: budget,
		}, nil
	}
	session := SessionContext{
		BotID:                  "bot1",
		SessionID:              "parent1",
		ContextBudgetMaxTokens: 256000,
	}

	mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":                "worker",
		"task":              "first",
		"run_in_background": true,
	})
	waitUntil(t, 2*time.Second, func() bool {
		return reflect.DeepEqual(agent.queries(), []string{"first"})
	})
	queued := asMap(t, mustExecuteAgentTool(t, p, session, "send_message", map[string]any{
		"id":      "worker",
		"message": "second",
	}))
	if queued["status"] != string(background.TaskQueued) {
		t.Fatalf("expected second request queued, got %v", queued)
	}

	close(block)
	waitUntil(t, 2*time.Second, func() bool {
		return reflect.DeepEqual(agent.queries(), []string{"first", "second"})
	})
	secondCall, ok := agent.callAt(1)
	if !ok {
		t.Fatal("expected queued request to execute")
	}
	if secondCall.ContextBudgetMaxTokens != 32000 {
		t.Fatalf(
			"queued ContextBudgetMaxTokens = %d, want latest execution resolution 32000",
			secondCall.ContextBudgetMaxTokens,
		)
	}
}

func TestBackgroundSpawnPersistsUserMessageBeforeAgentCompletes(t *testing.T) {
	block := make(chan struct{})
	p, _, _, messages := newAgentControlProvider(t, &fakeSpawnAgent{block: block})
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	started := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":                "worker",
		"task":              "slow visible task",
		"run_in_background": true,
	}))
	childSessionID, _ := started["session_id"].(string)
	if childSessionID == "" {
		t.Fatalf("expected child session id, got %v", started)
	}
	defer close(block)

	waitUntil(t, 200*time.Millisecond, func() bool {
		stored, _ := messages.ListBySession(context.Background(), childSessionID)
		return len(stored) == 1
	})
	stored, _ := messages.ListBySession(context.Background(), childSessionID)
	if got := stored[0].Role; got != "user" {
		t.Fatalf("expected initial persisted message role user, got %q", got)
	}
	if !strings.Contains(string(stored[0].Content), "slow visible task") {
		t.Fatalf("expected persisted user content to include task, got %s", stored[0].Content)
	}
}

func TestQueuedAgentMessageDoesNotPersistBeforeItRuns(t *testing.T) {
	block := make(chan struct{})
	agent := &fakeSpawnAgent{block: block}
	p, _, _, messages := newAgentControlProvider(t, agent)
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	started := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":                "worker",
		"task":              "repeat",
		"run_in_background": true,
	}))
	childSessionID, _ := started["session_id"].(string)
	if childSessionID == "" {
		t.Fatalf("expected child session id, got %v", started)
	}
	waitUntil(t, 200*time.Millisecond, func() bool {
		stored, _ := messages.ListBySession(context.Background(), childSessionID)
		return len(stored) == 1
	})

	queued := asMap(t, mustExecuteAgentTool(t, p, session, "send_message", map[string]any{
		"id":      "worker",
		"message": "repeat",
	}))
	if queued["status"] != string(background.TaskQueued) {
		t.Fatalf("expected queued second message, got %v", queued)
	}
	stored, _ := messages.ListBySession(context.Background(), childSessionID)
	if len(stored) != 1 {
		raw, _ := json.Marshal(stored)
		t.Fatalf("queued message should not be persisted before it runs, got %d: %s", len(stored), raw)
	}
	if !strings.Contains(string(stored[0].Content), "repeat") {
		t.Fatalf("expected only running task persisted, got %s", stored[0].Content)
	}

	close(block)
	waitUntil(t, 2*time.Second, func() bool {
		return len(agent.queries()) == 2
	})
	secondCall, ok := agent.callAt(1)
	if !ok {
		t.Fatal("expected queued message to run")
	}
	if got := len(secondCall.Messages); got != 2 {
		t.Fatalf("expected second run history to exclude current repeated query, got %d messages", got)
	}
	if secondCall.Messages[0].Role != sdk.MessageRoleUser || strings.TrimSpace(messageTextContent(secondCall.Messages[0])) != "repeat" {
		t.Fatalf("expected previous user turn first in history, got %+v", secondCall.Messages[0])
	}
	if secondCall.Messages[1].Role != sdk.MessageRoleAssistant {
		t.Fatalf("expected previous assistant turn second in history, got %+v", secondCall.Messages[1])
	}
	waitUntil(t, 2*time.Second, func() bool {
		stored, _ := messages.ListBySession(context.Background(), childSessionID)
		return len(stored) == 4
	})
}

func TestBackgroundWaitTimeoutDoesNotCancelRunningAgentTask(t *testing.T) {
	block := make(chan struct{})
	p, mgr, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{block: block})
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}
	started := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":                "worker",
		"task":              "slow",
		"run_in_background": true,
	}))

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := mgr.WaitForSessionTask(waitCtx, session.BotID, session.SessionID, started["task_id"].(string), 0); err == nil {
		t.Fatal("expected wait timeout")
	}
	if task := mgr.GetForSession("bot1", "parent1", started["task_id"].(string)); task == nil || task.Snapshot().Status != background.TaskRunning {
		t.Fatalf("wait timeout should not cancel task, got %+v", task)
	}
	close(block)
}

func TestKillBackgroundCancelsRunningAndQueuedAgentTasks(t *testing.T) {
	block := make(chan struct{})
	agent := &fakeSpawnAgent{block: block}
	admitter := &fakeSubagentAdmitter{}
	p, mgr, _, _ := newAgentControlProviderWithAdmitter(t, agent, admitter)
	session := SessionContext{BotID: "bot1", SessionID: "parent1"}

	first := asMap(t, mustExecuteAgentTool(t, p, session, "spawn_agent", map[string]any{
		"id":                "worker",
		"task":              "first",
		"run_in_background": true,
	}))
	waitUntil(t, time.Second, func() bool {
		return reflect.DeepEqual(agent.queries(), []string{"first"})
	})
	second := asMap(t, mustExecuteAgentTool(t, p, session, "send_message", map[string]any{"id": "worker", "message": "second"}))

	if err := mgr.KillForSession("bot1", "parent1", second["task_id"].(string)); err != nil {
		t.Fatalf("kill queued task failed: %v", err)
	}
	if task := mgr.Get(second["task_id"].(string)); task == nil || task.Snapshot().Status != background.TaskKilled {
		t.Fatalf("expected queued task killed, got %+v", task)
	}

	if err := mgr.KillForSession("bot1", "parent1", first["task_id"].(string)); err != nil {
		t.Fatalf("kill running task failed: %v", err)
	}
	waitUntil(t, time.Second, func() bool {
		task := mgr.Get(first["task_id"].(string))
		return task != nil && task.Snapshot().Status == background.TaskKilled
	})
	waitUntil(t, time.Second, func() bool {
		return len(admitter.terminals()) == 1
	})
	if terminals := admitter.terminals(); len(terminals) != 1 || terminals[0].cause != "" {
		t.Fatalf("killed terminal = %#v, want one cancellation-derived release", terminals)
	}
	if got := agent.queries(); !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatalf("killed queued task should not run, got queries %v", got)
	}
}

func TestListAgentsScopedByCurrentSession(t *testing.T) {
	p, _, _, _ := newAgentControlProvider(t, &fakeSpawnAgent{})
	sessionA := SessionContext{BotID: "bot1", SessionID: "parent-a"}
	sessionB := SessionContext{BotID: "bot1", SessionID: "parent-b"}

	mustExecuteAgentTool(t, p, sessionA, "spawn_agent", map[string]any{"id": "alpha", "task": "a"})
	mustExecuteAgentTool(t, p, sessionB, "spawn_agent", map[string]any{"id": "beta", "task": "b"})

	listA := asMap(t, mustExecuteAgentTool(t, p, sessionA, "list_agents", map[string]any{}))
	agents := listA["agents"].([]map[string]any)
	if len(agents) != 1 || agents[0]["agent_id"] != "alpha" {
		t.Fatalf("expected only session A agent, got %v", listA)
	}
}
