package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/event"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/grok/grokcfg"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
)

// fakeGrok answers the ACP session lifecycle and records what it was asked.
type fakeGrok struct {
	mu      sync.Mutex
	created int
	resumed []acp.ResumeSessionRequest
	closed  []acp.SessionId
	// switched holds the process's last model and effort choice, which Grok
	// opens every new session on.
	switched map[string]string
	// approve releases a sign-in, as the user confirming the device code
	// does; a nil channel ends it at once with authErr.
	approve   chan struct{}
	authErr   error
	signingIn bool
	// conn is the agent's end of the connection, for notifications.
	conn      *acp.Connection
	compacted []string
	forked    []map[string]any
	// models is what models/list answers.
	models map[string]any
}

// authenticate waits for the user outside the lock, as Grok answers other
// requests while a sign-in is under way.
func (g *fakeGrok) authenticate(ctx context.Context) (any, *acp.RequestError) {
	g.mu.Lock()
	g.signingIn = true
	approve, authErr := g.approve, g.authErr
	g.mu.Unlock()
	if approve == nil {
		return nil, &acp.RequestError{Code: -32000, Message: authErr.Error()}
	}
	select {
	case <-approve:
		return map[string]any{"_meta": map[string]any{"email": "dev@example.test", "team_id": "team-1"}}, nil
	case <-ctx.Done():
		return nil, &acp.RequestError{Code: -32800, Message: "cancelled"}
	}
}

func (g *fakeGrok) configOptions() []map[string]any {
	current := func(id, initial string) string {
		if value := g.switched[id]; value != "" {
			return value
		}
		return initial
	}
	return []map[string]any{{
		"type": "select", "id": "model", "name": "Model", "category": "model", "currentValue": current("model", "grok-a"),
		"options": []map[string]any{{"value": "grok-a", "name": "A"}, {"value": "grok-b", "name": "B"}},
	}, {
		"type": "select", "id": "effort", "name": "Effort", "category": "thought_level", "currentValue": current("effort", "low"),
		"options": []map[string]any{{"value": "low", "name": "Low"}, {"value": "high", "name": "High"}},
	}}
}

func (g *fakeGrok) handle(ctx context.Context, method string, params json.RawMessage) (any, *acp.RequestError) {
	if method == acp.AgentMethodAuthenticate {
		return g.authenticate(ctx)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch method {
	case signInURLMethod:
		// Grok names the URL only once a sign-in is under way.
		if !g.signingIn {
			return map[string]any{"auth_url": nil, "external_provider": false, "mode": nil}, nil
		}
		return map[string]any{"auth_url": "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234", "external_provider": false, "mode": "device"}, nil
	case acp.AgentMethodInitialize:
		return map[string]any{"protocolVersion": acp.ProtocolVersionNumber, "agentCapabilities": map[string]any{
			"sessionCapabilities": map[string]any{"resume": map[string]any{}, "close": map[string]any{}},
		}}, nil
	case acp.AgentMethodSessionNew:
		g.created++
		return map[string]any{"sessionId": fmt.Sprintf("new-%d", g.created), "configOptions": g.configOptions()}, nil
	case acp.AgentMethodSessionSetConfigOption:
		var request struct {
			ConfigID string `json:"configId"`
			Value    string `json:"value"`
		}
		_ = json.Unmarshal(params, &request)
		if g.switched == nil {
			g.switched = map[string]string{}
		}
		g.switched[request.ConfigID] = request.Value
		return map[string]any{"configOptions": g.configOptions()}, nil
	case acp.AgentMethodSessionResume:
		var request acp.ResumeSessionRequest
		_ = json.Unmarshal(params, &request)
		g.resumed = append(g.resumed, request)
		if request.SessionId == "gone" {
			return nil, &acp.RequestError{Code: -32603, Message: "Path not found.", Data: map[string]any{"code": "FS_NOT_FOUND"}}
		}
		return map[string]any{}, nil
	case acp.AgentMethodSessionClose:
		var request acp.CloseSessionRequest
		_ = json.Unmarshal(params, &request)
		g.closed = append(g.closed, request.SessionId)
		return map[string]any{}, nil
	case compactMethod:
		var request struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(params, &request)
		g.compacted = append(g.compacted, request.SessionID)
		return map[string]any{}, nil
	case forkMethod:
		var request map[string]any
		_ = json.Unmarshal(params, &request)
		g.forked = append(g.forked, request)
		return map[string]any{"newSessionId": "fork-1", "parentSessionId": request["sourceSessionId"]}, nil
	case modelsMethod:
		return map[string]any{"result": g.models}, nil
	case sessionInfoMethod:
		var request struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(params, &request)
		return map[string]any{"result": map[string]any{"sessionId": request.SessionID, "model": "grok-a", "context": map[string]any{"used": 1234, "total": 256000}}}, nil
	default:
		return nil, acp.NewMethodNotFound(method)
	}
}

func newTestServer(t *testing.T, grok *fakeGrok) *server {
	t.Helper()
	toAgentReader, toAgentWriter := io.Pipe()
	toClientReader, toClientWriter := io.Pipe()
	grok.conn = acp.NewConnection(grok.handle, toClientWriter, toAgentReader)
	t.Cleanup(func() {
		_ = toAgentWriter.Close()
		_ = toClientWriter.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := acpclient.Connect(ctx, struct {
		io.Reader
		io.Writer
	}{toClientReader, toAgentWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &server{conn: conn, logger: slog.Default(), threads: map[string]*thread{}, toolStatus: map[string]mcpStatus{}, statusWake: make(chan struct{})}
	srv.subscribe()
	return srv
}

func TestApplyTurnConfigSkipsAnAgentEffortTheModelLacks(t *testing.T) {
	ctx := context.Background()
	session, err := newTestServer(t, &fakeGrok{}).conn.NewSession(ctx, acpclient.SessionConfig{ProjectPath: "/data"})
	if err != nil {
		t.Fatal(err)
	}
	if got := session.ReasoningState(); len(got.Available) != 2 {
		t.Fatalf("reasoning state = %+v, want the two efforts the session offers", got)
	}
	if err := applyTurnConfig(ctx, session, nil, "", "", "xhigh"); err != nil {
		t.Fatalf("applyTurnConfig() error = %v, want the Agent-wide effort left out", err)
	}
	if err := applyTurnConfig(ctx, session, nil, "", "xhigh", ""); err == nil {
		t.Fatal("the turn's own effort must fail when the model lacks it")
	}
}

func TestApplyTurnConfigPutsWhatTheTurnLeavesUnnamedOnTheDefaults(t *testing.T) {
	ctx := context.Background()
	srv := newTestServer(t, &fakeGrok{})
	other, err := srv.conn.NewSession(ctx, acpclient.SessionConfig{ProjectPath: "/data"})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyTurnConfig(ctx, other, nil, "grok-b", "high", ""); err != nil {
		t.Fatal(err)
	}
	session, err := srv.conn.NewSession(ctx, acpclient.SessionConfig{ProjectPath: "/data"})
	if err != nil {
		t.Fatal(err)
	}
	if session.ModelState().CurrentModelID != "grok-b" {
		t.Fatalf("new session opened on %q, want the other conversation's choice it inherits", session.ModelState().CurrentModelID)
	}
	catalog := []external.ModelOption{{ID: "grok-a", Default: true, DefaultReasoningEffort: "low"}, {ID: "grok-b", DefaultReasoningEffort: "high"}}
	if err := applyTurnConfig(ctx, session, catalog, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if model, effort := session.ModelState().CurrentModelID, session.ReasoningState().CurrentEffort; model != "grok-a" || effort != "low" {
		t.Fatalf("session runs %s/%s, want the defaults grok-a/low", model, effort)
	}
}

func TestOpenThreadTreatsTheStoredSessionIDAsTheAuthority(t *testing.T) {
	grok := &fakeGrok{}
	srv := newTestServer(t, grok)
	ctx := context.Background()
	open := func(srv *server, storedID string) (*thread, bool) {
		t.Helper()
		input := external.PromptInput{ThreadID: "thread-1", Sink: external.EventSinkFunc(func(event.StreamEvent) {})}
		if storedID != "" {
			input.RuntimeMetadata = map[string]any{metadataSessionIDKey: storedID}
		}
		th, lost, err := srv.openThread(ctx, toolmount.Gateway{}, nil, nil, input, grokcfg.PermissionAsk, true)
		if err != nil {
			t.Fatalf("openThread(stored=%q) error = %v", storedID, err)
		}
		return th, lost
	}

	first, _ := open(srv, "")
	if first.session.ID() != "new-1" {
		t.Fatalf("first turn session = %q, want a new session", first.session.ID())
	}
	if again, _ := open(srv, "new-1"); again != first {
		t.Fatal("a turn whose stored id names the live session must reuse it")
	}

	// Clearing the conversation's history clears the stored id. Grok still
	// holds the old session in this process; picking it up would bring the
	// deleted history back.
	cleared, _ := open(srv, "")
	if cleared.session.ID() != "new-2" || len(grok.closed) != 1 || grok.closed[0] != "new-1" {
		t.Fatalf("after a cleared id: session %q, closed %v; want a new session and the old one closed", cleared.session.ID(), grok.closed)
	}

	// A fresh process has no live session and resumes from the stored id,
	// stating the permission mode again because Grok does not keep it.
	restarted := newTestServer(t, grok)
	resumed, lost := open(restarted, "new-2")
	last := grok.resumed[len(grok.resumed)-1]
	if resumed.session.ID() != "new-2" || lost || last.Meta["yoloMode"] != false || last.Meta["autoMode"] != false {
		t.Fatalf("resume = session %q lost=%v meta=%v", resumed.session.ID(), lost, last.Meta)
	}

	// Grok answering that the session is gone is the one case a new session
	// replaces it, and the caller is told so.
	replaced, lost := open(newTestServer(t, grok), "gone")
	if !lost || replaced.session.ID() != "new-3" {
		t.Fatalf("refused resume = session %q lost=%v, want a new session reported as lost", replaced.session.ID(), lost)
	}
}
