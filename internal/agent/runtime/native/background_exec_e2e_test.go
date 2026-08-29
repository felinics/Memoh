package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/background"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// ---------------------------------------------------------------------------
// Mock container service with controllable Exec behavior
// ---------------------------------------------------------------------------

type execBehavior struct {
	stdout   string
	stderr   string
	exitCode int32
	delay    time.Duration // how long before sending output
}

type mockExecContainerService struct {
	pb.UnimplementedContainerServiceServer

	mu        sync.Mutex
	behaviors map[string]execBehavior // command prefix -> behavior
	written   map[string][]byte       // path -> content (WriteFile)
}

func newMockExecContainerService() *mockExecContainerService {
	return &mockExecContainerService{
		behaviors: make(map[string]execBehavior),
		written:   make(map[string][]byte),
	}
}

func (s *mockExecContainerService) setBehavior(cmdPrefix string, b execBehavior) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.behaviors[cmdPrefix] = b
}

func (s *mockExecContainerService) findBehavior(cmd string) (execBehavior, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for prefix, b := range s.behaviors {
		if strings.Contains(cmd, prefix) {
			return b, true
		}
	}
	return execBehavior{}, false
}

func (s *mockExecContainerService) Exec(stream pb.ContainerService_ExecServer) error {
	// Read config message.
	input, err := stream.Recv()
	if err != nil {
		return err
	}
	cmd := input.GetCommand()

	b, ok := s.findBehavior(cmd)
	if !ok {
		// Default: instant success with echoed command.
		b = execBehavior{stdout: fmt.Sprintf("[executed] %s\n", cmd), exitCode: 0}
	}

	if b.delay > 0 {
		select {
		case <-time.After(b.delay):
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}

	if b.stdout != "" {
		if err := stream.Send(&pb.ExecOutput{
			Stream: pb.ExecOutput_STDOUT,
			Data:   []byte(b.stdout),
		}); err != nil {
			return err
		}
	}
	if b.stderr != "" {
		if err := stream.Send(&pb.ExecOutput{
			Stream: pb.ExecOutput_STDERR,
			Data:   []byte(b.stderr),
		}); err != nil {
			return err
		}
	}
	return stream.Send(&pb.ExecOutput{
		Stream:   pb.ExecOutput_EXIT,
		ExitCode: b.exitCode,
	})
}

func (s *mockExecContainerService) WriteFile(_ context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written[req.GetPath()] = req.GetContent()
	return &pb.WriteFileResponse{}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func setupExecTestInfra(t *testing.T, svc *mockExecContainerService) (bridge.Provider, func()) {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, svc)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		<-done
	}

	bp := &agentReadMediaBridgeProvider{client: bridge.NewClientFromConn(conn)}
	return bp, cleanup
}

// ---------------------------------------------------------------------------
// E2E Test: Explicit background exec
// ---------------------------------------------------------------------------

func TestE2E_ExplicitBackgroundExec(t *testing.T) {
	t.Parallel()

	svc := newMockExecContainerService()
	svc.setBehavior("npm install", execBehavior{
		stdout:   "added 42 packages\n",
		exitCode: 0,
		delay:    100 * time.Millisecond, // simulate some work
	})

	bp, cleanup := setupExecTestInfra(t, svc)
	defer cleanup()

	bgMgr := background.New(nil)

	// Model calls exec with run_in_background. Completion should not inject
	// a notification into later model steps.
	var step2Params sdk.GenerateParams
	modelProvider := &agentReadMediaMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			switch call {
			case 1:
				// Model decides to run npm install in background.
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "exec",
						Input: map[string]any{
							"command":           "npm install",
							"run_in_background": true,
							"description":       "Install dependencies",
						},
					}},
				}, nil
			case 2:
				// Model sees tool result with background_started.
				// It should do something else or reply.
				// Simulate waiting a bit so the background task has time to complete.
				time.Sleep(300 * time.Millisecond)
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-2",
						ToolName:   "exec",
						Input: map[string]any{
							"command": "echo hello",
						},
					}},
				}, nil
			case 3:
				// Step 3 should not receive a background notification.
				step2Params = params
				return &sdk.GenerateResult{
					Text:         "All done!",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			default:
				return &sdk.GenerateResult{
					Text:         "unexpected",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			}
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, bp, bgMgr, "/data"),
	})

	result, err := a.Generate(context.Background(), RunConfig{
		Model:             &sdk.Model{ID: "mock", Provider: modelProvider},
		Messages:          []sdk.Message{sdk.UserMessage("install deps and say hi")},
		System:            "You are a helpful bot.",
		SupportsToolCall:  true,
		Identity:          SessionContext{BotID: "bot-test-1", SessionID: "sess-1"},
		BackgroundManager: bgMgr,
	})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if result.Text != "All done!" {
		t.Errorf("unexpected text: %q", result.Text)
	}

	// Verify later params do not contain a background notification.
	found := false
	for _, msg := range step2Params.Messages {
		if msg.Role == sdk.MessageRoleUser {
			for _, part := range msg.Content {
				if tp, ok := part.(sdk.TextPart); ok {
					if strings.Contains(tp.Text, "task-"+"notification") &&
						strings.Contains(tp.Text, "completed") {
						found = true
					}
				}
			}
		}
	}
	if found {
		t.Error("background task notification should not be injected into later model steps")
	}
}

// ---------------------------------------------------------------------------
// E2E Test: Foreground timeout flips to background
// ---------------------------------------------------------------------------

func TestE2E_ForegroundTimeoutFlip(t *testing.T) {
	t.Parallel()

	svc := newMockExecContainerService()
	// Command takes 3 seconds — longer than our 1-second soft timeout.
	svc.setBehavior("slow-build", execBehavior{
		stdout:   "build completed successfully\n",
		exitCode: 0,
		delay:    3 * time.Second,
	})

	bp, cleanup := setupExecTestInfra(t, svc)
	defer cleanup()

	bgMgr := background.New(nil)

	var toolResult map[string]any
	modelProvider := &agentReadMediaMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			switch call {
			case 1:
				// Model runs a command with short timeout (will flip).
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "exec",
						Input: map[string]any{
							"command":     "slow-build",
							"timeout":     1, // 1 second — will flip
							"description": "Run slow build",
						},
					}},
				}, nil
			case 2:
				// Extract the tool result from step 1.
				toolResult = extractToolResult(t, params, "call-1")
				return &sdk.GenerateResult{
					Text:         "Build moved to background.",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			default:
				return &sdk.GenerateResult{
					Text:         "unexpected",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			}
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, bp, bgMgr, "/data"),
	})

	result, err := a.Generate(context.Background(), RunConfig{
		Model:             &sdk.Model{ID: "mock", Provider: modelProvider},
		Messages:          []sdk.Message{sdk.UserMessage("run the build")},
		System:            "You are a helpful bot.",
		SupportsToolCall:  true,
		Identity:          SessionContext{BotID: "bot-test-2", SessionID: "sess-2"},
		BackgroundManager: bgMgr,
	})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if result.Text != "Build moved to background." {
		t.Errorf("unexpected text: %q", result.Text)
	}

	// The tool result should indicate auto_backgrounded.
	if toolResult == nil {
		t.Fatal("tool result not captured")
	}
	status, _ := toolResult["status"].(string)
	if status != "auto_backgrounded" {
		t.Errorf("expected status auto_backgrounded, got %q", status)
	}
	taskID, _ := toolResult["task_id"].(string)
	if taskID == "" {
		t.Error("expected non-empty task_id")
	}
	msg, _ := toolResult["message"].(string)
	if !strings.Contains(msg, "no work was lost") {
		t.Errorf("expected flip message mentioning no work lost, got %q", msg)
	}

	// Wait for the background task to complete and verify the snapshot result.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snap, _, err := bgMgr.WaitForSessionTask(ctx, "bot-test-2", "sess-2", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	if snap.Status != background.TaskCompleted {
		t.Errorf("expected completed, got %s", snap.Status)
	}
	if !strings.Contains(snap.OutputTail, "build completed") {
		t.Errorf("expected build output in tail, got %q", snap.OutputTail)
	}
}

// ---------------------------------------------------------------------------
// E2E Test: Sleep command rejection
// ---------------------------------------------------------------------------

func TestE2E_SleepRejection(t *testing.T) {
	t.Parallel()

	svc := newMockExecContainerService()
	bp, cleanup := setupExecTestInfra(t, svc)
	defer cleanup()

	bgMgr := background.New(nil)

	var sleepToolResult map[string]any
	var sleepWasError bool
	modelProvider := &agentReadMediaMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			switch call {
			case 1:
				// Model tries to sleep 10.
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "exec",
						Input: map[string]any{
							"command": "sleep 10",
						},
					}},
				}, nil
			case 2:
				// Check the tool result — should be an error.
				sleepToolResult, sleepWasError = extractToolResultWithError(params, "call-1")
				return &sdk.GenerateResult{
					Text:         "Got it, won't sleep.",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			default:
				return &sdk.GenerateResult{
					Text:         "unexpected",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			}
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, bp, bgMgr, "/data"),
	})

	result, err := a.Generate(context.Background(), RunConfig{
		Model:             &sdk.Model{ID: "mock", Provider: modelProvider},
		Messages:          []sdk.Message{sdk.UserMessage("wait 10 seconds")},
		System:            "You are a bot.",
		SupportsToolCall:  true,
		Identity:          SessionContext{BotID: "bot-test-3", SessionID: "sess-3"},
		BackgroundManager: bgMgr,
	})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if result.Text != "Got it, won't sleep." {
		t.Errorf("unexpected text: %q", result.Text)
	}

	if !sleepWasError {
		t.Error("expected sleep command to return is_error=true")
	}
	_ = sleepToolResult // the error message is in the tool result
}

// ---------------------------------------------------------------------------
// E2E Test: Running tasks summary injection
// ---------------------------------------------------------------------------

func TestE2E_RunningTasksSummaryInjected(t *testing.T) {
	t.Parallel()

	svc := newMockExecContainerService()
	// Long-running task that won't complete during the test.
	svc.setBehavior("long-task", execBehavior{
		delay: 30 * time.Second,
	})

	bp, cleanup := setupExecTestInfra(t, svc)
	defer cleanup()

	bgMgr := background.New(nil)

	var step3Params sdk.GenerateParams
	modelProvider := &agentReadMediaMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			switch call {
			case 1:
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "exec",
						Input: map[string]any{
							"command":           "long-task",
							"run_in_background": true,
							"description":       "Long running task",
						},
					}},
				}, nil
			case 2:
				// Do another tool call so prepareStep fires again.
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-2",
						ToolName:   "exec",
						Input: map[string]any{
							"command": "echo check",
						},
					}},
				}, nil
			case 3:
				// Capture the params; the running tasks summary should ride a
				// message, never the system prompt.
				step3Params = cloneGenerateParams(params)
				return &sdk.GenerateResult{
					Text:         "Done checking.",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			default:
				return &sdk.GenerateResult{
					Text:         "unexpected",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			}
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, bp, bgMgr, "/data"),
	})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:             &sdk.Model{ID: "mock", Provider: modelProvider},
		Messages:          []sdk.Message{sdk.UserMessage("start background and check")},
		System:            "You are a bot.",
		SupportsToolCall:  true,
		Identity:          SessionContext{BotID: "bot-test-4", SessionID: "sess-4"},
		BackgroundManager: bgMgr,
	})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}

	if strings.Contains(step3Params.System, "Currently running background tasks:") {
		t.Errorf("running tasks summary must not rewrite the system prompt, got: %s", step3Params.System)
	}
	if got := backgroundSummaryCount(step3Params.Messages); got != 1 {
		t.Fatalf("summary messages = %d, want exactly 1", got)
	}
	last := step3Params.Messages[len(step3Params.Messages)-1]
	text, _ := last.Content[0].(sdk.TextPart)
	if last.Role != sdk.MessageRoleUser || !strings.Contains(text.Text, "Long running task") {
		t.Errorf("expected tail user message carrying the task summary, got: %#v", last)
	}
}

// ---------------------------------------------------------------------------
// Helpers for extracting tool results from params
// ---------------------------------------------------------------------------

func extractToolResult(t *testing.T, params sdk.GenerateParams, toolCallID string) map[string]any {
	t.Helper()
	for _, msg := range params.Messages {
		if msg.Role != sdk.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			tr, ok := part.(sdk.ToolResultPart)
			if !ok || tr.ToolCallID != toolCallID {
				continue
			}
			raw, _ := json.Marshal(tr.Result)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			return m
		}
	}
	t.Fatalf("tool result for %s not found in params", toolCallID)
	return nil
}

func extractToolResultWithError(params sdk.GenerateParams, toolCallID string) (map[string]any, bool) {
	for _, msg := range params.Messages {
		if msg.Role != sdk.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			tr, ok := part.(sdk.ToolResultPart)
			if !ok || tr.ToolCallID != toolCallID {
				continue
			}
			raw, _ := json.Marshal(tr.Result)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			return m, tr.IsError
		}
	}
	return nil, false
}
