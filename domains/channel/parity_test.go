package channel_test

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
	channelclient "github.com/memohai/memoh/internal/rpc/channel/client"
	channelserver "github.com/memohai/memoh/internal/rpc/channel/server"
)

type channelRuntime interface {
	UpsertChannelConfig(context.Context, channel.UpsertConfigCommand) (channel.Config, error)
	SetChannelDisabled(context.Context, channel.SetDisabledCommand) (channel.Config, error)
	DeleteChannelConfig(context.Context, channel.DeleteChannelConfigCommand) error
	SetWebhookEndpoint(context.Context, channel.SetWebhookEndpointCommand) (channel.WebhookEndpoint, error)
	ListIdentityProjections(context.Context, []string) ([]channel.IdentityProjection, error)
	ListConversationProjections(context.Context, channel.ConversationProjectionRequest) ([]channel.ConversationProjection, error)
	ReactToChannelMessage(context.Context, channel.ReactCommand) error
	ConnectionStatuses(context.Context, string, string) ([]channel.ConnectionStatus, error)
	TunnelStatus(context.Context) (channel.TunnelStatus, error)
	RefreshEmailProvider(context.Context, channel.RefreshEmailProviderCommand) error
	SendEmail(context.Context, channel.SendEmailCommand) (channel.SendEmailResult, error)
}

type fakeProvider struct {
	mu                sync.Mutex
	calls             map[string]int
	err               error
	statuses          []channel.ConnectionStatus
	identities        []channel.IdentityProjection
	projections       []channel.ConversationProjection
	projectionRequest channel.ConversationProjectionRequest
	tunnel            channel.TunnelStatus
	refresh           channel.RefreshEmailProviderCommand
	email             channel.SendEmailCommand
	block             bool
	called            chan string
}

func newFake() *fakeProvider {
	return &fakeProvider{calls: map[string]int{}, called: make(chan string, 16)}
}

func (f *fakeProvider) call(ctx context.Context, name string) error {
	f.mu.Lock()
	f.calls[name]++
	block, err := f.block, f.err
	f.mu.Unlock()
	f.called <- name
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeProvider) count(name string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[name] }

func (f *fakeProvider) UpsertChannelConfig(ctx context.Context, cmd channel.UpsertConfigCommand) (channel.Config, error) {
	if err := f.call(ctx, "upsert"); err != nil {
		return channel.Config{}, err
	}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.FixedZone("test", 9*60*60))
	return channel.Config{ID: "cfg-1", TeamID: cmd.TeamID, BotID: cmd.BotID, ChannelType: cmd.ChannelType, ProviderConfig: cmd.Config, ExternalIdentity: cmd.ExternalIdentity, SelfIdentity: cmd.SelfIdentity, Routing: cmd.Routing, Disabled: cmd.Disabled != nil && *cmd.Disabled, VerifiedAt: cmd.VerifiedAt, CreatedAt: now, UpdatedAt: now.Add(time.Minute)}, nil
}

func (f *fakeProvider) SetChannelDisabled(ctx context.Context, cmd channel.SetDisabledCommand) (channel.Config, error) {
	if err := f.call(ctx, "set_status"); err != nil {
		return channel.Config{}, err
	}
	return baseConfig(cmd.TeamID, cmd.BotID, cmd.ChannelType, cmd.Disabled), nil
}

func (f *fakeProvider) DeleteChannelConfig(ctx context.Context, _ channel.DeleteChannelConfigCommand) error {
	return f.call(ctx, "delete")
}

func (f *fakeProvider) SetWebhookEndpoint(ctx context.Context, cmd channel.SetWebhookEndpointCommand) (channel.WebhookEndpoint, error) {
	if err := f.call(ctx, "webhook"); err != nil {
		return channel.WebhookEndpoint{}, err
	}
	return channel.WebhookEndpoint{Endpoint: cmd.Endpoint}, nil
}

func (f *fakeProvider) ListIdentityProjections(ctx context.Context, _ []string) ([]channel.IdentityProjection, error) {
	if err := f.call(ctx, "identities"); err != nil {
		return nil, err
	}
	return append([]channel.IdentityProjection(nil), f.identities...), nil
}

func (f *fakeProvider) ListConversationProjections(ctx context.Context, request channel.ConversationProjectionRequest) ([]channel.ConversationProjection, error) {
	f.mu.Lock()
	f.projectionRequest = request
	f.mu.Unlock()
	if err := f.call(ctx, "projections"); err != nil {
		return nil, err
	}
	return append([]channel.ConversationProjection(nil), f.projections...), nil
}

func (f *fakeProvider) ReactToChannelMessage(ctx context.Context, _ channel.ReactCommand) error {
	return f.call(ctx, "react")
}

func (f *fakeProvider) ConnectionStatuses(ctx context.Context, _, _ string) ([]channel.ConnectionStatus, error) {
	if err := f.call(ctx, "statuses"); err != nil {
		return nil, err
	}
	return append([]channel.ConnectionStatus(nil), f.statuses...), nil
}

func (f *fakeProvider) TunnelStatus(ctx context.Context) (channel.TunnelStatus, error) {
	if err := f.call(ctx, "tunnel"); err != nil {
		return channel.TunnelStatus{}, err
	}
	return f.tunnel, nil
}

func (f *fakeProvider) RefreshEmailProvider(ctx context.Context, cmd channel.RefreshEmailProviderCommand) error {
	f.mu.Lock()
	f.refresh = cmd
	f.mu.Unlock()
	return f.call(ctx, "refresh_email")
}

func (f *fakeProvider) SendEmail(ctx context.Context, cmd channel.SendEmailCommand) (channel.SendEmailResult, error) {
	f.mu.Lock()
	f.email = cmd
	f.mu.Unlock()
	if err := f.call(ctx, "send_email"); err != nil {
		return channel.SendEmailResult{}, err
	}
	return channel.SendEmailResult{MessageID: "mail-1"}, nil
}

func (f *fakeProvider) emailCommands() (channel.RefreshEmailProviderCommand, channel.SendEmailCommand) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refresh, f.email
}

func baseConfig(teamID, botID string, typ channel.ChannelType, disabled bool) channel.Config {
	now := time.Date(2026, 7, 23, 3, 0, 0, 0, time.UTC)
	return channel.Config{ID: "cfg-1", TeamID: teamID, BotID: botID, ChannelType: typ, ProviderConfig: channel.MatrixConfig{}, Disabled: disabled, CreatedAt: now, UpdatedAt: now}
}

type transportFactory struct {
	name string
	open func(*testing.T, *fakeProvider) channelRuntime
}

var transports = []transportFactory{
	{name: "local", open: func(t *testing.T, fake *fakeProvider) channelRuntime {
		t.Helper()
		return channel.NewService(channel.Dependencies{Admin: fake, Delivery: fake, Status: fake, Email: fake, Identity: fake, Conversations: fake})
	}},
	{name: "grpc", open: openGRPC},
}

func openGRPC(t *testing.T, fake *fakeProvider) channelRuntime {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	channelpb.RegisterChannelAdminServiceServer(server, channelserver.NewAdmin(fake, fake, fake))
	channelpb.RegisterChannelDeliveryServiceServer(server, channelserver.NewDelivery(fake))
	channelpb.RegisterChannelStatusServiceServer(server, channelserver.NewStatus(fake))
	channelpb.RegisterChannelEmailServiceServer(server, channelserver.NewEmail(fake))
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///bufconn", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); server.Stop(); _ = listener.Close() })
	return channelclient.New(conn)
}

func TestLocalAndGRPCParity(t *testing.T) {
	for _, factory := range transports {
		t.Run(factory.name, func(t *testing.T) {
			fake := newFake()
			fake.statuses = []channel.ConnectionStatus{
				{ConfigID: "z", BotID: "bot", ChannelType: channel.ChannelTypeTelegram, UpdatedAt: time.Date(2026, 7, 23, 12, 0, 0, 0, time.FixedZone("test", 9*60*60))},
				{ConfigID: "a", BotID: "bot", ChannelType: channel.ChannelTypeMatrix, UpdatedAt: time.Date(2026, 7, 23, 11, 0, 0, 0, time.FixedZone("test", 9*60*60))},
			}
			fake.identities = []channel.IdentityProjection{{
				ID: "11111111-1111-1111-1111-111111111111", Channel: "slack",
				ChannelSubjectID: "U1", DisplayName: "Alice", AvatarURL: "avatar",
			}}
			fake.projections = []channel.ConversationProjection{{
				RouteID: "route-1", Channel: "slack", ConversationType: "group", ConversationID: "C1",
				ConversationName: "Team", ConversationAvatarURL: "avatar",
			}}
			fake.tunnel = channel.TunnelStatus{Enabled: true, Mode: channel.TunnelModeManaged, Status: channel.TunnelStateReady, PublicBaseURL: "https://example.test"}
			runtime := factory.open(t, fake)
			disabled := true
			verified := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
			config, err := runtime.UpsertChannelConfig(t.Context(), channel.UpsertConfigCommand{TeamID: "team", BotID: "bot", ChannelType: channel.ChannelTypeMatrix, Config: channel.MatrixConfig{HomeserverURL: "https://matrix.test", AccessToken: "secret", UserID: "@bot:test", SyncTimeout: 3 * time.Second, AutoJoinInvites: true}, ExternalIdentity: "@bot:test", SelfIdentity: channel.EmptyIdentity{}, Routing: channel.MatrixRoutingState{SinceToken: "s1"}, Disabled: &disabled, VerifiedAt: &verified})
			if err != nil {
				t.Fatal(err)
			}
			if config.TeamID != "team" || config.ChannelType != channel.ChannelTypeMatrix || !config.Disabled || config.CreatedAt.Location() != time.UTC {
				t.Fatalf("unexpected config: %#v", config)
			}
			if _, err := runtime.SetChannelDisabled(t.Context(), channel.SetDisabledCommand{TeamID: "team", BotID: "bot", ChannelType: channel.ChannelTypeMatrix, Disabled: true}); err != nil {
				t.Fatal(err)
			}
			if err := runtime.DeleteChannelConfig(t.Context(), channel.DeleteChannelConfigCommand{TeamID: "team", BotID: "bot", ChannelType: channel.ChannelTypeMatrix}); err != nil {
				t.Fatal(err)
			}
			if got, err := runtime.SetWebhookEndpoint(t.Context(), channel.SetWebhookEndpointCommand{TeamID: "team", BotID: "bot", ChannelType: channel.ChannelTypeMatrix, Endpoint: "https://example.test/channels/matrix/webhook/cfg-1"}); err != nil || got.Endpoint == "" {
				t.Fatalf("webhook = %#v, %v", got, err)
			}
			identities, err := runtime.ListIdentityProjections(t.Context(), []string{"11111111-1111-1111-1111-111111111111"})
			if err != nil || !reflect.DeepEqual(identities, fake.identities) {
				t.Fatalf("identities = %#v, %v", identities, err)
			}
			if calls := fake.count("identities"); calls != 1 {
				t.Fatalf("identity calls = %d, want 1", calls)
			}
			projections, err := runtime.ListConversationProjections(t.Context(), channel.ConversationProjectionRequest{
				BotID: "bot", RouteIDs: []string{"11111111-1111-1111-1111-111111111111"}, ChannelType: "slack",
			})
			if err != nil || len(projections) != 1 || projections[0].ConversationName != "Team" {
				t.Fatalf("projections = %#v, %v", projections, err)
			}
			if calls := fake.count("projections"); calls != 1 {
				t.Fatalf("projection calls = %d, want 1", calls)
			}
			if fake.projectionRequest.BotID != "bot" || fake.projectionRequest.ChannelType != "slack" || len(fake.projectionRequest.RouteIDs) != 1 {
				t.Fatalf("projection request = %#v", fake.projectionRequest)
			}
			_, err = runtime.ListConversationProjections(t.Context(), channel.ConversationProjectionRequest{
				BotID: "bot", RouteIDs: []string{"11111111-1111-1111-1111-111111111111"},
			})
			if err != nil {
				t.Fatalf("unfiltered projection error = %v", err)
			}
			if calls := fake.count("projections"); calls != 2 {
				t.Fatalf("projection calls = %d, want 2", calls)
			}
			if fake.projectionRequest.ChannelType != "" || len(fake.projectionRequest.RouteIDs) != 1 {
				t.Fatalf("unfiltered projection request = %#v", fake.projectionRequest)
			}
			if err := runtime.ReactToChannelMessage(t.Context(), channel.ReactCommand{TeamID: "team", BotID: "bot", ChannelType: channel.ChannelTypeMatrix, Target: "room", MessageID: "msg", Emoji: "+1"}); err != nil {
				t.Fatal(err)
			}
			statuses, err := runtime.ConnectionStatuses(t.Context(), "team", "bot")
			if err != nil {
				t.Fatal(err)
			}
			if len(statuses) != 2 || statuses[0].ChannelType != channel.ChannelTypeMatrix || statuses[0].UpdatedAt.Location() != time.UTC {
				t.Fatalf("statuses = %#v", statuses)
			}
			if got, err := runtime.TunnelStatus(t.Context()); err != nil || !reflect.DeepEqual(got, fake.tunnel) {
				t.Fatalf("tunnel = %#v, %v", got, err)
			}
			if err := runtime.RefreshEmailProvider(t.Context(), channel.RefreshEmailProviderCommand{TeamID: "team", ProviderID: "provider"}); err != nil {
				t.Fatal(err)
			}
			if got, err := runtime.SendEmail(t.Context(), channel.SendEmailCommand{TeamID: "team", BotID: "bot", To: []string{"a@example.test"}, Subject: "subject", Body: "body"}); err != nil || got.MessageID != "mail-1" {
				t.Fatalf("email = %#v, %v", got, err)
			}
		})
	}
}

func TestEmailLocalAndGRPCParity(t *testing.T) {
	refresh := channel.RefreshEmailProviderCommand{TeamID: "team-1", ProviderID: "provider-1"}
	send := channel.SendEmailCommand{
		TeamID: "team-1", BotID: "bot-1", ProviderID: "provider-1",
		To: []string{"one@example.test", "two@example.test"}, Subject: "subject", Body: "<p>body</p>", HTML: true,
	}
	for _, factory := range transports {
		t.Run(factory.name+"/mapping", func(t *testing.T) {
			fake := newFake()
			runtime := factory.open(t, fake)
			if err := runtime.RefreshEmailProvider(t.Context(), refresh); err != nil {
				t.Fatalf("RefreshEmailProvider() error = %v", err)
			}
			got, err := runtime.SendEmail(t.Context(), send)
			if err != nil || got.MessageID != "mail-1" {
				t.Fatalf("SendEmail() = %#v, %v", got, err)
			}
			gotRefresh, gotSend := fake.emailCommands()
			if !reflect.DeepEqual(gotRefresh, refresh) || !reflect.DeepEqual(gotSend, send) {
				t.Fatalf("email commands = %#v / %#v, want %#v / %#v", gotRefresh, gotSend, refresh, send)
			}
			if fake.count("refresh_email") != 1 || fake.count("send_email") != 1 {
				t.Fatalf("email calls = refresh:%d send:%d", fake.count("refresh_email"), fake.count("send_email"))
			}
		})

		t.Run(factory.name+"/typed error", func(t *testing.T) {
			fake := newFake()
			fake.err = channel.NewDomainError(channel.ErrProviderFailed, channel.ErrorDetail{
				Reason: channel.ErrorReasonProviderFailed, ResourceID: "provider-1", Retryable: true,
			})
			_, err := factory.open(t, fake).SendEmail(t.Context(), send)
			if !errors.Is(err, channel.ErrProviderFailed) {
				t.Fatalf("SendEmail() error = %v", err)
			}
			var domain *channel.DomainError
			if !errors.As(err, &domain) || domain.Detail.ResourceID != "provider-1" || !domain.Detail.Retryable {
				t.Fatalf("typed email error = %#v", domain)
			}
			if fake.count("send_email") != 1 {
				t.Fatalf("send calls = %d, want 1", fake.count("send_email"))
			}
		})

		t.Run(factory.name+"/cancellation", func(t *testing.T) {
			fake := newFake()
			fake.block = true
			runtime := factory.open(t, fake)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				_, err := runtime.SendEmail(ctx, send)
				done <- err
			}()
			if called := <-fake.called; called != "send_email" {
				t.Fatalf("called = %q", called)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled SendEmail() error = %v", err)
			}
			if fake.count("send_email") != 1 {
				t.Fatalf("send calls = %d, want 1", fake.count("send_email"))
			}
		})
	}
}

func TestProviderConfigRoundTrips(t *testing.T) {
	values := []struct {
		typ   channel.ChannelType
		value channel.ProviderConfig
	}{
		{channel.ChannelTypeDingTalk, channel.DingTalkConfig{AppKey: "key", AppSecret: "secret"}},
		{channel.ChannelTypeDiscord, channel.DiscordConfig{BotToken: "token"}},
		{channel.ChannelTypeFeishu, channel.FeishuConfig{AppID: "id", AppSecret: "secret", Region: channel.FeishuRegionLark, InboundMode: channel.FeishuInboundModeWebhook}},
		{channel.ChannelTypeLine, channel.LineConfig{ChannelSecret: "secret", ChannelAccessToken: "token"}},
		{channel.ChannelTypeMatrix, channel.MatrixConfig{HomeserverURL: "https://matrix.test", AccessToken: "token", UserID: "user", SyncTimeout: time.Second}},
		{channel.ChannelTypeMisskey, channel.MisskeyConfig{InstanceURL: "https://misskey.test", AccessToken: "token"}},
		{channel.ChannelTypeQQ, channel.QQConfig{AppID: "id", AppSecret: "secret", MarkdownSupport: true}},
		{channel.ChannelTypeSlack, channel.SlackConfig{BotToken: "bot", AppToken: "app"}},
		{channel.ChannelTypeTelegram, channel.TelegramConfig{BotToken: "token", APIBaseURL: "https://telegram.test", HTTPProxy: &channel.HTTPProxy{URL: "http://proxy.test"}}},
		{channel.ChannelTypeWeChatOA, channel.WeChatOAConfig{AppID: "id", AppSecret: "secret", EncryptionMode: channel.EncryptionModeSafe}},
		{channel.ChannelTypeWeCom, channel.WeComConfig{BotID: "id", Credential: "secret", Heartbeat: time.Second, ACKTimeout: 2 * time.Second, WriteTimeout: 3 * time.Second, ReadTimeout: 4 * time.Second}},
		{channel.ChannelTypeWeixin, channel.WeixinConfig{Token: "token", BaseURL: "https://weixin.test", PollTimeout: time.Second}},
	}
	for _, item := range values {
		t.Run(reflect.TypeOf(item.value).Name(), func(t *testing.T) {
			fake := newFake()
			runtime := openGRPC(t, fake)
			got, err := runtime.UpsertChannelConfig(t.Context(), channel.UpsertConfigCommand{TeamID: "team", BotID: "bot", ChannelType: item.typ, Config: item.value})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.ProviderConfig, item.value) {
				t.Fatalf("config = %#v, want %#v", got.ProviderConfig, item.value)
			}
		})
	}
}

func TestIdentityAndRoutingRoundTrips(t *testing.T) {
	tests := []struct {
		name     string
		typ      channel.ChannelType
		config   channel.ProviderConfig
		identity channel.ChannelSelfIdentity
		routing  channel.ChannelRoutingState
	}{
		{"dingtalk", channel.ChannelTypeDingTalk, channel.DingTalkConfig{}, channel.DingTalkIdentity{AppKey: "app", Name: "bot"}, channel.EmptyRoutingState{}},
		{"feishu", channel.ChannelTypeFeishu, channel.FeishuConfig{Region: channel.FeishuRegionFeishu, InboundMode: channel.FeishuInboundModeWebSocket}, channel.FeishuIdentity{OpenID: "open", Name: "bot", AvatarURL: "https://example.test/avatar"}, channel.EmptyRoutingState{}},
		{"line", channel.ChannelTypeLine, channel.LineConfig{}, channel.LineIdentity{BotUserID: "user", BasicID: "basic", DisplayName: "bot"}, channel.EmptyRoutingState{}},
		{"misskey", channel.ChannelTypeMisskey, channel.MisskeyConfig{}, channel.UserIdentity{UserID: "user", Username: "bot", Name: "Bot"}, channel.EmptyRoutingState{}},
		{"slack", channel.ChannelTypeSlack, channel.SlackConfig{}, channel.SlackIdentity{UserID: "user", BotID: "bot", TeamID: "team", Username: "bot", Team: "workspace"}, channel.EmptyRoutingState{}},
		{"telegram", channel.ChannelTypeTelegram, channel.TelegramConfig{}, channel.UserIdentity{UserID: "user", Username: "bot", Name: "Bot"}, channel.EmptyRoutingState{}},
		{"wechat_oa", channel.ChannelTypeWeChatOA, channel.WeChatOAConfig{EncryptionMode: channel.EncryptionModePlain}, channel.WeChatOAIdentity{AppID: "app"}, channel.EmptyRoutingState{}},
		{"wecom", channel.ChannelTypeWeCom, channel.WeComConfig{}, channel.WeComIdentity{BotID: "bot", AIBotID: "ai"}, channel.EmptyRoutingState{}},
		{"matrix_routing", channel.ChannelTypeMatrix, channel.MatrixConfig{}, channel.EmptyIdentity{}, channel.MatrixRoutingState{SinceToken: "since"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFake()
			runtime := openGRPC(t, fake)
			got, err := runtime.UpsertChannelConfig(t.Context(), channel.UpsertConfigCommand{TeamID: "team", BotID: "bot", ChannelType: test.typ, Config: test.config, SelfIdentity: test.identity, Routing: test.routing})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.SelfIdentity, test.identity) {
				t.Fatalf("identity = %#v, want %#v", got.SelfIdentity, test.identity)
			}
			if !reflect.DeepEqual(got.Routing, test.routing) {
				t.Fatalf("routing = %#v, want %#v", got.Routing, test.routing)
			}
		})
	}
}

func TestTypedErrorAndZeroRetry(t *testing.T) {
	for _, factory := range transports {
		t.Run(factory.name, func(t *testing.T) {
			fake := newFake()
			fake.err = channel.NewDomainError(channel.ErrConfigNotFound, channel.ErrorDetail{Reason: channel.ErrorReasonConfigNotFound, ResourceID: "cfg-1"})
			runtime := factory.open(t, fake)
			_, err := runtime.UpsertChannelConfig(t.Context(), channel.UpsertConfigCommand{TeamID: "team", BotID: "bot", ChannelType: channel.ChannelTypeMatrix, Config: channel.MatrixConfig{}})
			if !errors.Is(err, channel.ErrConfigNotFound) {
				t.Fatalf("error = %v", err)
			}
			var domain *channel.DomainError
			if !errors.As(err, &domain) || domain.Detail.ResourceID != "cfg-1" {
				t.Fatalf("domain error = %#v", domain)
			}
			if fake.count("upsert") != 1 {
				t.Fatalf("calls = %d, want 1", fake.count("upsert"))
			}
			if factory.name == "grpc" && status.Code(err) != codes.NotFound {
				t.Fatalf("code = %s", status.Code(err))
			}
		})
	}
}

func TestPermissionDeniedPreservesTypedIdentity(t *testing.T) {
	tests := []struct {
		name     string
		cause    error
		reason   channel.ErrorReason
		notCause error
	}{
		{"team_not_served", channel.ErrTeamNotServed, channel.ErrorReasonTeamNotServed, channel.ErrForbidden},
		{"forbidden", channel.ErrForbidden, channel.ErrorReasonForbidden, channel.ErrTeamNotServed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFake()
			fake.err = test.cause
			runtime := openGRPC(t, fake)
			_, err := runtime.SetChannelDisabled(t.Context(), channel.SetDisabledCommand{TeamID: "other-team", BotID: "bot", ChannelType: channel.ChannelTypeMatrix})
			if status.Code(err) != codes.PermissionDenied || !errors.Is(err, test.cause) {
				t.Fatalf("error = %v, code = %s", err, status.Code(err))
			}
			if errors.Is(err, test.notCause) {
				t.Fatalf("error aliases %v", test.notCause)
			}
			var domain *channel.DomainError
			if !errors.As(err, &domain) || domain.Detail.Reason != test.reason {
				t.Fatalf("detail = %#v, want reason %d", domain, test.reason)
			}
		})
	}
}

func TestPrivateProviderErrorIsSanitized(t *testing.T) {
	for _, factory := range transports {
		t.Run(factory.name, func(t *testing.T) {
			fake := newFake()
			fake.err = errors.New("password=secret database failure")
			runtime := factory.open(t, fake)
			err := runtime.RefreshEmailProvider(t.Context(), channel.RefreshEmailProviderCommand{TeamID: "team", ProviderID: "provider"})
			if !errors.Is(err, channel.ErrUnknown) {
				t.Fatalf("error = %v", err)
			}
			if err.Error() == fake.err.Error() {
				t.Fatalf("private provider error leaked: %v", err)
			}
			if factory.name == "grpc" && status.Code(err) != codes.Internal {
				t.Fatalf("code = %s", status.Code(err))
			}
		})
	}
}

func TestEmptyStatusesAreNonNil(t *testing.T) {
	for _, factory := range transports {
		t.Run(factory.name, func(t *testing.T) {
			got, err := factory.open(t, newFake()).ConnectionStatuses(t.Context(), "team", "bot")
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || len(got) != 0 {
				t.Fatalf("statuses = %#v", got)
			}
		})
	}
}

func TestCancellationPropagates(t *testing.T) {
	for _, factory := range transports {
		t.Run(factory.name, func(t *testing.T) {
			fake := newFake()
			fake.block = true
			runtime := factory.open(t, fake)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- runtime.ReactToChannelMessage(ctx, channel.ReactCommand{TeamID: "team", BotID: "bot", ChannelType: channel.ChannelTypeMatrix, Target: "room", MessageID: "msg", Emoji: "+1"})
			}()
			if called := <-fake.called; called != "react" {
				t.Fatalf("called = %q", called)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			if fake.count("react") != 1 {
				t.Fatalf("calls = %d", fake.count("react"))
			}
		})
	}
}

func TestUnimplementedIsDeploymentError(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///bufconn", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); server.Stop(); _ = listener.Close() })
	_, err = channelclient.New(conn).TunnelStatus(t.Context())
	if status.Code(err) != codes.Unimplemented || !errors.Is(err, channelclient.ErrDeployment) || errors.Is(err, channel.ErrUnavailable) {
		t.Fatalf("error = %v, code = %s", err, status.Code(err))
	}
}

func TestUnavailableDoesNotSynthesizeStatus(t *testing.T) {
	fake := newFake()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	channelpb.RegisterChannelStatusServiceServer(server, channelserver.NewStatus(fake))
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///bufconn", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	runtime := channelclient.New(conn)
	server.Stop()
	_ = listener.Close()
	t.Cleanup(func() { _ = conn.Close() })
	got, err := runtime.TunnelStatus(t.Context())
	if !errors.Is(err, channel.ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if got != (channel.TunnelStatus{}) {
		t.Fatalf("synthesized status = %#v", got)
	}
}

func TestFinalServiceSurfaceHasNoSend(t *testing.T) {
	service := reflect.TypeOf((*channelpb.ChannelDeliveryServiceClient)(nil)).Elem()
	if _, ok := service.MethodByName("SendMessage"); ok {
		t.Fatal("SendMessage must remain blocked until AD1 and D11 close")
	}
}

func TestRegistrationSurfaceIsTypedOnly(t *testing.T) {
	fake := newFake()
	server := grpc.NewServer()
	channelpb.RegisterChannelAdminServiceServer(server, channelserver.NewAdmin(fake, fake, fake))
	channelpb.RegisterChannelDeliveryServiceServer(server, channelserver.NewDelivery(fake))
	channelpb.RegisterChannelStatusServiceServer(server, channelserver.NewStatus(fake))
	channelpb.RegisterChannelEmailServiceServer(server, channelserver.NewEmail(fake))
	services := server.GetServiceInfo()
	want := []string{"memoh.channel.ChannelAdminService", "memoh.channel.ChannelDeliveryService", "memoh.channel.ChannelEmailService", "memoh.channel.ChannelStatusService"}
	for _, name := range want {
		if _, ok := services[name]; !ok {
			t.Fatalf("missing service %q: %#v", name, services)
		}
	}
	if len(services) != len(want) {
		t.Fatalf("unexpected service registration: %#v", services)
	}
}
