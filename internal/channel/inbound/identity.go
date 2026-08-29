package inbound

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/identities"
)

// IdentityDecision indicates whether the inbound message should be stopped with an optional reply.
type IdentityDecision struct {
	Stop  bool
	Reply channel.Message
}

// InboundIdentity carries the resolved channel identity for an inbound message.
type InboundIdentity struct {
	BotID             string
	ChannelConfigID   string
	SubjectID         string
	ChannelIdentityID string
	UserID            string
	DisplayName       string
	AvatarURL         string
	ForceReply        bool
}

// IdentityState bundles resolved identity with an optional early-exit decision.
type IdentityState struct {
	Identity InboundIdentity
	Decision *IdentityDecision
}

type identityContextKey struct{}

// WithIdentityState stores IdentityState in the context.
func WithIdentityState(ctx context.Context, state IdentityState) context.Context {
	return context.WithValue(ctx, identityContextKey{}, state)
}

// IdentityStateFromContext retrieves IdentityState from the context.
func IdentityStateFromContext(ctx context.Context) (IdentityState, bool) {
	if ctx == nil {
		return IdentityState{}, false
	}
	raw := ctx.Value(identityContextKey{})
	if raw == nil {
		return IdentityState{}, false
	}
	state, ok := raw.(IdentityState)
	return state, ok
}

// ChannelIdentityService is the minimal interface for channel identity resolution.
type ChannelIdentityService interface {
	ResolveByChannelIdentity(ctx context.Context, channel, channelSubjectID, displayName string, meta map[string]any) (identities.ChannelIdentity, error)
	Canonicalize(ctx context.Context, channelIdentityID string) (string, error)
}

type channelIdentityUserResolver interface {
	ListUserIDsByChannelIdentity(ctx context.Context, channelIdentityID string) ([]string, error)
}

// PolicyService resolves access policy for a bot.
type PolicyService interface {
	BotOwnerUserID(ctx context.Context, botID string) (string, error)
}

// IdentityResolver implements identity resolution with bot scope checks.
type IdentityResolver struct {
	registry          *channel.Registry
	channelIdentities ChannelIdentityService
	policy            PolicyService
	logger            *slog.Logger
	unboundReply      string
}

// NewIdentityResolver creates an IdentityResolver.
func NewIdentityResolver(
	log *slog.Logger,
	registry *channel.Registry,
	channelIdentityService ChannelIdentityService,
	policyService PolicyService,
	unboundReply string,
) *IdentityResolver {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(unboundReply) == "" {
		unboundReply = "Access denied. Please contact the administrator."
	}
	return &IdentityResolver{
		registry:          registry,
		channelIdentities: channelIdentityService,
		policy:            policyService,
		logger:            log.With(slog.String("component", "channel_identity")),
		unboundReply:      unboundReply,
	}
}

// Middleware returns a channel middleware that resolves identity before processing.
func (r *IdentityResolver) Middleware() channel.Middleware {
	return func(next channel.InboundHandler) channel.InboundHandler {
		return func(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage) error {
			state, err := r.Resolve(ctx, cfg, msg)
			if err != nil {
				return err
			}
			return next(WithIdentityState(ctx, state), cfg, msg)
		}
	}
}

// Resolve performs two-phase identity resolution:
//  1. Global identity: (channel, channel_subject_id) -> channel_identity_id (unconditional)
//  2. Authorization: owner/member checks plus public bot guest passthrough
func (r *IdentityResolver) Resolve(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage) (IdentityState, error) {
	if r.channelIdentities == nil {
		return IdentityState{}, errors.New("identity resolver not configured")
	}

	botID := strings.TrimSpace(msg.BotID)
	if botID == "" {
		botID = cfg.BotID
	}

	channelConfigID := cfg.ID
	if r.registry != nil && r.registry.IsConfigless(msg.Channel) {
		channelConfigID = ""
	}
	subjectID := extractSubjectIdentity(msg)
	displayName, avatarURL := r.resolveProfile(ctx, cfg, msg, subjectID)

	state := IdentityState{
		Identity: InboundIdentity{
			BotID:           botID,
			ChannelConfigID: channelConfigID,
			SubjectID:       subjectID,
		},
	}

	// Phase 1: Global identity resolution (unconditional).
	if subjectID == "" {
		return state, errors.New("cannot resolve identity: no channel_subject_id")
	}

	channelIdentityID, err := r.resolveIdentity(ctx, msg, subjectID, displayName, avatarURL)
	if err != nil {
		return state, err
	}
	state.Identity.ChannelIdentityID = channelIdentityID
	state.Identity.UserID = r.configlessUserID(msg)
	if strings.TrimSpace(state.Identity.UserID) == "" {
		userID, err := r.accountUserIDForChannelIdentity(ctx, channelIdentityID)
		if err != nil {
			return state, err
		}
		state.Identity.UserID = userID
	}
	state.Identity.DisplayName = displayName
	state.Identity.AvatarURL = avatarURL

	// Owner bypass — owner messages always pass identity resolution.
	if r.policy != nil && strings.TrimSpace(state.Identity.UserID) != "" {
		ownerUserID, err := r.policy.BotOwnerUserID(ctx, botID)
		if err != nil {
			return state, err
		}
		if strings.TrimSpace(ownerUserID) == strings.TrimSpace(state.Identity.UserID) {
			return state, nil
		}
	}

	// Non-owner messages pass identity resolution; downstream ACL decides allow/deny.
	return state, nil
}

func (r *IdentityResolver) accountUserIDForChannelIdentity(ctx context.Context, channelIdentityID string) (string, error) {
	resolver, ok := r.channelIdentities.(channelIdentityUserResolver)
	if !ok {
		return "", nil
	}
	userIDs, err := resolver.ListUserIDsByChannelIdentity(ctx, channelIdentityID)
	if err != nil {
		return "", err
	}
	if len(userIDs) == 1 {
		return strings.TrimSpace(userIDs[0]), nil
	}
	if len(userIDs) > 1 && r.logger != nil {
		r.logger.Warn("channel identity has multiple linked users; account principal is ambiguous",
			slog.String("channel_identity_id", strings.TrimSpace(channelIdentityID)),
			slog.Int("linked_user_count", len(userIDs)))
	}
	return "", nil
}

func (r *IdentityResolver) resolveIdentity(ctx context.Context, msg channel.InboundMessage, primarySubjectID, displayName, avatarURL string) (string, error) {
	candidates := identitySubjectCandidates(msg, primarySubjectID)
	if len(candidates) == 0 {
		return "", errors.New("cannot resolve identity: no channel_subject_id")
	}

	var meta map[string]any
	if strings.TrimSpace(avatarURL) != "" {
		meta = map[string]any{"avatar_url": strings.TrimSpace(avatarURL)}
	}

	for _, subjectID := range candidates {
		channelIdentity, err := r.channelIdentities.ResolveByChannelIdentity(ctx, msg.Channel.String(), subjectID, displayName, meta)
		if err != nil {
			return "", fmt.Errorf("resolve channel identity: %w", err)
		}
		channelIdentityID := strings.TrimSpace(channelIdentity.ID)
		if channelIdentityID == "" {
			continue
		}
		return channelIdentityID, nil
	}
	return "", errors.New("cannot resolve identity: channel identity not found")
}

func extractSubjectIdentity(msg channel.InboundMessage) string {
	if strings.TrimSpace(msg.Sender.SubjectID) != "" {
		return strings.TrimSpace(msg.Sender.SubjectID)
	}
	if value := strings.TrimSpace(msg.Sender.Attribute("open_id")); value != "" {
		return value
	}
	if value := strings.TrimSpace(msg.Sender.Attribute("user_id")); value != "" {
		return value
	}
	if value := strings.TrimSpace(msg.Sender.Attribute("username")); value != "" {
		return value
	}
	return strings.TrimSpace(msg.Sender.DisplayName)
}

func identitySubjectCandidates(msg channel.InboundMessage, primary string) []string {
	candidates := make([]string, 0, 3)
	appendUnique := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range candidates {
			if existing == value {
				return
			}
		}
		candidates = append(candidates, value)
	}

	appendUnique(primary)
	appendUnique(msg.Sender.Attribute("open_id"))
	if !strings.EqualFold(strings.TrimSpace(msg.Channel.String()), "feishu") {
		appendUnique(msg.Sender.Attribute("user_id"))
	}
	return candidates
}

func extractDisplayName(msg channel.InboundMessage) string {
	if strings.TrimSpace(msg.Sender.DisplayName) != "" {
		return strings.TrimSpace(msg.Sender.DisplayName)
	}
	if value := strings.TrimSpace(msg.Sender.Attribute("display_name")); value != "" {
		return value
	}
	if value := strings.TrimSpace(msg.Sender.Attribute("name")); value != "" {
		return value
	}
	if value := strings.TrimSpace(msg.Sender.Attribute("username")); value != "" {
		return value
	}
	return ""
}

func (r *IdentityResolver) configlessUserID(msg channel.InboundMessage) string {
	if r.registry == nil || !r.registry.IsConfigless(msg.Channel) {
		return ""
	}
	return strings.TrimSpace(msg.Sender.Attribute("user_id"))
}

// resolveProfile resolves display name and avatar URL for the sender.
// Always queries directory for avatar; prefers message-level display name over directory name.
func (r *IdentityResolver) resolveProfile(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, subjectID string) (string, string) {
	displayName := extractDisplayName(msg)
	dirName, avatarURL := r.resolveProfileFromDirectory(ctx, cfg, msg, subjectID)
	if displayName == "" {
		displayName = dirName
	}
	return displayName, avatarURL
}

// resolveProfileFromDirectory looks up the directory for sender display name and avatar URL.
func (r *IdentityResolver) resolveProfileFromDirectory(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, subjectID string) (string, string) {
	if r.registry == nil {
		return "", ""
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return "", ""
	}
	directoryAdapter, ok := r.registry.DirectoryAdapter(msg.Channel)
	if !ok || directoryAdapter == nil {
		return "", ""
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	entry, err := directoryAdapter.ResolveEntry(lookupCtx, cfg, subjectID, channel.DirectoryEntryUser)
	if err != nil {
		if r.logger != nil {
			r.logger.Debug(
				"resolve profile from directory failed",
				slog.String("channel", msg.Channel.String()),
				slog.String("subject_id", subjectID),
				slog.Any("error", err),
			)
		}
		return "", ""
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		name = strings.TrimSpace(entry.Handle)
	}
	return name, strings.TrimSpace(entry.AvatarURL)
}

func extractThreadID(msg channel.InboundMessage) string {
	if msg.Message.Thread != nil && strings.TrimSpace(msg.Message.Thread.ID) != "" {
		return strings.TrimSpace(msg.Message.Thread.ID)
	}
	if strings.TrimSpace(msg.Conversation.ThreadID) != "" {
		return strings.TrimSpace(msg.Conversation.ThreadID)
	}
	return ""
}
