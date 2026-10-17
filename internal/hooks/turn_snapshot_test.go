package hooks

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type countingHookBridgeServer struct {
	*hookBridgeTestServer
	reads atomic.Int32
}

func (s *countingHookBridgeServer) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	s.reads.Add(1)
	return s.hookBridgeTestServer.ReadRaw(req, stream)
}

func newCountingHookService(t *testing.T, config string) (*Service, *countingHookBridgeServer) {
	t.Helper()
	files := map[string][]byte{}
	if config != "" {
		files[DefaultConfigPath] = []byte(config)
	}
	server := &countingHookBridgeServer{hookBridgeTestServer: &hookBridgeTestServer{files: files}}
	return NewService(nil, hookBridgeProvider{client: newHookBridgeTestClient(t, server)}), server
}

func TestTurnSnapshotLoadsConfigOncePerTurn(t *testing.T) {
	t.Parallel()

	service, server := newCountingHookService(t, `{"version":1,"hooks":[{"event":"UserMessageReceived","actions":[{"type":"command","command":"true"}]}]}`)
	ctx := WithTurnSnapshot(t.Context())
	if WithTurnSnapshot(ctx) != ctx {
		t.Fatal("WithTurnSnapshot replaced the turn's snapshot")
	}
	for range 3 {
		cfg, exists, err := service.LoadEffective(ctx, "bot-1")
		if err != nil || !exists || len(cfg.Hooks) != 1 {
			t.Fatalf("cfg=%+v exists=%v err=%v", cfg, exists, err)
		}
	}
	if got := server.reads.Load(); got != 1 {
		t.Fatalf("config read %d times in one turn, want 1", got)
	}

	if _, _, err := service.LoadEffective(t.Context(), "bot-1"); err != nil {
		t.Fatal(err)
	}
	if got := server.reads.Load(); got != 2 {
		t.Fatalf("a load outside the turn reused its snapshot: reads = %d", got)
	}
}

func TestBrokenConfigIsUnavailableForTheWholeTurn(t *testing.T) {
	t.Parallel()

	service, server := newCountingHookService(t, `{"version": 1, "hooks": [`)
	ctx := WithTurnSnapshot(t.Context())
	for range 2 {
		if _, _, err := service.LoadEffective(ctx, "bot-1"); !errors.Is(err, ErrConfigUnavailable) {
			t.Fatalf("LoadEffective error = %v, want ErrConfigUnavailable", err)
		}
	}
	if _, err := service.Run(ctx, Request{Event: EventPreToolUse, BotID: "bot-1"}, nil); !errors.Is(err, ErrConfigUnavailable) {
		t.Fatalf("Run error = %v, want ErrConfigUnavailable so policy gates fail closed", err)
	}
	if got := server.reads.Load(); got != 1 {
		t.Fatalf("broken config read %d times in one turn, want 1", got)
	}
}

func TestUnavailableWorkspaceSkipsConfigRead(t *testing.T) {
	t.Parallel()

	service, server := newCountingHookService(t, `{"version":1,"hooks":[]}`)
	ctx := bridge.WithWorkspaceAvailability(t.Context())
	bridge.MarkWorkspaceUnavailable(ctx)
	if _, _, err := service.LoadEffective(ctx, "bot-1"); !errors.Is(err, ErrConfigUnavailable) || !errors.Is(err, bridge.ErrUnavailable) {
		t.Fatalf("LoadEffective error = %v, want ErrConfigUnavailable caused by the unavailable workspace", err)
	}
	if got := server.reads.Load(); got != 0 {
		t.Fatalf("config read %d times from an unavailable workspace", got)
	}
}

func TestMissingConfigIsNoHooks(t *testing.T) {
	t.Parallel()

	service, _ := newCountingHookService(t, "")
	cfg, exists, err := service.LoadEffective(WithTurnSnapshot(t.Context()), "bot-1")
	if err != nil || exists || len(cfg.Hooks) != 0 {
		t.Fatalf("cfg=%+v exists=%v err=%v, want no hooks", cfg, exists, err)
	}
}

type workspaceInfoHookProvider struct {
	hookBridgeProvider
	info  bridge.WorkspaceInfo
	err   error
	calls atomic.Int32
}

func (p *workspaceInfoHookProvider) WorkspaceInfo(context.Context, string) (bridge.WorkspaceInfo, error) {
	p.calls.Add(1)
	return p.info, p.err
}

type failingTargetHookProvider struct {
	hookBridgeProvider
	err error
}

func (p failingTargetHookProvider) CurrentWorkspaceTargetID(context.Context, string) (string, error) {
	return "", p.err
}

// A background task derived from a turn starts a scope of its own: neither
// the hooks snapshot nor the workspace verdict travels between them.
func TestWithNewAuxiliaryTurnStartsAnIsolatedScope(t *testing.T) {
	t.Parallel()

	service, server := newCountingHookService(t, `{"version":1,"hooks":[]}`)
	turn := WithAuxiliaryTurn(t.Context())
	if WithAuxiliaryTurn(turn) != turn {
		t.Fatal("WithAuxiliaryTurn replaced the turn's scope")
	}
	if _, _, err := service.LoadEffective(turn, "bot-1"); err != nil {
		t.Fatal(err)
	}
	task := WithNewAuxiliaryTurn(context.WithoutCancel(turn))
	if WithAuxiliaryTurn(task) != task {
		t.Fatal("WithNewAuxiliaryTurn did not open a scope of its own")
	}
	if _, _, err := service.LoadEffective(task, "bot-1"); err != nil {
		t.Fatal(err)
	}
	if got := server.reads.Load(); got != 2 {
		t.Fatalf("config reads = %d, want the task to load its own snapshot", got)
	}
	bridge.MarkWorkspaceUnavailable(task)
	if bridge.WorkspaceUnavailableFromContext(turn) {
		t.Fatal("the task's workspace verdict reached the parent turn")
	}
	if _, _, err := service.LoadEffective(turn, "bot-1"); err != nil {
		t.Fatalf("the parent turn lost its snapshot: %v", err)
	}
}

func TestTurnWorkspaceInfoIsReadOncePerTurn(t *testing.T) {
	t.Parallel()

	provider := &workspaceInfoHookProvider{info: bridge.WorkspaceInfo{Backend: bridge.WorkspaceBackendRemote, DefaultWorkDir: "/srv/work"}}
	ctx := WithAuxiliaryTurn(t.Context())
	for range 3 {
		info := TurnWorkspaceInfo(ctx, provider, "bot-1")
		if info.CWD != "/srv/work" || info.Runtime != bridge.WorkspaceBackendRemote {
			t.Fatalf("info = %+v", info)
		}
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("workspace described %d times in one turn, want 1", got)
	}
	if info := TurnWorkspaceInfo(bridge.WithWorkspaceTarget(ctx, "other-target"), provider, "bot-1"); info.CWD != "/srv/work" {
		t.Fatalf("info = %+v", info)
	}
	if got := provider.calls.Load(); got != 2 {
		t.Fatalf("a different workspace target reused the description: calls = %d", got)
	}
	TurnWorkspaceInfo(t.Context(), provider, "bot-1")
	if got := provider.calls.Load(); got != 3 {
		t.Fatalf("a read outside the turn reused its snapshot: calls = %d", got)
	}
	if info := TurnWorkspaceInfo(ctx, nil, "bot-1"); info.CWD != DefaultWorkDir || info.Runtime != bridge.WorkspaceBackendContainer {
		t.Fatalf("info without a provider = %+v", info)
	}
}

// Once a turn has found its workspace unavailable, describing it for a hook
// request does not ask the workspace again; an unreachable workspace found
// here is recorded for the rest of the turn.
func TestTurnWorkspaceInfoSkipsAnUnavailableWorkspace(t *testing.T) {
	t.Parallel()

	provider := &workspaceInfoHookProvider{info: bridge.WorkspaceInfo{DefaultWorkDir: "/srv/work"}}
	ctx := WithAuxiliaryTurn(t.Context())
	bridge.MarkWorkspaceUnavailable(ctx)
	if info := TurnWorkspaceInfo(ctx, provider, "bot-1"); info.CWD != DefaultWorkDir {
		t.Fatalf("info = %+v, want the default workspace", info)
	}
	if got := provider.calls.Load(); got != 0 {
		t.Fatalf("an unavailable workspace was described %d times", got)
	}

	down := &workspaceInfoHookProvider{err: bridge.ErrUnavailable}
	ctx = WithAuxiliaryTurn(t.Context())
	TurnWorkspaceInfo(ctx, down, "bot-1")
	if !bridge.WorkspaceUnavailableFromContext(ctx) {
		t.Fatal("an unreachable workspace was not recorded for the turn")
	}
	TurnWorkspaceInfo(ctx, down, "bot-1")
	if got := down.calls.Load(); got != 1 {
		t.Fatalf("an unreachable workspace was asked %d times, want 1", got)
	}

	broken := &workspaceInfoHookProvider{err: errors.New("permission denied")}
	ctx = WithAuxiliaryTurn(t.Context())
	TurnWorkspaceInfo(ctx, broken, "bot-1")
	if bridge.WorkspaceUnavailableFromContext(ctx) {
		t.Fatal("a failed description marked the workspace unavailable")
	}
}

// Not knowing which workspace the bot uses is a database or identity failure,
// not a hooks configuration that could not be read; it is reported as itself
// so the turn is rejected instead of continuing text-only.
func TestWorkspaceTargetResolutionFailureIsNotConfigUnavailable(t *testing.T) {
	t.Parallel()

	dbErr := errors.New("database unavailable")
	service := NewService(nil, failingTargetHookProvider{err: dbErr})
	ctx := WithAuxiliaryTurn(t.Context())
	if _, _, err := service.LoadEffective(ctx, "bot-1"); !errors.Is(err, dbErr) || errors.Is(err, ErrConfigUnavailable) {
		t.Fatalf("LoadEffective error = %v, want %v and not ErrConfigUnavailable", err, dbErr)
	}
	if _, err := service.Run(ctx, Request{Event: EventUserMessageReceived, BotID: "bot-1"}, nil); !errors.Is(err, dbErr) || errors.Is(err, ErrConfigUnavailable) {
		t.Fatalf("Run error = %v, want %v and not ErrConfigUnavailable", err, dbErr)
	}
}
