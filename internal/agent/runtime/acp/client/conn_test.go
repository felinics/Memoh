package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
)

// failAfterOutputPrompt makes the agent send three chunks and then fail the
// prompt.
const failAfterOutputPrompt = "fail-after-output"

// hangUpAfterOutputPrompt makes the agent send hangUpChunks chunks, answer
// the prompt, and close the connection right after.
const (
	hangUpAfterOutputPrompt = "hang-up-after-output"
	hangUpChunks            = 200
)

// multiSessionAgent is an ACP agent that hosts several sessions on one
// connection and answers each prompt with a chunk naming its session.
type multiSessionAgent struct {
	conn   *acp.Connection
	hangUp func()

	mu      sync.Mutex
	next    int
	resumed []acp.ResumeSessionRequest
	closed  []acp.SessionId
}

func startMultiSessionAgent(t *testing.T) (*multiSessionAgent, io.ReadWriter) {
	t.Helper()
	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	agent := &multiSessionAgent{hangUp: func() { _ = agentToClientWriter.Close() }}
	agent.conn = acp.NewConnection(agent.handle, agentToClientWriter, clientToAgentReader)
	t.Cleanup(func() {
		_ = clientToAgentWriter.Close()
		_ = agentToClientWriter.Close()
	})
	return agent, struct {
		io.Reader
		io.Writer
	}{agentToClientReader, clientToAgentWriter}
}

func (a *multiSessionAgent) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	switch method {
	case acp.AgentMethodInitialize:
		return map[string]any{
			"protocolVersion": acp.ProtocolVersionNumber,
			"agentCapabilities": map[string]any{
				"sessionCapabilities": map[string]any{"resume": map[string]any{}, "close": map[string]any{}},
			},
		}, nil
	case acp.AgentMethodSessionNew:
		a.mu.Lock()
		a.next++
		id := acp.SessionId(fmt.Sprintf("session-%d", a.next))
		a.mu.Unlock()
		// Agents announce commands before session/new returns the ID.
		a.notify(ctx, id, map[string]any{
			"sessionUpdate":     "available_commands_update",
			"availableCommands": []map[string]any{{"name": "cmd-" + string(id), "description": "d"}},
		})
		return map[string]any{"sessionId": id}, nil
	case acp.AgentMethodSessionResume:
		var request acp.ResumeSessionRequest
		_ = json.Unmarshal(params, &request)
		a.mu.Lock()
		a.resumed = append(a.resumed, request)
		a.mu.Unlock()
		return map[string]any{}, nil
	case acp.AgentMethodSessionPrompt:
		var request acp.PromptRequest
		_ = json.Unmarshal(params, &request)
		if strings.Contains(string(params), failAfterOutputPrompt) {
			for _, text := range []string{"one ", "two ", "three"} {
				a.notify(ctx, request.SessionId, map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": text},
				})
			}
			return nil, acp.NewInternalError("upstream failed")
		}
		if strings.Contains(string(params), hangUpAfterOutputPrompt) {
			for range hangUpChunks {
				a.notify(ctx, request.SessionId, map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": "x"},
				})
			}
			// The response goes out as this handler returns.
			time.AfterFunc(20*time.Millisecond, a.hangUp)
			return map[string]any{"stopReason": "end_turn"}, nil
		}
		a.notify(ctx, request.SessionId, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": "from " + string(request.SessionId)},
		})
		return map[string]any{"stopReason": "end_turn", "_meta": map[string]any{"session": string(request.SessionId)}}, nil
	case acp.AgentMethodSessionClose:
		var request acp.CloseSessionRequest
		_ = json.Unmarshal(params, &request)
		a.mu.Lock()
		a.closed = append(a.closed, request.SessionId)
		a.mu.Unlock()
		return map[string]any{}, nil
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func (a *multiSessionAgent) notify(ctx context.Context, id acp.SessionId, update map[string]any) {
	_ = a.conn.SendNotification(ctx, acp.ClientMethodSessionUpdate, map[string]any{"sessionId": id, "update": update})
}

func TestConnKeepsSessionsOfOneProcessApart(t *testing.T) {
	agent, transport := startMultiSessionAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	first, err := conn.NewSession(ctx, SessionConfig{ProjectPath: "/data"})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	second, err := conn.NewSession(ctx, SessionConfig{ProjectPath: "/data"})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	for _, session := range []*Session{first, second} {
		if !session.AdvertisesCommand("cmd-" + session.ID()) {
			t.Fatalf("session %s lost the commands announced before its ID was known", session.ID())
		}
	}

	results := make([]PromptResult, 2)
	var wg sync.WaitGroup
	for i, session := range []*Session{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, promptErr := session.Prompt(ctx, "hi")
			if promptErr != nil {
				t.Errorf("Prompt(%s) error = %v", session.ID(), promptErr)
			}
			results[i] = result
		}()
	}
	wg.Wait()
	for i, session := range []*Session{first, second} {
		if want := "from " + session.ID(); results[i].Text != want {
			t.Fatalf("session %s text = %q, want %q", session.ID(), results[i].Text, want)
		}
		if results[i].Meta["session"] != session.ID() {
			t.Fatalf("session %s meta = %v, want its own prompt response _meta", session.ID(), results[i].Meta)
		}
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	result, err := second.Prompt(ctx, "again")
	if err != nil || result.Text != "from "+second.ID() {
		t.Fatalf("sibling prompt after close = %q, %v; closing one session must not end the connection", result.Text, err)
	}

	resumed, err := conn.ResumeSession(ctx, first.ID(), SessionConfig{ProjectPath: "/data", Meta: map[string]any{"mode": "ask"}})
	if err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	result, err = resumed.Prompt(ctx, "back")
	if err != nil || result.Text != "from "+first.ID() {
		t.Fatalf("resumed prompt = %q, %v", result.Text, err)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.closed) != 1 || agent.closed[0] != acp.SessionId(first.ID()) {
		t.Fatalf("closed sessions = %v, want only %s", agent.closed, first.ID())
	}
	if len(agent.resumed) != 1 || agent.resumed[0].Meta["mode"] != "ask" || agent.resumed[0].Cwd != "/data" {
		t.Fatalf("resume requests = %#v, want the session's cwd and _meta", agent.resumed)
	}
}

func TestConnFailedPromptKeepsTheOutputSentBeforeTheError(t *testing.T) {
	_, transport := startMultiSessionAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	session, err := conn.NewSession(ctx, SessionConfig{ProjectPath: "/data"})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	result, err := session.Prompt(ctx, failAfterOutputPrompt)
	if err == nil {
		t.Fatal("Prompt() error = nil, want the agent's failure")
	}
	if result.Text != "one two three" {
		t.Fatalf("text of the failed prompt = %q, want everything sent before the error", result.Text)
	}
}

func TestConnPromptKeepsItsOutputWhenTheAgentHangsUpRightAfter(t *testing.T) {
	_, transport := startMultiSessionAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	session, err := conn.NewSession(ctx, SessionConfig{ProjectPath: "/data"})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	delivered := 0
	result, err := session.Prompt(ctx, hangUpAfterOutputPrompt, EventSinkFunc(func(event.StreamEvent) { delivered++ }))
	if err != nil {
		t.Fatalf("Prompt() error = %v, want the turn the agent answered", err)
	}
	if want := strings.Repeat("x", hangUpChunks); result.Text != want || delivered != hangUpChunks {
		t.Fatalf("got %d chars and %d delivered events, want all %d", len(result.Text), delivered, hangUpChunks)
	}
}
