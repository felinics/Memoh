package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/agent/turn"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/messageconv"
)

// fakeSubagentThreadService extends the background fake with the subagent
// thread surface the direct-chat path probes for.
type fakeSubagentThreadService struct {
	fakeBackgroundSessionService
	config      session.SubagentConfig
	configErr   error
	forkContext []session.SubagentForkContextMessage
}

func (f *fakeSubagentThreadService) GetSubagentConfig(context.Context, string) (session.SubagentConfig, error) {
	return f.config, f.configErr
}

func (f *fakeSubagentThreadService) ListSubagentForkContext(context.Context, string) ([]session.SubagentForkContextMessage, error) {
	return f.forkContext, nil
}

func TestApplySubagentThreadDefaults(t *testing.T) {
	svc := &fakeSubagentThreadService{
		fakeBackgroundSessionService: fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				if sessionID == "sub-1" {
					return session.Thread{ID: sessionID, Type: session.TypeSubagent}, nil
				}
				return session.Thread{ID: sessionID, Type: session.TypeChat}, nil
			},
		},
		config: session.SubagentConfig{ModelUUID: "model-uuid-1", ModelID: "gpt-x", ProviderName: "prov"},
	}
	s := &Service{sessionService: svc}

	got := s.applySubagentThreadDefaults(context.Background(), ChatRequest{ThreadID: "sub-1"}, false)
	if got.SessionType != session.TypeSubagent {
		t.Fatalf("session type = %q, want subagent", got.SessionType)
	}
	if !got.SkipMemoryExtraction || !got.SkipTitleGeneration {
		t.Fatal("subagent direct turn must skip memory extraction and title generation")
	}
	if got.Model != "model-uuid-1" {
		t.Fatalf("model = %q, want pinned model uuid", got.Model)
	}

	// An explicit model choice from the request wins over the pinned config.
	got = s.applySubagentThreadDefaults(context.Background(), ChatRequest{ThreadID: "sub-1", Model: "user-choice"}, false)
	if got.Model != "user-choice" {
		t.Fatalf("model = %q, want the request's own choice", got.Model)
	}

	// Non-subagent threads pass through untouched.
	got = s.applySubagentThreadDefaults(context.Background(), ChatRequest{ThreadID: "chat-1"}, false)
	if got.SessionType != "" || got.Model != "" || got.SkipMemoryExtraction {
		t.Fatalf("chat thread was rewritten: %+v", got)
	}

	// A turn that already carries a session type is never rewritten.
	got = s.applySubagentThreadDefaults(context.Background(), ChatRequest{ThreadID: "sub-1", SessionType: "schedule"}, false)
	if got.SessionType != "schedule" {
		t.Fatalf("session type = %q, want schedule preserved", got.SessionType)
	}

	// A session with a remembered pair keeps it: the pin is only the initial
	// default (issue #879), so hasMemory suppresses the pin fill — but the
	// subagent execution surface still applies.
	got = s.applySubagentThreadDefaults(context.Background(), ChatRequest{ThreadID: "sub-1"}, true)
	if got.SessionType != session.TypeSubagent || !got.SkipMemoryExtraction || !got.SkipTitleGeneration {
		t.Fatalf("subagent surface lost with memory: %+v", got)
	}
	if got.Model != "" {
		t.Fatalf("model = %q, want pin suppressed by session memory", got.Model)
	}
}

// The defaults are worthless if they stay inside resolve: Chat and StreamChat
// keep their own copy of the request for title generation, the step committer
// and terminal persistence, so resolve has to hand the effective request back.
func TestResolveReturnsTheEffectiveSubagentRequest(t *testing.T) {
	svc := &fakeSubagentThreadService{
		fakeBackgroundSessionService: fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (session.Thread, error) {
				return session.Thread{ID: sessionID, Type: session.TypeSubagent}, nil
			},
		},
		config: session.SubagentConfig{ModelUUID: "model-uuid-1", ModelID: "gpt-x", ProviderName: "prov"},
	}
	s := &Service{sessionService: svc, logger: slog.New(slog.DiscardHandler)}

	// Resolution fails at model selection (no settings service), which is the
	// point: even the failure path must not hand back a request that has lost
	// the subagent defaults.
	_, effective, err := s.resolve(context.Background(), ChatRequest{
		BotID: "bot-1", ChatID: "chat-1", ThreadID: "sub-1", Query: "hello",
	})
	if err == nil {
		t.Fatal("resolve unexpectedly succeeded without a settings service")
	}
	if effective.SessionType != session.TypeSubagent {
		t.Fatalf("session type = %q, want subagent", effective.SessionType)
	}
	if !effective.SkipMemoryExtraction || !effective.SkipTitleGeneration {
		t.Fatalf("skip flags lost on the way out of resolve: %+v", effective)
	}
	if effective.Model != "model-uuid-1" {
		t.Fatalf("model = %q, want the pinned model uuid", effective.Model)
	}
}

func TestApplySubagentThreadDefaultsSurvivesLookupFailure(t *testing.T) {
	svc := &fakeSubagentThreadService{
		fakeBackgroundSessionService: fakeBackgroundSessionService{
			getFn: func(context.Context, string) (session.Thread, error) {
				return session.Thread{}, errors.New("db down")
			},
		},
	}
	s := &Service{sessionService: svc}
	got := s.applySubagentThreadDefaults(context.Background(), ChatRequest{ThreadID: "sub-1"}, false)
	if got.SessionType != "" {
		t.Fatalf("lookup failure must leave the request untouched, got type %q", got.SessionType)
	}
}

func TestSubagentForkContextModelMessages(t *testing.T) {
	forkMsg, _ := json.Marshal(map[string]any{
		"role":    "user",
		"content": []map[string]any{{"type": "text", "text": "parent context"}},
	})
	// Fork rows are written in the stored shape: argument objects and
	// nested annotations, never SDK JSON.
	storedCall := json.RawMessage(`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"call-1","toolName":"exec","input":{"command":"ls && pwd"},"providerMetadata":{"approval":{"approval_id":"a1","status":"approved"}}}]}`)
	svc := &fakeSubagentThreadService{
		forkContext: []session.SubagentForkContextMessage{
			{Role: "user", Message: forkMsg},
			{Role: "user", Message: json.RawMessage(`not json`)},
			{Role: "assistant", Message: storedCall},
		},
	}
	s := &Service{sessionService: svc}

	messages := s.subagentForkContextModelMessages(context.Background(), ChatRequest{
		ThreadID: "sub-1", SessionType: session.TypeSubagent,
	})
	if len(messages) != 2 {
		t.Fatalf("got %d fork messages, want 2 (invalid row skipped)", len(messages))
	}
	if messages[0].Role != "user" {
		t.Fatalf("fork message role = %q, want user", messages[0].Role)
	}
	call, ok := messageconv.ModelMessageToSDKMessage(turn.ModelMessage{Role: messages[1].Role, Content: messages[1].Content}).Content[0].(sdk.ToolCallPart)
	if !ok {
		t.Fatalf("fork tool-call row = %s, want a typed tool call", messages[1].Content)
	}
	if args, _ := toolexec.ArgumentsValue(call.Input).(map[string]any); args["command"] != "ls && pwd" {
		t.Fatalf("fork tool call input = %#v", toolexec.ArgumentsValue(call.Input))
	}
	if approval, ok := partmeta.Object(call.ProviderMetadata, partmeta.KeyApproval); !ok || approval["status"] != "approved" {
		t.Fatalf("fork approval annotation = %#v", call.ProviderMetadata)
	}

	// Non-subagent turns never load fork context.
	if got := s.subagentForkContextModelMessages(context.Background(), ChatRequest{ThreadID: "sub-1"}); got != nil {
		t.Fatalf("fork context loaded for a plain chat turn: %v", got)
	}
}
