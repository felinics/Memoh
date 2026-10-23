package native

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type contextFileService struct {
	pb.UnimplementedContainerServiceServer
	delay       time.Duration
	failures    map[string]error
	reads       atomic.Int32
	deadlineSet atomic.Bool
}

func (s *contextFileService) ReadFile(ctx context.Context, req *pb.ReadFileRequest) (*pb.ReadFileResponse, error) {
	s.reads.Add(1)
	if _, ok := ctx.Deadline(); ok {
		s.deadlineSet.Store(true)
	}
	if err := s.failures[req.GetPath()]; err != nil {
		return nil, err
	}
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &pb.ReadFileResponse{Content: "content of " + req.GetPath()}, nil
}

// A slow but healthy workspace is waited for: the reads carry no deadline of
// their own, every file loads and nothing is reported unavailable.
func TestLoadSystemFilesWaitsForSlowHealthyWorkspace(t *testing.T) {
	svc := &contextFileService{delay: 1500 * time.Millisecond}
	provider, cleanup := setupExecTestInfra(t, svc)
	defer cleanup()
	ctx := bridge.WithWorkspaceAvailability(t.Context())
	for _, file := range NewFSClient(provider, "bot", nil).LoadSystemFiles(ctx) {
		if file.LoadStatus != "" || !strings.Contains(file.Content, file.Filename) {
			t.Fatalf("file=%+v", file)
		}
	}
	if bridge.WorkspaceUnavailableFromContext(ctx) {
		t.Fatal("a slow workspace was marked unavailable")
	}
	if svc.deadlineSet.Load() {
		t.Fatal("a context file read carried a deadline of its own")
	}
}

func TestLoadSystemFilesSeparatesMissingFromUnreadable(t *testing.T) {
	svc := &contextFileService{failures: map[string]error{
		"/data/AGENTS.md":   status.Error(codes.NotFound, "no such file"),
		"/data/PROFILES.md": status.Error(codes.PermissionDenied, "permission denied"),
	}}
	provider, cleanup := setupExecTestInfra(t, svc)
	defer cleanup()
	ctx := bridge.WithWorkspaceAvailability(t.Context())
	files := NewFSClient(provider, "bot", nil).LoadSystemFiles(ctx)
	want := map[string]string{"AGENTS.md": "", "MEMORY.md": "", "PROFILES.md": SystemFileUnavailable}
	for _, file := range files {
		if file.LoadStatus != want[file.Filename] {
			t.Errorf("%s: status = %q, want %q", file.Filename, file.LoadStatus, want[file.Filename])
		}
		if file.LoadStatus != "" && file.Content != "" {
			t.Errorf("%s: unreadable file carries content %q", file.Filename, file.Content)
		}
	}
	if bridge.WorkspaceUnavailableFromContext(ctx) {
		t.Fatal("one unreadable file marked the whole workspace unavailable")
	}
}

// Once the workspace is found unreachable the remaining files are not read:
// the turn asks an unreachable workspace once, not once per file.
func TestLoadSystemFilesStopsReadingAnUnreachableWorkspace(t *testing.T) {
	svc := &contextFileService{failures: map[string]error{
		"/data/AGENTS.md": status.Error(codes.Unavailable, "bridge down"),
	}}
	provider, cleanup := setupExecTestInfra(t, svc)
	defer cleanup()
	ctx := bridge.WithWorkspaceAvailability(t.Context())
	for _, file := range NewFSClient(provider, "bot", nil).LoadSystemFiles(ctx) {
		if file.LoadStatus != SystemFileUnavailable {
			t.Fatalf("file=%+v, want unavailable", file)
		}
	}
	if got := svc.reads.Load(); got != 1 {
		t.Fatalf("reads = %d, want 1", got)
	}
	if !bridge.WorkspaceUnavailableFromContext(ctx) {
		t.Fatal("an unreachable workspace was not recorded for the turn")
	}
}

func TestSystemPromptReportsLoadStatusOnlyWhenSomethingFailed(t *testing.T) {
	healthy := GenerateSystemPrompt(SystemPromptParams{Files: []SystemFile{{Filename: "AGENTS.md"}}})
	if strings.Contains(healthy, "Context loading status") {
		t.Fatal("a healthy turn carries a loading status section")
	}
	degraded := GenerateSystemPrompt(SystemPromptParams{
		Files:       []SystemFile{{Filename: "MEMORY.md", LoadStatus: SystemFileUnavailable}},
		LoadNotices: []string{HooksUnavailableNotice, HooksUnavailableNotice},
	})
	if !strings.Contains(degraded, "MEMORY.md could not be read") {
		t.Fatalf("missing file status in prompt:\n%s", degraded)
	}
	if strings.Count(degraded, HooksUnavailableNotice) != 1 {
		t.Fatal("notices are not deduplicated")
	}
}

// A hooks configuration that cannot be parsed does not block the model call:
// the application has already disabled tools and told the model.
func TestBrokenHooksConfigDoesNotBlockModelCall(t *testing.T) {
	t.Parallel()
	svc := newMockExecContainerService()
	svc.written[hooks.DefaultConfigPath] = []byte(`{"version": 1, "hooks": [`)
	provider, cleanup := setupExecTestInfra(t, svc)
	defer cleanup()
	model := &preflightCountingProvider{}
	_, err := New(Deps{BridgeProvider: provider, HookService: hooks.NewService(nil, provider)}).Generate(t.Context(), RunConfig{
		Model:    &sdk.Model{ID: "broken-hooks", Provider: model, Type: sdk.ModelTypeChat},
		Identity: SessionContext{BotID: "bot-1"},
	})
	if generateCalls, _ := model.calls(); generateCalls != 1 {
		t.Fatalf("model calls = %d (err=%v), want the model to be called", generateCalls, err)
	}
}
