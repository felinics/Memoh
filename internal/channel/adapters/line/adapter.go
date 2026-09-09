package line

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"

	"github.com/felinics/memoh/internal/channel"
)

const (
	lineMaxMessageUTF16Units            = 5000
	lineEventDedupeTTL                  = 24 * time.Hour
	lineEventDedupeMaxEntries           = 50000
	lineMaxWebhookBodyBytes       int64 = 1 << 20
	lineMaxMediaEventsPerCallback       = 5
	lineCallbackBlobBudgetBytes         = 40 << 20
	lineBlobMaxBytes                    = 20 << 20
)

var statusCodePattern = regexp.MustCompile(`unexpected status code:\s*(\d+)`)

type Adapter struct {
	logger                *slog.Logger
	client                lineClientFactory
	publicBaseURLProvider publicBaseURLProvider

	seenMu sync.Mutex
	seen   map[string]time.Time

	counterMu sync.Mutex
	counters  map[string]int64
}

func NewAdapter(log *slog.Logger) *Adapter {
	if log == nil {
		log = slog.Default()
	}
	return &Adapter{
		logger:   log.With(slog.String("adapter", "line")),
		client:   defaultClientFactory{},
		seen:     make(map[string]time.Time),
		counters: make(map[string]int64),
	}
}

type publicBaseURLProvider interface {
	PublicBaseURL() string
}

type publicMediaPathSigner interface {
	SignPublicMediaPath(path string) (string, bool)
}

func (a *Adapter) SetPublicBaseURLProvider(provider publicBaseURLProvider) {
	if a != nil {
		a.publicBaseURLProvider = provider
	}
}

func (*Adapter) SelfIdentityPolicy() channel.SelfIdentityPolicy {
	return channel.SelfIdentityPolicy{
		RefreshOnCredentialsChange:       true,
		RequireDiscoveryOnEnable:         true,
		RequiredSelfIdentityKey:          "bot_user_id",
		DiscoveryErrorMessage:            "line bot identity discovery failed",
		MissingIdentityMessage:           "line bot identity discovery returned no bot user id",
		DuplicateExternalIdentityMessage: "line bot is already configured by another channel config",
	}
}

func (*Adapter) Type() channel.ChannelType { return Type }

func (*Adapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:               Type,
		DisplayName:        "LINE",
		AckDisabledWebhook: true,
		Capabilities: channel.ChannelCapabilities{
			Text:           true,
			Attachments:    true,
			Media:          true, // v1 supports LINE-hosted inbound images/files and outbound PNG/JPEG images, not every LINE media type.
			Reply:          true, // Framework reply target support; outbound delivery uses PushMessage, not LINE native ReplyMessage.
			BlockStreaming: true,
			ChatTypes:      []string{channel.ConversationTypePrivate},
		},
		OutboundPolicy: channel.OutboundPolicy{
			TextChunkLimit: lineMaxMessageUTF16Units,
			ChunkerMode:    channel.ChunkerModeText,
			MediaOrder:     channel.OutboundOrderTextFirst,
			// LINE sends text and images in multiple PushMessage calls. Keep this
			// at one attempt unless PushMessage retry keys are added for idempotency.
			RetryMax: 1,
		},
		ConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				configKeyChannelSecret: {
					Type:     channel.FieldSecret,
					Required: true,
					Order:    0,
					Title:    "Channel Secret",
				},
				configKeyChannelAccessToken: {
					Type:     channel.FieldSecret,
					Required: true,
					Order:    10,
					Title:    "Channel Access Token",
				},
			},
		},
		UserConfigSchema: channel.ConfigSchema{
			Version: 1,
			Fields: map[string]channel.FieldSchema{
				userConfigKeyUserID: {Type: channel.FieldString, Required: true, Title: "User ID"},
			},
		},
		TargetSpec: channel.TargetSpec{
			Format: "Uxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
			Hints: []channel.TargetHint{
				{Label: "LINE User ID", Example: "U4af4980629..."},
			},
		},
	}
}

func (*Adapter) NormalizeConfig(raw map[string]any) (map[string]any, error) {
	return normalizeConfig(raw)
}

func (*Adapter) NormalizeUserConfig(raw map[string]any) (map[string]any, error) {
	return normalizeUserConfig(raw)
}

func (*Adapter) NormalizeTarget(raw string) string { return normalizeTarget(raw) }

func (*Adapter) ResolveTarget(userConfig map[string]any) (string, error) {
	return resolveTarget(userConfig)
}

func (*Adapter) MatchBinding(config map[string]any, criteria channel.BindingCriteria) bool {
	return matchBinding(config, criteria)
}

func (*Adapter) BuildUserConfig(identity channel.Identity) map[string]any {
	return buildUserConfig(identity)
}

func (a *Adapter) DiscoverSelf(ctx context.Context, credentials map[string]any) (map[string]any, string, error) {
	cfg, err := parseConfigForUse(credentials)
	if err != nil {
		return nil, "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, lineAPITimeout)
	defer cancel()
	client, err := a.client.NewMessagingClient(callCtx, cfg.ChannelAccessToken)
	if err != nil {
		return nil, "", sanitizeLineError("line create messaging client failed", err)
	}
	info, err := client.GetBotInfo()
	if err != nil {
		return nil, "", sanitizeLineError("line get bot info failed", err)
	}
	botUserID := strings.TrimSpace(info.UserId)
	if botUserID == "" {
		return nil, "", errors.New("line get bot info returned empty user id")
	}
	identity := map[string]any{
		"bot_user_id": botUserID,
	}
	if value := strings.TrimSpace(info.BasicId); value != "" {
		identity["basic_id"] = value
	}
	if value := strings.TrimSpace(info.DisplayName); value != "" {
		identity["display_name"] = value
	}
	return identity, botUserID, nil
}

func (a *Adapter) SetWebhookEndpoint(ctx context.Context, credentials map[string]any, endpoint string) error {
	cfg, err := parseConfigForUse(credentials)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, lineAPITimeout)
	defer cancel()
	client, err := a.client.NewMessagingClient(callCtx, cfg.ChannelAccessToken)
	if err != nil {
		return sanitizeLineError("line create messaging client failed", err)
	}
	if _, err := client.SetWebhookEndpoint(&messaging_api.SetWebhookEndpointRequest{Endpoint: strings.TrimSpace(endpoint)}); err != nil {
		return sanitizeLineError("line set webhook endpoint failed", err)
	}
	return nil
}

func (*Adapter) Connect(_ context.Context, cfg channel.ChannelConfig, _ channel.InboundHandler) (channel.Connection, error) {
	if _, err := parseConfigForUse(cfg.Credentials); err != nil {
		return nil, err
	}
	return channel.NewConnection(cfg, func(context.Context) error { return nil }), nil
}

func (*Adapter) httpError(status int, message string) error {
	if strings.TrimSpace(message) == "" {
		message = http.StatusText(status)
	}
	return echo.NewHTTPError(status, message)
}

func (a *Adapter) logWarn(message string, attrs ...any) {
	if a != nil && a.logger != nil {
		a.logger.Warn(message, attrs...) //nolint:sloglint // message is a constant string supplied by internal callers
	}
}

func (a *Adapter) logDebug(message string, attrs ...any) {
	if a != nil && a.logger != nil {
		a.logger.Debug(message, attrs...) //nolint:sloglint // message is a constant string supplied by internal callers
	}
}

func (a *Adapter) incrementCounter(name string) int64 {
	if a == nil {
		return 0
	}
	a.counterMu.Lock()
	defer a.counterMu.Unlock()
	a.counters[name]++
	return a.counters[name]
}

func (a *Adapter) claimEvent(key string, now time.Time) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}
	a.seenMu.Lock()
	defer a.seenMu.Unlock()
	a.sweepSeenLocked(now)
	seenAt, ok := a.seen[key]
	if ok && now.Sub(seenAt) <= lineEventDedupeTTL {
		return false
	}
	a.seen[key] = now
	return true
}

func (a *Adapter) forgetEvent(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	a.seenMu.Lock()
	defer a.seenMu.Unlock()
	delete(a.seen, key)
}

func (a *Adapter) markDone(key string, now time.Time) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	a.seenMu.Lock()
	defer a.seenMu.Unlock()
	a.seen[key] = now
	a.sweepSeenLocked(now)
}

func (a *Adapter) sweepSeenLocked(now time.Time) {
	if len(a.seen) < 512 {
		return
	}
	for key, seenAt := range a.seen {
		if now.Sub(seenAt) > lineEventDedupeTTL {
			delete(a.seen, key)
		}
	}
	for len(a.seen) > lineEventDedupeMaxEntries {
		for key := range a.seen {
			delete(a.seen, key)
			break
		}
	}
}

func hashValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func sanitizeLineError(operation string, err error) error {
	if err == nil {
		return nil
	}
	reason := "request_failed"
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "timeout"
	} else if errors.Is(err, context.Canceled) {
		reason = "cancelled"
	} else if match := statusCodePattern.FindStringSubmatch(err.Error()); len(match) == 2 {
		reason = "status_" + match[1]
	} else if _, convErr := strconv.Atoi(strings.TrimSpace(err.Error())); convErr == nil {
		reason = "status_" + strings.TrimSpace(err.Error())
	}
	return fmt.Errorf("%s: %s", operation, reason)
}
