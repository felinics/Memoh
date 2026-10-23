package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	ctr "github.com/felinics/memoh/internal/container"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type stubWorkspaceTargets struct {
	target workspace.ResolvedWorkspaceTarget
	err    error
	calls  *int
}

func (s stubWorkspaceTargets) ResolveWorkspaceTarget(context.Context, string, string) (workspace.ResolvedWorkspaceTarget, error) {
	if s.calls != nil {
		*s.calls++
	}
	return s.target, s.err
}

var nativeTarget = workspace.ResolvedWorkspaceTarget{
	TargetID: workspace.WorkspaceTargetNative,
	Kind:     workspace.WorkspaceTargetNative,
	Name:     "Server Workspace",
}

// A workspace that is not ready keeps its target and the turn continues as a
// text-only conversation; later stages see the verdict instead of resolving
// the workspace again.
func TestUnavailableWorkspaceContinuesTheTurnWithoutIt(t *testing.T) {
	calls := 0
	s := &Service{skillLoader: failingSkillLoader{}, workspaceTargets: stubWorkspaceTargets{
		target: nativeTarget,
		err:    fmt.Errorf("%w: %w", workspace.ErrCapabilityUnavailable, bridge.ErrUnavailable),
		calls:  &calls,
	}}
	ctx, req, err := s.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1"})
	if err != nil || req.WorkspaceTargetID != workspace.WorkspaceTargetNative {
		t.Fatalf("req=%+v err=%v", req, err)
	}
	if !bridge.WorkspaceUnavailableFromContext(ctx) {
		t.Fatal("the turn does not know its workspace is unavailable")
	}
	skills, notices := s.loadTurnSkills(ctx, "bot-1")
	if len(skills) != 0 || len(notices) != 1 {
		t.Fatalf("skills=%v notices=%v, want the listing skipped and reported", skills, notices)
	}
	cfg := s.prepareRunConfig(ctx, native.RunConfig{SupportsToolCall: true, LoadNotices: notices, Identity: native.SessionContext{BotID: "bot-1"}})
	if cfg.SupportsToolCall || !strings.Contains(cfg.System, native.WorkspaceUnavailableNotice) {
		t.Fatalf("SupportsToolCall=%v, want tools disabled and the notice in the prompt", cfg.SupportsToolCall)
	}
	if strings.Contains(cfg.System, native.SkillsUnavailableNotice) || strings.Contains(cfg.System, native.HooksUnavailableNotice) {
		t.Fatal("the workspace notice is repeated per auxiliary source")
	}
	if calls != 1 {
		t.Fatalf("workspace resolved %d times, want 1", calls)
	}
	if err := s.ValidateWorkspaceTarget(t.Context(), "bot-1", workspace.WorkspaceTargetNative); err != nil {
		t.Fatalf("selecting a known but unavailable workspace = %v, want accepted", err)
	}
}

func TestWorkspaceResolutionFailuresOtherThanAvailabilityStillReject(t *testing.T) {
	for name, err := range map[string]error{
		"database":       errors.New("database unavailable"),
		"unknown target": workspace.ErrWorkspaceTargetNotFound,
	} {
		s := &Service{workspaceTargets: stubWorkspaceTargets{err: err}}
		if _, _, got := s.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1"}); !errors.Is(got, err) {
			t.Errorf("%s: error = %v, want %v", name, got, err)
		}
	}
	// Availability without a known target is not something to continue on.
	s := &Service{workspaceTargets: stubWorkspaceTargets{err: workspace.ErrCapabilityUnavailable}}
	if _, _, err := s.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1"}); err == nil {
		t.Fatal("an unavailable workspace without identity was accepted")
	}
}

type failingHooksProvider struct{ err error }

func (p failingHooksProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return nil, p.err
}

// brokenHooksServer serves a hooks.json that cannot be parsed.
type brokenHooksServer struct {
	pb.UnimplementedContainerServiceServer
}

func (brokenHooksServer) ReadRaw(_ *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	return stream.Send(&pb.DataChunk{Data: []byte(`{"version": 1, "hooks": [`)})
}

type brokenHooksProvider struct{ client *bridge.Client }

func (p brokenHooksProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return p.client, nil
}

func newBrokenHooksProvider(t *testing.T) brokenHooksProvider {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterContainerServiceServer(server, brokenHooksServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return brokenHooksProvider{client: bridge.NewClientFromConn(conn)}
}

// When the hooks configuration cannot be loaded the user message is still
// answered, but the policy the hooks enforce is unknown, so no tools run.
func TestUnavailableHooksConfigAllowsTextOnlyTurn(t *testing.T) {
	s := &Service{hookService: hooks.NewService(nil, newBrokenHooksProvider(t))}
	ctx := hooks.WithAuxiliaryTurn(t.Context())
	req := ChatRequest{BotID: "bot-1", Query: "hello"}
	got, err := s.applyUserMessageHook(ctx, req)
	if err != nil || got.Query != req.Query {
		t.Fatalf("applyUserMessageHook = %+v, %v; want the message to pass", got, err)
	}
	cfg := s.prepareRunConfig(ctx, native.RunConfig{SupportsToolCall: true, Identity: native.SessionContext{BotID: "bot-1"}})
	if cfg.SupportsToolCall || !strings.Contains(cfg.System, native.HooksUnavailableNotice) {
		t.Fatalf("SupportsToolCall=%v, want tools disabled and the hooks notice in the prompt", cfg.SupportsToolCall)
	}
	if strings.Contains(cfg.System, native.WorkspaceUnavailableNotice) {
		t.Fatal("an unreadable hooks file was reported as an unavailable workspace")
	}
}

// A workspace that refused the read for a reason of its own is not a hooks
// configuration that could not be read: the user message is rejected with
// that cause instead of continuing without its hooks.
func TestWorkspaceRefusalStillRejectsTheMessage(t *testing.T) {
	s := &Service{hookService: hooks.NewService(nil, failingHooksProvider{err: fmt.Errorf("workspace client: %w", ctr.ErrConflict)})}
	if _, err := s.applyUserMessageHook(hooks.WithAuxiliaryTurn(t.Context()), ChatRequest{BotID: "bot-1", Query: "hello"}); !errors.Is(err, ctr.ErrConflict) {
		t.Fatalf("applyUserMessageHook error = %v, want the workspace refusal", err)
	}
}

func TestHealthyTurnKeepsToolsAndNoLoadStatus(t *testing.T) {
	s := &Service{hookService: hooks.NewService(nil, nil)}
	cfg := s.prepareRunConfig(hooks.WithAuxiliaryTurn(t.Context()), native.RunConfig{SupportsToolCall: true, Identity: native.SessionContext{BotID: "bot-1"}})
	if !cfg.SupportsToolCall || strings.Contains(cfg.System, "Context loading status") {
		t.Fatalf("SupportsToolCall=%v, want a healthy turn untouched", cfg.SupportsToolCall)
	}
}

type failingSkillLoader struct{}

func (failingSkillLoader) LoadSkills(context.Context, string) ([]SkillEntry, error) {
	return nil, bridge.ErrUnavailable
}

type countingWorkspaceInfoProvider struct {
	calls atomic.Int32
}

func (*countingWorkspaceInfoProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return nil, bridge.ErrUnavailable
}

func (p *countingWorkspaceInfoProvider) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	p.calls.Add(1)
	return bridge.WorkspaceInfo{DefaultWorkDir: "/srv/work"}, nil
}

// Every hook event of a turn reports the same workspace description, read
// once; a workspace the turn found unavailable is not asked at all.
func TestHookEventsShareOneWorkspaceDescriptionPerTurn(t *testing.T) {
	provider := &countingWorkspaceInfoProvider{}
	s := &Service{
		agent:       native.New(native.Deps{BridgeProvider: provider}),
		hookService: hooks.NewService(nil, nil),
	}
	ctx := hooks.WithAuxiliaryTurn(t.Context())
	for _, event := range []string{hooks.EventUserMessageReceived, hooks.EventBeforePromptBuild, hooks.EventAfterPromptBuild} {
		if _, err := s.runBaseHook(ctx, "bot-1", "sess-1", "chat-1", event, nil); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("workspace described %d times in one turn, want 1", got)
	}
	if info := s.hookWorkspace(ctx, "bot-1"); info.CWD != "/srv/work" {
		t.Fatalf("hook workspace = %+v", info)
	}

	unavailable := hooks.WithAuxiliaryTurn(t.Context())
	bridge.MarkWorkspaceUnavailable(unavailable)
	if info := s.hookWorkspace(unavailable, "bot-1"); info.CWD != hooks.DefaultWorkDir {
		t.Fatalf("hook workspace for an unavailable workspace = %+v", info)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("an unavailable workspace was described: calls = %d", got)
	}
}

// A degraded turn withholds its tools and says why; the history it carries
// keeps its tool blocks, which the native runtime replays as text only in the
// requests it sends.
func TestDegradedTurnWithholdsToolsAndKeepsHistory(t *testing.T) {
	s := &Service{workspaceTargets: stubWorkspaceTargets{
		target: nativeTarget,
		err:    fmt.Errorf("%w: %w", workspace.ErrCapabilityUnavailable, bridge.ErrUnavailable),
	}}
	ctx, _, err := s.prepareWorkspaceRequest(t.Context(), ChatRequest{BotID: "bot-1"})
	if err != nil {
		t.Fatal(err)
	}
	history := []sdk.Message{
		sdk.UserMessage("list the files"),
		{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
			sdk.TextPart{Text: "Listing."},
			sdk.ToolCallPart{ToolCallID: "call-1", ToolName: "exec", Input: sdk.ParseToolArguments(`{"command":"ls"}`)},
		}},
		sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "exec", Result: sdk.TextOutput("a.txt")}),
	}
	cfg := s.prepareRunConfig(ctx, native.RunConfig{SupportsToolCall: true, Messages: history, Identity: native.SessionContext{BotID: "bot-1"}})
	if cfg.SupportsToolCall || cfg.ToolsWithheld != native.ToolsWithheldWorkspaceUnavailable {
		t.Fatalf("SupportsToolCall=%v ToolsWithheld=%q, want tools withheld for the workspace", cfg.SupportsToolCall, cfg.ToolsWithheld)
	}
	if cfg.Messages[2].Role != sdk.MessageRoleTool {
		t.Fatalf("the stored history was rewritten: %+v", cfg.Messages[2])
	}

	healthy := (&Service{}).prepareRunConfig(hooks.WithAuxiliaryTurn(t.Context()), native.RunConfig{SupportsToolCall: true, Messages: history, Identity: native.SessionContext{BotID: "bot-1"}})
	if !healthy.SupportsToolCall || healthy.ToolsWithheld != "" {
		t.Fatalf("a healthy turn withheld its tools: %q", healthy.ToolsWithheld)
	}
}

// When an earlier stage of the turn already resolved the target and found
// the workspace unavailable, preparing the request again keeps that identity
// instead of waiting on the workspace once more.
func TestAlreadyUnavailableTurnReusesResolvedTarget(t *testing.T) {
	calls := 0
	s := &Service{
		workspaceTargets: stubWorkspaceTargets{target: nativeTarget, calls: &calls},
		botPermissions:   workspaceRequestPermission(true),
	}
	ctx := hooks.WithAuxiliaryTurn(t.Context())
	bridge.MarkWorkspaceUnavailable(ctx)
	prior := &WorkspaceTarget{TargetID: workspace.WorkspaceTargetNative, Kind: workspace.WorkspaceTargetNative, Name: "Server Workspace"}
	ctx, req, err := s.prepareWorkspaceRequest(ctx, ChatRequest{BotID: "bot-1", ThreadID: "sess-1", WorkspaceTarget: prior})
	if err != nil || calls != 0 || req.WorkspaceTargetID != workspace.WorkspaceTargetNative {
		t.Fatalf("err=%v calls=%d req=%+v, want the recorded target reused without resolving", err, calls, req)
	}
	if prepared, ok := preparedWorkspaceTargetFromContext(ctx, "bot-1", "sess-1"); !ok || prepared.TargetID != workspace.WorkspaceTargetNative {
		t.Fatalf("prepared target = %+v, %v", prepared, ok)
	}
	// The prepared target belongs to its bot and session only.
	if _, ok := preparedWorkspaceTargetFromContext(ctx, "bot-2", "sess-1"); ok {
		t.Fatal("another bot adopted the prepared target")
	}
	if _, ok := preparedWorkspaceTargetFromContext(ctx, "bot-1", "sess-2"); ok {
		t.Fatal("another session adopted the prepared target")
	}
	// A different explicit target is still resolved.
	if _, _, err := s.prepareWorkspaceRequest(ctx, ChatRequest{BotID: "bot-1", UserID: "u", WorkspaceTarget: prior, WorkspaceTargetID: "other"}); err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want an explicit different target resolved once", err, calls)
	}
}
