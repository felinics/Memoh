package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	approval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/mcp"
)

type capabilityTestConnections struct {
	CapabilityConnections
	conn    mcp.Connection
	creates int
	update  mcp.UpsertRequest
	failure error
}

func (*capabilityTestConnections) Delete(context.Context, string, string) error { return nil }

func (f *capabilityTestConnections) UpdateProbeResult(_ context.Context, _, _ string, status string, tools []mcp.ToolDescriptor, _ string) error {
	f.conn.Status = status
	f.conn.ToolsCache = tools
	return nil
}

func (f *capabilityTestConnections) Get(_ context.Context, bot, id string) (mcp.Connection, error) {
	if bot != f.conn.BotID || id != f.conn.ID {
		return mcp.Connection{}, errors.New("wrong bot or connection")
	}
	return f.conn, f.failure
}

func (f *capabilityTestConnections) Create(_ context.Context, bot string, req mcp.UpsertRequest) (mcp.Connection, error) {
	f.creates++
	return mcp.Connection{ID: "new", BotID: bot, Name: req.Name, Config: map[string]any{}}, f.failure
}

func (f *capabilityTestConnections) Update(_ context.Context, _, _ string, req mcp.UpsertRequest) (mcp.Connection, error) {
	f.update = req
	return f.conn, f.failure
}

func (f *capabilityTestConnections) ListByBot(context.Context, string) ([]mcp.Connection, error) {
	return []mcp.Connection{f.conn}, f.failure
}

type capabilityTestOAuth struct {
	CapabilityOAuth
	status mcp.OAuthStatus
}

func (capabilityTestOAuth) Discover(context.Context, string) (*mcp.DiscoveryResult, error) {
	return &mcp.DiscoveryResult{}, nil
}

func (capabilityTestOAuth) SaveDiscovery(context.Context, string, *mcp.DiscoveryResult) error {
	return nil
}

func (capabilityTestOAuth) StartAuthorization(context.Context, string, string, string, string) (*mcp.AuthorizeResult, error) {
	return &mcp.AuthorizeResult{AuthorizationURL: "https://example.test/authorize"}, nil
}

func (f capabilityTestOAuth) GetStatus(context.Context, string) (*mcp.OAuthStatus, error) {
	return &f.status, nil
}

type capabilityTestApproval struct {
	approval.FlowService
	beforeDecision func()
	reviews        int
	input          approval.CreatePendingInput
	decision       string
}

func (*capabilityTestApproval) EvaluatePolicy(context.Context, approval.CreatePendingInput) (approval.Evaluation, error) {
	return approval.Evaluation{Decision: approval.DecisionNeedsApproval}, nil
}

func (f *capabilityTestApproval) CreatePending(_ context.Context, input approval.CreatePendingInput) (approval.Request, error) {
	f.reviews++
	f.input = input
	return approval.Request{ID: "approval", Status: approval.StatusPending}, nil
}

func (f *capabilityTestApproval) WaitForDecision(context.Context, string) (approval.Request, error) {
	if f.beforeDecision != nil {
		f.beforeDecision()
	}
	status := f.decision
	if status == "" {
		status = approval.StatusApproved
	}
	return approval.Request{ID: "approval", Status: status}, nil
}
func (*capabilityTestApproval) RegisterWaiter(string) func() { return func() {} }
func (*capabilityTestApproval) Reject(context.Context, string, string, string) (approval.Request, error) {
	return approval.Request{ID: "approval", Status: approval.StatusRejected}, nil
}

func capabilityFixture(t *testing.T) (*CapabilityProvider, *capabilityTestConnections, *capabilityTestApproval, SessionContext) {
	t.Helper()
	connections := &capabilityTestConnections{conn: mcp.Connection{ID: "connection", BotID: "bot", Name: "Test", Type: "http", Active: true, Config: map[string]any{"url": "https://example.test/mcp", "headers": map[string]any{"Authorization": "SECRET"}}}}
	review := &capabilityTestApproval{}
	p := NewCapabilityProvider(nil, CapabilityOptions{Connections: connections, OAuth: capabilityTestOAuth{}, Approval: review, Probe: func(context.Context, string, mcp.Connection) ([]mcp.ToolDescriptor, error) { return nil, nil }, Access: func(_ context.Context, identity, bot string, _ bool) error {
		if identity != "actor" || bot != "bot" {
			return errors.New("denied")
		}
		return nil
	}})
	session := SessionContext{BotID: "bot", ChannelIdentityID: "actor", SessionID: "session", SessionType: "chat", CanRequestUserInput: true, Emitter: func(ToolStreamEvent) {}}
	return p, connections, review, session
}

func callCapability(t *testing.T, p *CapabilityProvider, session SessionContext, args map[string]any) any {
	t.Helper()
	tools, err := p.Tools(t.Context(), session)
	if err != nil || len(tools) == 0 {
		t.Fatalf("tools: %v", err)
	}
	result, err := tools[0].Execute(&sdk.ToolExecContext{Context: t.Context(), ToolCallID: "call"}, args)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertCapabilityCode(t *testing.T, result any, code apperror.Code) {
	t.Helper()
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), string(code)) {
		t.Fatalf("result = %s, want %s", data, code)
	}
}

func assertCapabilityMessage(t *testing.T, result any) {
	t.Helper()
	data, ok := result.(map[string]any)
	if !ok || strings.TrimSpace(StringArg(data, "message")) == "" {
		t.Fatalf("result has no actionable message: %#v", result)
	}
}

func TestCapabilityManagementRechecksPermissionAfterApproval(t *testing.T) {
	p, connections, review, session := capabilityFixture(t)
	allowed := true
	p.opts.Access = func(context.Context, string, string, bool) error {
		if !allowed {
			return errors.New("revoked")
		}
		return nil
	}
	review.beforeDecision = func() { allowed = false }
	result := callCapability(t, p, session, map[string]any{"action": "create", "name": "New", "url": "https://example.test/mcp"})
	assertCapabilityCode(t, result, apperror.CodeCapabilityAccessDenied)
	assertCapabilityMessage(t, result)
	if connections.creates != 0 || review.reviews != 1 {
		t.Fatalf("created=%d reviews=%d", connections.creates, review.reviews)
	}
}

func TestCapabilityManagementRejectsUntrustedFieldsBeforeReview(t *testing.T) {
	for _, field := range []string{"bot_id", "headers", "env", "revision", "target_id"} {
		t.Run(field, func(t *testing.T) {
			p, connections, review, session := capabilityFixture(t)
			result := callCapability(t, p, session, map[string]any{"action": "create", "name": "New", "url": "https://example.test/mcp", field: "SECRET"})
			assertCapabilityCode(t, result, apperror.CodeCapabilityRequestInvalid)
			if connections.creates != 0 || review.reviews != 0 {
				t.Fatal("invalid input reached management")
			}
		})
	}
}

func TestCapabilityManagementDoesNotExposeSecrets(t *testing.T) {
	p, connections, review, session := capabilityFixture(t)
	connections.conn.Config["url"] = "https://user:SECRET@example.test/mcp?key=SECRET"
	result := callCapability(t, p, session, map[string]any{"action": "get", "connection_id": "connection"})
	assertCapabilityMessage(t, result)
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "SECRET") {
		t.Fatalf("leak: %s", data)
	}
	if review.reviews != 0 {
		t.Fatal("read prompted")
	}
	connections.failure = errors.New("upstream rejected SECRET")
	result = callCapability(t, p, session, map[string]any{"action": "get", "connection_id": "connection"})
	data, _ = json.Marshal(result)
	if strings.Contains(string(data), "SECRET") {
		t.Fatalf("error leak: %s", data)
	}
	assertCapabilityCode(t, result, apperror.CodeCapabilityOperationFailed)
	assertCapabilityMessage(t, result)
}

func TestCapabilityMCPResultsExplainNextStep(t *testing.T) {
	p, _, _, session := capabilityFixture(t)
	tests := []map[string]any{
		{"action": "list"},
		{"action": "get", "connection_id": "connection"},
		{"action": "create", "name": "New", "url": "https://example.test/mcp"},
		{"action": "update", "connection_id": "connection", "name": "Renamed"},
		{"action": "probe", "connection_id": "connection"},
		{"action": "authorize", "connection_id": "connection", "auth_method": "api_key"},
		{"action": "authorize", "connection_id": "connection", "auth_method": "oauth"},
		{"action": "delete", "connection_id": "connection"},
	}
	for _, args := range tests {
		t.Run(StringArg(args, "action")+StringArg(args, "auth_method"), func(t *testing.T) {
			result := callCapability(t, p, session, args)
			assertCapabilityMessage(t, result)
		})
	}
}

func TestCapabilityMCPPartialUpdateKeepsCredentialAndDisabledState(t *testing.T) {
	p, connections, _, session := capabilityFixture(t)
	connections.conn.Active = false
	result := callCapability(t, p, session, map[string]any{"action": "update", "connection_id": "connection", "name": "Renamed"})
	if connections.update.Name != "Renamed" || connections.update.Headers["Authorization"] != "SECRET" || *connections.update.Active {
		t.Fatalf("update lost config: %v", result)
	}
}

func TestCapabilityMCPChangingServerDropsExistingCredential(t *testing.T) {
	p, connections, _, session := capabilityFixture(t)
	callCapability(t, p, session, map[string]any{"action": "update", "connection_id": "connection", "url": "https://different.test/mcp"})
	if len(connections.update.Headers) != 0 {
		t.Fatal("carried credentials to another server")
	}
}

func TestCapabilityManagementNeedsLiveApproval(t *testing.T) {
	p, connections, _, session := capabilityFixture(t)
	session.CanRequestUserInput = false
	result := callCapability(t, p, session, map[string]any{"action": "create", "name": "New", "url": "https://example.test/mcp"})
	assertCapabilityCode(t, result, apperror.CodeCapabilityApprovalRequired)
	if connections.creates != 0 {
		t.Fatal("noninteractive install ran")
	}
}

func TestCapabilityMCPAuthorizationStateBeforeAndAfterCallback(t *testing.T) {
	p, _, _, session := capabilityFixture(t)
	for _, test := range []struct {
		status mcp.OAuthStatus
		want   string
	}{{mcp.OAuthStatus{Configured: true}, "needs_authorization"}, {mcp.OAuthStatus{Configured: true, HasToken: true}, "authorized"}, {mcp.OAuthStatus{Configured: true, HasToken: true, Expired: true}, "needs_reauthorization"}} {
		p.opts.OAuth = capabilityTestOAuth{status: test.status}
		result := callCapability(t, p, session, map[string]any{"action": "get", "connection_id": "connection"}).(map[string]any)
		if result["auth_status"] != test.want {
			t.Fatalf("auth status: %v", result)
		}
	}
}

func TestCapabilityUsageFollowsAvailableTools(t *testing.T) {
	p, _, _, session := capabilityFixture(t)
	if usage := p.Usage(t.Context(), session, NewAvailableTools(nil)); usage != "" {
		t.Fatal(usage)
	}
	usage := p.Usage(t.Context(), session, NewAvailableTools([]sdk.Tool{{Name: ToolMCPManage().String()}}))
	if !strings.Contains(usage, "mcp_manage") || strings.Contains(usage, "app_manage") {
		t.Fatal(usage)
	}
}
