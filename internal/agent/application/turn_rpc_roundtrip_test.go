package application

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/agent/turn/grpctransport"
	"github.com/felinics/memoh/internal/agent/turn/turnpb"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	intrpc "github.com/felinics/memoh/internal/rpc"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

// These tests cross the seam a split deployment runs on: the channel process
// holds a turn RPC client, and the server process serves the real application
// service behind the turn RPC server. Only the model and the stores are fake.

// serveTurnOverRPC exposes service through the turn RPC server and returns the
// client the channel process would use.
func serveTurnOverRPC(t *testing.T, service turn.Service) *grpctransport.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	server := intrpc.NewServer(slog.New(slog.DiscardHandler), "secret")
	turnpb.RegisterTurnServiceServer(server, grpctransport.NewServer(slog.New(slog.DiscardHandler), service))
	go func() { _ = server.Serve(lis) }()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(intrpc.UnaryClientAuth("secret")),
		grpc.WithStreamInterceptor(intrpc.StreamClientAuth("secret")),
	)
	if err != nil {
		t.Fatalf("dial turn rpc: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
		_ = lis.Close()
	})
	return grpctransport.NewClient(conn, grpctransport.WithClientLogger(slog.New(slog.DiscardHandler)))
}

// newRPCRuntime is the session runtime the server process runs turns on. Its
// ledger records each run's terminal state, which run_terminal reports.
func newRPCRuntime(t *testing.T, owner string) *sessionruntime.Manager {
	t.Helper()
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{
		OwnerID: owner, OwnerLeaseTTL: time.Minute, Ledger: sessionledger.New(), Fence: abortAlignmentFence{},
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

// recordingModelServer is an OpenAI-compatible endpoint that answers every
// streaming call with one text reply and keeps the request bodies it got.
type recordingModelServer struct {
	mu     sync.Mutex
	bodies []string
}

func (m *recordingModelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.bodies = append(m.bodies, string(body))
	m.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-rpc\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":null}]}\n\n")
	_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-rpc\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
}

func (m *recordingModelServer) requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.bodies...)
}

// rpcChatQueries adds the reads a run's terminal write makes: the run asked
// for no decisions.
type rpcChatQueries struct {
	directLifecycleQueries
}

func (*rpcChatQueries) ListToolApprovalsByRun(context.Context, pgtype.UUID) ([]sqlc.ToolApprovalRequest, error) {
	return nil, nil
}

func (*rpcChatQueries) ListUserInputsByRun(context.Context, pgtype.UUID) ([]sqlc.UserInputRequest, error) {
	return nil, nil
}

// newRPCChatService is the server process's application service for chat
// turns: the native runtime calling a recorded model endpoint.
func newRPCChatService(t *testing.T) (*Service, *recordingModelServer) {
	t.Helper()
	model := &recordingModelServer{}
	server := httptest.NewServer(model)
	t.Cleanup(server.Close)

	provider := modelSelectionProviderRow(t, directLifecycleProviderID, string(models.ClientTypeOpenAICompletions), true)
	provider.Config, _ = json.Marshal(map[string]any{"api_key": "test-key", "base_url": server.URL})
	chatModel := modelSelectionModelRow(t, directLifecycleModelID, "rpc-roundtrip-model", provider.ID, models.ModelTypeChat, true)
	chatModel.Config = []byte(`{"context_window":128000}`)
	queries := &rpcChatQueries{directLifecycleQueries{
		modelSelectionFakeQueries: modelSelectionFakeQueries{
			models:   map[string]sqlc.Model{chatModel.ModelID: chatModel},
			provider: provider,
		},
		modelID: chatModel.ID,
	}}
	logger := slog.New(slog.DiscardHandler)
	service := &Service{
		agent:             native.New(native.Deps{Logger: logger}),
		modelsService:     models.NewService(logger, queries),
		queries:           queries,
		contextLifecycles: &synchronizedLifecycleStore{},
		messageService:    &recordingMessageService{},
		settingsService:   settings.NewService(logger, queries, nil, nil),
		sessionService: &fakeBackgroundSessionService{
			getFn: func(_ context.Context, sessionID string) (sessionpkg.Thread, error) {
				return sessionpkg.Thread{
					ID: sessionID, BotID: lifecycleTestBotID, Type: sessionpkg.TypeChat,
					SessionMode: sessionpkg.TypeChat, RuntimeType: sessionpkg.RuntimeModel,
				}, nil
			},
		},
		streamHTTPClient: server.Client(),
		logger:           logger,
	}
	service.SetSessionRuntime(newRPCRuntime(t, "owner-rpc-chat"))
	return service, model
}

// drainOverRPC reads a remote handle to its end.
func drainOverRPC(t *testing.T, handle turn.RunHandle) ([]turn.Event, []error) {
	t.Helper()
	var (
		events []turn.Event
		errs   []error
	)
	timeout := time.After(10 * time.Second)
	for eventCh, errCh := handle.Events(), handle.Errs(); eventCh != nil || errCh != nil; {
		select {
		case event, ok := <-eventCh:
			if !ok {
				eventCh = nil
				continue
			}
			events = append(events, event)
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			errs = append(errs, err)
		case <-timeout:
			t.Fatal("turn over rpc did not end")
		}
	}
	return events, errs
}

func requireCompletedRunTerminal(t *testing.T, handle turn.RunHandle, events []turn.Event, errs []error) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("turn errors over rpc = %v", errs)
	}
	if !turn.ReportsRunTerminal(handle) {
		t.Fatal("remote handle does not report run_terminal")
	}
	if len(events) == 0 {
		t.Fatal("no events over rpc")
	}
	last := events[len(events)-1]
	terminal, ok := turn.RunTerminalFrom(last)
	if !ok || terminal.State != turn.RunStateCompleted || terminal.ErrorCode != "" {
		t.Fatalf("last event over rpc = %s %s, want completed run_terminal", last.Kind, last.Payload)
	}
	if last.RunID != handle.RunID() || last.ThreadID == "" || last.Seq != int64(len(events)) {
		t.Fatalf("run_terminal envelope = run %q thread %q seq %d, want run %q seq %d",
			last.RunID, last.ThreadID, last.Seq, handle.RunID(), len(events))
	}
}

// A skill named in an IM message reaches the model in a split deployment:
// its body crosses the turn RPC and is rendered into the model request, next
// to the attachment the message carried.
func TestChatTurnOverRPCDeliversRequestedSkillsAndAttachments(t *testing.T) {
	service, model := newRPCChatService(t)
	client := serveTurnOverRPC(t, service)

	const skillBody = "PRIVATE_REQUESTED_SKILL_BODY"
	const attachmentPath = "/data/media/report.pdf"
	handle, err := client.StartTurn(context.Background(), turn.StartTurnCommand{
		SchemaVersion:   1,
		TeamID:          "team-1",
		Mode:            turn.ModeChat,
		BotID:           lifecycleTestBotID,
		ChatID:          lifecycleTestBotID,
		ThreadID:        lifecycleTestSessionID,
		IdempotencyKey:  "msg-rpc-1",
		Query:           "summarize the report",
		UserVisibleText: "summarize the report",
		Attachments: []turn.Attachment{{
			Type: "file", Path: attachmentPath, Name: "report.pdf", Mime: "application/pdf",
		}},
		RequestedSkills: []turn.RequestedSkillContext{{
			Name: "alpha", Description: "Alpha skill", Content: skillBody, SourceKind: "registry",
			OpaqueSourceID: "opaque-alpha", ContentHash: "hash-alpha", Identity: "alpha@registry",
		}},
		SkipMemoryExtraction: true,
		SkipTitleGeneration:  true,
	})
	if err != nil {
		t.Fatalf("start turn over rpc: %v", err)
	}
	events, errs := drainOverRPC(t, handle)
	requireCompletedRunTerminal(t, handle, events, errs)

	requests := model.requests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if !strings.Contains(request, skillBody) || !strings.Contains(request, `requested_skill name=\"alpha\" source_kind=\"registry\"`) {
		t.Fatalf("model request lacks the requested skill: %s", request)
	}
	if !strings.Contains(request, attachmentPath) {
		t.Fatalf("model request lacks the attachment: %s", request)
	}
}

// recordingDiscussResolver records the routing a discuss turn resolved its
// run config with and the image references it inlined.
type recordingDiscussResolver struct {
	fakeDiscussService
	mu       sync.Mutex
	resolved []string
	images   []timeline.ImageAttachmentRef
}

func (r *recordingDiscussResolver) ResolveRunConfig(ctx context.Context, botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken string) (ResolveRunConfigResult, error) {
	r.mu.Lock()
	r.resolved = []string{botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken}
	r.mu.Unlock()
	return r.fakeDiscussService.ResolveRunConfig(ctx, botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken)
}

func (r *recordingDiscussResolver) InlineImageAttachments(_ context.Context, _ string, refs []timeline.ImageAttachmentRef) [][]sdk.ImagePart {
	r.mu.Lock()
	r.images = append(r.images, refs...)
	r.mu.Unlock()
	parts := make([][]sdk.ImagePart, len(refs))
	for i := range refs {
		parts[i] = []sdk.ImagePart{{Image: "data:image/png;base64,AA==", MediaType: "image/png"}}
	}
	return parts
}

// A discuss turn started over the RPC runs on the context the channel
// composed and the routing it resolved, and ends with run_terminal.
func TestDiscussTurnOverRPCKeepsComposedContextAndRouting(t *testing.T) {
	agent := &fakeAgentStreamer{}
	resolver := &recordingDiscussResolver{fakeDiscussService: fakeDiscussService{
		resolveResult: ResolveRunConfigResult{RunConfig: native.RunConfig{SupportsImageInput: true}, ModelID: "model-1"},
	}}
	service := newDiscussTestService(&fakeRunner{}, agent, resolver)
	service.SetSessionRuntime(newRPCRuntime(t, "owner-rpc-discuss"))
	client := serveTurnOverRPC(t, service)

	cmd := turn.StartTurnCommand{
		SchemaVersion:           1,
		TeamID:                  "team-1",
		Mode:                    turn.ModeDiscuss,
		BotID:                   "bot-1",
		ThreadID:                "sess-1",
		RouteID:                 "route-1",
		SourceChannelIdentityID: "ci-1",
		CurrentChannel:          "telegram",
		ReplyTarget:             "tg:42",
		ConversationType:        "group",
		SessionToken:            "session-token",
		ChatToken:               "chat-token",
		ToolHTTPURL:             "http://tools.invalid",
		DiscussMessages: []turn.DiscussMessage{
			{Role: "user", Content: `<message id="1">PRIVATE_DISCUSS_CONTEXT</message>`},
		},
		DiscussImageRefs: []turn.DiscussImageRef{{ContentHash: "img-hash", Mime: "image/png"}},
		DiscussAddressed: true,
	}
	handle, err := client.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatalf("start discuss turn over rpc: %v", err)
	}
	events, errs := drainOverRPC(t, handle)
	requireCompletedRunTerminal(t, handle, events, errs)
	if events[0].Kind != turn.DiscussEventRunResolved {
		t.Fatalf("first discuss event = %s, want %s", events[0].Kind, turn.DiscussEventRunResolved)
	}

	resolver.mu.Lock()
	resolved, images := resolver.resolved, resolver.images
	resolver.mu.Unlock()
	if want := []string{"bot-1", "sess-1", "ci-1", "telegram", "tg:42", "group", "session-token"}; !reflect.DeepEqual(resolved, want) {
		t.Fatalf("discuss run config resolved with %q, want %q", resolved, want)
	}
	if len(images) != 1 || images[0].ContentHash != "img-hash" || images[0].Mime != "image/png" {
		t.Fatalf("inlined image refs = %+v", images)
	}
	if agent.lastConfig == nil {
		t.Fatal("discuss turn did not reach the runtime")
	}
	rendered, err := json.Marshal(agent.lastConfig.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "PRIVATE_DISCUSS_CONTEXT") || !strings.Contains(string(rendered), "data:image/png;base64,AA==") {
		t.Fatalf("runtime messages lack the composed discuss context: %s", rendered)
	}
}
