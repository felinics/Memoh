package inbound

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/acl"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/discuss"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/route"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/command"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/media"
	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/slash"
)

type fakeChatGateway struct {
	resp             fakeChatResponse
	err              error
	gotReq           turn.StartTurnCommand
	onChat           func(turn.StartTurnCommand)
	userInputCalls   int
	userInputInput   turn.UserInputResponse
	userInputErr     error
	userInputStarted chan struct{}
	userInputRelease chan struct{}
	advanceCalls     int
	advanceInput     userinput.AdvanceTextInput
	advanceResult    userinput.AdvanceTextResult
	advanceErr       error
}

type fakeChatResponse struct {
	Messages []turn.ModelMessage
}

type fakeTurnRun struct {
	events chan turn.Event
	errs   chan error
}

func (*fakeTurnRun) RunID() string                                    { return "run-1" }
func (h *fakeTurnRun) Events() <-chan turn.Event                      { return h.events }
func (h *fakeTurnRun) Errs() <-chan error                             { return h.errs }
func (*fakeTurnRun) Inject(context.Context, turn.InjectMessage) error { return nil }
func (*fakeTurnRun) AddOutboundAssets([]turn.OutboundAssetRef)        {}
func (*fakeTurnRun) Cancel()                                          {}

func (f *fakeChatGateway) AdvancePlainTextUserInput(_ context.Context, input userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error) {
	f.advanceCalls++
	f.advanceInput = input
	return f.advanceResult, f.advanceErr
}

type nativeUserInputTestAdapter struct{ typ channel.ChannelType }

func (a *nativeUserInputTestAdapter) Type() channel.ChannelType { return a.typ }

func (a *nativeUserInputTestAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{Type: a.typ, Capabilities: channel.ChannelCapabilities{Text: true, NativeUserInput: true}}
}

func TestRejectReservedSkillMetadataInInboundMessage(t *testing.T) {
	tests := []struct {
		name string
		msg  channel.InboundMessage
	}{
		{
			name: "message metadata",
			msg: channel.InboundMessage{Message: channel.Message{Metadata: map[string]any{
				"model_requested_skills": []string{"alpha"},
			}}},
		},
		{
			name: "part metadata",
			msg: channel.InboundMessage{Message: channel.Message{Parts: []channel.MessagePart{{
				Type:     channel.MessagePartText,
				Text:     "hello",
				Metadata: map[string]any{"loaded_skills": []string{"alpha"}},
			}}}},
		},
		{
			name: "attachment metadata",
			msg: channel.InboundMessage{Message: channel.Message{Attachments: []channel.Attachment{{
				Type:     channel.AttachmentImage,
				URL:      "https://example.test/image.png",
				Metadata: map[string]any{"modelContextSkills": []string{"alpha"}},
			}}}},
		},
		{
			name: "reply attachment metadata",
			msg: channel.InboundMessage{Message: channel.Message{Reply: &channel.ReplyRef{Attachments: []channel.Attachment{{
				Type:     channel.AttachmentFile,
				URL:      "https://example.test/file.txt",
				Metadata: map[string]any{"requestedSkills": []string{"alpha"}},
			}}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := channel.RejectReservedSkillMetadata(tt.msg.Message)
			var slashErr slash.Error
			if !errors.As(err, &slashErr) || slashErr.Code != slash.CodeReservedSkillMetadata {
				t.Fatalf("err = %#v, want %s", err, slash.CodeReservedSkillMetadata)
			}
		})
	}
}

func (f *fakeChatGateway) StartTurn(_ context.Context, cmd turn.StartTurnCommand) (turn.RunHandle, error) {
	f.gotReq = cmd
	if f.onChat != nil {
		f.onChat(cmd)
	}
	events := make(chan turn.Event, 1)
	errs := make(chan error, 1)
	if f.err != nil {
		errs <- f.err
		close(events)
		close(errs)
		return &fakeTurnRun{events: events, errs: errs}, nil
	}
	payload := map[string]any{
		"type":     "agent_end",
		"messages": f.resp.Messages,
	}
	data, err := json.Marshal(payload)
	if err == nil {
		events <- turn.Event{
			RunID:    "run-1",
			TeamID:   cmd.TeamID,
			ThreadID: cmd.ThreadID,
			Seq:      1,
			Kind:     "agent_end",
			Payload:  data,
		}
	}
	close(events)
	close(errs)
	return &fakeTurnRun{events: events, errs: errs}, nil
}

func (*fakeChatGateway) RespondToolApproval(context.Context, turn.ToolApprovalResponse, chan<- json.RawMessage) error {
	return nil
}

func (f *fakeChatGateway) RespondUserInput(_ context.Context, input turn.UserInputResponse, eventCh chan<- json.RawMessage) error {
	f.userInputCalls++
	f.userInputInput = input
	if f.userInputStarted != nil {
		close(f.userInputStarted)
	}
	if f.userInputRelease != nil {
		<-f.userInputRelease
	}
	if eventCh != nil {
		eventCh <- json.RawMessage(`{"type":"agent_end"}`)
	}
	return f.userInputErr
}

type fakeReplySender struct {
	sent   []channel.OutboundMessage
	events []channel.StreamEvent
}

func (s *fakeReplySender) Send(_ context.Context, msg channel.OutboundMessage) error {
	s.sent = append(s.sent, msg)
	return nil
}

func (s *fakeReplySender) OpenStream(_ context.Context, target string, _ channel.StreamOptions) (channel.OutboundStream, error) {
	return &fakeOutboundStream{
		sender: s,
		target: strings.TrimSpace(target),
	}, nil
}

type fakeOutboundStream struct {
	sender *fakeReplySender
	target string
}

func (s *fakeOutboundStream) Push(_ context.Context, event channel.StreamEvent) error {
	if s == nil || s.sender == nil {
		return nil
	}
	s.sender.events = append(s.sender.events, event)
	if event.Type == channel.StreamEventFinal && event.Final != nil && !event.Final.Message.IsEmpty() {
		s.sender.sent = append(s.sender.sent, channel.OutboundMessage{
			Target:  s.target,
			Message: event.Final.Message,
		})
	}
	return nil
}

func (*fakeOutboundStream) Close(_ context.Context) error {
	return nil
}

type fakeProcessingStatusNotifier struct {
	startedHandle channel.ProcessingStatusHandle
	startedErr    error
	completedErr  error
	failedErr     error
	events        []string
	info          []channel.ProcessingStatusInfo
	completedSeen channel.ProcessingStatusHandle
	failedSeen    channel.ProcessingStatusHandle
	failedCause   error
}

func (n *fakeProcessingStatusNotifier) ProcessingStarted(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, info channel.ProcessingStatusInfo) (channel.ProcessingStatusHandle, error) {
	n.events = append(n.events, "started")
	n.info = append(n.info, info)
	return n.startedHandle, n.startedErr
}

func (n *fakeProcessingStatusNotifier) ProcessingCompleted(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, info channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle) error {
	n.events = append(n.events, "completed")
	n.info = append(n.info, info)
	n.completedSeen = handle
	return n.completedErr
}

func (n *fakeProcessingStatusNotifier) ProcessingFailed(_ context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, info channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle, cause error) error {
	n.events = append(n.events, "failed")
	n.info = append(n.info, info)
	n.failedSeen = handle
	n.failedCause = cause
	return n.failedErr
}

type fakeProcessingStatusAdapter struct {
	notifier *fakeProcessingStatusNotifier
}

func (*fakeProcessingStatusAdapter) Type() channel.ChannelType {
	return channel.ChannelType("feishu")
}

func (*fakeProcessingStatusAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type: channel.ChannelType("feishu"),
		Capabilities: channel.ChannelCapabilities{
			Text:  true,
			Reply: true,
		},
	}
}

func (a *fakeProcessingStatusAdapter) ProcessingStarted(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, info channel.ProcessingStatusInfo) (channel.ProcessingStatusHandle, error) {
	return a.notifier.ProcessingStarted(ctx, cfg, msg, info)
}

func (a *fakeProcessingStatusAdapter) ProcessingCompleted(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, info channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle) error {
	return a.notifier.ProcessingCompleted(ctx, cfg, msg, info, handle)
}

func (a *fakeProcessingStatusAdapter) ProcessingFailed(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, info channel.ProcessingStatusInfo, handle channel.ProcessingStatusHandle, cause error) error {
	return a.notifier.ProcessingFailed(ctx, cfg, msg, info, handle, cause)
}

type fakeChatService struct {
	resolveResult route.ResolveConversationResult
	resolveErr    error
	persisted     []messagepkg.Message
	persistedIn   []messagepkg.PersistInput
}

type fakeChatACL struct {
	allowed bool
	err     error
	calls   int
	lastReq acl.EvaluateRequest
}

type fakeCommandRoleResolver struct {
	role string
	err  error
}

func (f *fakeCommandRoleResolver) GetMemberRole(_ context.Context, _, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.role, nil
}

type fakeSessionEnsurer struct {
	activeSession SessionResult
	activeErr     error
	createErr     error
	lastRouteID   string
	lastSpec      NewSessionSpec
}

func (f *fakeSessionEnsurer) EnsureActiveSession(_ context.Context, _, routeID, _ string) (SessionResult, error) {
	f.lastRouteID = routeID
	if f.activeErr != nil {
		return SessionResult{}, f.activeErr
	}
	return f.activeSession, nil
}

func (f *fakeSessionEnsurer) GetActiveSession(_ context.Context, routeID string) (SessionResult, error) {
	f.lastRouteID = routeID
	if f.activeErr != nil {
		return SessionResult{}, f.activeErr
	}
	return f.activeSession, nil
}

func (f *fakeSessionEnsurer) CreateNewSession(_ context.Context, _, routeID, _ string, spec NewSessionSpec) (SessionResult, error) {
	f.lastRouteID = routeID
	f.lastSpec = spec
	if f.createErr != nil {
		return SessionResult{}, f.createErr
	}
	if strings.TrimSpace(f.activeSession.ID) == "" {
		return SessionResult{
			ID:                    "created-session",
			Type:                  spec.Type,
			Mode:                  spec.Mode,
			Runtime:               spec.Runtime,
			RuntimeOwnerAccountID: spec.RuntimeOwnerAccountID,
		}, nil
	}
	return f.activeSession, nil
}

type fakeRequestedSkillResolver struct {
	items []skillset.ResolvedSkill
	err   error
	calls int
	botID string
	names []string
}

func (f *fakeRequestedSkillResolver) ResolveTextRequestedSkills(_ context.Context, botID string, names []string) ([]skillset.ResolvedSkill, error) {
	f.calls++
	f.botID = botID
	f.names = append([]string(nil), names...)
	if f.err != nil {
		return nil, f.err
	}
	return append([]skillset.ResolvedSkill(nil), f.items...), nil
}

type fakeDefaultChatRuntimeReader struct {
	settings DefaultChatRuntimeSettings
	err      error
}

func (f fakeDefaultChatRuntimeReader) DefaultChatRuntime(_ context.Context, _ string) (DefaultChatRuntimeSettings, error) {
	if f.err != nil {
		return DefaultChatRuntimeSettings{}, f.err
	}
	return f.settings, nil
}

type fakeACPAgentSetupReader struct {
	metadata map[string]any
	err      error
}

func (f fakeACPAgentSetupReader) ACPAgentSetupMetadata(_ context.Context, _ string) (map[string]any, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.metadata, nil
}

type fakeBotPermissionChecker struct {
	allowed bool
	err     error
	account string
	values  map[string]bool
}

func (f *fakeBotPermissionChecker) HasBotPermission(_ context.Context, botID, accountID, permission string) (bool, error) {
	f.account = accountID
	if f.err != nil {
		return false, f.err
	}
	if f.values != nil {
		return f.values[botID+":"+accountID+":"+permission], nil
	}
	return f.allowed, nil
}

func jwtBearerSubject(t *testing.T, bearerToken, secret string) string {
	t.Helper()
	raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(bearerToken), "Bearer "))
	if raw == "" {
		t.Fatal("bearer token is empty")
	}
	parsed, err := jwt.Parse(raw, func(_ *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	if err != nil {
		t.Fatalf("parse jwt: %v", err)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		t.Fatalf("invalid jwt claims: %#v", parsed.Claims)
	}
	sub, _ := claims["sub"].(string)
	return sub
}

type fakeCommandQueries struct {
	messageCount int64
	usage        int64
	cacheRow     dbsqlc.GetSessionCacheStatsRow
	skills       []string

	gotCountSession pgtype.UUID // captures the session passed to CountMessagesBySession
}

func (*fakeCommandQueries) GetLatestSessionIDByBot(_ context.Context, _ pgtype.UUID) (pgtype.UUID, error) {
	return pgtype.UUID{}, errors.New("unexpected latest session lookup")
}

func (f *fakeCommandQueries) CountMessagesBySession(_ context.Context, sessionID pgtype.UUID) (int64, error) {
	f.gotCountSession = sessionID
	return f.messageCount, nil
}

func (f *fakeCommandQueries) GetLatestAssistantUsage(_ context.Context, _ pgtype.UUID) (int64, error) {
	return f.usage, nil
}

func (f *fakeCommandQueries) GetSessionCacheStats(_ context.Context, _ pgtype.UUID) (dbsqlc.GetSessionCacheStatsRow, error) {
	return f.cacheRow, nil
}

func (f *fakeCommandQueries) GetSessionUsedSkills(_ context.Context, _ pgtype.UUID) ([]string, error) {
	return f.skills, nil
}

func (*fakeCommandQueries) GetTokenUsageByDayAndType(_ context.Context, _ dbsqlc.GetTokenUsageByDayAndTypeParams) ([]dbsqlc.GetTokenUsageByDayAndTypeRow, error) {
	return nil, nil
}

func (*fakeCommandQueries) GetTokenUsageByModel(_ context.Context, _ dbsqlc.GetTokenUsageByModelParams) ([]dbsqlc.GetTokenUsageByModelRow, error) {
	return nil, nil
}

func (f *fakeChatACL) Evaluate(_ context.Context, req acl.EvaluateRequest) (bool, error) {
	f.calls++
	f.lastReq = req
	if f.err != nil {
		return false, f.err
	}
	return f.allowed, nil
}

type fakeMediaIngestor struct {
	nextID          string
	nextMime        string
	ingestErr       error
	calls           int
	inputs          []media.IngestInput
	payloads        [][]byte
	storageKeyAsset media.Asset
	storageKeyErr   error
}

func (f *fakeMediaIngestor) Stat(_ context.Context, _, contentHash string) (media.Asset, error) {
	asset := f.storageKeyAsset
	if asset.ContentHash == "" {
		asset = media.Asset{
			ContentHash: contentHash,
			Mime:        "application/octet-stream",
			StorageKey:  "test/" + contentHash,
		}
	}
	return asset, nil
}

func (f *fakeMediaIngestor) Open(_ context.Context, _, contentHash string) (io.ReadCloser, media.Asset, error) {
	asset := f.storageKeyAsset
	if asset.ContentHash == "" {
		asset = media.Asset{
			ContentHash: contentHash,
			Mime:        "application/octet-stream",
			StorageKey:  "test/" + contentHash,
		}
	}
	return io.NopCloser(bytes.NewReader([]byte("test"))), asset, nil
}

func (f *fakeMediaIngestor) Ingest(_ context.Context, input media.IngestInput) (media.Asset, error) {
	f.calls++
	f.inputs = append(f.inputs, input)
	if input.Reader != nil {
		payload, _ := io.ReadAll(input.Reader)
		f.payloads = append(f.payloads, payload)
	}
	if f.ingestErr != nil {
		return media.Asset{}, f.ingestErr
	}
	id := strings.TrimSpace(f.nextID)
	if id == "" {
		id = "asset-test-id"
	}
	mime := strings.TrimSpace(f.nextMime)
	if mime == "" {
		mime = strings.TrimSpace(input.Mime)
	}
	return media.Asset{
		ContentHash: id,
		Mime:        mime,
		StorageKey:  "test/" + id,
	}, nil
}

func (f *fakeMediaIngestor) GetByStorageKey(_ context.Context, _, _ string) (media.Asset, error) {
	return f.storageKeyAsset, f.storageKeyErr
}

func (*fakeMediaIngestor) IngestContainerFile(_ context.Context, _, _ string) (media.Asset, error) {
	return media.Asset{}, errors.New("not implemented in test")
}

func (*fakeMediaIngestor) AccessPath(_ context.Context, asset media.Asset) string {
	return "/data/.memoh/media/" + asset.StorageKey
}

type fakeStorageProvider struct {
	objects map[string][]byte
}

func (f *fakeStorageProvider) Put(_ context.Context, key string, reader io.Reader) error {
	if f.objects == nil {
		f.objects = make(map[string][]byte)
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	f.objects[key] = payload
	return nil
}

func (f *fakeStorageProvider) Open(_ context.Context, key string) (io.ReadCloser, error) {
	payload, ok := f.objects[key]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func (f *fakeStorageProvider) Delete(_ context.Context, key string) error {
	delete(f.objects, key)
	return nil
}

func (*fakeStorageProvider) AccessPath(_ context.Context, key string) string {
	return "/data/.memoh/media/" + key
}

type fakeAttachmentResolverAdapter struct {
	typ     channel.ChannelType
	payload channel.AttachmentPayload
}

func (f *fakeAttachmentResolverAdapter) Type() channel.ChannelType {
	if f != nil && strings.TrimSpace(f.typ.String()) != "" {
		return f.typ
	}
	return channel.ChannelType("resolver-test")
}

func (f *fakeAttachmentResolverAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        f.Type(),
		DisplayName: "ResolverTest",
		Capabilities: channel.ChannelCapabilities{
			Text:        true,
			Attachments: true,
		},
	}
}

func (f *fakeAttachmentResolverAdapter) ResolveAttachment(_ context.Context, _ channel.ChannelConfig, _ channel.Attachment) (channel.AttachmentPayload, error) {
	if f != nil && f.payload.Reader != nil {
		return f.payload, nil
	}
	return channel.AttachmentPayload{
		Reader: io.NopCloser(strings.NewReader("resolver-bytes")),
		Mime:   "application/octet-stream",
		Name:   "resolver.bin",
		Size:   int64(len("resolver-bytes")),
	}, nil
}

func (f *fakeChatService) ResolveConversation(_ context.Context, _ route.ResolveInput) (route.ResolveConversationResult, error) {
	if f.resolveErr != nil {
		return route.ResolveConversationResult{}, f.resolveErr
	}
	return f.resolveResult, nil
}

func (f *fakeChatService) Persist(_ context.Context, input messagepkg.PersistInput) (messagepkg.Message, error) {
	f.persistedIn = append(f.persistedIn, input)
	msg := messagepkg.Message{
		BotID:                   input.BotID,
		SessionID:               input.SessionID,
		SenderChannelIdentityID: input.SenderChannelIdentityID,
		SenderUserID:            input.SenderUserID,
		ExternalMessageID:       input.ExternalMessageID,
		SourceReplyToMessageID:  input.SourceReplyToMessageID,
		Role:                    input.Role,
		Content:                 input.Content,
		Metadata:                input.Metadata,
	}
	f.persisted = append(f.persisted, msg)
	return msg, nil
}

func TestChannelInboundProcessorWithIdentity(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("AI reply")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
		Conversation: channel.Conversation{
			ID:   "chat-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "hello" {
		t.Errorf("expected query 'hello', got: %s", gateway.gotReq.Query)
	}
	if gateway.gotReq.UserID != "" {
		t.Errorf("expected empty user_id, got: %s", gateway.gotReq.UserID)
	}
	if gateway.gotReq.SourceChannelIdentityID != "channelIdentity-1" {
		t.Errorf("expected source_channel_identity_id 'channelIdentity-1', got: %s", gateway.gotReq.SourceChannelIdentityID)
	}
	if gateway.gotReq.ChatID != "bot-1" {
		t.Errorf("expected bot-scoped chat id 'bot-1', got: %s", gateway.gotReq.ChatID)
	}
	if len(sender.sent) != 1 || sender.sent[0].Message.PlainText() != "AI reply" {
		t.Fatalf("expected AI reply, got: %+v", sender.sent)
	}
}

func TestTurnIdempotencyKeyScopesExternalMessageIDByRoute(t *testing.T) {
	first := turnIdempotencyKey(channel.ChannelType("telegram"), "route-1", "42")
	retry := turnIdempotencyKey(channel.ChannelType("telegram"), "route-1", "42")
	otherChat := turnIdempotencyKey(channel.ChannelType("telegram"), "route-2", "42")
	otherPlatform := turnIdempotencyKey(channel.ChannelType("discord"), "route-1", "42")

	if first == "" || first != retry {
		t.Fatalf("same delivery produced unstable key: %q, %q", first, retry)
	}
	if first == otherChat {
		t.Fatal("same platform message ID collided across routes")
	}
	if first == otherPlatform {
		t.Fatal("same message ID collided across platforms")
	}
	if got := turnIdempotencyKey(channel.ChannelType("telegram"), "route-1", "  "); got != "" {
		t.Fatalf("empty external message ID produced key %q", got)
	}
}

func TestChannelInboundProcessorRespondCommandRoutesUserInput(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"},
		linkedUserIDs: map[string][]string{
			"channelIdentity-1": {"user-1"},
		},
	}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeACPAgent}})
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "msg-1", Text: "/respond input-1 Plan B"},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
		Conversation: channel.Conversation{
			ID:   "chat-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	if gateway.userInputCalls != 1 {
		t.Fatalf("RespondUserInput calls = %d, want 1", gateway.userInputCalls)
	}
	got := gateway.userInputInput
	if got.BotID != "bot-1" || got.ThreadID != "session-1" || got.ExplicitID != "input-1" || got.TextAnswer != "Plan B" {
		t.Fatalf("user input input = %#v", got)
	}
	if got.ActorChannelIdentityID != "channelIdentity-1" || got.ActorUserID != "user-1" {
		t.Fatalf("actor fields = %#v", got)
	}
	if gateway.gotReq.BotID != "" {
		t.Fatalf("ordinary chat should not run, got request %#v", gateway.gotReq)
	}
}

func TestChannelInboundProcessorPlainTextUserInputShowsOnlyNextQuestion(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{advanceResult: userinput.AdvanceTextResult{
		Handled: true,
		Request: userinput.Request{
			ID: "input-1",
			UIPayload: userinput.UIPayload{Questions: []userinput.UIQuestion{
				{ID: "q1", Text: "Previous question", Kind: userinput.QuestionKindText},
				{ID: "q2", Text: "Choose topics", Kind: userinput.QuestionKindMultiSelect, Options: []userinput.UIOption{
					{ID: "q2.o1", Label: "Go"}, {ID: "q2.o2", Label: "Rust"},
				}},
			}},
			Interaction: userinput.TextInteractionState{
				QuestionIndex: 1,
				Answers:       []userinput.QuestionAnswer{{QuestionID: "q1", Text: "hidden answer"}},
			},
		},
	}}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1"}})
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		BotID: "bot-1", Channel: channel.ChannelType("weixin"), ReplyTarget: "target-id",
		Message:      channel.Message{ID: "msg-2", Text: "first answer"},
		Sender:       channel.Identity{SubjectID: "ext-1"},
		Conversation: channel.Conversation{ID: "chat-1", Type: channel.ConversationTypePrivate},
	}
	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: msg.Channel}, msg, sender); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	if gateway.advanceCalls != 1 || gateway.userInputCalls != 0 || gateway.gotReq.BotID != "" {
		t.Fatalf("unexpected routing: advance=%d respond=%d chat=%#v", gateway.advanceCalls, gateway.userInputCalls, gateway.gotReq)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sender.sent))
	}
	rendered := sender.sent[0].Message.PlainText()
	for _, want := range []string{"2/2", "Choose topics", "1. Go", "2. Rust", "multiple numbers", "back"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("next prompt missing %q:\n%s", want, rendered)
		}
	}
	for _, hidden := range []string{"Previous question", "hidden answer"} {
		if strings.Contains(rendered, hidden) {
			t.Fatalf("next prompt leaked %q:\n%s", hidden, rendered)
		}
	}
}

func TestChannelInboundProcessorPlainTextUserInputCompletesWithFullSummary(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	request := userinput.Request{
		ID: "input-1",
		UIPayload: userinput.UIPayload{Questions: []userinput.UIQuestion{
			{ID: "q1", Text: "Plan?", Kind: userinput.QuestionKindSingleSelect, Options: []userinput.UIOption{{ID: "q1.o1", Label: "Alpha"}, {ID: "q1.o2", Label: "Beta"}}},
			{ID: "q2", Text: "Notes?", Kind: userinput.QuestionKindText},
		}},
		Interaction: userinput.TextInteractionState{QuestionIndex: 1, Completed: true, Answers: []userinput.QuestionAnswer{
			{QuestionID: "q1", OptionIDs: []string{"q1.o2"}}, {QuestionID: "q2", Text: "Ship today"},
		}},
	}
	gateway := &fakeChatGateway{advanceResult: userinput.AdvanceTextResult{Handled: true, Request: request}}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1"}})
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		BotID: "bot-1", Channel: channel.ChannelType("weixin"), ReplyTarget: "target-id",
		Message: channel.Message{ID: "msg-2", Text: "Ship today"}, Sender: channel.Identity{SubjectID: "ext-1"},
		Conversation: channel.Conversation{ID: "chat-1", Type: channel.ConversationTypePrivate},
	}
	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: msg.Channel}, msg, sender); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	if gateway.userInputCalls != 1 || gateway.userInputInput.ExplicitID != "input-1" || len(gateway.userInputInput.Answers) != 2 {
		t.Fatalf("response routing = calls:%d input:%#v", gateway.userInputCalls, gateway.userInputInput)
	}
	if len(sender.sent) == 0 {
		t.Fatal("completion summary was not sent")
	}
	summary := sender.sent[0].Message.PlainText()
	for _, want := range []string{"1. Plan?", "Answer: Beta", "2. Notes?", "Answer: Ship today"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestChannelInboundProcessorNativeUserInputBypassesTextFallback(t *testing.T) {
	registry := channel.NewRegistry()
	registry.MustRegister(&nativeUserInputTestAdapter{typ: channel.ChannelType("native-test")})
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{resp: fakeChatResponse{Messages: []turn.ModelMessage{{Role: "assistant", Content: turn.NewTextContent("normal reply")}}}}
	processor := NewChannelInboundProcessor(slog.Default(), registry, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1"}})
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		BotID: "bot-1", Channel: channel.ChannelType("native-test"), ReplyTarget: "target-id",
		Message: channel.Message{Text: "normal chat"}, Sender: channel.Identity{SubjectID: "ext-1"},
		Conversation: channel.Conversation{ID: "chat-1", Type: channel.ConversationTypePrivate},
	}
	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: msg.Channel}, msg, sender); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	if gateway.advanceCalls != 0 || gateway.gotReq.Query != "normal chat" {
		t.Fatalf("native channel routing: advance=%d query=%q", gateway.advanceCalls, gateway.gotReq.Query)
	}
}

func TestChannelInboundProcessorModeCommandBypassesTextFallback(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{resp: fakeChatResponse{Messages: []turn.ModelMessage{{Role: "assistant", Content: turn.NewTextContent("normal reply")}}}}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1"}})
	msg := channel.InboundMessage{
		BotID: "bot-1", Channel: channel.ChannelType("weixin"), ReplyTarget: "target-id",
		Message: channel.Message{Text: "/btw side question"}, Sender: channel.Identity{SubjectID: "ext-1"},
		Conversation: channel.Conversation{ID: "chat-1", Type: channel.ConversationTypePrivate},
	}
	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: msg.Channel}, msg, &fakeReplySender{}); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	if gateway.advanceCalls != 0 {
		t.Fatalf("mode command advanced user input %d times", gateway.advanceCalls)
	}
}

func TestChannelInboundProcessorPlainTextUserInputIgnoresUndirectedGroupMessage(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{advanceResult: userinput.AdvanceTextResult{Handled: true}}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1"}})
	msg := channel.InboundMessage{
		BotID: "bot-1", Channel: channel.ChannelType("wecom"), ReplyTarget: "group-id",
		Message: channel.Message{Text: "1"}, Sender: channel.Identity{SubjectID: "ext-1"},
		Conversation: channel.Conversation{ID: "group-1", Type: channel.ConversationTypeGroup},
	}
	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: msg.Channel}, msg, &fakeReplySender{}); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	if gateway.advanceCalls != 0 {
		t.Fatalf("undirected group message advanced user input %d times", gateway.advanceCalls)
	}
}

// A group message that is directed at the bot (is_mentioned) must reach the
// plain-text ask_user fallback. Guards the DingTalk/WeCom P1 fix: those
// adapters mark @-gated group messages so group answers are not silently
// dropped. Kept as the mirror of the undirected case above.
func TestChannelInboundProcessorPlainTextUserInputHandlesDirectedGroupMessage(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{advanceResult: userinput.AdvanceTextResult{Handled: true}}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1"}})
	msg := channel.InboundMessage{
		BotID: "bot-1", Channel: channel.ChannelType("wecom"), ReplyTarget: "group-id",
		Message:      channel.Message{Text: "1"},
		Sender:       channel.Identity{SubjectID: "ext-1"},
		Conversation: channel.Conversation{ID: "group-1", Type: channel.ConversationTypeGroup},
		Metadata:     map[string]any{"is_mentioned": true},
	}
	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: msg.Channel}, msg, &fakeReplySender{}); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	if gateway.advanceCalls != 1 {
		t.Fatalf("directed group message advanced user input %d times, want 1", gateway.advanceCalls)
	}
}

func TestChannelInboundProcessorRespondReplyUsesReplyTargetAndPreservesAnswer(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"},
		linkedUserIDs: map[string][]string{
			"channelIdentity-1": {"user-1"},
		},
	}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeACPAgent}})
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "msg-1", Text: "/respond Plan B", Reply: &channel.ReplyRef{MessageID: "ask-msg-1"}},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
		Conversation: channel.Conversation{
			ID:   "chat-1",
			Type: channel.ConversationTypePrivate,
		},
	}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	got := gateway.userInputInput
	if got.ExplicitID != "" {
		t.Fatalf("ExplicitID = %q, want empty so reply lookup can run", got.ExplicitID)
	}
	if got.ReplyExternalMessageID != "ask-msg-1" {
		t.Fatalf("ReplyExternalMessageID = %q, want ask-msg-1", got.ReplyExternalMessageID)
	}
	if got.TextAnswer != "Plan B" {
		t.Fatalf("TextAnswer = %q, want original-case answer", got.TextAnswer)
	}
}

func TestChannelInboundProcessorRespondCallbackUsesBoundRequest(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"},
		linkedUserIDs:   map[string][]string{"channelIdentity-1": {"user-1"}},
	}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeChat}})
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{ID: "ask-msg-1", Text: "/respond q1.o2", Reply: &channel.ReplyRef{MessageID: "ask-msg-1"}},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
		Conversation: channel.Conversation{
			ID:   "chat-1",
			Type: channel.ConversationTypePrivate,
		},
		Metadata: map[string]any{"is_mentioned": true, "user_input_id": "input-1"},
	}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	got := gateway.userInputInput
	if got.ExplicitID != "input-1" || got.TextAnswer != "q1.o2" {
		t.Fatalf("user input input = %#v", got)
	}
}

func TestChannelInboundProcessorRespondStructuredAnswersFromWizard(t *testing.T) {
	// Telegram multi-step wizard submits fully structured Answers so multi-
	// question prompts never depend on free-text /respond parsing.
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"},
		linkedUserIDs:   map[string][]string{"channelIdentity-1": {"user-1"}},
	}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeChat}})
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{ID: "ask-msg-1", Text: "/respond", Reply: &channel.ReplyRef{MessageID: "ask-msg-1"}},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
		Conversation: channel.Conversation{
			ID:   "chat-1",
			Type: channel.ConversationTypePrivate,
		},
		Metadata: map[string]any{
			"is_mentioned":  true,
			"user_input_id": "input-1",
			"user_input_answers": []any{
				map[string]any{"question_id": "q1", "text": "写个脚本"},
				map[string]any{"question_id": "q2", "skipped": true},
			},
		},
	}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	got := gateway.userInputInput
	if got.ExplicitID != "input-1" {
		t.Fatalf("ExplicitID = %q", got.ExplicitID)
	}
	if len(got.Answers) != 2 || got.Answers[0].Text != "写个脚本" || !got.Answers[1].Skipped {
		t.Fatalf("Answers = %#v", got.Answers)
	}
}

func TestChannelInboundProcessorAutoCreatesDefaultACPSession(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-acp"},
		linkedUserIDs:   map[string][]string{"channelIdentity-acp": {"account-user-acp"}},
	}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-acp", RouteID: "route-acp"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("ACP reply")},
			},
		},
	}
	ensurer := &fakeSessionEnsurer{activeErr: errors.New("no active session")}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetSessionEnsurer(ensurer)
	processor.SetACPProfileResolver(testACPProfiles{})
	processor.SetDefaultChatRuntime(fakeDefaultChatRuntimeReader{settings: DefaultChatRuntimeSettings{
		Runtime:     sessionpkg.RuntimeACPAgent,
		ACPAgentID:  "codex",
		ProjectPath: "/data/app",
		ProjectMode: sessionpkg.DefaultACPProjectMode,
	}})
	permChecker := &fakeBotPermissionChecker{allowed: true}
	processor.SetBotPermissionChecker(permChecker)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("web")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("web"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
		Conversation: channel.Conversation{
			ID:   "chat-acp",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ensurer.lastSpec.Runtime != sessionpkg.RuntimeACPAgent || ensurer.lastSpec.Type != sessionpkg.TypeACPAgent {
		t.Fatalf("created spec = %#v, want ACP runtime session", ensurer.lastSpec)
	}
	if ensurer.lastSpec.RuntimeOwnerAccountID != "account-user-acp" {
		t.Fatalf("runtime owner = %q, want account user", ensurer.lastSpec.RuntimeOwnerAccountID)
	}
	if ensurer.lastSpec.CreatedByUserID != "account-user-acp" {
		t.Fatalf("created_by_user_id = %q, want account user", ensurer.lastSpec.CreatedByUserID)
	}
	if permChecker.account != "account-user-acp" {
		t.Fatalf("permission principal = %q, want account user", permChecker.account)
	}
	if got := newSessionMetadataString(ensurer.lastSpec.Metadata, "acp_agent_id"); got != "codex" {
		t.Fatalf("acp_agent_id = %q, want codex", got)
	}
	if gateway.gotReq.ThreadID != "created-session" {
		t.Fatalf("StreamChat session = %q, want created-session", gateway.gotReq.ThreadID)
	}
}

func TestChannelInboundProcessorDefaultACPRequiresWorkspaceExec(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-no-exec"},
		linkedUserIDs:   map[string][]string{"channelIdentity-no-exec": {"account-user-no-exec"}},
	}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-acp", RouteID: "route-acp"}}
	gateway := &fakeChatGateway{}
	ensurer := &fakeSessionEnsurer{activeErr: errors.New("no active session")}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetSessionEnsurer(ensurer)
	processor.SetACPProfileResolver(testACPProfiles{})
	processor.SetDefaultChatRuntime(fakeDefaultChatRuntimeReader{settings: DefaultChatRuntimeSettings{
		Runtime:    sessionpkg.RuntimeACPAgent,
		ACPAgentID: "codex",
	}})
	processor.SetBotPermissionChecker(&fakeBotPermissionChecker{allowed: false})
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("web")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("web"),
		Message:     channel.Message{ID: "msg-1", Text: "hello"},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
		Conversation: channel.Conversation{
			ID:   "chat-acp",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ensurer.lastSpec.Runtime != "" {
		t.Fatalf("session should not be created when workspace_exec is missing, got spec %#v", ensurer.lastSpec)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("chat should not run, got query %q", gateway.gotReq.Query)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Message.PlainText(), "permission to run workspace commands") {
		t.Fatalf("expected workspace_exec feedback, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorActiveACPRequiresRuntimeOwner(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-acp"},
		linkedUserIDs:   map[string][]string{"channelIdentity-acp": {"account-user-acp"}},
	}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-acp", RouteID: "route-acp"}}
	gateway := &fakeChatGateway{}
	ensurer := &fakeSessionEnsurer{activeSession: SessionResult{
		ID:      "active-acp-session",
		Type:    sessionpkg.TypeACPAgent,
		Runtime: sessionpkg.RuntimeACPAgent,
	}}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetSessionEnsurer(ensurer)
	processor.SetBotPermissionChecker(&fakeBotPermissionChecker{allowed: true})
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("web")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("web"),
		Message:     channel.Message{ID: "msg-1", Text: "hello"},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
		Conversation: channel.Conversation{
			ID:   "chat-acp",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("chat should not run without runtime owner, got query %q", gateway.gotReq.Query)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Message.PlainText(), "runtime owner") {
		t.Fatalf("expected runtime owner feedback, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorActiveACPRequiresCurrentActorOwnerOrManage(t *testing.T) {
	const (
		ownerID   = "account-owner"
		actorID   = "account-other"
		managerID = "account-manager"
	)

	t.Run("workspace exec non-owner cannot drive runtime", func(t *testing.T) {
		channelIdentitySvc := &fakeChannelIdentityService{
			channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-other"},
			linkedUserIDs:   map[string][]string{"channelIdentity-other": {actorID}},
		}
		chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-acp", RouteID: "route-acp"}}
		gateway := &fakeChatGateway{}
		ensurer := &fakeSessionEnsurer{activeSession: SessionResult{
			ID:                    "active-acp-session",
			Type:                  sessionpkg.TypeACPAgent,
			Runtime:               sessionpkg.RuntimeACPAgent,
			RuntimeOwnerAccountID: ownerID,
		}}
		processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
		processor.SetSessionEnsurer(ensurer)
		processor.SetBotPermissionChecker(&fakeBotPermissionChecker{values: map[string]bool{
			"bot-1:" + ownerID + ":" + bots.PermissionWorkspaceExec: true,
			"bot-1:" + actorID + ":" + bots.PermissionWorkspaceExec: true,
		}})
		sender := &fakeReplySender{}

		cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("web")}
		msg := channel.InboundMessage{
			BotID:       "bot-1",
			Channel:     channel.ChannelType("web"),
			Message:     channel.Message{ID: "msg-1", Text: "hello"},
			ReplyTarget: "target-id",
			Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
			Conversation: channel.Conversation{
				ID:   "chat-acp",
				Type: channel.ConversationTypePrivate,
			},
		}

		if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gateway.gotReq.Query != "" {
			t.Fatalf("chat should not run for non-owner actor, got query %q", gateway.gotReq.Query)
		}
		if len(sender.sent) != 1 {
			t.Fatalf("expected ACP feedback, got %+v", sender.sent)
		}
	})

	t.Run("manager cannot drive another user's runtime", func(t *testing.T) {
		channelIdentitySvc := &fakeChannelIdentityService{
			channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-manager"},
			linkedUserIDs:   map[string][]string{"channelIdentity-manager": {managerID}},
		}
		chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-acp", RouteID: "route-acp"}}
		gateway := &fakeChatGateway{
			resp: fakeChatResponse{Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("ok")},
			}},
		}
		ensurer := &fakeSessionEnsurer{activeSession: SessionResult{
			ID:                    "active-acp-session",
			Type:                  sessionpkg.TypeACPAgent,
			Runtime:               sessionpkg.RuntimeACPAgent,
			RuntimeOwnerAccountID: ownerID,
		}}
		processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
		processor.SetSessionEnsurer(ensurer)
		processor.SetBotPermissionChecker(&fakeBotPermissionChecker{values: map[string]bool{
			"bot-1:" + ownerID + ":" + bots.PermissionWorkspaceExec: true,
			"bot-1:" + managerID + ":" + bots.PermissionManage:      true,
		}})
		sender := &fakeReplySender{}

		cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("web")}
		msg := channel.InboundMessage{
			BotID:       "bot-1",
			Channel:     channel.ChannelType("web"),
			Message:     channel.Message{ID: "msg-1", Text: "hello"},
			ReplyTarget: "target-id",
			Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "Manager"},
			Conversation: channel.Conversation{
				ID:   "chat-acp",
				Type: channel.ConversationTypePrivate,
			},
		}

		if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gateway.gotReq.Query != "" {
			t.Fatalf("chat should not run for manager on another user's runtime, got query %q", gateway.gotReq.Query)
		}
		if len(sender.sent) != 1 {
			t.Fatalf("expected ACP feedback, got %+v", sender.sent)
		}
	})
}

func TestChannelInboundProcessorDiscussACPAllowsNonOwnerMemberThroughACL(t *testing.T) {
	const (
		ownerID = "account-owner"
		actorID = "account-member"
	)
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-member"},
		linkedUserIDs:   map[string][]string{"channelIdentity-member": {actorID}},
	}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-discuss", RouteID: "route-discuss"}}
	gateway := &fakeChatGateway{}
	ensurer := &fakeSessionEnsurer{activeSession: SessionResult{
		ID:                    "active-discuss-acp-session",
		Type:                  sessionpkg.TypeDiscuss,
		Runtime:               sessionpkg.RuntimeACPAgent,
		RuntimeOwnerAccountID: ownerID,
	}}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
	processor.SetSessionEnsurer(ensurer)
	processor.SetBotPermissionChecker(&fakeBotPermissionChecker{values: map[string]bool{
		"bot-1:" + ownerID + ":" + bots.PermissionWorkspaceExec: true,
		"bot-1:" + actorID + ":" + bots.PermissionWorkspaceExec: true,
	}})
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	driver := discuss.NewDiscussDriver(discuss.DiscussDriverDeps{})
	defer driver.StopAll()
	processor.SetPipeline(pipeline, nil, driver)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "msg-1", Text: "@bot hello"},
		ReplyTarget: "group-target",
		Sender:      channel.Identity{SubjectID: "ext-member", DisplayName: "Member"},
		Conversation: channel.Conversation{
			ID:   "group-discuss",
			Type: channel.ConversationTypeGroup,
			Name: "Group",
		},
		Metadata: map[string]any{
			"is_mentioned": true,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("non-owner group member should not receive ACP owner feedback in discuss mode, got %+v", sender.sent)
	}
	if !driver.HasSession("active-discuss-acp-session") {
		t.Fatalf("discuss driver did not receive the ACP discuss session")
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("discuss mode should not run the regular chat gateway, got query %q", gateway.gotReq.Query)
	}
}

func TestChannelInboundProcessorIssueRuntimeBearerTokenUsesRuntimeOwner(t *testing.T) {
	const secret = "runtime-token-secret"
	processor := NewChannelInboundProcessor(
		slog.Default(),
		nil,
		nil,
		nil,
		nil,
		nil,
		&fakePolicyService{ownerUserID: "bot-owner"},
		secret,
		0,
	)
	identity := InboundIdentity{BotID: "bot-1", UserID: "caller-user"}

	channelToken := processor.issueChannelBearerToken(context.Background(), identity, "")
	if got := jwtBearerSubject(t, channelToken, secret); got != "bot-owner" {
		t.Fatalf("channel token subject = %q, want bot-owner", got)
	}

	runtimeToken := processor.issueRuntimeBearerToken(context.Background(), identity, "runtime-owner", "")
	if got := jwtBearerSubject(t, runtimeToken, secret); got != "runtime-owner" {
		t.Fatalf("runtime token subject = %q, want runtime-owner", got)
	}

	modelSessionToken := processor.issueSessionBearerToken(context.Background(), identity, SessionResult{Type: sessionpkg.TypeDiscuss, Runtime: sessionpkg.RuntimeModel}, "", "")
	if got := jwtBearerSubject(t, modelSessionToken, secret); got != "bot-owner" {
		t.Fatalf("model discuss session token subject = %q, want bot-owner", got)
	}

	acpSessionToken := processor.issueSessionBearerToken(context.Background(), identity, SessionResult{Type: sessionpkg.TypeDiscuss, Runtime: sessionpkg.RuntimeACPAgent}, "runtime-owner", "")
	if got := jwtBearerSubject(t, acpSessionToken, secret); got != "runtime-owner" {
		t.Fatalf("ACP discuss session token subject = %q, want runtime-owner", got)
	}
}

func TestChannelInboundProcessorDeniedByACL(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-2"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-denied", RouteID: "route-denied"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	aclSvc := &fakeChatACL{allowed: false}
	processor.SetACLService(aclSvc)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "stranger-1"},
		Conversation: channel.Conversation{
			ID:   "chat-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "" {
		t.Error("denied user should not trigger chat call")
	}
}

func TestChannelInboundProcessorACLDeniedManagerMessageDoesNotSuggestLink(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-manager"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-denied", RouteID: "route-denied"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: false})
	processor.SetCommandHandler(command.NewHandler(
		nil,
		&fakeCommandRoleResolver{role: "manager"},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	))
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "manager-1"},
		Conversation: channel.Conversation{
			ID:   "chat-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "" {
		t.Error("denied manager should not trigger chat call")
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one denial reply, got: %+v", sender.sent)
	}
	reply := sender.sent[0].Message.PlainText()
	if !strings.Contains(reply, "manage this bot") {
		t.Fatalf("expected manager-specific denial reply, got: %s", reply)
	}
	if strings.Contains(reply, "/link") || strings.Contains(reply, "connection code") {
		t.Fatalf("manager denial must not suggest account linking, got: %s", reply)
	}
}

func TestChannelInboundProcessorACLGuestDeniedDowngradesToNotify(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-acl-deny"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-acl", RouteID: "route-acl"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	aclSvc := &fakeChatACL{allowed: false}
	processor.SetACLService(aclSvc)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "guest-1"},
		Conversation: channel.Conversation{
			ID:   "chat-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if aclSvc.calls != 1 {
		t.Fatalf("expected acl to be checked once, got %d", aclSvc.calls)
	}
	if aclSvc.lastReq.ChannelType != "feishu" ||
		aclSvc.lastReq.SourceScope.ConversationType != channel.ConversationTypePrivate ||
		aclSvc.lastReq.SourceScope.ConversationID != "chat-1" {
		t.Fatalf("unexpected acl evaluate request: %+v", aclSvc.lastReq)
	}
	if gateway.gotReq.Query != "" {
		t.Fatal("ACL denied guest should not trigger chat call")
	}
	// A directed (DM) denied message is no longer silently dropped: the sender
	// gets one access/bind hint so a forgetful owner can /link in.
	if len(sender.sent) != 1 {
		t.Fatalf("ACL denied guest (DM) should receive one access hint reply, got %+v", sender.sent)
	}
	if !strings.Contains(sender.sent[0].Message.Text, "/link") {
		t.Fatalf("expected access/bind hint mentioning /link, got %q", sender.sent[0].Message.Text)
	}
	if len(chatSvc.persistedIn) != 1 {
		t.Fatalf("ACL denied guest should persist 1 passive message (replacing inbox), got %d", len(chatSvc.persistedIn))
	}
	if chatSvc.persistedIn[0].Role != "user" {
		t.Fatalf("passive message role should be user, got %q", chatSvc.persistedIn[0].Role)
	}
}

func TestChannelInboundProcessorACLReceivesThreadScope(t *testing.T) {
	cases := []struct {
		name string
		msg  channel.InboundMessage
	}{
		{
			name: "conversation thread id",
			msg: channel.InboundMessage{
				BotID:       "bot-1",
				Channel:     channel.ChannelType("discord"),
				Message:     channel.Message{Text: "hello"},
				ReplyTarget: "discord:thread-1",
				Sender:      channel.Identity{SubjectID: "guest-thread"},
				Conversation: channel.Conversation{
					ID:       "guild-chat-1",
					Type:     channel.ConversationTypeThread,
					ThreadID: "thread-1",
				},
				Metadata: map[string]any{
					"is_mentioned": true,
				},
			},
		},
		{
			name: "message thread compatibility",
			msg: channel.InboundMessage{
				BotID:       "bot-1",
				Channel:     channel.ChannelType("discord"),
				Message:     channel.Message{Text: "hello", Thread: &channel.ThreadRef{ID: "thread-1"}},
				ReplyTarget: "discord:thread-1",
				Sender:      channel.Identity{SubjectID: "guest-thread"},
				Conversation: channel.Conversation{
					ID:   "guild-chat-1",
					Type: channel.ConversationTypeThread,
				},
				Metadata: map[string]any{
					"is_mentioned": true,
				},
			},
		},
		{
			name: "message thread wins over conversation compatibility fallback",
			msg: channel.InboundMessage{
				BotID:       "bot-1",
				Channel:     channel.ChannelType("discord"),
				Message:     channel.Message{Text: "hello", Thread: &channel.ThreadRef{ID: "thread-1"}},
				ReplyTarget: "discord:thread-1",
				Sender:      channel.Identity{SubjectID: "guest-thread"},
				Conversation: channel.Conversation{
					ID:       "guild-chat-1",
					Type:     channel.ConversationTypeThread,
					ThreadID: "thread-from-conversation",
				},
				Metadata: map[string]any{
					"is_mentioned": true,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-thread-scope"}}
			policySvc := &fakePolicyService{}
			chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-thread", RouteID: "route-thread"}}
			gateway := &fakeChatGateway{}
			processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
			aclSvc := &fakeChatACL{allowed: false}
			processor.SetACLService(aclSvc)
			sender := &fakeReplySender{}

			if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1"}, tc.msg, sender); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if aclSvc.calls != 1 {
				t.Fatalf("expected acl to be checked once, got %d", aclSvc.calls)
			}
			if aclSvc.lastReq.ChannelType != "discord" ||
				aclSvc.lastReq.SourceScope.ConversationType != channel.ConversationTypeThread ||
				aclSvc.lastReq.SourceScope.ConversationID != "guild-chat-1" ||
				aclSvc.lastReq.SourceScope.ThreadID != "thread-1" {
				t.Fatalf("unexpected thread acl evaluate request: %+v", aclSvc.lastReq)
			}
		})
	}
}

func TestChannelInboundProcessorQQAndWeixinWriteCommandsNeedLinkedManager(t *testing.T) {
	for _, channelType := range []channel.ChannelType{channel.ChannelType("qq"), channel.ChannelType("weixin")} {
		t.Run(channelType.String(), func(t *testing.T) {
			channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-im-command"}}
			policySvc := &fakePolicyService{}
			chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-im-command", RouteID: "route-im-command"}}
			gateway := &fakeChatGateway{}
			processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
			aclSvc := &fakeChatACL{allowed: false}
			processor.SetACLService(aclSvc)
			processor.SetCommandHandler(command.NewHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
			sender := &fakeReplySender{}

			msg := channel.InboundMessage{
				BotID:       "bot-1",
				Channel:     channelType,
				Message:     channel.Message{Text: "/model set"},
				ReplyTarget: "target-id",
				Sender:      channel.Identity{SubjectID: "im-user-1"},
				Conversation: channel.Conversation{
					ID:   "im-user-1",
					Type: channel.ConversationTypePrivate,
				},
			}

			if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channelType}, msg, sender); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gateway.gotReq.Query != "" {
				t.Fatalf("slash command should not trigger chat call, got query %q", gateway.gotReq.Query)
			}
			if len(sender.sent) != 1 {
				t.Fatalf("expected one command reply, got %d", len(sender.sent))
			}
			if !strings.Contains(sender.sent[0].Message.Text, "Only the bot owner") {
				t.Fatalf("expected write command denial, got %q", sender.sent[0].Message.Text)
			}
		})
	}
}

func TestChannelInboundProcessorRejectsDirectSkillBeforeAutoDiscussSession(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-skill-use-group"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-skill-use-group", RouteID: "route-skill-use-group"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	aclSvc := &fakeChatACL{allowed: true}
	processor.SetACLService(aclSvc)
	ensurer := &fakeSessionEnsurer{activeErr: errors.New("no active session")}
	processor.SetSessionEnsurer(ensurer)
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{ID: "msg-1", Text: "@bot /alpha hello"},
		ReplyTarget: "telegram:group-1",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "group-1",
			Type: channel.ConversationTypeGroup,
		},
		Metadata: map[string]any{
			"is_mentioned": true,
			"bot_alias":    "bot",
		},
	}

	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("skill slash should not trigger chat call, got query %q", gateway.gotReq.Query)
	}
	if ensurer.lastSpec.Type != "" {
		t.Fatalf("skill slash should not create discuss session, got spec %+v", ensurer.lastSpec)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Message.PlainText(), "not supported") {
		t.Fatalf("expected unsupported skill slash reply, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorRejectsDirectSkillBeforeActiveStreamInjection(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-skill-use-active"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-skill-use-active", RouteID: "route-skill-use-active"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeChat, Runtime: sessionpkg.RuntimeModel}})
	dispatcher := NewRouteDispatcher(slog.Default())
	dispatcher.MarkActive("route-skill-use-active")
	processor.SetDispatcher(dispatcher)
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{ID: "msg-1", Text: "@bot /alpha hello"},
		ReplyTarget: "telegram:group-1",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "group-1",
			Type: channel.ConversationTypeGroup,
		},
		Metadata: map[string]any{
			"is_mentioned": true,
			"bot_alias":    "bot",
		},
	}

	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("skill slash should not trigger chat call, got query %q", gateway.gotReq.Query)
	}
	if len(chatSvc.persistedIn) != 0 {
		t.Fatalf("skill slash should not persist before active-stream reject, got %+v", chatSvc.persistedIn)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Message.PlainText(), "not supported") {
		t.Fatalf("expected unsupported skill slash reply, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorRejectsDirectSkillDuringContinuationStream(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-skill-use-continuation"},
		linkedUserIDs:   map[string][]string{"channelIdentity-skill-use-continuation": {"user-1"}},
	}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-skill-use-continuation", RouteID: "route-skill-use-continuation"}}
	gateway := &fakeChatGateway{
		userInputStarted: make(chan struct{}),
		userInputRelease: make(chan struct{}),
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeChat, Runtime: sessionpkg.RuntimeModel}})
	processor.SetDispatcher(NewRouteDispatcher(slog.Default()))
	skillResolver := &fakeRequestedSkillResolver{items: []skillset.ResolvedSkill{{Name: "alpha", Content: "alpha skill content"}}}
	processor.SetRequestedSkillResolver(skillResolver)
	sender := &fakeReplySender{}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}
	baseMsg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		ReplyTarget: "telegram:group-1",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "group-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	done := make(chan error, 1)
	go func() {
		msg := baseMsg
		msg.Message = channel.Message{ID: "msg-respond", Text: "/respond input-1 Plan B"}
		done <- processor.HandleInbound(context.Background(), cfg, msg, sender)
	}()

	<-gateway.userInputStarted
	msg := baseMsg
	msg.Message = channel.Message{ID: "msg-skill", Text: "/alpha hello"}
	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("skill slash HandleInbound() error = %v", err)
	}
	close(gateway.userInputRelease)
	if err := <-done; err != nil {
		t.Fatalf("respond HandleInbound() error = %v", err)
	}
	if skillResolver.calls != 0 {
		t.Fatalf("skill resolver calls = %d, want 0 during active continuation", skillResolver.calls)
	}
	if gateway.gotReq.BotID != "" {
		t.Fatalf("ordinary chat should not run during active continuation, got request %#v", gateway.gotReq)
	}
	if len(sender.sent) == 0 || !strings.Contains(sender.sent[0].Message.PlainText(), "not supported") {
		t.Fatalf("expected unsupported skill slash reply, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorDirectSkillStartsStreamWithDispatcherInjectCh(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-skill-use-dispatch"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-skill-use-dispatch", RouteID: "route-skill-use-dispatch"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeChat, Runtime: sessionpkg.RuntimeModel}})
	processor.SetDispatcher(NewRouteDispatcher(slog.Default()))
	skillResolver := &fakeRequestedSkillResolver{items: []skillset.ResolvedSkill{{
		Name:       "alpha",
		Content:    "alpha skill content",
		SourceKind: "managed",
		Identity:   "managed|alpha|managed|opaque-alpha",
	}}}
	processor.SetRequestedSkillResolver(skillResolver)
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{ID: "msg-1", Text: "@bot /alpha hello"},
		ReplyTarget: "telegram:group-1",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "group-1",
			Type: channel.ConversationTypeGroup,
		},
		Metadata: map[string]any{
			"is_mentioned": true,
			"bot_alias":    "bot",
		},
	}

	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skillResolver.calls != 1 || skillResolver.botID != "bot-1" || len(skillResolver.names) != 1 || skillResolver.names[0] != "alpha" {
		t.Fatalf("skill resolver call = (%d, %q, %#v), want bot-1 alpha", skillResolver.calls, skillResolver.botID, skillResolver.names)
	}
	if gateway.gotReq.Query != "hello" {
		t.Fatalf("StreamChat query = %q, want hello", gateway.gotReq.Query)
	}
	if gateway.gotReq.UserMessageKind != turn.UserMessageKindSkillActivation {
		t.Fatalf("UserMessageKind = %q, want skill_activation", gateway.gotReq.UserMessageKind)
	}
	if gateway.gotReq.SkillActivation == nil || len(gateway.gotReq.SkillActivation.Skills) != 1 || gateway.gotReq.SkillActivation.Skills[0].Name != "alpha" {
		t.Fatalf("SkillActivation = %#v, want alpha", gateway.gotReq.SkillActivation)
	}
	if len(gateway.gotReq.RequestedSkills) != 1 || gateway.gotReq.RequestedSkills[0].Name != "alpha" || gateway.gotReq.RequestedSkills[0].Content != "alpha skill content" {
		t.Fatalf("StreamChat requested skills = %#v, want resolved alpha", gateway.gotReq.RequestedSkills)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("skill slash stream should not send slash error reply, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorDirectSkillAllowsEmptyPrompt(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-skill-empty-prompt"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-skill-empty-prompt", RouteID: "route-skill-empty-prompt"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeChat, Runtime: sessionpkg.RuntimeModel}})
	skillResolver := &fakeRequestedSkillResolver{items: []skillset.ResolvedSkill{{
		Name:       "alpha",
		Content:    "alpha skill content",
		SourceKind: "managed",
		Identity:   "managed|alpha|managed|opaque-alpha",
	}}}
	processor.SetRequestedSkillResolver(skillResolver)
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{ID: "msg-1", Text: "/alpha"},
		ReplyTarget: "telegram:dm-1",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skillResolver.calls != 1 {
		t.Fatalf("skill resolver calls = %d, want 1", skillResolver.calls)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("StreamChat query = %q, want empty visible prompt", gateway.gotReq.Query)
	}
	if !strings.Contains(gateway.gotReq.ModelQuery, "activated the following skill") {
		t.Fatalf("StreamChat model query = %q, want activation marker", gateway.gotReq.ModelQuery)
	}
	if !gateway.gotReq.SkipMemoryExtraction || !gateway.gotReq.SkipTitleGeneration {
		t.Fatalf("empty-prompt activation should skip memory/title, got memory=%v title=%v", gateway.gotReq.SkipMemoryExtraction, gateway.gotReq.SkipTitleGeneration)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("skill slash stream should not send slash error reply, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorDirectSkillRejectsReplyWithUnknownAttachmentState(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-skill-reply-unknown"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-skill-reply-unknown", RouteID: "route-skill-reply-unknown"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeChat, Runtime: sessionpkg.RuntimeModel}})
	skillResolver := &fakeRequestedSkillResolver{items: []skillset.ResolvedSkill{{
		Name:       "alpha",
		Content:    "alpha skill content",
		SourceKind: "managed",
		Identity:   "managed|alpha|managed|opaque-alpha",
	}}}
	processor.SetRequestedSkillResolver(skillResolver)
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("telegram"),
		Message: channel.Message{
			ID:    "msg-1",
			Text:  "/alpha",
			Reply: &channel.ReplyRef{MessageID: "source-msg"},
		},
		ReplyTarget: "telegram:dm-1",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skillResolver.calls != 0 {
		t.Fatalf("skill resolver calls = %d, want 0 before reply attachment state is known", skillResolver.calls)
	}
	if gateway.gotReq.BotID != "" {
		t.Fatalf("StreamChat should not run for slash reply with unknown attachment state, got %#v", gateway.gotReq)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Message.PlainText(), "attachment") {
		t.Fatalf("expected attachment slash error reply, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorDirectSkillAllowsReplyKnownWithoutAttachments(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-skill-reply-known"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-skill-reply-known", RouteID: "route-skill-reply-known"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeChat, Runtime: sessionpkg.RuntimeModel}})
	skillResolver := &fakeRequestedSkillResolver{items: []skillset.ResolvedSkill{{
		Name:       "alpha",
		Content:    "alpha skill content",
		SourceKind: "managed",
		Identity:   "managed|alpha|managed|opaque-alpha",
	}}}
	processor.SetRequestedSkillResolver(skillResolver)
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("telegram"),
		Message: channel.Message{
			ID:    "msg-1",
			Text:  "/alpha",
			Reply: &channel.ReplyRef{MessageID: "source-msg", AttachmentsKnown: true},
		},
		ReplyTarget: "telegram:dm-1",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skillResolver.calls != 1 {
		t.Fatalf("skill resolver calls = %d, want 1", skillResolver.calls)
	}
	if gateway.gotReq.SourceReplyToMessageID != "source-msg" {
		t.Fatalf("SourceReplyToMessageID = %q, want source-msg", gateway.gotReq.SourceReplyToMessageID)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("skill slash stream should not send slash error reply, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorDirectSkillRejectsForwardWithUnknownAttachmentState(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-skill-forward-unknown"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-skill-forward-unknown", RouteID: "route-skill-forward-unknown"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetACLService(&fakeChatACL{allowed: true})
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-1", Type: sessionpkg.TypeChat, Runtime: sessionpkg.RuntimeModel}})
	skillResolver := &fakeRequestedSkillResolver{items: []skillset.ResolvedSkill{{
		Name:       "alpha",
		Content:    "alpha skill content",
		SourceKind: "managed",
		Identity:   "managed|alpha|managed|opaque-alpha",
	}}}
	processor.SetRequestedSkillResolver(skillResolver)
	sender := &fakeReplySender{}

	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("misskey"),
		Message: channel.Message{
			ID:      "msg-1",
			Text:    "/alpha",
			Forward: &channel.ForwardRef{MessageID: "forward-msg"},
		},
		ReplyTarget: "misskey:note-1",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "note-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("misskey")}, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skillResolver.calls != 0 {
		t.Fatalf("skill resolver calls = %d, want 0 before forward attachment state is known", skillResolver.calls)
	}
	if gateway.gotReq.BotID != "" {
		t.Fatalf("StreamChat should not run for slash forward with unknown attachment state, got %#v", gateway.gotReq)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Message.PlainText(), "attachment") {
		t.Fatalf("expected attachment slash error reply, got %+v", sender.sent)
	}
}

func TestChannelInboundProcessorIgnoreEmpty(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-3"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1"}
	msg := channel.InboundMessage{Message: channel.Message{Text: "  "}}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("empty message should not error: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("empty message should not produce reply: %+v", sender.sent)
	}
	if gateway.gotReq.Query != "" {
		t.Error("empty message should not trigger chat call")
	}
}

func TestChannelInboundProcessorStatusUsesRouteSession(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-status"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-status", RouteID: "route-status"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetSessionEnsurer(&fakeSessionEnsurer{
		activeSession: SessionResult{ID: "11111111-1111-1111-1111-111111111111", Type: "chat"},
	})
	cmdQueries := &fakeCommandQueries{
		messageCount: 9,
		usage:        512,
		cacheRow: dbsqlc.GetSessionCacheStatsRow{
			CacheReadTokens:  64,
			TotalInputTokens: 512,
		},
		skills: []string{"search"},
	}
	processor.SetCommandHandler(command.NewHandler(
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cmdQueries,
		nil,
		nil,
		nil,
	))
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("discord")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("discord"),
		Message:     channel.Message{Text: "/status"},
		ReplyTarget: "discord:status",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "conv-status",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one status reply, got %d", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0].Message.Text, "Session Status — current conversation") {
		t.Fatalf("expected current conversation scope, got %q", sender.sent[0].Message.Text)
	}
	// Session ID is no longer echoed into user-facing output; assert directly that
	// the route's active session drove the status query.
	if got, _ := cmdQueries.gotCountSession.Value(); got != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("expected active route session to drive status query, got %v", got)
	}
}

func TestBuildInboundQueryAttachmentOnlyReturnsEmpty(t *testing.T) {
	t.Parallel()

	msg := channel.Message{
		Attachments: []channel.Attachment{
			{Type: channel.AttachmentImage},
			{Type: channel.AttachmentImage},
		},
	}
	if got := strings.TrimSpace(msg.PlainText()); got != "" {
		t.Fatalf("expected empty query for attachment-only message, got %q", got)
	}
}

func TestChannelInboundProcessorDirectedModeCommandPermissionDeniedReplies(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-denied-status"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-denied-status", RouteID: "route-denied-status"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetCommandHandler(command.NewHandler(
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		&fakeChatACL{allowed: false},
		nil,
		nil,
	))
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("discord")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("discord"),
		Message:     channel.Message{ID: "msg-denied-status", Text: "/status"},
		ReplyTarget: "discord:denied-status",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "conv-denied-status",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one permission-denied reply, got %d", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0].Message.Text, "permission") {
		t.Fatalf("expected permission denied slash error, got %q", sender.sent[0].Message.Text)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("denied command should not trigger chat, got query %q", gateway.gotReq.Query)
	}
	if len(chatSvc.persisted) != 0 {
		t.Fatalf("denied command should not persist passive message, got %d persisted", len(chatSvc.persisted))
	}
}

func TestChannelInboundProcessorAttachmentOnlyUsesFallbackQuery(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-fallback"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-fallback", RouteID: "route-fallback"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("AI reply")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}
	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("telegram"),
		Message: channel.Message{
			Attachments: []channel.Attachment{
				{Type: channel.AttachmentImage, URL: "https://example.com/a.png"},
				{Type: channel.AttachmentImage, URL: "https://example.com/b.png"},
			},
		},
		ReplyTarget: "chat-123",
		Sender:      channel.Identity{SubjectID: "ext-1"},
		Conversation: channel.Conversation{
			ID:   "conv-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("expected empty query for attachment-only message, got %q", gateway.gotReq.Query)
	}
	if len(gateway.gotReq.Attachments) != 2 {
		t.Fatalf("expected attachments to pass through, got %d", len(gateway.gotReq.Attachments))
	}
}

func TestChannelInboundProcessorSilentReply(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-4"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-4", RouteID: "route-4"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("NO_REPLY")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1"}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{Text: "test"},
		ReplyTarget: "chat-123",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "conv-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("NO_REPLY should suppress output: %+v", sender.sent)
	}
}

func TestBuildChannelMessageKeepsReasoningTextMarkdownOnRichChannels(t *testing.T) {
	msg := buildChannelMessage(turn.AssistantOutput{
		Content: "**bold**",
		Parts: []turn.ContentPart{
			{Type: "text", Text: "**bold**"},
		},
	}, channel.ChannelCapabilities{Text: true, Markdown: true, RichText: true})

	if len(msg.Parts) != 0 {
		t.Fatalf("plain text content part should not be promoted to rich Parts: %#v", msg.Parts)
	}
	if msg.Text != "**bold**" {
		t.Fatalf("Text = %q, want markdown source", msg.Text)
	}
	if msg.Format != channel.MessageFormatMarkdown {
		t.Fatalf("Format = %q, want markdown", msg.Format)
	}
}

func TestBuildChannelMessagePromotesStyledPartWithoutDuplicateText(t *testing.T) {
	msg := buildChannelMessage(turn.AssistantOutput{
		Content: "bold",
		Parts: []turn.ContentPart{
			{Type: "text", Text: "bold", Styles: []string{"bold"}},
		},
	}, channel.ChannelCapabilities{Text: true, Markdown: true, RichText: true})

	if msg.Text != "" {
		t.Fatalf("rich Parts message should not keep duplicate Text, got %q", msg.Text)
	}
	if msg.Format != channel.MessageFormatRich {
		t.Fatalf("Format = %q, want rich", msg.Format)
	}
	if len(msg.Parts) != 1 {
		t.Fatalf("Parts len = %d, want 1", len(msg.Parts))
	}
	if got := msg.Parts[0]; got.Type != channel.MessagePartText || got.Text != "bold" || len(got.Styles) != 1 || got.Styles[0] != channel.MessageStyleBold {
		t.Fatalf("unexpected rich part: %#v", got)
	}
}

func TestChannelInboundProcessorGroupPassiveSync(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-5"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-5", RouteID: "route-5"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("AI reply")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1"}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "msg-1", Text: "hello everyone"},
		ReplyTarget: "chat_id:oc_123",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "oc_123",
			Type: "group",
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("group passive sync should not trigger chat call")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("group passive sync should not send reply: %+v", sender.sent)
	}
	if len(chatSvc.persisted) != 1 {
		t.Fatalf("group passive sync should persist 1 passive message (replacing inbox), got: %d", len(chatSvc.persisted))
	}
	if chatSvc.persisted[0].Role != "user" {
		t.Fatalf("passive message role should be user, got %q", chatSvc.persisted[0].Role)
	}
}

func TestChannelInboundProcessorGroupMentionTriggersReply(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-6"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-6", RouteID: "route-6"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("AI reply")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1"}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "msg-2", Text: "@bot ping"},
		ReplyTarget: "chat_id:oc_123",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "oc_123",
			Type: "group",
		},
		Metadata: map[string]any{
			"is_mentioned": true,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query == "" {
		t.Fatalf("group mention should trigger chat call")
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one outbound reply, got %d", len(sender.sent))
	}
	if gateway.gotReq.UserMessagePersisted {
		t.Fatalf("expected UserMessagePersisted=false: user message persistence is deferred to storeRound")
	}
	if !gateway.gotReq.MentionsBot {
		t.Fatalf("expected is_mentioned metadata to be carried into ChatRequest")
	}
}

type failingOpenStreamSender struct {
	err error
}

func (*failingOpenStreamSender) Send(_ context.Context, _ channel.OutboundMessage) error {
	return nil
}

func (s *failingOpenStreamSender) OpenStream(_ context.Context, _ string, _ channel.StreamOptions) (channel.OutboundStream, error) {
	if s != nil && s.err != nil {
		return nil, s.err
	}
	return nil, errors.New("open stream failed")
}

type failingCloseSender struct {
	err error
}

func (*failingCloseSender) Send(_ context.Context, _ channel.OutboundMessage) error {
	return nil
}

func (s *failingCloseSender) OpenStream(_ context.Context, target string, _ channel.StreamOptions) (channel.OutboundStream, error) {
	return &failingCloseStream{target: strings.TrimSpace(target), err: s.err}, nil
}

type failingCloseStream struct {
	target string
	err    error
}

func (*failingCloseStream) Push(_ context.Context, _ channel.StreamEvent) error {
	return nil
}

func (s *failingCloseStream) Close(_ context.Context) error {
	if s != nil && s.err != nil {
		return s.err
	}
	return errors.New("close stream failed")
}

func TestChannelInboundProcessorDoesNotPersistBeforeOpenStream(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-openstream"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-openstream", RouteID: "route-openstream"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &failingOpenStreamSender{err: errors.New("stream unavailable")}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-openstream", BotID: "bot-1", ChannelType: channel.ChannelType("qq")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("qq"),
		Message:     channel.Message{ID: "msg-openstream-1", Text: "hello"},
		ReplyTarget: "c2c:user-openid",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "conv-openstream",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err == nil || err.Error() != "stream unavailable" {
		t.Fatalf("expected open stream error, got: %v", err)
	}
	if len(chatSvc.persistedIn) != 0 {
		t.Fatalf("user message persistence is deferred to storeRound; expected 0 persisted, got %d", len(chatSvc.persistedIn))
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("runner should not be called when stream open fails")
	}
}

func TestChannelInboundProcessorReturnsCloseStreamError(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-closestream"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-closestream", RouteID: "route-closestream"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("ok")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &failingCloseSender{err: errors.New("wechat send failed")}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-closestream", BotID: "bot-1", ChannelType: channel.ChannelType("wechatoa")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("wechatoa"),
		Message:     channel.Message{ID: "msg-closestream-1", Text: "hello"},
		ReplyTarget: "openid:user-openid",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "conv-closestream",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err == nil || err.Error() != "wechat send failed" {
		t.Fatalf("expected close stream error, got: %v", err)
	}
}

func TestChannelInboundProcessorPersistsAttachmentAssetRefs(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-asset"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-asset", RouteID: "route-asset"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("ok")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-asset", BotID: "bot-1"}
	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("feishu"),
		Message: channel.Message{
			ID:   "msg-asset-1",
			Text: "attachment test",
			Attachments: []channel.Attachment{
				{
					Type:        channel.AttachmentImage,
					URL:         "https://example.com/img.png",
					ContentHash: "asset-1",
					Name:        "img.png",
					Mime:        "image/png",
				},
			},
		},
		ReplyTarget: "chat_id:oc_asset",
		Sender:      channel.Identity{SubjectID: "ext-asset"},
		Conversation: channel.Conversation{
			ID:   "oc_asset",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chatSvc.persistedIn) != 0 {
		t.Fatalf("user message persistence is deferred to storeRound; expected 0 persisted, got %d", len(chatSvc.persistedIn))
	}
	if len(gateway.gotReq.Attachments) != 1 {
		t.Fatalf("expected one gateway attachment, got %d", len(gateway.gotReq.Attachments))
	}
	if got := gateway.gotReq.Attachments[0].ContentHash; got != "asset-1" {
		t.Fatalf("expected gateway attachment content_hash asset-1, got %q", got)
	}
}

func TestChannelInboundProcessorIngestsPlatformKeyWithResolver(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-resolver"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-resolver", RouteID: "route-resolver"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("ok")},
			},
		},
	}
	registry := channel.NewRegistry()
	registry.MustRegister(&fakeAttachmentResolverAdapter{})
	processor := NewChannelInboundProcessor(slog.Default(), registry, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	mediaSvc := &fakeMediaIngestor{nextID: "asset-resolved-1", nextMime: "application/octet-stream"}
	processor.SetMediaService(mediaSvc)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-resolver", BotID: "bot-1", ChannelType: channel.ChannelType("resolver-test")}
	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("resolver-test"),
		Message: channel.Message{
			ID:   "msg-resolver-1",
			Text: "attachment resolver test",
			Attachments: []channel.Attachment{
				{
					Type:        channel.AttachmentFile,
					PlatformKey: "platform-file-1",
				},
			},
		},
		ReplyTarget: "resolver-target",
		Sender:      channel.Identity{SubjectID: "resolver-user"},
		Conversation: channel.Conversation{
			ID:   "resolver-conv",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mediaSvc.calls != 1 {
		t.Fatalf("expected media ingest to be called once, got %d", mediaSvc.calls)
	}
	if len(gateway.gotReq.Attachments) != 1 {
		t.Fatalf("expected one gateway attachment, got %d", len(gateway.gotReq.Attachments))
	}
	if got := gateway.gotReq.Attachments[0].ContentHash; got != "asset-resolved-1" {
		t.Fatalf("expected resolved asset id, got %q", got)
	}
	if len(chatSvc.persistedIn) != 0 {
		t.Fatalf("user message persistence is deferred to storeRound; expected 0 persisted, got %d", len(chatSvc.persistedIn))
	}
}

func TestChannelInboundProcessorIngestsBase64Attachment(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-base64"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-base64", RouteID: "route-base64"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("ok")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	mediaSvc := &fakeMediaIngestor{nextID: "asset-base64-1", nextMime: "image/png"}
	processor.SetMediaService(mediaSvc)
	sender := &fakeReplySender{}

	encoded := base64.StdEncoding.EncodeToString([]byte("fake-image-bytes"))
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-base64", BotID: "bot-1", ChannelType: channel.ChannelType("local")}
	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("local"),
		Message: channel.Message{
			ID:   "msg-base64-1",
			Text: "attachment base64 test",
			Attachments: []channel.Attachment{
				{
					Type:   channel.AttachmentImage,
					Base64: "data:image/png;base64," + encoded,
					Name:   "cat.png",
				},
			},
		},
		ReplyTarget: "web-target",
		Sender: channel.Identity{
			SubjectID: "web-subject",
			Attributes: map[string]string{
				"user_id": "web-user-id",
			},
		},
		Conversation: channel.Conversation{
			ID:   "web-conv",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mediaSvc.calls != 1 {
		t.Fatalf("expected media ingest to be called once, got %d", mediaSvc.calls)
	}
	if len(mediaSvc.payloads) != 1 || string(mediaSvc.payloads[0]) != "fake-image-bytes" {
		t.Fatalf("unexpected ingested payload: %+v", mediaSvc.payloads)
	}
	if len(gateway.gotReq.Attachments) != 1 {
		t.Fatalf("expected one gateway attachment, got %d", len(gateway.gotReq.Attachments))
	}
	gotAttachment := gateway.gotReq.Attachments[0]
	if gotAttachment.ContentHash != "asset-base64-1" {
		t.Fatalf("expected resolved asset id, got %q", gotAttachment.ContentHash)
	}
	if gotAttachment.Base64 != "" {
		t.Fatalf("expected base64 to be cleared after ingest, got %q", gotAttachment.Base64)
	}
	if !strings.HasPrefix(gotAttachment.Path, "/data/.memoh/media/") {
		t.Fatalf("expected attachment path under /data/.memoh/media/, got %q", gotAttachment.Path)
	}
	if len(chatSvc.persistedIn) != 0 {
		t.Fatalf("user message persistence is deferred to storeRound; expected 0 persisted, got %d", len(chatSvc.persistedIn))
	}
}

func TestChannelInboundProcessorIngestsQQFileAttachmentKeepsOriginalExtWhenMimeGeneric(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-qq-file"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-qq-file", RouteID: "route-qq-file"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("ok")},
			},
		},
	}
	registry := channel.NewRegistry()
	registry.MustRegister(&fakeAttachmentResolverAdapter{
		typ: channel.ChannelType("qq"),
		payload: channel.AttachmentPayload{
			Reader: io.NopCloser(bytes.NewReader([]byte{0x00, 0x01, 0x02, 0x03, 0x04})),
			Mime:   "application/octet-stream",
			Size:   5,
		},
	})
	processor := NewChannelInboundProcessor(slog.Default(), registry, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	storage := &fakeStorageProvider{}
	mediaSvc := media.NewService(slog.Default(), storage)
	processor.SetMediaService(mediaSvc)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-qq-file", BotID: "bot-1", ChannelType: channel.ChannelType("qq")}
	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("qq"),
		Message: channel.Message{
			ID:   "msg-qq-file-1",
			Text: "[User sent 1 attachment]",
			Attachments: []channel.Attachment{
				{
					Type:        channel.AttachmentFile,
					PlatformKey: "qq-file-1",
					Name:        "test.md",
					Mime:        "file",
				},
			},
		},
		ReplyTarget: "c2c:user-openid",
		Sender:      channel.Identity{SubjectID: "qq-user"},
		Conversation: channel.Conversation{
			ID:   "qq-user",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gateway.gotReq.Attachments) != 1 {
		t.Fatalf("expected one attachment in gateway request, got %d", len(gateway.gotReq.Attachments))
	}
	storageKey, _ := gateway.gotReq.Attachments[0].Metadata["storage_key"].(string)
	if !strings.HasSuffix(storageKey, ".md") {
		t.Fatalf("expected storage key to keep .md extension, got %q", storageKey)
	}
	if strings.HasSuffix(storageKey, ".bin") {
		t.Fatalf("expected storage key to avoid .bin fallback, got %q", storageKey)
	}
}

func TestChannelInboundProcessorPipelineUsesResolvedAttachments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("fake-telegram-photo"))
	}))
	defer server.Close()

	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-pipeline-asset"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-pipeline-asset", RouteID: "route-pipeline-asset"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("ok")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	mediaSvc := &fakeMediaIngestor{nextID: "asset-pipeline-photo", nextMime: "image/jpeg"}
	processor.SetMediaService(mediaSvc)
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: "session-pipeline-asset", Type: "chat"}})
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	processor.SetPipeline(pipeline, nil, nil)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-pipeline-asset", BotID: "bot-1", ChannelType: channel.ChannelTypeTelegram}
	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelTypeTelegram,
		Message: channel.Message{
			ID:   "msg-pipeline-asset-1",
			Text: "photo test",
			Attachments: []channel.Attachment{
				{
					Type:        channel.AttachmentImage,
					URL:         server.URL + "/file/bot123/photo.jpg",
					PlatformKey: "tg-photo-1",
					Name:        "photo.jpg",
					Mime:        "image/jpeg",
				},
			},
		},
		ReplyTarget: "12345",
		Sender:      channel.Identity{SubjectID: "telegram-user"},
		Conversation: channel.Conversation{
			ID:   "12345",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mediaSvc.calls != 1 {
		t.Fatalf("expected media ingest to be called once, got %d", mediaSvc.calls)
	}

	ic, ok := pipeline.GetIC("session-pipeline-asset")
	if !ok {
		t.Fatal("expected pipeline session to be created")
	}
	if len(ic.Nodes) == 0 || ic.Nodes[0].Message == nil {
		t.Fatal("expected first pipeline node to be a message")
	}
	atts := ic.Nodes[0].Message.Attachments
	if len(atts) != 1 {
		t.Fatalf("expected one pipeline attachment, got %d", len(atts))
	}
	if got := atts[0].FilePath; got != "/data/.memoh/media/test/asset-pipeline-photo" {
		t.Fatalf("expected pipeline attachment path to use media store, got %q", got)
	}
	if strings.Contains(atts[0].FilePath, "api.telegram.org") {
		t.Fatalf("expected pipeline attachment path to avoid telegram url, got %q", atts[0].FilePath)
	}
}

func TestChannelInboundProcessorPersonalGroupNonOwnerIgnored(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-member"}}
	policySvc := &fakePolicyService{ownerUserID: "channelIdentity-owner"}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-personal-1", RouteID: "route-personal-1"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("AI reply")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1"}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "msg-personal-1", Text: "hello"},
		ReplyTarget: "chat_id:oc_personal",
		Sender:      channel.Identity{SubjectID: "ext-member-1"},
		Conversation: channel.Conversation{
			ID:   "oc_personal",
			Type: "group",
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("non-owner should not trigger chat call")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("non-owner should be ignored silently: %+v", sender.sent)
	}
	if len(chatSvc.persisted) != 1 {
		t.Fatalf("non-owner group message should persist 1 passive message (replacing inbox), got %d", len(chatSvc.persisted))
	}
	if chatSvc.persisted[0].Role != "user" {
		t.Fatalf("passive message role should be user, got %q", chatSvc.persisted[0].Role)
	}
}

func TestChannelInboundProcessorPersonalGroupOwnerWithoutMentionUsesPassivePersistence(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-owner"}}
	policySvc := &fakePolicyService{ownerUserID: "channelIdentity-owner"}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-personal-2", RouteID: "route-personal-2"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("AI reply")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1"}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "msg-personal-2", Text: "owner says hi"},
		ReplyTarget: "chat_id:oc_personal",
		Sender:      channel.Identity{SubjectID: "ext-owner-1"},
		Conversation: channel.Conversation{
			ID:   "oc_personal",
			Type: "group",
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gateway.gotReq.Query != "" {
		t.Fatalf("owner group message without mention should not trigger chat call")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("owner group message without mention should not send reply")
	}
	if len(chatSvc.persisted) != 1 {
		t.Fatalf("owner non-mentioned message should persist 1 passive message (replacing inbox), got: %d", len(chatSvc.persisted))
	}
	if chatSvc.persisted[0].Role != "user" {
		t.Fatalf("passive message role should be user, got %q", chatSvc.persisted[0].Role)
	}
}

func TestChannelInboundProcessorProcessingStatusSuccessLifecycle(t *testing.T) {
	notifier := &fakeProcessingStatusNotifier{
		startedHandle: channel.ProcessingStatusHandle{Token: "reaction-1"},
	}
	registry := channel.NewRegistry()
	registry.MustRegister(&fakeProcessingStatusAdapter{notifier: notifier})
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("AI reply")},
			},
		},
		onChat: func(_ turn.StartTurnCommand) {
			if len(notifier.events) != 1 || notifier.events[0] != "started" {
				t.Fatalf("expected started before chat call, got events: %+v", notifier.events)
			}
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), registry, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "om_123", Text: "hello"},
		ReplyTarget: "chat_id:oc_123",
		Sender:      channel.Identity{SubjectID: "ext-1"},
		Conversation: channel.Conversation{
			ID:   "oc_123",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.events) != 2 || notifier.events[0] != "started" || notifier.events[1] != "completed" {
		t.Fatalf("unexpected processing status lifecycle: %+v", notifier.events)
	}
	if notifier.completedSeen.Token != "reaction-1" {
		t.Fatalf("expected completed token reaction-1, got: %q", notifier.completedSeen.Token)
	}
	if notifier.failedCause != nil {
		t.Fatalf("expected failed cause nil, got: %v", notifier.failedCause)
	}
	if len(notifier.info) == 0 || notifier.info[0].SourceMessageID != "om_123" {
		t.Fatalf("expected processing info source message id om_123, got: %+v", notifier.info)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one outbound reply, got %d", len(sender.sent))
	}
}

func TestChannelInboundProcessorProcessingStatusFailureLifecycle(t *testing.T) {
	notifier := &fakeProcessingStatusNotifier{
		startedHandle: channel.ProcessingStatusHandle{Token: "reaction-2"},
	}
	registry := channel.NewRegistry()
	registry.MustRegister(&fakeProcessingStatusAdapter{notifier: notifier})
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-2"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-2", RouteID: "route-2"}}
	chatErr := errors.New("chat gateway unavailable")
	gateway := &fakeChatGateway{err: chatErr}
	processor := NewChannelInboundProcessor(slog.Default(), registry, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "om_456", Text: "hello"},
		ReplyTarget: "chat_id:oc_456",
		Sender:      channel.Identity{SubjectID: "ext-2"},
		Conversation: channel.Conversation{
			ID:   "oc_456",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if !errors.Is(err, chatErr) {
		t.Fatalf("expected chat error, got: %v", err)
	}
	if len(notifier.events) != 2 || notifier.events[0] != "started" || notifier.events[1] != "failed" {
		t.Fatalf("unexpected processing status lifecycle: %+v", notifier.events)
	}
	if !errors.Is(notifier.failedCause, chatErr) {
		t.Fatalf("expected failed cause chat error, got: %v", notifier.failedCause)
	}
	if notifier.failedSeen.Token != "reaction-2" {
		t.Fatalf("expected failed token reaction-2, got: %q", notifier.failedSeen.Token)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("expected no outbound reply on chat failure, got: %+v", sender.sent)
	}
}

func TestChannelInboundProcessorProcessingStatusErrorsAreBestEffort(t *testing.T) {
	notifier := &fakeProcessingStatusNotifier{
		startedErr:   errors.New("start notify failed"),
		completedErr: errors.New("completed notify failed"),
	}
	registry := channel.NewRegistry()
	registry.MustRegister(&fakeProcessingStatusAdapter{notifier: notifier})
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-3"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-3", RouteID: "route-3"}}
	gateway := &fakeChatGateway{
		resp: fakeChatResponse{
			Messages: []turn.ModelMessage{
				{Role: "assistant", Content: turn.NewTextContent("AI reply")},
			},
		},
	}
	processor := NewChannelInboundProcessor(slog.Default(), registry, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "om_789", Text: "hello"},
		ReplyTarget: "chat_id:oc_789",
		Sender:      channel.Identity{SubjectID: "ext-3"},
		Conversation: channel.Conversation{
			ID:   "oc_789",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notifier.events) != 2 || notifier.events[0] != "started" || notifier.events[1] != "completed" {
		t.Fatalf("unexpected processing status lifecycle: %+v", notifier.events)
	}
	if notifier.completedSeen.Token != "" {
		t.Fatalf("expected empty completed token after started failure, got: %q", notifier.completedSeen.Token)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one outbound reply, got %d", len(sender.sent))
	}
}

func TestChannelInboundProcessorProcessingFailedNotifyErrorDoesNotOverrideChatError(t *testing.T) {
	notifier := &fakeProcessingStatusNotifier{
		startedHandle: channel.ProcessingStatusHandle{Token: "reaction-4"},
		failedErr:     errors.New("failed notify error"),
	}
	registry := channel.NewRegistry()
	registry.MustRegister(&fakeProcessingStatusAdapter{notifier: notifier})
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-4"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-4", RouteID: "route-4"}}
	chatErr := errors.New("chat failed")
	gateway := &fakeChatGateway{err: chatErr}
	processor := NewChannelInboundProcessor(slog.Default(), registry, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{ID: "om_999", Text: "hello"},
		ReplyTarget: "chat_id:oc_999",
		Sender:      channel.Identity{SubjectID: "ext-4"},
		Conversation: channel.Conversation{
			ID:   "oc_999",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if !errors.Is(err, chatErr) {
		t.Fatalf("expected original chat error, got: %v", err)
	}
	if len(notifier.events) != 2 || notifier.events[0] != "started" || notifier.events[1] != "failed" {
		t.Fatalf("unexpected processing status lifecycle: %+v", notifier.events)
	}
}

func TestDownloadInboundAttachmentURLTooLarge(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "999999999")
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()

	_, err := openInboundAttachmentURL(context.Background(), server.URL)
	if err == nil {
		t.Fatalf("expected too-large error")
	}
	if !errors.Is(err, media.ErrAssetTooLarge) {
		t.Fatalf("expected ErrAssetTooLarge, got %v", err)
	}
}

func TestMapStreamChunkToChannelEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		chunk         string
		wantType      channel.StreamEventType
		wantDelta     string
		wantPhase     channel.StreamPhase
		wantToolName  string
		wantAttCount  int
		wantError     string
		wantNilEvents bool
	}{
		{
			name:      "text_delta",
			chunk:     `{"type":"text_delta","delta":"hello"}`,
			wantType:  channel.StreamEventDelta,
			wantDelta: "hello",
			wantPhase: channel.StreamPhaseText,
		},
		{
			name:          "text_delta empty",
			chunk:         `{"type":"text_delta","delta":""}`,
			wantNilEvents: true,
		},
		{
			name:      "reasoning_delta",
			chunk:     `{"type":"reasoning_delta","delta":"thinking"}`,
			wantType:  channel.StreamEventDelta,
			wantDelta: "thinking",
			wantPhase: channel.StreamPhaseReasoning,
		},
		{
			name:          "reasoning_delta empty",
			chunk:         `{"type":"reasoning_delta","delta":""}`,
			wantNilEvents: true,
		},
		{
			name:      "reasoning_start",
			chunk:     `{"type":"reasoning_start"}`,
			wantType:  channel.StreamEventPhaseStart,
			wantPhase: channel.StreamPhaseReasoning,
		},
		{
			name:      "reasoning_end",
			chunk:     `{"type":"reasoning_end"}`,
			wantType:  channel.StreamEventPhaseEnd,
			wantPhase: channel.StreamPhaseReasoning,
		},
		{
			name:      "text_start",
			chunk:     `{"type":"text_start"}`,
			wantType:  channel.StreamEventPhaseStart,
			wantPhase: channel.StreamPhaseText,
		},
		{
			name:      "text_end",
			chunk:     `{"type":"text_end"}`,
			wantType:  channel.StreamEventPhaseEnd,
			wantPhase: channel.StreamPhaseText,
		},
		{
			name:         "tool_call_start",
			chunk:        `{"type":"tool_call_start","toolName":"search_web","toolCallId":"tc_1","input":{"query":"test"}}`,
			wantType:     channel.StreamEventToolCallStart,
			wantToolName: "search_web",
		},
		{
			name:         "tool_call_end",
			chunk:        `{"type":"tool_call_end","toolName":"search_web","toolCallId":"tc_1","input":{"query":"test"},"result":{"ok":true}}`,
			wantType:     channel.StreamEventToolCallEnd,
			wantToolName: "search_web",
		},
		{
			name:         "attachment_delta",
			chunk:        `{"type":"attachment_delta","attachments":[{"type":"image","url":"https://example.com/img.png"}]}`,
			wantType:     channel.StreamEventAttachment,
			wantAttCount: 1,
		},
		{
			name:          "attachment_delta empty",
			chunk:         `{"type":"attachment_delta","attachments":[]}`,
			wantNilEvents: true,
		},
		{
			name:      "error",
			chunk:     `{"type":"error","error":"something failed"}`,
			wantType:  channel.StreamEventError,
			wantError: "something failed",
		},
		{
			name:      "error fallback to message",
			chunk:     `{"type":"error","message":"fallback msg"}`,
			wantType:  channel.StreamEventError,
			wantError: "fallback msg",
		},
		{
			name:     "agent_start",
			chunk:    `{"type":"agent_start","input":{"agent":"planner"}}`,
			wantType: channel.StreamEventAgentStart,
		},
		{
			name:     "agent_end",
			chunk:    `{"type":"agent_end","result":{"ok":true}}`,
			wantType: channel.StreamEventAgentEnd,
		},
		{
			name:     "processing_started",
			chunk:    `{"type":"processing_started"}`,
			wantType: channel.StreamEventProcessingStarted,
		},
		{
			name:     "processing_completed",
			chunk:    `{"type":"processing_completed"}`,
			wantType: channel.StreamEventProcessingCompleted,
		},
		{
			name:      "processing_failed",
			chunk:     `{"type":"processing_failed","error":"failed"}`,
			wantType:  channel.StreamEventProcessingFailed,
			wantError: "failed",
		},
		{
			name:          "empty chunk",
			chunk:         ``,
			wantNilEvents: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events, _, err := mapStreamChunkToChannelEvents(json.RawMessage(tt.chunk))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNilEvents {
				if len(events) > 0 {
					t.Fatalf("expected nil/empty events, got %d", len(events))
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(events))
			}
			ev := events[0]
			if ev.Type != tt.wantType {
				t.Fatalf("expected type %q, got %q", tt.wantType, ev.Type)
			}
			if tt.wantDelta != "" && ev.Delta != tt.wantDelta {
				t.Fatalf("expected delta %q, got %q", tt.wantDelta, ev.Delta)
			}
			if tt.wantPhase != "" && ev.Phase != tt.wantPhase {
				t.Fatalf("expected phase %q, got %q", tt.wantPhase, ev.Phase)
			}
			if tt.wantToolName != "" {
				if ev.ToolCall == nil {
					t.Fatal("expected non-nil ToolCall")
				}
				if ev.ToolCall.Name != tt.wantToolName {
					t.Fatalf("expected tool name %q, got %q", tt.wantToolName, ev.ToolCall.Name)
				}
			}
			if tt.wantAttCount > 0 && len(ev.Attachments) != tt.wantAttCount {
				t.Fatalf("expected %d attachments, got %d", tt.wantAttCount, len(ev.Attachments))
			}
			if tt.wantError != "" && ev.Error != tt.wantError {
				t.Fatalf("expected error %q, got %q", tt.wantError, ev.Error)
			}
		})
	}
}

func TestMapStreamChunkToChannelEvents_ToolCallFields(t *testing.T) {
	t.Parallel()

	chunk := `{"type":"tool_call_end","toolName":"calc","toolCallId":"c1","input":{"x":1},"result":{"sum":2}}`
	events, _, err := mapStreamChunkToChannelEvents(json.RawMessage(chunk))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	tc := events[0].ToolCall
	if tc == nil {
		t.Fatal("expected non-nil ToolCall")
		return
	}
	if tc.Name != "calc" || tc.CallID != "c1" {
		t.Fatalf("unexpected name/callID: %q / %q", tc.Name, tc.CallID)
	}
	if tc.Input == nil || tc.Result == nil {
		t.Fatal("expected non-nil Input and Result")
	}
}

func TestMapStreamChunkToChannelEvents_UserInputRequest(t *testing.T) {
	t.Parallel()

	chunk := `{"type":"user_input_request","toolName":"ask_user","toolCallId":"ask-1","userInputId":"input-1","shortId":7,"status":"pending","input":{"questions":[{"text":"Original model input","kind":"single_select","options":[{"label":"Alpha"},{"label":"Beta"}]}]},"metadata":{"ui_payload":{"version":2,"questions":[{"id":"q1","text":"Pick one","kind":"single_select","options":[{"id":"q1.o1","label":"Alpha"},{"id":"q1.o2","label":"Beta"}]}]}}}`
	events, _, err := mapStreamChunkToChannelEvents(json.RawMessage(chunk))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	tc := events[0].ToolCall
	if events[0].Type != channel.StreamEventToolCallStart || tc == nil {
		t.Fatalf("event = %#v, want tool call start", events[0])
	}
	if tc.Name != "ask_user" || tc.CallID != "ask-1" || tc.ShortID != 7 {
		t.Fatalf("tool call = %#v", tc)
	}
	input, ok := tc.Input.(map[string]any)
	if !ok {
		t.Fatalf("input = %#v, want map", tc.Input)
	}
	if input["user_input_id"] != "input-1" || input["status"] != "pending" {
		t.Fatalf("input = %#v", input)
	}
	payload, ok := input["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %#v", input["payload"])
	}
	questions, ok := payload["questions"].([]any)
	if !ok || len(questions) != 1 || questions[0].(map[string]any)["text"] != "Pick one" {
		t.Fatalf("payload questions = %#v", payload["questions"])
	}
	// The shared layer emits a single filter-keepalive marker; adapters with
	// native controls (Telegram) rebuild the real keyboard from the payload.
	if len(tc.Actions) != 1 || tc.Actions[0].Type != "user_input" || tc.Actions[0].Label != "Reply" || tc.Actions[0].Value != "respond:input-1" {
		t.Fatalf("actions = %#v", tc.Actions)
	}
}

func TestMapStreamChunkToChannelEvents_UserInputTextFallback(t *testing.T) {
	t.Parallel()

	chunk := `{"type":"user_input_request","toolName":"ask_user","toolCallId":"ask-1","userInputId":"input-1","shortId":7,"status":"pending","input":{"questions":[{"id":"q1","text":"Explain","kind":"text"}]}}`
	events, _, err := mapStreamChunkToChannelEvents(json.RawMessage(chunk))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].ToolCall == nil {
		t.Fatalf("events = %#v", events)
	}
	actions := events[0].ToolCall.Actions
	if len(actions) != 1 || actions[0].Label != "Reply" || actions[0].Value != "respond:input-1" {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestMapStreamChunkToChannelEvents_UserInputCustomOptionFallback(t *testing.T) {
	t.Parallel()

	chunk := `{"type":"user_input_request","toolName":"ask_user","toolCallId":"ask-1","userInputId":"input-1","shortId":7,"status":"pending","metadata":{"ui_payload":{"version":2,"questions":[{"id":"q1","text":"Pick one","kind":"single_select","allow_custom":true,"options":[{"id":"q1.o1","label":"Alpha"},{"id":"q1.o2","label":"Beta"}]}]}}}`
	events, _, err := mapStreamChunkToChannelEvents(json.RawMessage(chunk))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	actions := events[0].ToolCall.Actions
	if len(actions) != 1 || actions[0].Label != "Reply" || actions[0].Value != "respond:input-1" {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestMapStreamChunkToChannelEvents_UserInputMultiQuestionKeepsActions(t *testing.T) {
	t.Parallel()

	// Multi-question prompts must still emit a user_input action so the
	// show_tool_calls_in_im filter does not drop the pending card. Telegram
	// rebuilds the paged keyboard from the payload.
	chunk := `{"type":"user_input_request","toolName":"ask_user","toolCallId":"ask-1","userInputId":"input-1","shortId":7,"status":"pending","metadata":{"ui_payload":{"version":2,"questions":[{"id":"q1","text":"What?","kind":"text"},{"id":"q2","text":"How fast?","kind":"single_select","options":[{"id":"q2.o1","label":"Fast"},{"id":"q2.o2","label":"Slow"}]}]}}}`
	events, _, err := mapStreamChunkToChannelEvents(json.RawMessage(chunk))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].ToolCall == nil {
		t.Fatalf("events = %#v", events)
	}
	actions := events[0].ToolCall.Actions
	if len(actions) == 0 || actions[0].Type != "user_input" {
		t.Fatalf("multi-question must keep user_input actions, got %#v", actions)
	}
	// Filter must keep the event when tool calls are hidden in IM.
	sink := &recordingOutboundForUserInput{}
	stream := channel.NewToolCallDroppingStream(sink)
	if err := stream.Push(context.Background(), events[0]); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("filter dropped multi-question ask_user: %#v", sink.events)
	}
}

type recordingOutboundForUserInput struct {
	events []channel.StreamEvent
}

func (r *recordingOutboundForUserInput) Push(_ context.Context, event channel.StreamEvent) error {
	r.events = append(r.events, event)
	return nil
}

func (*recordingOutboundForUserInput) Close(context.Context) error { return nil }

func TestUserInputAnswersFromMetadata(t *testing.T) {
	t.Parallel()

	got := userInputAnswersFromMetadata(map[string]any{
		"user_input_answers": []any{
			map[string]any{"question_id": "q1", "text": "hello"},
			map[string]any{"question_id": "q2", "option_ids": []any{"q2.o1", "q2.o2"}},
		},
	})
	if len(got) != 2 || got[0].Text != "hello" || strings.Join(got[1].OptionIDs, ",") != "q2.o1,q2.o2" {
		t.Fatalf("answers = %#v", got)
	}
	if userInputAnswersFromMetadata(nil) != nil {
		t.Fatal("empty metadata should yield nil")
	}
}

func TestMapStreamChunkToChannelEvents_FinalMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		chunk     string
		eventType channel.StreamEventType
		wantEvent bool
	}{
		{
			name:      "agent_end",
			chunk:     `{"type":"agent_end","messages":[{"role":"assistant","content":"done"}]}`,
			eventType: channel.StreamEventAgentEnd,
			wantEvent: true,
		},
		{
			name:  "agent_abort",
			chunk: `{"type":"agent_abort","messages":[{"role":"assistant","content":"partial failure"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			events, messages, err := mapStreamChunkToChannelEvents(json.RawMessage(tt.chunk))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantEvent {
				if len(events) != 1 {
					t.Fatalf("expected 1 event, got %d", len(events))
				}
				if events[0].Type != tt.eventType {
					t.Fatalf("expected event type %q, got %q", tt.eventType, events[0].Type)
				}
			} else if len(events) != 0 {
				t.Fatalf("expected no channel event, got %d", len(events))
			}
			if len(messages) != 1 {
				t.Fatalf("expected 1 final message, got %d", len(messages))
			}
			if messages[0].Role != "assistant" {
				t.Fatalf("expected role assistant, got %q", messages[0].Role)
			}
		})
	}
}

func TestIngestOutboundAttachments_DataURL(t *testing.T) {
	t.Parallel()

	p := &ChannelInboundProcessor{}
	attachments := []channel.Attachment{
		{Type: channel.AttachmentImage, URL: "data:image/png;base64,iVBORw0KGgo=", Mime: "image/png"},
	}
	// Without media service, attachments pass through unchanged.
	result := p.ingestOutboundAttachments(context.Background(), "bot-1", channel.ChannelType("telegram"), attachments)
	if len(result) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(result))
	}
	if result[0].ContentHash != "" {
		t.Fatalf("expected empty content_hash without media service, got %q", result[0].ContentHash)
	}
}

func TestIngestOutboundAttachments_NonDataURL(t *testing.T) {
	t.Parallel()

	p := &ChannelInboundProcessor{}
	attachments := []channel.Attachment{
		{Type: channel.AttachmentImage, URL: "https://example.com/img.png"},
		{Type: channel.AttachmentImage, ContentHash: "existing-asset", URL: "/data/.memoh/media/img.png"},
	}
	result := p.ingestOutboundAttachments(context.Background(), "bot-1", channel.ChannelType("telegram"), attachments)
	if len(result) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(result))
	}
	if result[0].URL != "https://example.com/img.png" {
		t.Fatalf("expected public URL preserved, got %q", result[0].URL)
	}
	if result[1].ContentHash != "existing-asset" {
		t.Fatalf("expected existing content_hash preserved, got %q", result[1].ContentHash)
	}
}

func TestChannelAttachmentsToAssetRefs(t *testing.T) {
	t.Parallel()

	attachments := []channel.Attachment{
		{ContentHash: "a1", Type: channel.AttachmentImage},
		{Type: channel.AttachmentFile},
		{ContentHash: "a2", Type: channel.AttachmentAudio},
	}
	refs := channelAttachmentsToAssetRefs(attachments, "output")
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].ContentHash != "a1" || refs[0].Role != "output" {
		t.Fatalf("unexpected ref[0]: %+v", refs[0])
	}
	if refs[1].ContentHash != "a2" {
		t.Fatalf("unexpected ref[1]: %+v", refs[1])
	}
}

func TestIsDataURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  bool
	}{
		{"data:image/png;base64,abc", true},
		{"DATA:text/plain;base64,abc", true},
		{"https://example.com", false},
		{"/data/.memoh/media/img.png", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isDataURL(tt.input); got != tt.want {
			t.Errorf("isDataURL(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestExtractStorageKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		accessPath string
		botID      string
		want       string
	}{
		{"/data/.memoh/media/26da/26da0cc7.jpg", "bot-1", "26da/26da0cc7.jpg"},
		{"/data/.memoh/media/abcd/abcd1234.pdf", "bot-2", "abcd/abcd1234.pdf"},
		{"https://example.com/img.png", "bot-1", ""},
		{"", "bot-1", ""},
	}
	for _, tt := range tests {
		got := extractStorageKey(tt.accessPath, tt.botID)
		if got != tt.want {
			t.Errorf("extractStorageKey(%q, %q) = %q, want %q", tt.accessPath, tt.botID, got, tt.want)
		}
	}
}

func TestIsHTTPURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"https://example.com/img.png", true},
		{"http://localhost:8080/test", true},
		{"HTTP://EXAMPLE.COM", true},
		{"/data/.memoh/media/img.png", false},
		{"data:image/png;base64,abc", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isHTTPURL(tt.input); got != tt.want {
			t.Errorf("isHTTPURL(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestIngestOutboundAttachments_ContainerPath(t *testing.T) {
	t.Parallel()

	ms := &fakeMediaIngestor{
		storageKeyAsset: media.Asset{ContentHash: "resolved-asset-1", Mime: "image/jpeg", SizeBytes: 1024},
	}
	p := &ChannelInboundProcessor{mediaService: ms}
	attachments := []channel.Attachment{
		{Type: channel.AttachmentImage, Path: "/data/.memoh/media/26da/26da0cc7.jpg"},
	}
	result := p.ingestOutboundAttachments(context.Background(), "bot-1", channel.ChannelType("telegram"), attachments)
	if len(result) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(result))
	}
	if result[0].ContentHash != "resolved-asset-1" {
		t.Fatalf("expected content_hash resolved-asset-1, got %q", result[0].ContentHash)
	}
	if result[0].Metadata["bot_id"] != "bot-1" {
		t.Fatalf("expected bot_id in metadata, got %v", result[0].Metadata)
	}
}

func TestIngestOutboundAttachments_ContainerPathNotFound(t *testing.T) {
	t.Parallel()

	ms := &fakeMediaIngestor{
		storageKeyErr: errors.New("not found"),
	}
	p := &ChannelInboundProcessor{mediaService: ms}
	attachments := []channel.Attachment{
		{Type: channel.AttachmentImage, Path: "/data/.memoh/media/26da/missing.jpg"},
	}
	result := p.ingestOutboundAttachments(context.Background(), "bot-1", channel.ChannelType("telegram"), attachments)
	if len(result) != 1 {
		t.Fatalf("expected unresolved container attachment to remain unchanged, got %d", len(result))
	}
	if result[0].Path != "/data/.memoh/media/26da/missing.jpg" {
		t.Fatalf("expected original path preserved, got %q", result[0].Path)
	}
	if result[0].ContentHash != "" {
		t.Fatalf("expected empty content_hash for unresolved path, got %q", result[0].ContentHash)
	}
}

func TestMapChannelToChatAttachments(t *testing.T) {
	t.Parallel()

	attachments := []channel.Attachment{
		{
			Type:        channel.AttachmentImage,
			ContentHash: "asset-1",
			Path:        "/data/.memoh/media/ab/c.png",
			Base64:      "AAAA",
			Mime:        "image/png",
		},
		{
			Type: channel.AttachmentFile,
			URL:  "https://example.com/doc.pdf",
			Name: "doc.pdf",
		},
	}

	mapped := mapChannelToChatAttachments(attachments)
	if len(mapped) != 2 {
		t.Fatalf("expected 2 mapped attachments, got %d", len(mapped))
	}
	if mapped[0].Path != "/data/.memoh/media/ab/c.png" {
		t.Fatalf("expected asset attachment path, got %q", mapped[0].Path)
	}
	if !strings.HasPrefix(mapped[0].Base64, "data:image/png;base64,") {
		t.Fatalf("expected normalized base64 data url, got %q", mapped[0].Base64)
	}
	if mapped[1].URL != "https://example.com/doc.pdf" {
		t.Fatalf("expected non-asset attachment URL, got %q", mapped[1].URL)
	}
}

// TestChannelInboundProcessorCommandExecutesWithUnprovenReplyAttachments pins
// the interactive-keyboard / reply-thread regression fix: a known fixed
// command must execute even when the message carries a reply ref whose
// attachment state the adapter cannot vouch for (AttachmentsKnown=false).
// This is exactly the shape of (a) a Telegram inline-keyboard tap's synthetic
// command and (b) a reply-to-message "/status" on adapters that never set
// AttachmentsKnown (QQ, WeCom, Weixin, Misskey, Slack threads). Only skill
// activation is attachment fail-closed.
func TestChannelInboundProcessorCommandExecutesWithUnprovenReplyAttachments(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-cmd-reply"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-cmd-reply", RouteID: "route-cmd-reply"}}
	gateway := &fakeChatGateway{}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	processor.SetSessionEnsurer(&fakeSessionEnsurer{
		activeSession: SessionResult{ID: "11111111-1111-1111-1111-111111111111", Type: "chat"},
	})
	cmdQueries := &fakeCommandQueries{
		messageCount: 3,
		usage:        128,
		cacheRow: dbsqlc.GetSessionCacheStatsRow{
			CacheReadTokens:  16,
			TotalInputTokens: 128,
		},
	}
	processor.SetCommandHandler(command.NewHandler(
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cmdQueries,
		nil,
		nil,
		nil,
	))
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}
	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("telegram"),
		Message: channel.Message{
			Text: "/status",
			// Reply with unknown attachment state — must NOT block a command.
			Reply: &channel.ReplyRef{MessageID: "source-msg"},
		},
		ReplyTarget: "telegram:dm-1",
		Sender:      channel.Identity{SubjectID: "user-1"},
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected one status reply, got %d", len(sender.sent))
	}
	reply := sender.sent[0].Message.PlainText()
	if strings.Contains(reply, "attachment") {
		t.Fatalf("command was rejected by the attachment rule: %q", reply)
	}
	if !strings.Contains(reply, "Session Status") {
		t.Fatalf("expected /status output, got %q", reply)
	}
}

// tailThenErrGateway streams text deltas that are already buffered when the
// error arrives, mimicking a provider failing mid-stream after producing
// output.
type tailThenErrGateway struct {
	fakeChatGateway
	deltas []string
}

func (f *tailThenErrGateway) StartTurn(_ context.Context, cmd turn.StartTurnCommand) (turn.RunHandle, error) {
	f.gotReq = cmd
	events := make(chan turn.Event, len(f.deltas))
	errs := make(chan error, 1)
	for i, d := range f.deltas {
		payload, _ := json.Marshal(map[string]any{"type": "text_delta", "delta": d})
		events <- turn.Event{
			RunID:    "run-1",
			TeamID:   cmd.TeamID,
			ThreadID: cmd.ThreadID,
			Seq:      int64(i + 1),
			Kind:     "text_delta",
			Payload:  payload,
		}
	}
	errs <- errors.New("provider exploded mid-stream")
	close(events)
	close(errs)
	return &fakeTurnRun{events: events, errs: errs}, nil
}

// TestChannelInboundProcessorDeliversTailEventsBeforeError pins the drain
// fix: deltas the model already produced must reach the platform before the
// error is surfaced — the buffered turn.Service pair lets the error
// overtake them otherwise, truncating the user-visible reply.
func TestChannelInboundProcessorDeliversTailEventsBeforeError(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	policySvc := &fakePolicyService{}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
	gateway := &tailThenErrGateway{deltas: []string{"partial ", "answer"}}
	processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, policySvc, "", 0)
	sender := &fakeReplySender{}

	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}
	msg := channel.InboundMessage{
		BotID:        "bot-1",
		Channel:      channel.ChannelType("feishu"),
		Message:      channel.Message{Text: "hello"},
		ReplyTarget:  "target-id",
		Sender:       channel.Identity{SubjectID: "ext-1", DisplayName: "User1"},
		Conversation: channel.Conversation{ID: "chat-1", Type: channel.ConversationTypePrivate},
	}

	err := processor.HandleInbound(context.Background(), cfg, msg, sender)
	if err == nil {
		t.Fatal("expected stream error to propagate")
	}
	var deltas []string
	errorSeen := false
	for _, event := range sender.events {
		switch event.Type {
		case channel.StreamEventDelta:
			if errorSeen {
				t.Fatal("delta pushed after error event")
			}
			deltas = append(deltas, event.Delta)
		case channel.StreamEventError:
			errorSeen = true
		}
	}
	if got := strings.Join(deltas, ""); got != "partial answer" {
		t.Fatalf("tail deltas lost before error: %q", got)
	}
	if !errorSeen {
		t.Fatal("expected error event after tail deltas")
	}
}
