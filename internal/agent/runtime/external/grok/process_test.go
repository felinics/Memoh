package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpcore "github.com/felinics/memoh/internal/agent/runtime/external/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external/grok/grokcfg"
)

// fakeGrok answers what the driver asks Grok and records it.
type fakeGrok struct {
	mu      sync.Mutex
	created int
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
		// A refused authentication never produces a device URL. Advertising
		// one races the authenticate error response and makes refusal tests flaky.
		if !g.signingIn || g.authErr != nil {
			return map[string]any{"auth_url": nil, "external_provider": false, "mode": nil}, nil
		}
		return map[string]any{"auth_url": "https://accounts.x.ai/oauth2/device?user_code=ABCD-1234", "external_provider": false, "mode": "device"}, nil
	case acp.AgentMethodInitialize:
		return map[string]any{"protocolVersion": acp.ProtocolVersionNumber}, nil
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

func newTestProcess(t *testing.T, grok *fakeGrok) *grokProcess {
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
	return newGrokProcess(conn)
}

func TestApplyTurnConfigSkipsAnAgentEffortTheModelLacks(t *testing.T) {
	ctx := context.Background()
	session, err := newTestProcess(t, &fakeGrok{}).conn.NewSession(ctx, acpclient.SessionConfig{ProjectPath: "/data"})
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
	srv := newTestProcess(t, &fakeGrok{})
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

// Grok keeps neither the permission mode nor which session a conversation
// continues: the mode is stated on every open, a change to it opens the
// session again, and the stored id names the session to continue.
func TestSessionStatesWhatGrokDoesNotKeep(t *testing.T) {
	spec := func(mode, stored string) acpcore.SessionSpec {
		t.Helper()
		input := acpcore.PromptInput{PermissionMode: mode, RuntimeMetadata: map[string]any{}}
		if stored != "" {
			input.RuntimeMetadata[metadataSessionIDKey] = stored
		}
		got, err := (&Driver{}).Session(context.Background(), nil, input)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if spec(grokcfg.PermissionAsk, "").Variant == spec(grokcfg.PermissionAuto, "").Variant {
		t.Fatal("a session opened in another permission mode is reused")
	}
	if got := spec(grokcfg.PermissionAsk, "s1").Stored; got != (acpcore.StoredSession{ID: "s1", Owned: true, Known: true}) {
		t.Fatalf("stored session = %+v", got)
	}
	if got := spec(grokcfg.PermissionAsk, "").Stored; got != (acpcore.StoredSession{Known: true}) {
		t.Fatalf("a conversation without a stored id = %+v, want a known empty record", got)
	}
	for mode, want := range map[string][2]bool{
		grokcfg.PermissionAsk:           {false, false},
		grokcfg.PermissionAuto:          {false, true},
		grokcfg.PermissionAlwaysApprove: {true, false},
	} {
		meta := sessionMeta(mode)
		if meta["yoloMode"] != want[0] || meta["autoMode"] != want[1] {
			t.Fatalf("sessionMeta(%s) = %v", mode, meta)
		}
	}
}
