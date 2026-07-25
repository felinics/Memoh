package channelpb_test

import (
	"fmt"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
)

func TestChannelContractIdentity(t *testing.T) {
	t.Parallel()

	file := channelpb.File_internal_rpc_channel_channelpb_channel_proto
	if got, want := file.Package(), protoreflect.FullName("memoh.channel"); got != want {
		t.Fatalf("package = %q, want %q", got, want)
	}

	wantServices := map[protoreflect.Name][]protoreflect.Name{
		"ChannelAdminService":    {"UpsertConfig", "SetStatus", "DeleteConfig", "SetWebhookEndpoint", "ListIdentityProjections", "ListConversationProjections"},
		"ChannelDeliveryService": {"React"},
		"ChannelStatusService":   {"ListConnectionStatuses", "GetTunnelStatus"},
		"ChannelEmailService":    {"RefreshProvider", "SendEmail"},
	}
	services := file.Services()
	if services.Len() != len(wantServices) {
		t.Fatalf("service count = %d, want %d", services.Len(), len(wantServices))
	}
	for serviceName, wantMethods := range wantServices {
		service := services.ByName(serviceName)
		if service == nil {
			t.Fatalf("service %q is missing", serviceName)
		}
		if service.Methods().Len() != len(wantMethods) {
			t.Fatalf("%s method count = %d, want %d", serviceName, service.Methods().Len(), len(wantMethods))
		}
		for i, wantMethod := range wantMethods {
			if got := service.Methods().Get(i).Name(); got != wantMethod {
				t.Errorf("%s method %d = %q, want %q", serviceName, i, got, wantMethod)
			}
		}
	}

	for _, blocked := range []protoreflect.Name{"SendMessageRequest", "Message"} {
		if file.Messages().ByName(blocked) != nil {
			t.Errorf("blocked message %q must not be defined", blocked)
		}
	}
}

func TestChannelContractFieldNumbers(t *testing.T) {
	t.Parallel()

	file := channelpb.File_internal_rpc_channel_channelpb_channel_proto
	tests := []struct {
		message string
		fields  []string
	}{
		{"ErrorDetail", []string{"reason=1", "field=2", "resource_id=3", "limit=4", "retryable=5"}},
		{"UpsertConfigRequest", []string{"team_id=1", "bot_id=2", "channel_type=3", "config=4", "external_identity=5", "self_identity=6", "routing=7", "disabled=8", "verified_at=9"}},
		{"SetStatusRequest", []string{"team_id=1", "bot_id=2", "channel_type=3", "disabled=4"}},
		{"DeleteConfigRequest", []string{"team_id=1", "bot_id=2", "channel_type=3"}},
		{"SetWebhookEndpointRequest", []string{"team_id=1", "bot_id=2", "channel_type=3", "endpoint=4"}},
		{"SetWebhookEndpointResponse", []string{"endpoint=1"}},
		{"ListIdentityProjectionsRequest", []string{"identity_ids=1"}},
		{"ListIdentityProjectionsResponse", []string{"identities=1"}},
		{"IdentityProjection", []string{"id=1", "channel=2", "channel_subject_id=3", "display_name=4", "avatar_url=5"}},
		{"ListConversationProjectionsRequest", []string{"bot_id=1", "route_ids=2", "channel_type=3"}},
		{"ListConversationProjectionsResponse", []string{"projections=1"}},
		{"ConversationProjection", []string{"route_id=1", "channel=2", "conversation_type=3", "conversation_id=4", "thread_id=5", "conversation_name=6", "conversation_avatar_url=7"}},
		{"ChannelConfigResponse", []string{"config=1"}},
		{"ChannelConfig", []string{"id=1", "team_id=2", "bot_id=3", "channel_type=4", "provider_config=5", "external_identity=6", "self_identity=7", "routing=8", "disabled=9", "verified_at=10", "created_at=11", "updated_at=12"}},
		{"ReactRequest", []string{"team_id=1", "bot_id=2", "channel_type=3", "target=4", "message_id=5", "emoji=6", "remove=7"}},
		{"ListConnectionStatusesRequest", []string{"team_id=1", "bot_id=2"}},
		{"ListConnectionStatusesResponse", []string{"statuses=1"}},
		{"ConnectionStatus", []string{"config_id=1", "bot_id=2", "channel_type=3", "running=4", "last_error=5", "updated_at=6"}},
		{"GetTunnelStatusRequest", nil},
		{"GetTunnelStatusResponse", []string{"enabled=1", "mode=2", "status=3", "public_base_url=4", "error=5"}},
		{"RefreshProviderRequest", []string{"team_id=1", "provider_id=2"}},
		{"SendEmailRequest", []string{"team_id=1", "bot_id=2", "provider_id=3", "to=4", "subject=5", "body=6", "html=7"}},
		{"SendEmailResponse", []string{"message_id=1"}},
		{"DingTalkConfig", []string{"app_key=1", "app_secret=2"}},
		{"DiscordConfig", []string{"bot_token=1"}},
		{"FeishuConfig", []string{"app_id=1", "app_secret=2", "encrypt_key=3", "verification_token=4", "region=5", "inbound_mode=6"}},
		{"LineConfig", []string{"channel_secret=1", "channel_access_token=2"}},
		{"MatrixConfig", []string{"homeserver_url=1", "access_token=2", "user_id=3", "sync_timeout=4", "auto_join_invites=5"}},
		{"MisskeyConfig", []string{"instance_url=1", "access_token=2"}},
		{"QQConfig", []string{"app_id=1", "app_secret=2", "markdown_support=3", "enable_input_hint=4"}},
		{"SlackConfig", []string{"bot_token=1", "app_token=2"}},
		{"TelegramConfig", []string{"bot_token=1", "api_base_url=2", "http_proxy=3"}},
		{"WeChatOAConfig", []string{"app_id=1", "app_secret=2", "token=3", "encoding_aes_key=4", "encryption_mode=5", "http_proxy=6"}},
		{"WeComConfig", []string{"bot_id=1", "credential=2", "ws_url=3", "heartbeat=4", "ack_timeout=5", "write_timeout=6", "read_timeout=7"}},
		{"WeixinConfig", []string{"token=1", "base_url=2", "poll_timeout=3", "enable_typing=4"}},
		{"HttpProxy", []string{"url=1"}},
		{"DingTalkIdentity", []string{"app_key=1", "name=2"}},
		{"FeishuIdentity", []string{"open_id=1", "name=2", "avatar_url=3"}},
		{"LineIdentity", []string{"bot_user_id=1", "basic_id=2", "display_name=3"}},
		{"UserIdentity", []string{"user_id=1", "username=2", "name=3", "avatar_url=4"}},
		{"SlackIdentity", []string{"user_id=1", "bot_id=2", "team_id=3", "username=4", "team=5"}},
		{"WeChatOAIdentity", []string{"app_id=1"}},
		{"WeComIdentity", []string{"bot_id=1", "aibot_id=2"}},
		{"EmptyIdentity", nil},
		{"MatrixRoutingState", []string{"since_token=1"}},
		{"EmptyRoutingState", nil},
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			assertFields(t, file.Messages().ByName(protoreflect.Name(tt.message)), tt.fields)
		})
	}
}

func TestChannelContractOptionalPresence(t *testing.T) {
	t.Parallel()

	file := channelpb.File_internal_rpc_channel_channelpb_channel_proto
	wantOptional := map[protoreflect.Name][]protoreflect.Name{
		"UpsertConfigRequest": {"self_identity", "routing", "disabled", "verified_at"},
		"ChannelConfig":       {"self_identity", "routing", "verified_at"},
		"TelegramConfig":      {"http_proxy"},
		"WeChatOAConfig":      {"http_proxy"},
	}
	for messageName, fieldNames := range wantOptional {
		message := file.Messages().ByName(messageName)
		if message == nil {
			t.Fatalf("message %q is missing", messageName)
		}
		for _, fieldName := range fieldNames {
			field := message.Fields().ByName(fieldName)
			if field == nil || !field.HasOptionalKeyword() {
				t.Errorf("%s.%s must be proto3 optional", messageName, fieldName)
			}
		}
	}
}

func TestChannelContractEnumValues(t *testing.T) {
	t.Parallel()

	file := channelpb.File_internal_rpc_channel_channelpb_channel_proto
	tests := []struct {
		enum   string
		values []string
	}{
		{"ChannelType", []string{"CHANNEL_TYPE_UNSPECIFIED=0", "CHANNEL_TYPE_DINGTALK=1", "CHANNEL_TYPE_DISCORD=2", "CHANNEL_TYPE_FEISHU=3", "CHANNEL_TYPE_LINE=4", "CHANNEL_TYPE_MATRIX=5", "CHANNEL_TYPE_MISSKEY=6", "CHANNEL_TYPE_QQ=7", "CHANNEL_TYPE_SLACK=8", "CHANNEL_TYPE_TELEGRAM=9", "CHANNEL_TYPE_WECHAT_OA=10", "CHANNEL_TYPE_WECOM=11", "CHANNEL_TYPE_WEIXIN=12"}},
		{"TunnelMode", []string{"TUNNEL_MODE_UNSPECIFIED=0", "TUNNEL_MODE_DISABLED=1", "TUNNEL_MODE_CONFIGURED=2", "TUNNEL_MODE_MANAGED=3"}},
		{"TunnelState", []string{"TUNNEL_STATE_UNSPECIFIED=0", "TUNNEL_STATE_DISABLED=1", "TUNNEL_STATE_STARTING=2", "TUNNEL_STATE_READY=3", "TUNNEL_STATE_ERROR=4"}},
		{"FeishuRegion", []string{"FEISHU_REGION_UNSPECIFIED=0", "FEISHU_REGION_FEISHU=1", "FEISHU_REGION_LARK=2"}},
		{"FeishuInboundMode", []string{"FEISHU_INBOUND_MODE_UNSPECIFIED=0", "FEISHU_INBOUND_MODE_WEBSOCKET=1", "FEISHU_INBOUND_MODE_WEBHOOK=2"}},
		{"EncryptionMode", []string{"ENCRYPTION_MODE_UNSPECIFIED=0", "ENCRYPTION_MODE_PLAIN=1", "ENCRYPTION_MODE_COMPAT=2", "ENCRYPTION_MODE_SAFE=3"}},
		{"ChannelErrorReason", []string{"CHANNEL_ERROR_REASON_UNSPECIFIED=0", "CHANNEL_ERROR_REASON_CONFIG_NOT_FOUND=1", "CHANNEL_ERROR_REASON_DISCOVERY_FAILED=2", "CHANNEL_ERROR_REASON_ENABLE_FAILED=3", "CHANNEL_ERROR_REASON_INVALID_WEBHOOK=4", "CHANNEL_ERROR_REASON_WEBHOOK_UNSUPPORTED=5", "CHANNEL_ERROR_REASON_PAYLOAD_TOO_LARGE=6", "CHANNEL_ERROR_REASON_PROVIDER_FAILED=7", "CHANNEL_ERROR_REASON_TEAM_NOT_SERVED=8", "CHANNEL_ERROR_REASON_FORBIDDEN=9"}},
	}
	for _, tt := range tests {
		t.Run(tt.enum, func(t *testing.T) {
			assertEnumValues(t, file.Enums().ByName(protoreflect.Name(tt.enum)), tt.values)
		})
	}
}

func TestChannelContractOneofs(t *testing.T) {
	t.Parallel()

	file := channelpb.File_internal_rpc_channel_channelpb_channel_proto
	tests := []struct {
		message string
		oneof   string
		fields  []string
	}{
		{"ProviderConfig", "provider", []string{"dingtalk=1", "discord=2", "feishu=3", "line=4", "matrix=5", "misskey=6", "qq=7", "slack=8", "telegram=9", "wechat_oa=10", "wecom=11", "weixin=12"}},
		{"ChannelSelfIdentity", "identity", []string{"dingtalk=1", "feishu=2", "line=3", "misskey=4", "slack=5", "telegram=6", "wechat_oa=7", "wecom=8", "empty=9"}},
		{"ChannelRoutingState", "state", []string{"matrix=1", "empty=2"}},
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			message := file.Messages().ByName(protoreflect.Name(tt.message))
			if message == nil {
				t.Fatalf("message %q is missing", tt.message)
			}
			oneof := message.Oneofs().ByName(protoreflect.Name(tt.oneof))
			if oneof == nil {
				t.Fatalf("oneof %q is missing", tt.oneof)
			}
			assertFields(t, message, tt.fields)
			for i := range message.Fields().Len() {
				if got := message.Fields().Get(i).ContainingOneof(); got != oneof {
					t.Errorf("field %q belongs to oneof %q, want %q", message.Fields().Get(i).Name(), got.Name(), oneof.Name())
				}
			}
		})
	}
}

func TestConversationProjectionChannelTypeIsPlainOptionalValue(t *testing.T) {
	t.Parallel()

	message := channelpb.File_internal_rpc_channel_channelpb_channel_proto.Messages().ByName("ListConversationProjectionsRequest")
	if message == nil {
		t.Fatal("ListConversationProjectionsRequest is missing")
	}
	if message.Oneofs().Len() != 0 {
		t.Fatalf("oneof count = %d, want 0", message.Oneofs().Len())
	}
	field := message.Fields().ByName("channel_type")
	if field == nil {
		t.Fatal("channel_type is missing")
	}
	if field.HasOptionalKeyword() || field.HasPresence() {
		t.Error("channel_type must be a plain proto3 string; an empty value means no filter")
	}
}

func TestChannelContractHasNoGenericOrCompatibilityWire(t *testing.T) {
	t.Parallel()

	file := channelpb.File_internal_rpc_channel_channelpb_channel_proto
	messages := file.Messages()
	for i := range messages.Len() {
		message := messages.Get(i)
		if message.ReservedNames().Len() != 0 || message.ReservedRanges().Len() != 0 {
			t.Errorf("message %q contains reserved compatibility declarations", message.FullName())
		}
		for j := range message.Fields().Len() {
			field := message.Fields().Get(j)
			if field.IsMap() || field.Kind() == protoreflect.BytesKind {
				t.Errorf("field %q uses a generic map/bytes wire", field.FullName())
			}
			if field.Kind() != protoreflect.MessageKind {
				continue
			}
			switch field.Message().FullName() {
			case "google.protobuf.Any", "google.protobuf.Struct":
				t.Errorf("field %q uses forbidden %q", field.FullName(), field.Message().FullName())
			}
		}
	}
}

func assertFields(t *testing.T, message protoreflect.MessageDescriptor, want []string) {
	t.Helper()
	if message == nil {
		t.Fatal("message is missing")
	}
	if message.Fields().Len() != len(want) {
		t.Fatalf("%s field count = %d, want %d", message.FullName(), message.Fields().Len(), len(want))
	}
	for i, expected := range want {
		field := message.Fields().Get(i)
		got := fmt.Sprintf("%s=%d", field.Name(), field.Number())
		if got != expected {
			t.Errorf("%s field %d = %q, want %q", message.FullName(), i, got, expected)
		}
	}
}

func assertEnumValues(t *testing.T, enum protoreflect.EnumDescriptor, want []string) {
	t.Helper()
	if enum == nil {
		t.Fatal("enum is missing")
	}
	if enum.Values().Len() != len(want) {
		t.Fatalf("%s value count = %d, want %d", enum.FullName(), enum.Values().Len(), len(want))
	}
	for i, expected := range want {
		value := enum.Values().Get(i)
		got := fmt.Sprintf("%s=%d", value.Name(), value.Number())
		if got != expected {
			t.Errorf("%s value %d = %q, want %q", enum.FullName(), i, got, expected)
		}
	}
}
