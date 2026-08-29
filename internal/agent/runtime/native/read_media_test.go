package native

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const agentReadMediaTestBufSize = 1 << 20

// agentReadMediaContainerService implements both ReadFile and ReadRaw so
// that the merged read tool (ContainerProvider) can detect binary files
// and then delegate to ReadImageFromContainer.
type agentReadMediaContainerService struct {
	pb.UnimplementedContainerServiceServer
	files map[string][]byte
}

func (s *agentReadMediaContainerService) ReadFile(_ context.Context, req *pb.ReadFileRequest) (*pb.ReadFileResponse, error) {
	data, ok := s.files[req.GetPath()]
	if !ok {
		return nil, status.Error(codes.NotFound, "not found")
	}
	_ = data
	// All files in this test fixture are images → binary.
	return &pb.ReadFileResponse{Binary: true}, nil
}

func (s *agentReadMediaContainerService) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	data, ok := s.files[req.GetPath()]
	if !ok {
		return status.Error(codes.NotFound, "not found")
	}
	if len(data) == 0 {
		return nil
	}
	return stream.Send(&pb.DataChunk{Data: data})
}

type agentReadMediaBridgeProvider struct {
	client *bridge.Client
}

func (p *agentReadMediaBridgeProvider) MCPClient(_ context.Context, _ string) (*bridge.Client, error) {
	return p.client, nil
}

func newAgentReadMediaBridgeProvider(t *testing.T, files map[string][]byte) bridge.Provider {
	t.Helper()

	lis := bufconn.Listen(agentReadMediaTestBufSize)
	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, &agentReadMediaContainerService{files: files})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})

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
	t.Cleanup(func() { _ = conn.Close() })

	return &agentReadMediaBridgeProvider{client: bridge.NewClientFromConn(conn)}
}

type agentReadMediaMockProvider struct {
	name    string
	calls   int
	handler func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error)
}

func (m *agentReadMediaMockProvider) Name() string {
	if m.name != "" {
		return m.name
	}
	return "mock"
}

func (*agentReadMediaMockProvider) ListModels(context.Context) ([]sdk.Model, error) {
	return nil, nil
}

func (*agentReadMediaMockProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK, Message: "ok"}
}

func (*agentReadMediaMockProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true, Message: "supported"}, nil
}

func (m *agentReadMediaMockProvider) DoGenerate(_ context.Context, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
	m.calls++
	return m.handler(m.calls, params)
}

func (m *agentReadMediaMockProvider) DoStream(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
	result, err := m.DoGenerate(ctx, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan sdk.StreamPart, 8)
	go func() {
		defer close(ch)
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		if result.Text != "" {
			ch <- &sdk.TextStartPart{ID: "mock"}
			ch <- &sdk.TextDeltaPart{ID: "mock", Text: result.Text}
			ch <- &sdk.TextEndPart{ID: "mock"}
		}
		for _, tc := range result.ToolCalls {
			ch <- &sdk.StreamToolCallPart{
				ToolCallID: tc.ToolCallID,
				ToolName:   tc.ToolName,
				Input:      tc.Input,
			}
		}
		ch <- &sdk.FinishStepPart{FinishReason: result.FinishReason, Usage: result.Usage, Response: result.Response}
		ch <- &sdk.FinishPart{FinishReason: result.FinishReason, TotalUsage: result.Usage}
	}()
	return &sdk.StreamResult{Stream: ch}, nil
}

func assertInjectedReadMediaMessage(t *testing.T, msg sdk.Message, expectedImage, expectedMediaType string) {
	t.Helper()

	if msg.Role != sdk.MessageRoleUser {
		t.Fatalf("expected injected read_media message role %q, got %q", sdk.MessageRoleUser, msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected one injected content part, got %d", len(msg.Content))
	}
	image, ok := msg.Content[0].(sdk.ImagePart)
	if !ok {
		t.Fatalf("expected sdk.ImagePart, got %T", msg.Content[0])
	}
	if image.Image != expectedImage {
		t.Fatalf("unexpected injected image payload: %q", image.Image)
	}
	if image.MediaType != expectedMediaType {
		t.Fatalf("unexpected injected media type: %q", image.MediaType)
	}
}

func TestAgentGenerateReadMediaInjectsImageIntoNextStep(t *testing.T) {
	t.Parallel()

	// The PNG data must contain a null byte (\x00) so that the execRead
	// binary probe (bytes.IndexByte(probe, 0)) detects it as binary and
	// delegates to ReadImageFromContainer. Real PNG files always contain
	// null bytes in their IHDR and other chunks.
	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00payload")
	expectedDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)

	modelProvider := &agentReadMediaMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call == 1 {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "read",
						Input:      map[string]any{"path": "/data/images/demo.png"},
					}},
				}, nil
			}

			if len(params.Messages) < 4 {
				t.Fatalf("expected prior tool and injected messages, got %d", len(params.Messages))
			}

			last := params.Messages[len(params.Messages)-1]
			if last.Role != sdk.MessageRoleUser {
				t.Fatalf("expected last message to be injected user image, got %s", last.Role)
			}
			if len(last.Content) != 1 {
				t.Fatalf("expected one injected content part, got %d", len(last.Content))
			}
			image, ok := last.Content[0].(sdk.ImagePart)
			if !ok {
				t.Fatalf("expected sdk.ImagePart, got %T", last.Content[0])
			}
			if image.Image != expectedDataURL {
				t.Fatalf("unexpected injected image payload: %q", image.Image)
			}
			if image.MediaType != "image/png" {
				t.Fatalf("unexpected injected media type: %q", image.MediaType)
			}

			var toolResult sdk.ToolResultPart
			foundToolMessage := false
			for _, msg := range params.Messages {
				if msg.Role != sdk.MessageRoleTool || len(msg.Content) == 0 {
					continue
				}
				part, ok := msg.Content[0].(sdk.ToolResultPart)
				if !ok {
					continue
				}
				toolResult = part
				foundToolMessage = true
				break
			}
			if !foundToolMessage {
				t.Fatal("expected tool result message before second step")
			}
			raw, err := json.Marshal(toolResult.Result)
			if err != nil {
				t.Fatalf("marshal tool result: %v", err)
			}
			if !bytes.Contains(raw, []byte(`"ok":true`)) {
				t.Fatalf("expected compact success metadata, got %s", raw)
			}
			if bytes.Contains(raw, []byte(expectedDataURL)) || bytes.Contains(raw, []byte("payload")) {
				t.Fatalf("tool result leaked image bytes: %s", raw)
			}

			return &sdk.GenerateResult{
				Text:         "done",
				FinishReason: sdk.FinishReasonStop,
			}, nil
		},
	}

	// ContainerProvider normalizes paths by stripping the workdir prefix,
	// so the mock files map must use the normalized (relative) path.
	bp := newAgentReadMediaBridgeProvider(t, map[string][]byte{
		"images/demo.png": pngBytes,
	})

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, bp, nil, "/data"),
	})

	var committed []*sdk.StepResult
	result, err := a.Generate(context.Background(), RunConfig{
		Model:              &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:           []sdk.Message{sdk.UserMessage("look at the image")},
		SupportsImageInput: true,
		SupportsToolCall:   true,
		Identity: SessionContext{
			BotID: "bot-1",
		},
		OnStepCommitted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			committed = append(committed, step)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Text != "done" {
		t.Fatalf("unexpected result text: %q", result.Text)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("expected persisted step + injected history, got %d messages", len(result.Messages))
	}
	assertInjectedReadMediaMessage(t, result.Messages[2], expectedDataURL, "image/png")
	if len(committed) != 2 || len(committed[1].Messages) != 2 {
		t.Fatalf("committed steps = %#v, want injected message plus final assistant", committed)
	}
	assertInjectedReadMediaMessage(t, committed[1].Messages[0], expectedDataURL, "image/png")
	if result.Messages[3].Role != sdk.MessageRoleAssistant {
		t.Fatalf("expected final persisted message to be assistant, got %s", result.Messages[3].Role)
	}
	if modelProvider.calls != 2 {
		t.Fatalf("expected 2 model calls, got %d", modelProvider.calls)
	}
}

func TestAgentExecuteToolReadMediaReturnsPublicResultOnly(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00payload")
	bp := newAgentReadMediaBridgeProvider(t, map[string][]byte{
		"images/demo.png": pngBytes,
	})

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, bp, nil, "/data"),
	})

	part, err := a.ExecuteTool(context.Background(), RunConfig{
		Model:              &sdk.Model{ID: "mock-model", Provider: &agentReadMediaMockProvider{}},
		SupportsImageInput: true,
		SupportsToolCall:   true,
		Identity: SessionContext{
			BotID: "bot-1",
		},
	}, sdk.ToolCall{
		ToolCallID: "call-1",
		ToolName:   agenttools.ReadMediaToolName().String(),
		Input:      map[string]any{"path": "/data/images/demo.png"},
	})
	if err != nil {
		t.Fatalf("ExecuteTool(read) returned error: %v", err)
	}
	result, ok := part.Result.(agenttools.ReadMediaToolResult)
	if !ok {
		t.Fatalf("ExecuteTool(read) result = %T, want public ReadMediaToolResult", part.Result)
	}
	if !result.OK || result.Path != "images/demo.png" || result.Mime != "image/png" {
		t.Fatalf("unexpected public read result: %#v", result)
	}
	raw := fmt.Sprintf("%#v", part.Result)
	if strings.Contains(raw, "ImageBase64") || strings.Contains(raw, "ImageMediaType") || strings.Contains(raw, base64.StdEncoding.EncodeToString(pngBytes)) {
		t.Fatalf("ExecuteTool(read) leaked internal image payload: %#v", part.Result)
	}
}

func TestDecorateReadMediaToolsConcurrentExecutions(t *testing.T) {
	t.Parallel()

	const calls = 32
	imageBase64 := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00payload"))
	wrapped, state := decorateReadMediaTools(&sdk.Model{ID: "mock-model"}, []sdk.Tool{{
		Name: agenttools.ReadMediaToolName().String(),
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			return agenttools.ReadMediaToolOutput{
				Public: agenttools.ReadMediaToolResult{
					OK:   true,
					Path: "/data/image.png",
					Mime: "image/png",
				},
				ImageBase64:    imageBase64,
				ImageMediaType: "image/png",
			}, nil
		},
	}})
	if state == nil || len(wrapped) != 1 {
		t.Fatalf("decorateReadMediaTools did not wrap read tool: state=%v tools=%d", state, len(wrapped))
	}

	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		i := i
		go func() {
			defer wg.Done()
			result, err := wrapped[0].Execute(&sdk.ToolExecContext{
				Context:    context.Background(),
				ToolCallID: fmt.Sprintf("call-%02d", i),
				ToolName:   agenttools.ReadMediaToolName().String(),
			}, map[string]any{"path": "/data/image.png"})
			if err != nil {
				t.Errorf("wrapped read execute returned error: %v", err)
				return
			}
			if _, ok := result.(agenttools.ReadMediaToolResult); !ok {
				t.Errorf("wrapped read execute result = %T, want public ReadMediaToolResult", result)
			}
		}()
	}
	wg.Wait()

	next := state.prepareStep(&sdk.GenerateParams{})
	if next == nil || len(next.Messages) != 1 {
		t.Fatalf("prepareStep messages = %#v, want one injected message", next)
	}
	if got := len(next.Messages[0].Content); got != calls {
		t.Fatalf("injected image count = %d, want %d", got, calls)
	}
}

func TestAgentGenerateReadMediaInjectsAnthropicSafeImageIntoNextStep(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00payload")
	expectedBase64 := base64.StdEncoding.EncodeToString(pngBytes)

	modelProvider := &agentReadMediaMockProvider{
		name: "anthropic-messages",
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call == 1 {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "read",
						Input:      map[string]any{"path": "/data/images/demo.png"},
					}},
				}, nil
			}

			last := params.Messages[len(params.Messages)-1]
			image, ok := last.Content[0].(sdk.ImagePart)
			if !ok {
				t.Fatalf("expected sdk.ImagePart, got %T", last.Content[0])
			}
			if image.Image != expectedBase64 {
				t.Fatalf("expected raw base64 for anthropic, got %q", image.Image)
			}
			if image.MediaType != "image/png" {
				t.Fatalf("unexpected injected media type: %q", image.MediaType)
			}
			if strings.HasPrefix(image.Image, "data:") {
				t.Fatalf("anthropic image payload must not be a data URL: %q", image.Image)
			}

			return &sdk.GenerateResult{
				Text:         "done",
				FinishReason: sdk.FinishReasonStop,
			}, nil
		},
	}

	// ContainerProvider normalizes paths by stripping the workdir prefix,
	// so the mock files map must use the normalized (relative) path.
	bp := newAgentReadMediaBridgeProvider(t, map[string][]byte{
		"images/demo.png": pngBytes,
	})

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, bp, nil, "/data"),
	})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:              &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:           []sdk.Message{sdk.UserMessage("look at the image")},
		SupportsImageInput: true,
		SupportsToolCall:   true,
		Identity: SessionContext{
			BotID: "bot-1",
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
}

func TestAgentStreamReadMediaPersistsInjectedImageInTerminalMessages(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00payload")
	expectedDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)

	modelProvider := &agentReadMediaMockProvider{
		handler: func(call int, _ sdk.GenerateParams) (*sdk.GenerateResult, error) {
			if call == 1 {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "read",
						Input:      map[string]any{"path": "/data/images/demo.png"},
					}},
				}, nil
			}
			return &sdk.GenerateResult{
				Text:         "done",
				FinishReason: sdk.FinishReasonStop,
			}, nil
		},
	}

	// ContainerProvider normalizes paths by stripping the workdir prefix,
	// so the mock files map must use the normalized (relative) path.
	bp := newAgentReadMediaBridgeProvider(t, map[string][]byte{
		"images/demo.png": pngBytes,
	})

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, bp, nil, "/data"),
	})

	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:              &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:           []sdk.Message{sdk.UserMessage("look at the image")},
		SupportsImageInput: true,
		SupportsToolCall:   true,
		Identity: SessionContext{
			BotID: "bot-1",
		},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	if terminal.Type != EventAgentEnd {
		t.Fatalf("expected terminal event %q, got %q", EventAgentEnd, terminal.Type)
	}

	var messages []sdk.Message
	if err := json.Unmarshal(terminal.Messages, &messages); err != nil {
		t.Fatalf("unmarshal terminal messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected persisted step + injected history, got %d messages", len(messages))
	}
	assertInjectedReadMediaMessage(t, messages[2], expectedDataURL, "image/png")
	if messages[3].Role != sdk.MessageRoleAssistant {
		t.Fatalf("expected final persisted message to be assistant, got %s", messages[3].Role)
	}
}

func TestBuildReadMediaPartPDFBecomesFilePart(t *testing.T) {
	out := agenttools.ReadMediaToolOutput{
		FileBase64:    "JVBERi0xLjQ=",
		FileMediaType: "application/pdf",
		Filename:      "report.pdf",
	}
	part := buildReadMediaPart(string(models.ClientTypeOpenAICompletions), out)
	fp, ok := part.(sdk.FilePart)
	if !ok {
		t.Fatalf("expected sdk.FilePart, got %T", part)
	}
	// Bare base64 regardless of client type — provider framing belongs to the
	// twilight adapters, unlike the image lane's per-client data URL shaping.
	if fp.Data != "JVBERi0xLjQ=" {
		t.Fatalf("expected bare base64, got %q", fp.Data)
	}
	if fp.MediaType != "application/pdf" || fp.Filename != "report.pdf" {
		t.Fatalf("unexpected part: %+v", fp)
	}
}

func TestMediaPartHasContent(t *testing.T) {
	if mediaPartHasContent(sdk.ImagePart{}) || mediaPartHasContent(sdk.FilePart{}) || mediaPartHasContent(nil) {
		t.Fatal("empty parts must report no content")
	}
	if !mediaPartHasContent(sdk.ImagePart{Image: "data:image/png;base64,abc"}) {
		t.Fatal("image with payload must report content")
	}
	if !mediaPartHasContent(sdk.FilePart{Data: "JVBERi0="}) {
		t.Fatal("file with payload must report content")
	}
}
