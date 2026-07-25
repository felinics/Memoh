package channel

import "time"

// ProviderConfig is the closed set of external channel provider configurations.
type ProviderConfig interface {
	isProviderConfig()
}

type DingTalkConfig struct {
	AppKey    string
	AppSecret string
}

func (DingTalkConfig) isProviderConfig() {}

type DiscordConfig struct {
	BotToken string
}

func (DiscordConfig) isProviderConfig() {}

type FeishuRegion uint8

const (
	FeishuRegionUnspecified FeishuRegion = iota
	FeishuRegionFeishu
	FeishuRegionLark
)

type FeishuInboundMode uint8

const (
	FeishuInboundModeUnspecified FeishuInboundMode = iota
	FeishuInboundModeWebSocket
	FeishuInboundModeWebhook
)

type FeishuConfig struct {
	AppID             string
	AppSecret         string
	EncryptKey        string
	VerificationToken string
	Region            FeishuRegion
	InboundMode       FeishuInboundMode
}

func (FeishuConfig) isProviderConfig() {}

type LineConfig struct {
	ChannelSecret      string
	ChannelAccessToken string
}

func (LineConfig) isProviderConfig() {}

type MatrixConfig struct {
	HomeserverURL   string
	AccessToken     string
	UserID          string
	SyncTimeout     time.Duration
	AutoJoinInvites bool
}

func (MatrixConfig) isProviderConfig() {}

type MisskeyConfig struct {
	InstanceURL string
	AccessToken string
}

func (MisskeyConfig) isProviderConfig() {}

type QQConfig struct {
	AppID           string
	AppSecret       string
	MarkdownSupport bool
	EnableInputHint bool
}

func (QQConfig) isProviderConfig() {}

type SlackConfig struct {
	BotToken string
	AppToken string
}

func (SlackConfig) isProviderConfig() {}

type HTTPProxy struct {
	URL string
}

type TelegramConfig struct {
	BotToken   string
	APIBaseURL string
	HTTPProxy  *HTTPProxy
}

func (TelegramConfig) isProviderConfig() {}

type EncryptionMode uint8

const (
	EncryptionModeUnspecified EncryptionMode = iota
	EncryptionModePlain
	EncryptionModeCompat
	EncryptionModeSafe
)

type WeChatOAConfig struct {
	AppID          string
	AppSecret      string
	Token          string
	EncodingAESKey string
	EncryptionMode EncryptionMode
	HTTPProxy      *HTTPProxy
}

func (WeChatOAConfig) isProviderConfig() {}

type WeComConfig struct {
	BotID        string
	Credential   string
	WSURL        string
	Heartbeat    time.Duration
	ACKTimeout   time.Duration
	WriteTimeout time.Duration
	ReadTimeout  time.Duration
}

func (WeComConfig) isProviderConfig() {}

type WeixinConfig struct {
	Token        string
	BaseURL      string
	PollTimeout  time.Duration
	EnableTyping bool
}

func (WeixinConfig) isProviderConfig() {}

// ChannelSelfIdentity is the closed set of provider self-identity projections.
type ChannelSelfIdentity interface {
	isChannelSelfIdentity()
}

type DingTalkIdentity struct {
	AppKey string
	Name   string
}

func (DingTalkIdentity) isChannelSelfIdentity() {}

type FeishuIdentity struct {
	OpenID    string
	Name      string
	AvatarURL string
}

func (FeishuIdentity) isChannelSelfIdentity() {}

type LineIdentity struct {
	BotUserID   string
	BasicID     string
	DisplayName string
}

func (LineIdentity) isChannelSelfIdentity() {}

type UserIdentity struct {
	UserID    string
	Username  string
	Name      string
	AvatarURL string
}

func (UserIdentity) isChannelSelfIdentity() {}

type SlackIdentity struct {
	UserID   string
	BotID    string
	TeamID   string
	Username string
	Team     string
}

func (SlackIdentity) isChannelSelfIdentity() {}

type WeChatOAIdentity struct {
	AppID string
}

func (WeChatOAIdentity) isChannelSelfIdentity() {}

type WeComIdentity struct {
	BotID   string
	AIBotID string
}

func (WeComIdentity) isChannelSelfIdentity() {}

type EmptyIdentity struct{}

func (EmptyIdentity) isChannelSelfIdentity() {}

// ChannelRoutingState is the closed set of provider routing cursors.
type ChannelRoutingState interface {
	isChannelRoutingState()
}

type MatrixRoutingState struct {
	SinceToken string
}

func (MatrixRoutingState) isChannelRoutingState() {}

type EmptyRoutingState struct{}

func (EmptyRoutingState) isChannelRoutingState() {}

type UpsertConfigCommand struct {
	TeamID           string
	BotID            string
	ChannelType      ChannelType
	Config           ProviderConfig
	ExternalIdentity string
	SelfIdentity     ChannelSelfIdentity
	Routing          ChannelRoutingState
	Disabled         *bool
	VerifiedAt       *time.Time
}

type SetDisabledCommand struct {
	TeamID      string
	BotID       string
	ChannelType ChannelType
	Disabled    bool
}

type DeleteChannelConfigCommand struct {
	TeamID      string
	BotID       string
	ChannelType ChannelType
}

type SetWebhookEndpointCommand struct {
	TeamID      string
	BotID       string
	ChannelType ChannelType
	Endpoint    string
}

type WebhookEndpoint struct {
	Endpoint string
}

type Config struct {
	ID               string
	TeamID           string
	BotID            string
	ChannelType      ChannelType
	ProviderConfig   ProviderConfig
	ExternalIdentity string
	SelfIdentity     ChannelSelfIdentity
	Routing          ChannelRoutingState
	Disabled         bool
	VerifiedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
