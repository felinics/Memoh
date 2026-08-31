package inbound

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/felinics/memoh/internal/acl"
	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/attachment"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/discuss"
	"github.com/felinics/memoh/internal/channel/route"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/runtimekind"
	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/slash"
)

var base64Std = base64.StdEncoding

const (
	silentReplyToken        = "NO_REPLY"
	minDuplicateTextLength  = 10
	processingStatusTimeout = 60 * time.Second
)

var whitespacePattern = regexp.MustCompile(`\s+`)

// RouteResolver resolves and manages channel routes.
type RouteResolver interface {
	ResolveConversation(ctx context.Context, input route.ResolveInput) (route.ResolveConversationResult, error)
}

type channelReactor interface {
	React(ctx context.Context, botID string, channelType channel.ChannelType, req channel.ReactRequest) error
}

type chatACL interface {
	Evaluate(ctx context.Context, req acl.EvaluateRequest) (bool, error)
}

type mediaIngestor interface {
	channel.OutboundAttachmentStore
	channel.ContainerAttachmentIngester
}

// speechSynthesizer synthesizes text to speech audio.
type speechSynthesizer interface {
	Synthesize(ctx context.Context, modelID string, text string, overrideCfg map[string]any) ([]byte, string, error)
}

// speechModelResolver looks up the speech model ID configured for a bot.
type speechModelResolver interface {
	ResolveSpeechModelID(ctx context.Context, botID string) (string, error)
}

// TranscriptionResult is the minimal speech-to-text response shape needed by inbound routing.
type TranscriptionResult interface {
	GetText() string
}

// transcriptionRecognizer converts inbound audio to text using a configured model.
type transcriptionRecognizer interface {
	Transcribe(ctx context.Context, modelID string, audio []byte, filename string, contentType string, overrideCfg map[string]any) (TranscriptionResult, error)
}

// transcriptionModelResolver looks up the transcription model ID configured for a bot.
type transcriptionModelResolver interface {
	ResolveTranscriptionModelID(ctx context.Context, botID string) (string, error)
}

// SessionEnsurer resolves or creates an active session for a route.
type SessionEnsurer interface {
	EnsureActiveSession(ctx context.Context, botID, routeID, channelType string) (SessionResult, error)
	GetActiveSession(ctx context.Context, routeID string) (SessionResult, error)
	// CreateNewSession always creates a fresh session and sets it as the
	// active session for the given route, replacing any previous one.
	// Spec.Type defaults to "chat" if empty.
	CreateNewSession(ctx context.Context, botID, routeID, channelType string, spec NewSessionSpec) (SessionResult, error)
}

type ToolApprovalRunner interface {
	RespondToolApproval(ctx context.Context, input turn.ToolApprovalResponse, eventCh chan<- json.RawMessage) error
}

type UserInputRunner interface {
	RespondUserInput(ctx context.Context, input turn.UserInputResponse, eventCh chan<- json.RawMessage) error
}

type PlainTextUserInputRunner interface {
	AdvancePlainTextUserInput(ctx context.Context, input userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error)
}

// IMDisplayOptionsReader exposes bot-level IM display preferences.
// Implementations typically adapt the settings service.
type IMDisplayOptionsReader interface {
	// ShowToolCallsInIM reports whether tool_call lifecycle events should
	// reach IM adapters for the given bot. Returns false by default when the
	// bot or its settings cannot be resolved.
	ShowToolCallsInIM(ctx context.Context, botID string) (bool, error)
}

type DefaultChatRuntimeSettings struct {
	BotAgentID  string
	Runtime     string
	ACPAgentID  string
	ProjectPath string
	ProjectMode string
}

type DefaultChatRuntimeReader interface {
	DefaultChatRuntime(ctx context.Context, botID string) (DefaultChatRuntimeSettings, error)
}

type ACPAgentSetupReader interface {
	ACPAgentSetupMetadata(ctx context.Context, botID string) (map[string]any, error)
}

type BotPermissionChecker interface {
	HasBotPermission(ctx context.Context, botID, accountID, permission string) (bool, error)
}

type RequestedSkillResolver interface {
	ResolveTextRequestedSkills(ctx context.Context, botID string, names []string) ([]skillset.ResolvedSkill, error)
}

// CommandHandler is the command-control surface used by inbound channels.
// The Server process supplies the local implementation; the standalone
// Channel process supplies an authenticated RPC client.
type CommandHandler interface {
	CommandAccess(context.Context, command.ExecuteInput) (bool, error)
	CurrentContext(context.Context, string) (command.CurrentContext, error)
	ExecuteResult(context.Context, command.ExecuteInput) (*command.Result, error)
	ExecuteWithInput(context.Context, command.ExecuteInput) (string, error)
	HasCommandResource(string) bool
	MemberRole(context.Context, string, string) (string, error)
	ResolveLocale(context.Context, string) string
}

// SessionResult carries the minimum fields needed from a session.
type SessionResult struct {
	ID                    string
	Type                  string
	Mode                  string
	Runtime               string
	RuntimeOwnerAccountID string
}

type NewSessionSpec struct {
	BotAgentID            string
	Mode                  string
	Runtime               string
	Type                  string
	Metadata              map[string]any
	Title                 string
	CreatedByUserID       string
	RuntimeOwnerAccountID string
}

// ChannelInboundProcessor routes channel inbound messages to the chat gateway.
type ChannelInboundProcessor struct {
	turnSvc             turn.Service
	routeResolver       RouteResolver
	message             messagepkg.Writer
	mediaService        mediaIngestor
	reactor             channelReactor
	commandHandler      CommandHandler
	registry            *channel.Registry
	logger              *slog.Logger
	jwtSecret           string
	tokenTTL            time.Duration
	identity            *IdentityResolver
	policy              PolicyService
	dispatcher          *RouteDispatcher
	acl                 chatACL
	observer            channel.StreamObserver
	speechService       speechSynthesizer
	speechModelResolver speechModelResolver
	transcriber         transcriptionRecognizer
	sttModelResolver    transcriptionModelResolver
	sessionEnsurer      SessionEnsurer
	pipeline            *timeline.Pipeline
	eventStore          *timeline.EventStore
	discussDriver       *discuss.DiscussDriver
	imDisplayOptions    IMDisplayOptionsReader
	defaultChatRuntime  DefaultChatRuntimeReader
	acpAgentSetup       ACPAgentSetupReader
	acpProfiles         turn.ACPProfileResolver
	permissionChecker   BotPermissionChecker
	skillResolver       RequestedSkillResolver

	// activeStreams maps "botID:routeID" to a context.CancelFunc for the
	// currently running agent stream. Used by /stop to abort generation
	// on external channels (Telegram, Discord, etc.).
	activeStreams sync.Map
}

// NewChannelInboundProcessor creates a processor with channel identity-based resolution.
func NewChannelInboundProcessor(
	log *slog.Logger,
	registry *channel.Registry,
	routeResolver RouteResolver,
	messageWriter messagepkg.Writer,
	turnSvc turn.Service,
	channelIdentityService ChannelIdentityService,
	policyService PolicyService,
	jwtSecret string,
	tokenTTL time.Duration,
) *ChannelInboundProcessor {
	if log == nil {
		log = slog.Default()
	}
	if tokenTTL <= 0 {
		tokenTTL = 5 * time.Minute
	}
	identityResolver := NewIdentityResolver(log, registry, channelIdentityService, policyService, "")
	return &ChannelInboundProcessor{
		turnSvc:       turnSvc,
		routeResolver: routeResolver,
		message:       messageWriter,
		registry:      registry,
		logger:        log.With(slog.String("component", "channel_router")),
		jwtSecret:     strings.TrimSpace(jwtSecret),
		tokenTTL:      tokenTTL,
		identity:      identityResolver,
		policy:        policyService,
	}
}

func (p *ChannelInboundProcessor) SetACLService(service chatACL) {
	if p == nil {
		return
	}
	p.acl = service
}

// IdentityMiddleware returns the identity resolution middleware.
func (p *ChannelInboundProcessor) IdentityMiddleware() channel.Middleware {
	if p == nil || p.identity == nil {
		return nil
	}
	return p.identity.Middleware()
}

// SetMediaService configures media ingestion support for inbound attachments.
func (p *ChannelInboundProcessor) SetMediaService(mediaService mediaIngestor) {
	if p == nil {
		return
	}
	p.mediaService = mediaService
}

// SetReactor configures the channel reactor for handling inline emoji reactions.
func (p *ChannelInboundProcessor) SetReactor(reactor channelReactor) {
	if p == nil {
		return
	}
	p.reactor = reactor
}

// SetStreamObserver configures an observer that receives copies of all stream
// events produced for non-local channels (e.g. Telegram, Feishu). This enables
// cross-channel visibility in the WebUI without coupling adapters to the hub.
func (p *ChannelInboundProcessor) SetStreamObserver(observer channel.StreamObserver) {
	if p == nil {
		return
	}
	p.observer = observer
}

// SetSpeechService configures the speech synthesizer and settings reader for
// handling <speech> tag events (speech_delta) that require server-side audio synthesis.
func (p *ChannelInboundProcessor) SetSpeechService(synth speechSynthesizer, modelResolver speechModelResolver) {
	if p == nil {
		return
	}
	p.speechService = synth
	p.speechModelResolver = modelResolver
}

// SetTranscriptionService configures speech-to-text processing for inbound audio attachments.
func (p *ChannelInboundProcessor) SetTranscriptionService(recognizer transcriptionRecognizer, modelResolver transcriptionModelResolver) {
	if p == nil {
		return
	}
	p.transcriber = recognizer
	p.sttModelResolver = modelResolver
}

// SetSessionEnsurer configures the session ensurer for auto-creating sessions on routes.
func (p *ChannelInboundProcessor) SetSessionEnsurer(ensurer SessionEnsurer) {
	if p == nil {
		return
	}
	p.sessionEnsurer = ensurer
}

// SetCommandHandler configures the slash command handler for intercepting
// /command messages before they reach the LLM.
func (p *ChannelInboundProcessor) SetCommandHandler(handler CommandHandler) {
	if p == nil {
		return
	}
	p.commandHandler = handler
}

func (p *ChannelInboundProcessor) SetRequestedSkillResolver(resolver RequestedSkillResolver) {
	if p == nil {
		return
	}
	p.skillResolver = resolver
}

// SetPipeline configures the DCP pipeline, event store, and discuss driver.
func (p *ChannelInboundProcessor) SetPipeline(pipeline *timeline.Pipeline, store *timeline.EventStore, driver *discuss.DiscussDriver) {
	if p == nil {
		return
	}
	p.pipeline = pipeline
	p.eventStore = store
	p.discussDriver = driver
}

// SetDispatcher configures the per-route message dispatcher for inject/queue/parallel modes.
func (p *ChannelInboundProcessor) SetDispatcher(dispatcher *RouteDispatcher) {
	if p == nil {
		return
	}
	p.dispatcher = dispatcher
}

// SetIMDisplayOptions configures the reader used to gate IM-facing stream
// events (e.g. tool call lifecycle) on bot-level display preferences. When
// nil, tool call events are always dropped before reaching IM adapters.
func (p *ChannelInboundProcessor) SetIMDisplayOptions(reader IMDisplayOptionsReader) {
	if p == nil {
		return
	}
	p.imDisplayOptions = reader
}

func (p *ChannelInboundProcessor) SetDefaultChatRuntime(reader DefaultChatRuntimeReader) {
	if p == nil {
		return
	}
	p.defaultChatRuntime = reader
}

func (p *ChannelInboundProcessor) SetACPAgentSetupReader(reader ACPAgentSetupReader) {
	if p == nil {
		return
	}
	p.acpAgentSetup = reader
}

func (p *ChannelInboundProcessor) SetACPProfileResolver(resolver turn.ACPProfileResolver) {
	if p == nil {
		return
	}
	p.acpProfiles = resolver
}

func (p *ChannelInboundProcessor) SetBotPermissionChecker(checker BotPermissionChecker) {
	if p == nil {
		return
	}
	p.permissionChecker = checker
}

// shouldShowToolCallsInIM reports whether tool_call_start / tool_call_end
// events should reach the IM adapter for the given bot. Failures and missing
// configuration default to false so tool calls remain hidden unless explicitly
// enabled.
func (p *ChannelInboundProcessor) shouldShowToolCallsInIM(ctx context.Context, botID string) bool {
	if p == nil || p.imDisplayOptions == nil {
		return false
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return false
	}
	show, err := p.imDisplayOptions.ShowToolCallsInIM(ctx, botID)
	if err != nil {
		if p.logger != nil {
			p.logger.Debug(
				"show_tool_calls_in_im lookup failed, defaulting to hidden",
				slog.String("bot_id", botID),
				slog.Any("error", err),
			)
		}
		return false
	}
	return show
}

// HandleInbound processes an inbound channel message through identity resolution and chat gateway.
func (p *ChannelInboundProcessor) HandleInbound(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, sender channel.StreamReplySender) (retErr error) {
	if p.turnSvc == nil {
		return errors.New("channel inbound processor not configured")
	}
	if sender == nil {
		return errors.New("reply sender not configured")
	}
	text := strings.TrimSpace(msg.Message.PlainText())
	if p.logger != nil {
		p.logger.Debug("inbound handle start",
			slog.String("channel", msg.Channel.String()),
			slog.String("message_id", strings.TrimSpace(msg.Message.ID)),
			slog.String("query", strings.TrimSpace(text)),
			slog.Int("attachments", len(msg.Message.Attachments)),
			slog.String("conversation_type", strings.TrimSpace(msg.Conversation.Type)),
			slog.String("conversation_id", strings.TrimSpace(msg.Conversation.ID)),
		)
	}
	if strings.TrimSpace(msg.Message.PlainText()) == "" && len(msg.Message.Attachments) == 0 {
		if p.logger != nil {
			p.logger.Debug("inbound dropped empty", slog.String("channel", msg.Channel.String()))
		}
		return nil
	}
	if err := channel.RejectReservedSkillMetadata(msg.Message); err != nil {
		return p.sendSlashError(ctx, sender, msg, slash.CodeReservedSkillMetadata)
	}
	state, err := p.requireIdentity(ctx, cfg, msg)
	if err != nil {
		return err
	}
	if state.Decision != nil && state.Decision.Stop {
		if !state.Decision.Reply.IsEmpty() {
			return sender.Send(ctx, channel.OutboundMessage{
				Target:  strings.TrimSpace(msg.ReplyTarget),
				Message: state.Decision.Reply,
			})
		}
		if p.logger != nil {
			p.logger.Info(
				"inbound dropped by identity policy (no reply sent)",
				slog.String("channel", msg.Channel.String()),
				slog.String("bot_id", strings.TrimSpace(state.Identity.BotID)),
				slog.String("conversation_type", strings.TrimSpace(msg.Conversation.Type)),
				slog.String("conversation_id", strings.TrimSpace(msg.Conversation.ID)),
			)
		}
		return nil
	}

	identity := state.Identity

	// Intercept slash commands before they reach the LLM.
	// Use raw_text (without prepended quote/forward context) so that
	// quoted content like "[Reply to Bot: /fs list]\n hello" doesn't
	// accidentally match a command.
	// In group chats, only process if the message is directed at this bot
	// (via @mention or reply) to avoid all bots responding to the same command.
	cmdText := rawTextForCommand(msg, text)
	slashDecision := p.classifyChannelSlash(cmdText, msg, identity)
	invocation := slashDecision.Invocation
	slashDirected := slashDecision.Directed
	isNewCommand := invocationHasResource(invocation, "new")
	isStartCommand := invocationHasResource(invocation, "start")
	isStopCommand := invocationHasResource(invocation, "stop")
	isStatusCommand := invocationHasResource(invocation, "status", "context")
	isToolApprovalCommand := invocationHasResource(invocation, "approve", "reject")
	isUserInputResponseCommand := invocationHasResource(invocation, "respond")
	isModeCommand := invocationHasResource(invocation, "now", "next", "btw")
	var pendingSkillIntent *slash.SkillIntent
	switch slashDecision.Kind {
	case slash.DecisionRejectNoop:
		return nil
	case slash.DecisionSkillIntent:
		intent := slashDecision.SkillIntent
		pendingSkillIntent = &intent
	case slash.DecisionReject, slash.DecisionUnknownSlash, slash.DecisionUnsupportedCommand:
		code := slashDecision.Code
		if code == "" {
			code = slash.CodeUnknownSlash
		}
		return p.sendSlashError(ctx, sender, msg, code)
	}

	// /start, /new, /stop, and /status require channel-layer handling outside
	// the generic command handler (which runs before route resolution).
	//
	// /new, /stop, and /status run before the chat ACL gate below, so gate them
	// with the same command-access policy (chat ACL + manage) — otherwise an
	// outsider who cannot chat could still reset/stop/inspect the bot's session.
	// /start is intentionally left ungated: it only returns a static welcome
	// message and acts as the onboarding entry point for users who cannot chat yet.
	if (isDirectedAtBot(msg) || slashDirected) &&
		(isNewCommand || isStopCommand || isStatusCommand) &&
		p.commandHandler != nil {
		ok, accErr := p.commandHandler.CommandAccess(ctx, command.ExecuteInput{
			BotID:             strings.TrimSpace(identity.BotID),
			ChannelIdentityID: strings.TrimSpace(identity.ChannelIdentityID),
			UserID:            strings.TrimSpace(identity.UserID),
			Text:              cmdText,
			Invocation:        invocation,
			ChannelType:       msg.Channel.String(),
			ConversationType:  strings.TrimSpace(msg.Conversation.Type),
			ConversationID:    strings.TrimSpace(msg.Conversation.ID),
			ThreadID:          extractThreadID(msg),
		})
		if accErr != nil || !ok {
			if p.logger != nil {
				p.logger.Info("mode command denied by acl",
					slog.String("channel", msg.Channel.String()),
					slog.String("bot_id", strings.TrimSpace(identity.BotID)),
					slog.String("channel_identity_id", strings.TrimSpace(identity.ChannelIdentityID)),
					slog.Any("error", accErr),
				)
			}
			return p.sendSlashError(ctx, sender, msg, slash.CodePermissionDenied)
		}
	}
	if isStartCommand && (isDirectedAtBot(msg) || slashDirected) {
		return p.handleStartCommand(ctx, msg, sender, identity)
	}
	if isNewCommand && invocation != nil && (isDirectedAtBot(msg) || slashDirected) {
		return p.handleNewSessionCommand(ctx, cfg, msg, sender, identity, *invocation)
	}
	if isStopCommand && (isDirectedAtBot(msg) || slashDirected) {
		return p.handleStopCommand(ctx, cfg, msg, sender, identity)
	}
	if isStatusCommand && invocation != nil && (isDirectedAtBot(msg) || slashDirected) {
		return p.handleStatusCommand(ctx, cfg, msg, sender, identity, *invocation)
	}

	// Skip generic command handler for mode-prefix commands (/btw, /now, /next)
	// so they pass through to mode detection below.
	if pendingSkillIntent == nil && slashDecision.Kind == slash.DecisionCommandAction && p.commandHandler != nil && !isModeCommand && !isToolApprovalCommand && !isUserInputResponseCommand && invocation != nil && (isDirectedAtBot(msg) || slashDirected) {
		loc := p.localizer(ctx, identity.BotID)
		result, err := p.commandHandler.ExecuteResult(ctx, command.ExecuteInput{
			BotID:             strings.TrimSpace(identity.BotID),
			ChannelIdentityID: strings.TrimSpace(identity.ChannelIdentityID),
			UserID:            strings.TrimSpace(identity.UserID),
			Text:              cmdText,
			Invocation:        invocation,
			ChannelType:       msg.Channel.String(),
			ConversationType:  strings.TrimSpace(msg.Conversation.Type),
			ConversationID:    strings.TrimSpace(msg.Conversation.ID),
			ThreadID:          extractThreadID(msg),
			CommandTarget:     telegramGroupBotUsername(msg),
			Locale:            loc.Locale(),
		})
		var caps channel.ChannelCapabilities
		if p.registry != nil {
			caps, _ = p.registry.GetCapabilities(msg.Channel)
		}
		var outMsg channel.Message
		if err != nil {
			if p.logger != nil {
				p.logger.Warn("command execution failed", slog.Any("error", err))
			}
			outMsg = plainTextMessage(friendlyOps(loc, "ops.verb.completeCommand"), caps)
		} else {
			outMsg = renderResult(result, RenderContext{Caps: caps, T: loc})
		}
		// A command re-dispatched from an interactive button carries the id of
		// the message to edit in place, so navigation/selection updates the
		// existing message instead of posting a new one. A freshly-typed command
		// instead replies to (quotes) the triggering command message.
		if editID, ok := msg.Metadata["edit_message_id"].(string); ok && strings.TrimSpace(editID) != "" && caps.Edit {
			outMsg.ID = strings.TrimSpace(editID)
		} else if mid := strings.TrimSpace(msg.Message.ID); mid != "" {
			outMsg.Reply = &channel.ReplyRef{MessageID: mid}
		}
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  strings.TrimSpace(msg.ReplyTarget),
			Message: outMsg,
		})
	}

	resolvedAttachments := p.ingestInboundAttachments(ctx, cfg, msg, strings.TrimSpace(identity.BotID), msg.Message.Attachments)
	msg.Message.Attachments = resolvedAttachments
	if msg.Message.Reply != nil && len(msg.Message.Reply.Attachments) > 0 {
		msg.Message.Reply.Attachments = p.ingestInboundAttachments(ctx, cfg, msg, strings.TrimSpace(identity.BotID), msg.Message.Reply.Attachments)
	}
	hadVoiceAttachment := containsVoiceAttachment(resolvedAttachments)
	attachments := mapChannelToChatAttachments(resolvedAttachments)
	replyAttachments := mapChannelToChatAttachments(replyAttachmentsFromMessage(msg.Message.Reply))
	text = strings.TrimSpace(msg.Message.PlainText())

	// Detect inbound mode from message prefix (/btw, /now, /next).
	// Only applies to non-local channels; WebUI always uses the default flow.
	// Must run after buildInboundQuery so the prefix is stripped from the final text.
	inboundMode := ModeInject
	if !isLocalChannelType(msg.Channel) {
		if isModeCommand && invocation != nil {
			text = invocation.CommandText
		}
		inboundMode, text = DetectMode(text)
	}
	threadID := extractThreadID(msg)

	// Resolve or create the route via channel_routes.
	if p.routeResolver == nil {
		return errors.New("route resolver not configured")
	}
	routeMetadata := buildRouteMetadata(msg, identity)
	p.enrichConversationAvatar(ctx, cfg, msg, routeMetadata)
	resolved, err := p.routeResolver.ResolveConversation(ctx, route.ResolveInput{
		BotID:                  identity.BotID,
		Platform:               msg.Channel.String(),
		ExternalConversationID: msg.Conversation.ID,
		ExternalThreadID:       threadID,
		ConversationType:       msg.Conversation.Type,
		ChannelConfigID:        identity.ChannelConfigID,
		ReplyTarget:            strings.TrimSpace(msg.ReplyTarget),
		Metadata:               routeMetadata,
	})
	if err != nil {
		return fmt.Errorf("resolve route conversation: %w", err)
	}

	// Resolve the active session for this route. Creation happens only after
	// ACL and command gates so default ACP validation never fires for passive
	// or unauthorized traffic.
	sessionID := ""
	sessionType := ""
	sessionRuntime := ""
	sessionRuntimeOwner := ""
	if p.sessionEnsurer != nil {
		sess, sessErr := p.sessionEnsurer.GetActiveSession(ctx, resolved.RouteID)
		if sessErr == nil {
			sessionID = sess.ID
			sessionType = sess.Type
			sessionRuntime = sess.Runtime
			sessionRuntimeOwner = sess.RuntimeOwnerAccountID
		} else if p.logger != nil {
			p.logger.Debug("no active session for route; will create after gates if needed",
				slog.String("route_id", strings.TrimSpace(resolved.RouteID)),
				slog.Any("error", sessErr))
		}
	}

	// ACL gate: evaluate before events enter the pipeline. If denied, the
	// message is not persisted in the event store and not pushed into the
	// in-memory pipeline. This applies uniformly to chat and discuss modes.
	aclAllowed := true
	if p.acl != nil {
		allowed, aclErr := p.acl.Evaluate(ctx, acl.EvaluateRequest{
			BotID:             identity.BotID,
			ChannelIdentityID: identity.ChannelIdentityID,
			ChannelType:       msg.Channel.String(),
			SourceScope: acl.SourceScope{
				ConversationType: channel.NormalizeConversationType(msg.Conversation.Type),
				ConversationID:   strings.TrimSpace(msg.Conversation.ID),
				ThreadID:         threadID,
			},
		})
		if aclErr != nil {
			return fmt.Errorf("evaluate acl: %w", aclErr)
		}
		aclAllowed = allowed
	}

	if !aclAllowed {
		if pendingSkillIntent != nil {
			if isDirectedAtBot(msg) || slashDirected {
				return p.sendSlashError(ctx, sender, msg, slash.CodePermissionDenied)
			}
			return nil
		}
		p.persistPassiveMessage(ctx, identity, msg, text, attachments, resolved.RouteID, sessionID, "")
		if p.logger != nil {
			p.logger.Info(
				"inbound denied by acl — event not ingested",
				slog.String("channel", msg.Channel.String()),
				slog.String("bot_id", strings.TrimSpace(identity.BotID)),
				slog.String("channel_identity_id", strings.TrimSpace(identity.ChannelIdentityID)),
				slog.String("conversation_type", strings.TrimSpace(msg.Conversation.Type)),
			)
		}
		// Don't leave the sender in silence. For a directed message (a DM, or an
		// @mention/reply in a group) reply with an access/bind hint: a forgetful
		// owner learns how to link in via /link, and an outsider learns they
		// aren't permitted. Stay silent for undirected group chatter so we don't
		// spam the room with denials.
		if isDirectedAtBot(msg) {
			loc := p.localizer(ctx, identity.BotID)
			role := p.accessDeniedRole(ctx, identity)
			out := applyMessageFormat(channel.Message{Text: command.AccessDeniedMessage(loc, role)}, p.channelCaps(msg.Channel))
			if mid := strings.TrimSpace(msg.Message.ID); mid != "" {
				out.Reply = &channel.ReplyRef{MessageID: mid}
			}
			if sendErr := sender.Send(ctx, channel.OutboundMessage{
				Target:  strings.TrimSpace(msg.ReplyTarget),
				Message: out,
			}); sendErr != nil && p.logger != nil {
				p.logger.Warn("send acl-denied hint failed", slog.Any("error", sendErr))
			}
		}
		return nil
	}

	if isToolApprovalCommand && invocation != nil && (isDirectedAtBot(msg) || slashDirected) {
		return p.handleToolApprovalCommand(ctx, msg, sender, identity, resolved.RouteID, sessionID, *invocation)
	}
	if isUserInputResponseCommand && invocation != nil && (isDirectedAtBot(msg) || slashDirected) {
		return p.handleUserInputResponseCommand(ctx, msg, sender, identity, resolved.RouteID, sessionID, *invocation)
	}
	// Mode and skill commands remain control-plane messages even while an
	// ask_user request is pending; they must not become text-question answers.
	if pendingSkillIntent == nil && !isModeCommand {
		if handled, err := p.handlePlainTextUserInput(ctx, msg, sender, identity, resolved.RouteID, sessionID, text); handled || err != nil {
			return err
		}
	}
	if pendingSkillIntent != nil && p.dispatcher != nil && !isLocalChannelType(msg.Channel) && inboundMode != ModeParallel {
		if p.dispatcher.IsActive(strings.TrimSpace(resolved.RouteID)) {
			return p.sendSlashError(ctx, sender, msg, slash.CodeUnsupportedSkillSlashContext)
		}
	}

	var requestedSkillContexts []turn.RequestedSkillContext
	var skillActivation *turn.SkillActivation
	userMessageKind := ""
	userVisibleText := ""
	modelText := text
	var defaultSpec NewSessionSpec
	var defaultSpecShouldCreate bool
	var defaultSpecResolved bool
	if pendingSkillIntent != nil {
		if sessionID != "" && !sessionSupportsRequestedSkills(SessionResult{Type: sessionType, Runtime: sessionRuntime}) {
			return p.sendSlashError(ctx, sender, msg, slash.CodeUnsupportedSkillSlashContext)
		}
		if sessionID == "" {
			if p.sessionEnsurer == nil {
				return p.sendSlashError(ctx, sender, msg, slash.CodeUnsupportedSkillSlashContext)
			}
			spec, shouldCreate, specErr := p.defaultSessionSpecForInbound(ctx, identity, msg)
			if specErr != nil {
				if p.logger != nil {
					p.logger.Warn("resolve default session spec failed", slog.Any("error", specErr))
				}
				return p.sendACPFeedbackError(ctx, sender, msg, identity, specErr)
			}
			defaultSpec = spec
			defaultSpecShouldCreate = shouldCreate
			defaultSpecResolved = true
			if !shouldCreate || !newSessionSpecSupportsRequestedSkills(spec) {
				return p.sendSlashError(ctx, sender, msg, slash.CodeUnsupportedSkillSlashContext)
			}
		}
		resolvedSkills, resolveErr := p.resolveChannelRequestedSkills(ctx, identity.BotID, pendingSkillIntent.Names)
		if resolveErr != nil {
			code := slashErrorCode(resolveErr)
			if code == "" {
				code = slash.CodeUnsupportedSkillSlashContext
			}
			return p.sendSlashError(ctx, sender, msg, code)
		}
		resolvedSkillContexts := skillset.RequestedSkillContexts(resolvedSkills)
		requestedSkillContexts = make([]turn.RequestedSkillContext, len(resolvedSkillContexts))
		for i := range resolvedSkillContexts {
			requestedSkillContexts[i] = turn.RequestedSkillContext{
				Name:           resolvedSkillContexts[i].Name,
				Description:    resolvedSkillContexts[i].Description,
				Content:        resolvedSkillContexts[i].Content,
				SourceKind:     resolvedSkillContexts[i].SourceKind,
				OpaqueSourceID: resolvedSkillContexts[i].OpaqueSourceID,
				ContentHash:    resolvedSkillContexts[i].ContentHash,
				Identity:       resolvedSkillContexts[i].Identity,
			}
		}
		skillActivation = turn.NewSkillActivation(requestedSkillContexts, pendingSkillIntent.Prompt)
		text = strings.TrimSpace(pendingSkillIntent.Prompt)
		modelText = strings.TrimSpace(turn.SkillActivationModelQuery(skillActivation))
		userMessageKind = turn.UserMessageKindSkillActivation
		userVisibleText = strings.TrimSpace(pendingSkillIntent.Prompt)
		msg.Message.Text = userVisibleText
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]any)
		}
		msg.Metadata["raw_text"] = userVisibleText
	}

	shouldTrigger := shouldTriggerAssistantResponse(msg) || identity.ForceReply || pendingSkillIntent != nil
	if sessionID == "" && p.sessionEnsurer != nil {
		spec := defaultSpec
		shouldCreate := defaultSpecShouldCreate
		if !defaultSpecResolved {
			var specErr error
			spec, shouldCreate, specErr = p.defaultSessionSpecForInbound(ctx, identity, msg)
			if specErr != nil {
				if p.logger != nil {
					p.logger.Warn("resolve default session spec failed", slog.Any("error", specErr))
				}
				return p.sendACPFeedbackError(ctx, sender, msg, identity, specErr)
			}
		}
		if shouldCreate {
			if pendingSkillIntent != nil && !newSessionSpecSupportsRequestedSkills(spec) {
				return p.sendSlashError(ctx, sender, msg, slash.CodeUnsupportedSkillSlashContext)
			}
			sess, createErr := p.sessionEnsurer.CreateNewSession(ctx, identity.BotID, resolved.RouteID, msg.Channel.String(), spec)
			if createErr != nil {
				if p.logger != nil {
					p.logger.Warn("auto-create session failed", slog.Any("error", createErr))
				}
				return p.sendACPFeedbackError(ctx, sender, msg, identity, createErr)
			}
			sessionID = sess.ID
			sessionType = sess.Type
			sessionRuntime = sess.Runtime
			sessionRuntimeOwner = sess.RuntimeOwnerAccountID
		}
	}

	acpRuntimeSession := SessionResult{Type: sessionType, Runtime: sessionRuntime}
	if sessionUsesACPRuntime(acpRuntimeSession) {
		ownerPrincipal := strings.TrimSpace(sessionRuntimeOwner)
		var err error
		if ownerPrincipal == "" {
			err = sessionpkg.ErrACPRuntimeOwnerMissing
		} else {
			err = p.requireWorkspaceExecForACPPrincipal(ctx, identity.BotID, ownerPrincipal)
		}
		if err == nil && sessionRequiresACPRuntimeActor(acpRuntimeSession) && (shouldTrigger || isDirectedAtBot(msg)) {
			err = p.requireACPRuntimeActor(ctx, identity, ownerPrincipal)
		}
		if err != nil {
			p.persistPassiveMessage(ctx, identity, msg, text, attachments, resolved.RouteID, sessionID, "")
			if shouldTrigger || isDirectedAtBot(msg) {
				return p.sendACPFeedbackError(ctx, sender, msg, identity, err)
			}
			return nil
		}
	}

	// Push event into the DCP pipeline (persist + in-memory projection).
	// On first access for a session, replay persisted events to warm the pipeline.
	var latestRC timeline.RenderedContext
	var eventID string
	if p.pipeline != nil && sessionID != "" && pendingSkillIntent == nil {
		if _, loaded := p.pipeline.GetIC(sessionID); !loaded {
			p.replayPipelineSession(ctx, sessionID)
		}
		pipelineMsg := msg
		pipelineMsg.Message = msg.Message
		pipelineMsg.Message.Attachments = resolvedAttachments
		event := AdaptInbound(pipelineMsg, sessionID, identity.ChannelIdentityID, identity.DisplayName)
		var store sessionEventPersister
		if p.eventStore != nil {
			store = p.eventStore
		}
		eventID, latestRC = persistAndProjectEvent(ctx, store, p.pipeline, p.logger, identity.BotID, sessionID, event)
	}

	// Discuss mode: dispatch to the discuss driver and return.
	// The discuss driver autonomously decides whether to call the LLM.
	if sessionType == sessionpkg.TypeDiscuss && p.discussDriver != nil && latestRC != nil {
		chatToken := p.issueChatToken(identity, resolved.RouteID, msg)
		sessionToken := p.issueSessionBearerToken(ctx, identity, acpRuntimeSession, sessionRuntimeOwner, chatToken)
		p.discussDriver.NotifyRC(ctx, sessionID, latestRC, discuss.DiscussSessionConfig{
			TeamID:            cfg.TeamID,
			BotID:             identity.BotID,
			ThreadID:          sessionID,
			RouteID:           resolved.RouteID,
			ChannelIdentityID: identity.ChannelIdentityID,
			ReplyTarget:       strings.TrimSpace(msg.ReplyTarget),
			CurrentPlatform:   msg.Channel.String(),
			ConversationType:  strings.TrimSpace(msg.Conversation.Type),
			ConversationName:  strings.TrimSpace(msg.Conversation.Name),
			SessionToken:      sessionToken,
			ChatToken:         chatToken,
			ToolHTTPURL:       acpMCPToolsURLFromEnv(identity.BotID),
		})
		p.persistPassiveMessage(ctx, identity, msg, text, attachments, resolved.RouteID, sessionID, eventID)
		return nil
	}

	// Bot-centric history container:
	// always persist channel traffic under bot_id so WebUI can view unified cross-platform history.
	activeChatID := strings.TrimSpace(identity.BotID)
	if activeChatID == "" {
		activeChatID = strings.TrimSpace(resolved.BotID)
	}

	if sessionType == sessionpkg.TypeDiscuss || shouldTrigger {
		if transcript := p.transcribeInboundAttachments(ctx, strings.TrimSpace(identity.BotID), resolvedAttachments); transcript != "" {
			labeledTranscript := formatInboundTranscript(transcript)
			if msg.Message.Metadata == nil {
				msg.Message.Metadata = make(map[string]any)
			}
			msg.Message.Metadata["transcript"] = transcript
			if plain := strings.TrimSpace(msg.Message.PlainText()); plain == "" {
				msg.Message.Text = labeledTranscript
			} else if !strings.Contains(plain, transcript) {
				msg.Message.Text = plain + "\n\n" + labeledTranscript
			}
		} else if hadVoiceAttachment && strings.TrimSpace(msg.Message.PlainText()) == "" {
			msg.Message.Text = formatVoiceTranscriptionUnavailableNotice(resolvedAttachments)
		}
		if pendingSkillIntent != nil {
			text = strings.TrimSpace(userVisibleText)
			modelText = strings.TrimSpace(turn.SkillActivationModelQuery(skillActivation))
		} else {
			text = strings.TrimSpace(msg.Message.PlainText())
			modelText = text
		}
	}

	if !shouldTrigger {
		p.persistPassiveMessage(ctx, identity, msg, text, attachments, resolved.RouteID, sessionID, eventID)
		if p.logger != nil {
			p.logger.Info(
				"inbound not triggering assistant (group trigger condition not met)",
				slog.String("channel", msg.Channel.String()),
				slog.String("bot_id", strings.TrimSpace(identity.BotID)),
				slog.String("route_id", strings.TrimSpace(resolved.RouteID)),
				slog.Bool("is_mentioned", metadataBool(msg.Metadata, "is_mentioned")),
				slog.Bool("is_reply_to_bot", metadataBool(msg.Metadata, "is_reply_to_bot")),
				slog.String("conversation_type", strings.TrimSpace(msg.Conversation.Type)),
				slog.String("query", strings.TrimSpace(text)),
				slog.Int("attachments", len(attachments)),
			)
		}
		return nil
	}

	routeID := strings.TrimSpace(resolved.RouteID)

	// --- Dispatcher-based mode handling (inject / queue) ---
	// For non-parallel modes, when a route already has an active agent stream,
	// short-circuit here instead of starting a new stream.
	if p.dispatcher != nil && !isLocalChannelType(msg.Channel) && inboundMode != ModeParallel {
		if p.dispatcher.IsActive(routeID) {
			if pendingSkillIntent != nil {
				return p.sendSlashError(ctx, sender, msg, slash.CodeUnsupportedSkillSlashContext)
			}
			headerifiedText := turn.FormatUserHeader(turn.UserMessageHeaderInput{
				MessageID:         strings.TrimSpace(msg.Message.ID),
				ChannelIdentityID: strings.TrimSpace(identity.ChannelIdentityID),
				DisplayName:       strings.TrimSpace(identity.DisplayName),
				Channel:           msg.Channel.String(),
				ConversationType:  strings.TrimSpace(msg.Conversation.Type),
				ConversationName:  strings.TrimSpace(msg.Conversation.Name),
				Target:            strings.TrimSpace(msg.ReplyTarget),
				AttachmentPaths:   collectAttachmentPaths(attachments),
				Time:              time.Now().UTC(),
			}, text)

			switch inboundMode {
			case ModeInject:
				// Don't persist here — the injected message will be interleaved
				// at the correct position within the round by
				// interleaveInjectedMessages in storeRound.
				injected := p.dispatcher.Inject(routeID, InjectMessage{
					Text:            text,
					Attachments:     attachments,
					HeaderifiedText: headerifiedText,
				})
				if injected {
					p.sendModeConfirmation(ctx, sender, msg, identity, "inject")
				} else {
					if p.logger != nil {
						p.logger.Warn("inject failed (channel full), falling through to new stream",
							slog.String("route_id", routeID))
					}
					goto startStream
				}
				return nil

			case ModeQueue:
				p.persistPassiveMessage(ctx, identity, msg, text, attachments, routeID, sessionID, eventID)
				p.dispatcher.Enqueue(routeID, QueuedTask{
					Ctx:         ctx,
					Cfg:         cfg,
					Msg:         msg,
					Sender:      sender,
					Ident:       identity,
					Text:        text,
					Attachments: attachments,
				})
				p.sendModeConfirmation(ctx, sender, msg, identity, "queue")
				return nil
			}
		}
	}

startStream:

	// Issue chat token for reply routing.
	chatToken := ""
	if p.jwtSecret != "" && strings.TrimSpace(msg.ReplyTarget) != "" {
		signed, _, err := auth.GenerateChatToken(auth.ChatToken{
			BotID:             identity.BotID,
			ChatID:            activeChatID,
			RouteID:           resolved.RouteID,
			UserID:            identity.UserID,
			ChannelIdentityID: identity.ChannelIdentityID,
		}, p.jwtSecret, p.tokenTTL)
		if err != nil {
			if p.logger != nil {
				p.logger.Warn("issue chat token failed", slog.Any("error", err))
			}
		} else {
			chatToken = signed
		}
	}

	token := p.issueSessionBearerToken(ctx, identity, acpRuntimeSession, sessionRuntimeOwner, chatToken)

	var desc channel.Descriptor
	if p.registry != nil {
		desc, _ = p.registry.GetDescriptor(msg.Channel) //nolint:errcheck // descriptor lookup is best-effort
	}
	statusInfo := channel.ProcessingStatusInfo{
		BotID:             identity.BotID,
		ChatID:            activeChatID,
		RouteID:           resolved.RouteID,
		ChannelIdentityID: identity.ChannelIdentityID,
		UserID:            identity.UserID,
		Query:             text,
		ReplyTarget:       strings.TrimSpace(msg.ReplyTarget),
		SourceMessageID:   strings.TrimSpace(msg.Message.ID),
	}
	statusNotifier := p.resolveProcessingStatusNotifier(msg.Channel)
	statusHandle := channel.ProcessingStatusHandle{}
	if statusNotifier != nil {
		handle, notifyErr := p.notifyProcessingStarted(ctx, statusNotifier, cfg, msg, statusInfo)
		if notifyErr != nil {
			p.logProcessingStatusError("processing_started", msg, identity, notifyErr)
		} else {
			statusHandle = handle
		}
	}
	target := strings.TrimSpace(msg.ReplyTarget)
	if target == "" {
		err := errors.New("reply target missing")
		if statusNotifier != nil {
			if notifyErr := p.notifyProcessingFailed(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle, err); notifyErr != nil {
				p.logProcessingStatusError("processing_failed", msg, identity, notifyErr)
			}
		}
		return err
	}
	sourceMessageID := strings.TrimSpace(msg.Message.ID)
	replyRef := &channel.ReplyRef{Target: target}
	if sourceMessageID != "" {
		replyRef.MessageID = sourceMessageID
	}
	stream, err := sender.OpenStream(ctx, target, channel.StreamOptions{
		Reply:           replyRef,
		SourceMessageID: sourceMessageID,
		Metadata: map[string]any{
			"route_id":          resolved.RouteID,
			"conversation_type": msg.Conversation.Type,
		},
	})
	if err != nil {
		if statusNotifier != nil {
			if notifyErr := p.notifyProcessingFailed(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle, err); notifyErr != nil {
				p.logProcessingStatusError("processing_failed", msg, identity, notifyErr)
			}
		}
		return err
	}
	streamClosed := false
	closeStream := func() error {
		if streamClosed {
			return nil
		}
		streamClosed = true
		return stream.Close(context.WithoutCancel(ctx))
	}
	defer func() {
		if streamClosed {
			return
		}
		if closeErr := closeStream(); closeErr != nil {
			if p.logger != nil {
				p.logger.Error(
					"reply stream close failed",
					slog.String("channel", msg.Channel.String()),
					slog.String("channel_identity_id", identity.ChannelIdentityID),
					slog.String("user_id", identity.UserID),
					slog.Any("error", closeErr),
				)
			}
			if retErr == nil {
				retErr = closeErr
			}
		}
	}()

	// For non-local channels (IM adapters), optionally drop tool_call events
	// before they reach the adapter when the bot's show_tool_calls_in_im
	// setting is off. The filter sits inside the TeeStream so WebUI
	// observers still receive the full event stream.
	if !isLocalChannelType(msg.Channel) && !p.shouldShowToolCallsInIM(ctx, identity.BotID) {
		stream = channel.NewToolCallDroppingStream(stream)
	}

	// For non-local channels, wrap the stream so events are mirrored to the
	// RouteHub (and thus to Web UI and other local subscribers).
	if p.observer != nil && !isLocalChannelType(msg.Channel) {
		stream = channel.NewTeeStream(stream, p.observer, strings.TrimSpace(identity.BotID), msg.Channel)
		// Broadcast the inbound user message so WebUI can display it.
		broadcastText := text
		if pendingSkillIntent != nil {
			broadcastText = userVisibleText
		}
		p.broadcastInboundMessage(ctx, strings.TrimSpace(identity.BotID), msg, broadcastText, identity, resolvedAttachments)
	}

	if err := stream.Push(ctx, channel.StreamEvent{
		Type:   channel.StreamEventStatus,
		Status: channel.StreamStatusStarted,
	}); err != nil {
		if statusNotifier != nil {
			if notifyErr := p.notifyProcessingFailed(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle, err); notifyErr != nil {
				p.logProcessingStatusError("processing_failed", msg, identity, notifyErr)
			}
		}
		return err
	}

	// Mark this route as active in the dispatcher so subsequent messages
	// can be injected or queued. The dispatcher's queue is forwarded into
	// the run handle after StartTurn. Parallel mode (/now) skips the
	// dispatcher entirely — it must not interfere with the active flag or
	// drain the queue of another stream.
	var injectCh <-chan turn.InjectMessage
	if p.dispatcher != nil && !isLocalChannelType(msg.Channel) && inboundMode != ModeParallel {
		injectCh = p.dispatcher.MarkActive(routeID)
		defer func() {
			p.drainQueue(context.WithoutCancel(ctx), routeID)
		}()
	}

	cmd := turn.StartTurnCommand{
		SchemaVersion:             1,
		TeamID:                    cfg.TeamID,
		Mode:                      turn.ModeChat,
		BotID:                     identity.BotID,
		ChatID:                    activeChatID,
		ThreadID:                  sessionID,
		Token:                     token,
		UserID:                    identity.UserID,
		SourceChannelIdentityID:   identity.ChannelIdentityID,
		DisplayName:               identity.DisplayName,
		RouteID:                   resolved.RouteID,
		ChatToken:                 chatToken,
		IdempotencyKey:            turnIdempotencyKey(msg.Channel, resolved.RouteID, sourceMessageID),
		ExternalMessageID:         sourceMessageID,
		ReplyTarget:               target,
		ConversationType:          msg.Conversation.Type,
		ConversationName:          msg.Conversation.Name,
		SourceReplyToMessageID:    inboundReplyMessageID(msg.Message.Reply),
		ReplySender:               inboundReplySender(msg.Message.Reply),
		ReplyPreview:              inboundReplyPreview(msg.Message.Reply),
		ReplyAttachments:          replyAttachments,
		MentionsBot:               metadataBool(msg.Metadata, "is_mentioned"),
		RepliesToBot:              metadataBool(msg.Metadata, "is_reply_to_bot"),
		ForwardMessageID:          inboundForwardMessageID(msg.Message.Forward),
		ForwardFromUserID:         inboundForwardFromUserID(msg.Message.Forward),
		ForwardFromConversationID: inboundForwardFromConversationID(msg.Message.Forward),
		ForwardSender:             inboundForwardSender(msg.Message.Forward),
		ForwardDate:               inboundForwardDate(msg.Message.Forward),
		Query:                     text,
		ModelQuery:                modelText,
		UserMessageKind:           userMessageKind,
		UserVisibleText:           userVisibleText,
		SkillActivation:           skillActivation,
		SkipMemoryExtraction:      pendingSkillIntent != nil && userVisibleText == "",
		SkipTitleGeneration:       pendingSkillIntent != nil && userVisibleText == "",
		CurrentChannel:            msg.Channel.String(),
		Channels:                  []string{msg.Channel.String()},
		UserMessagePersisted:      false,
		Attachments:               attachments,
		RequestedSkills:           requestedSkillContexts,
		EventID:                   eventID,
	}
	if mid, _ := msg.Metadata["model_id"].(string); strings.TrimSpace(mid) != "" {
		cmd.Model = strings.TrimSpace(mid)
	}
	if re, _ := msg.Metadata["reasoning_effort"].(string); strings.TrimSpace(re) != "" {
		cmd.ReasoningEffort = strings.TrimSpace(re)
	}
	if targetID, _ := msg.Metadata["workspace_target_id"].(string); strings.TrimSpace(targetID) != "" {
		cmd.WorkspaceTargetID = strings.TrimSpace(targetID)
	}
	// Create a cancellable context so /stop can abort the stream.
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	streamKey := strings.TrimSpace(identity.BotID) + ":" + strings.TrimSpace(resolved.RouteID)
	p.activeStreams.Store(streamKey, streamCancel)
	defer p.activeStreams.Delete(streamKey)

	handle, startErr := p.turnSvc.StartTurn(streamCtx, cmd)
	if startErr != nil {
		if errors.Is(startErr, turn.ErrDuplicateTurn) {
			// Platform webhook redelivery of an already-claimed message:
			// treat as delivered, no user-visible error. The processing
			// marker added above must still be cleared — on Feishu it is a
			// reaction on the source message that otherwise sticks forever.
			if p.logger != nil {
				p.logger.Info(
					"duplicate inbound turn dropped",
					slog.String("channel", msg.Channel.String()),
					slog.String("external_message_id", sourceMessageID),
				)
			}
			if statusNotifier != nil {
				if notifyErr := p.notifyProcessingCompleted(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle); notifyErr != nil {
					p.logProcessingStatusError("processing_completed", msg, identity, notifyErr)
				}
			}
			return nil
		}
		if errors.Is(startErr, turn.ErrSessionBusy) {
			// The thread is already running a turn and the runtime persisted
			// nothing for this message, so the platform's own retry is what
			// carries it: the redelivery repeats this idempotency key and is
			// admitted as the same invocation once the thread frees up. Reporting
			// an error to the user would describe a transient queueing detail as a
			// failure, so only the marker is cleared and the error is returned to
			// the adapter, whose non-2xx response is what asks for the retry.
			if p.logger != nil {
				p.logger.Info(
					"inbound turn deferred: thread busy",
					slog.String("channel", msg.Channel.String()),
					slog.String("external_message_id", sourceMessageID),
				)
			}
			if statusNotifier != nil {
				if notifyErr := p.notifyProcessingCompleted(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle); notifyErr != nil {
					p.logProcessingStatusError("processing_completed", msg, identity, notifyErr)
				}
			}
			return startErr
		}
		if p.logger != nil {
			p.logger.Error(
				"start turn failed",
				slog.String("channel", msg.Channel.String()),
				slog.String("channel_identity_id", identity.ChannelIdentityID),
				slog.Any("error", startErr),
			)
		}
		_ = stream.Push(ctx, channel.StreamEvent{
			Type:  channel.StreamEventError,
			Error: startErr.Error(),
		})
		if statusNotifier != nil {
			if notifyErr := p.notifyProcessingFailed(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle, startErr); notifyErr != nil {
				p.logProcessingStatusError("processing_failed", msg, identity, notifyErr)
			}
		}
		return startErr
	}

	// Ordinal bookkeeping plus forwarding of outbound asset refs into the
	// running turn; the resolver attaches them at persist time.
	assets := &assetTracker{run: handle}

	// Forward queued inject messages into the running turn.
	if injectCh != nil {
		go func() {
			for {
				select {
				case m, ok := <-injectCh:
					if !ok {
						return
					}
					if injectErr := handle.Inject(streamCtx, m); injectErr != nil {
						// The message is lost and this forwarder stops; later
						// queued messages surface via drainQueue at turn end.
						// Losing this silently would contradict the 👀 receipt
						// the user already got.
						if p.logger != nil {
							p.logger.Warn(
								"inject into running turn failed, message dropped",
								slog.String("channel", msg.Channel.String()),
								slog.String("route_id", routeID),
								slog.Any("error", injectErr),
							)
						}
						return
					}
				case <-streamCtx.Done():
					return
				}
			}
		}()
	}

	chunkCh, streamErrCh := handle.Events(), handle.Errs()

	var (
		finalMessages []turn.ModelMessage
		streamErr     error
		pushBroken    bool
	)
	for chunkCh != nil || streamErrCh != nil {
		select {
		case turnEvent, ok := <-chunkCh:
			if !ok {
				chunkCh = nil
				continue
			}
			events, messages, parseErr := mapStreamChunkToChannelEvents(turnEvent.Payload)
			if parseErr != nil {
				if p.logger != nil {
					p.logger.Warn(
						"stream chunk parse failed",
						slog.String("channel", msg.Channel.String()),
						slog.String("channel_identity_id", identity.ChannelIdentityID),
						slog.String("user_id", identity.UserID),
						slog.Any("error", parseErr),
					)
				}
				continue
			}
			for i, event := range events {
				if isUserInputEvent(&events[i]) {
					events[i].ToolCall.Locale = p.localizer(ctx, identity.BotID).Locale()
				}
				if event.Type == channel.StreamEventAttachment && len(event.Attachments) > 0 {
					ingested := p.ingestOutboundAttachments(ctx, strings.TrimSpace(identity.BotID), msg.Channel, event.Attachments)
					events[i].Attachments = ingested
					assets.add(ingested)
				}
				if event.Type == channel.StreamEventReaction && len(event.Reactions) > 0 {
					p.dispatchReactions(ctx, identity.BotID, msg.Channel, target, sourceMessageID, event.Reactions)
					continue
				}
				if event.Type == channel.StreamEventSpeech && len(event.Speeches) > 0 {
					p.synthesizeAndPushVoice(ctx, strings.TrimSpace(identity.BotID), msg.Channel, event.Speeches, stream, assets)
					continue
				}
				if pushErr := stream.Push(ctx, events[i]); pushErr != nil {
					if streamErr == nil {
						streamErr = pushErr
					}
					pushBroken = true
					break
				}
			}
			if len(messages) > 0 {
				finalMessages = messages
			}
		case err, ok := <-streamErrCh:
			if !ok {
				streamErrCh = nil
				continue
			}
			// A run error must not abandon events already produced before
			// it (buffered text deltas, agent_end with the final
			// messages): keep consuming until the event channel closes,
			// matching the pre-split unbuffered ordering. Only a broken
			// push transport stops consumption early.
			if err != nil && streamErr == nil {
				streamErr = err
			}
		}
		if pushBroken {
			break
		}
	}

	if streamErr != nil {
		if p.logger != nil {
			p.logger.Error(
				"chat gateway stream failed",
				slog.String("channel", msg.Channel.String()),
				slog.String("channel_identity_id", identity.ChannelIdentityID),
				slog.String("user_id", identity.UserID),
				slog.Any("error", streamErr),
			)
		}
		if feedback := acpFeedbackFromError(streamErr); feedback != nil {
			_ = stream.Push(ctx, channel.StreamEvent{
				Type:  channel.StreamEventError,
				Error: strings.TrimSpace(feedback.Message),
			})
			if statusNotifier != nil {
				if notifyErr := p.notifyProcessingFailed(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle, streamErr); notifyErr != nil {
					p.logProcessingStatusError("processing_failed", msg, identity, notifyErr)
				}
			}
			_ = p.sendACPFeedbackError(ctx, sender, msg, identity, feedback)
			return streamErr
		}
		_ = stream.Push(ctx, channel.StreamEvent{
			Type:  channel.StreamEventError,
			Error: streamErr.Error(),
		})
		if statusNotifier != nil {
			if notifyErr := p.notifyProcessingFailed(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle, streamErr); notifyErr != nil {
				p.logProcessingStatusError("processing_failed", msg, identity, notifyErr)
			}
		}
		return streamErr
	}

	sentTexts, suppressReplies := collectMessageToolContext(p.registry, finalMessages, msg.Channel, target)
	if suppressReplies {
		if err := stream.Push(ctx, channel.StreamEvent{
			Type:   channel.StreamEventStatus,
			Status: channel.StreamStatusCompleted,
		}); err != nil {
			return err
		}
		if err := closeStream(); err != nil {
			if statusNotifier != nil {
				if notifyErr := p.notifyProcessingFailed(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle, err); notifyErr != nil {
					p.logProcessingStatusError("processing_failed", msg, identity, notifyErr)
				}
			}
			return err
		}
		if statusNotifier != nil {
			if notifyErr := p.notifyProcessingCompleted(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle); notifyErr != nil {
				p.logProcessingStatusError("processing_completed", msg, identity, notifyErr)
			}
		}
		return nil
	}

	outputs := turn.ExtractAssistantOutputs(finalMessages)
	for _, output := range outputs {
		outMessage := buildChannelMessage(output, desc.Capabilities)
		if outMessage.IsEmpty() {
			continue
		}
		plainText := strings.TrimSpace(outMessage.PlainText())
		if isSilentReplyText(plainText) {
			continue
		}
		if isMessagingToolDuplicate(plainText, sentTexts) {
			continue
		}
		if outMessage.Reply == nil && sourceMessageID != "" {
			outMessage.Reply = &channel.ReplyRef{
				Target:    target,
				MessageID: sourceMessageID,
			}
		}
		if err := stream.Push(ctx, channel.StreamEvent{
			Type: channel.StreamEventFinal,
			Final: &channel.StreamFinalizePayload{
				Message: outMessage,
			},
		}); err != nil {
			return err
		}
	}
	if err := stream.Push(ctx, channel.StreamEvent{
		Type:   channel.StreamEventStatus,
		Status: channel.StreamStatusCompleted,
	}); err != nil {
		return err
	}
	if err := closeStream(); err != nil {
		if statusNotifier != nil {
			if notifyErr := p.notifyProcessingFailed(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle, err); notifyErr != nil {
				p.logProcessingStatusError("processing_failed", msg, identity, notifyErr)
			}
		}
		return err
	}
	if statusNotifier != nil {
		if notifyErr := p.notifyProcessingCompleted(ctx, statusNotifier, cfg, msg, statusInfo, statusHandle); notifyErr != nil {
			p.logProcessingStatusError("processing_completed", msg, identity, notifyErr)
		}
	}
	return nil
}

func turnIdempotencyKey(channelType channel.ChannelType, routeID, externalMessageID string) string {
	externalMessageID = strings.TrimSpace(externalMessageID)
	if externalMessageID == "" {
		return ""
	}
	return strings.Join([]string{
		strings.TrimSpace(channelType.String()),
		strings.TrimSpace(routeID),
		externalMessageID,
	}, ":")
}

// sendModeConfirmation sends a lightweight acknowledgement to the user when
// their message is injected or queued rather than triggering a new stream.
func (p *ChannelInboundProcessor) sendModeConfirmation(
	ctx context.Context,
	_ channel.StreamReplySender,
	msg channel.InboundMessage,
	identity InboundIdentity,
	mode string,
) {
	target := strings.TrimSpace(msg.ReplyTarget)
	sourceMessageID := strings.TrimSpace(msg.Message.ID)
	if target == "" || sourceMessageID == "" {
		return
	}
	if p.reactor != nil {
		emoji := "👀"
		if mode == "queue" {
			emoji = "📋"
		}
		_ = p.reactor.React(ctx, strings.TrimSpace(identity.BotID), msg.Channel, channel.ReactRequest{
			Target:    target,
			MessageID: sourceMessageID,
			Emoji:     emoji,
		})
	}
}

func (p *ChannelInboundProcessor) accessDeniedRole(ctx context.Context, identity InboundIdentity) string {
	if p == nil || p.commandHandler == nil {
		return ""
	}
	role, err := p.commandHandler.MemberRole(ctx, identity.BotID, identity.ChannelIdentityID)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("resolve acl-denied role failed",
				slog.String("bot_id", strings.TrimSpace(identity.BotID)),
				slog.String("channel_identity_id", strings.TrimSpace(identity.ChannelIdentityID)),
				slog.Any("error", err),
			)
		}
		return ""
	}
	return role
}

// drainQueue marks the route as done and processes any queued tasks.
func (p *ChannelInboundProcessor) drainQueue(ctx context.Context, routeID string) {
	if p.dispatcher == nil {
		return
	}
	result := p.dispatcher.MarkDone(routeID)

	for _, fn := range result.PendingPersists {
		fn(ctx)
	}

	for _, task := range result.QueuedTasks {
		if p.logger != nil {
			p.logger.Info("processing queued task",
				slog.String("route_id", routeID),
				slog.String("query", strings.TrimSpace(task.Text)),
			)
		}
		if err := p.HandleInbound(ctx, task.Cfg, task.Msg, task.Sender); err != nil { //nolint:contextcheck // ctx is already WithoutCancel from the defer caller
			if p.logger != nil {
				p.logger.Error("queued task processing failed",
					slog.String("route_id", routeID),
					slog.Any("error", err),
				)
			}
		}
	}
}

func collectAttachmentPaths(attachments []turn.Attachment) []string {
	if len(attachments) == 0 {
		return nil
	}
	paths := make([]string, 0, len(attachments))
	for _, att := range attachments {
		if p := strings.TrimSpace(att.Path); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

func shouldTriggerAssistantResponse(msg channel.InboundMessage) bool {
	if isDirectConversationType(msg.Conversation.Type) {
		return true
	}
	if metadataBool(msg.Metadata, "is_mentioned") {
		return true
	}
	if metadataBool(msg.Metadata, "is_reply_to_bot") {
		return true
	}
	return false
}

// isDirectedAtBot reports whether the message is explicitly directed at this bot,
// either because it's a direct conversation, the bot is @mentioned, or it's a reply
// to this bot's message.
func isDirectedAtBot(msg channel.InboundMessage) bool {
	if isDirectConversationType(msg.Conversation.Type) {
		return true
	}
	return metadataBool(msg.Metadata, "is_mentioned") || metadataBool(msg.Metadata, "is_reply_to_bot")
}

func (p *ChannelInboundProcessor) classifyChannelSlash(text string, msg channel.InboundMessage, identity InboundIdentity) slash.Decision {
	return slash.Classify(slash.ClassifyInput{
		Text:           text,
		HasAttachments: hasSlashControlAttachments(msg),
		Surface:        slash.SurfaceChannel,
		IsGroup:        !channel.IsPrivateConversationType(msg.Conversation.Type),
		Directed:       isDirectedAtBot(msg),
		SupportsMode:   !isLocalChannelType(msg.Channel),
		BotAliases:     channelSlashAliases(msg, identity),
		KnownCommand: func(resource string) bool {
			return isChannelControlResource(resource) ||
				(p.commandHandler != nil && p.commandHandler.HasCommandResource(resource))
		},
	})
}

func isChannelControlResource(resource string) bool {
	switch strings.ToLower(strings.TrimSpace(resource)) {
	case "start", "new", "stop", "status", "context", "approve", "reject", "respond":
		return true
	default:
		return false
	}
}

func invocationHasResource(invocation *command.Invocation, resources ...string) bool {
	if invocation == nil {
		return false
	}
	resource := strings.ToLower(strings.TrimSpace(invocation.Parsed.Resource))
	for _, candidate := range resources {
		if resource == candidate {
			return true
		}
	}
	return false
}

func hasSlashControlAttachments(msg channel.InboundMessage) bool {
	if len(msg.Message.Attachments) > 0 {
		return true
	}
	if replyHasOrMayHaveAttachments(msg.Message.Reply) {
		return true
	}
	if forwardHasOrMayHaveAttachments(msg.Message.Forward) {
		return true
	}
	return false
}

func replyHasOrMayHaveAttachments(reply *channel.ReplyRef) bool {
	if reply == nil {
		return false
	}
	if len(reply.Attachments) > 0 {
		return true
	}
	if reply.AttachmentsKnown {
		return false
	}
	return strings.TrimSpace(reply.MessageID) != "" ||
		strings.TrimSpace(reply.Target) != "" ||
		strings.TrimSpace(reply.Sender) != "" ||
		strings.TrimSpace(reply.Preview) != ""
}

func forwardHasOrMayHaveAttachments(forward *channel.ForwardRef) bool {
	if forward == nil {
		return false
	}
	if forward.AttachmentsKnown {
		return false
	}
	return strings.TrimSpace(forward.MessageID) != "" ||
		strings.TrimSpace(forward.FromUserID) != "" ||
		strings.TrimSpace(forward.FromConversationID) != "" ||
		strings.TrimSpace(forward.Sender) != "" ||
		forward.Date > 0
}

func channelSlashAliases(msg channel.InboundMessage, identity InboundIdentity) []string {
	aliases := []string{
		identity.BotID,
		msg.BotID,
	}
	for _, key := range []string{"bot_username", "bot_name", "bot_alias"} {
		if value, ok := msg.Metadata[key].(string); ok {
			aliases = append(aliases, value)
		}
	}
	if values, ok := msg.Metadata["bot_aliases"].([]string); ok {
		aliases = append(aliases, values...)
	}
	out := make([]string, 0, len(aliases))
	seen := map[string]struct{}{}
	for _, alias := range aliases {
		alias = strings.Trim(strings.TrimSpace(alias), "@")
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, alias)
	}
	return out
}

func (p *ChannelInboundProcessor) sendSlashError(ctx context.Context, sender channel.StreamReplySender, msg channel.InboundMessage, code string) error {
	if code == "" {
		code = slash.CodeUnknownSlash
	}
	out := applyMessageFormat(channel.Message{Text: slashChannelMessage(p.localizer(ctx, msg.BotID), code)}, p.channelCaps(msg.Channel))
	if mid := strings.TrimSpace(msg.Message.ID); mid != "" {
		out.Reply = &channel.ReplyRef{MessageID: mid}
	}
	return sender.Send(ctx, channel.OutboundMessage{
		Target:  strings.TrimSpace(msg.ReplyTarget),
		Message: out,
	})
}

func slashChannelMessage(t *i18n.Localizer, code string) string {
	if key := slashChannelMessageKey(code); key != "" {
		return t.T(key)
	}
	return t.T("slash.error.generic")
}

func slashChannelMessageKey(code string) string {
	switch code {
	case slash.CodeUnknownSlash:
		return "slash.error.unknownSlash"
	case slash.CodeUnsupportedWebCommand:
		return "slash.error.unsupportedWebCommand"
	case slash.CodeInvalidSkillSlashSyntax:
		return "slash.error.invalidSkillSlashSyntax"
	case slash.CodeRequestedSkillNotFound:
		return "slash.error.requestedSkillNotFound"
	case slash.CodeRequestedSkillAmbiguous:
		return "slash.error.requestedSkillAmbiguous"
	case slash.CodeRequestedSkillDisabled:
		return "slash.error.requestedSkillDisabled"
	case slash.CodeRequestedSkillNotRuntimeUsable:
		return "slash.error.requestedSkillNotRuntimeUsable"
	case slash.CodeTooManyRequestedSkills:
		return "slash.error.tooManyRequestedSkills"
	case slash.CodeRequestedSkillContextTooLarge:
		return "slash.error.requestedSkillContextTooLarge"
	case slash.CodeSlashAttachmentsUnsupported:
		return "slash.error.slashAttachmentsUnsupported"
	case slash.CodeUnsupportedSkillSlashContext:
		return "slash.error.unsupportedSkillSlashContext"
	case slash.CodeUnsupportedLegacyEndpoint:
		return "slash.error.unsupportedLegacyEndpoint"
	case slash.CodeInvalidQuickActionScope:
		return "slash.error.invalidQuickActionScope"
	case slash.CodePermissionDenied:
		return "slash.error.permissionDenied"
	case slash.CodeReservedSkillMetadata:
		return "slash.error.reservedSkillMetadata"
	default:
		return ""
	}
}

func slashErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var slashErr slash.Error
	if errors.As(err, &slashErr) {
		return slashErr.Code
	}
	return ""
}

func (p *ChannelInboundProcessor) resolveChannelRequestedSkills(ctx context.Context, botID string, names []string) ([]skillset.ResolvedSkill, error) {
	if p.skillResolver == nil {
		return nil, slash.NewError(slash.CodeRequestedSkillNotRuntimeUsable)
	}
	return p.skillResolver.ResolveTextRequestedSkills(ctx, botID, names)
}

// channelCaps returns the capability matrix for a channel type, or the zero
// value when no registry is configured.
func (p *ChannelInboundProcessor) channelCaps(channelType channel.ChannelType) channel.ChannelCapabilities {
	if p.registry == nil {
		return channel.ChannelCapabilities{}
	}
	caps, _ := p.registry.GetCapabilities(channelType)
	return caps
}

// rawTextForCommand returns the original user text (without prepended
// quote/forward context) for slash-command detection. Adapters store the
// undecorated text as metadata["raw_text"]; this helper falls back to the
// full decorated text when the key is absent (e.g. direct messages or
// adapters that don't prepend context).
func rawTextForCommand(msg channel.InboundMessage, fallback string) string {
	if raw, ok := msg.Metadata["raw_text"].(string); ok && strings.TrimSpace(raw) != "" {
		return raw
	}
	return fallback
}

func inboundReplyMessageID(reply *channel.ReplyRef) string {
	if reply == nil {
		return ""
	}
	return strings.TrimSpace(reply.MessageID)
}

func inboundReplySender(reply *channel.ReplyRef) string {
	if reply == nil {
		return ""
	}
	return strings.TrimSpace(reply.Sender)
}

func inboundReplyPreview(reply *channel.ReplyRef) string {
	if reply == nil {
		return ""
	}
	return strings.TrimSpace(reply.Preview)
}

func replyAttachmentsFromMessage(reply *channel.ReplyRef) []channel.Attachment {
	if reply == nil {
		return nil
	}
	return reply.Attachments
}

func inboundForwardMessageID(forward *channel.ForwardRef) string {
	if forward == nil {
		return ""
	}
	return strings.TrimSpace(forward.MessageID)
}

func inboundForwardFromUserID(forward *channel.ForwardRef) string {
	if forward == nil {
		return ""
	}
	return strings.TrimSpace(forward.FromUserID)
}

func inboundForwardFromConversationID(forward *channel.ForwardRef) string {
	if forward == nil {
		return ""
	}
	return strings.TrimSpace(forward.FromConversationID)
}

func inboundForwardSender(forward *channel.ForwardRef) string {
	if forward == nil {
		return ""
	}
	return strings.TrimSpace(forward.Sender)
}

func inboundForwardDate(forward *channel.ForwardRef) int64 {
	if forward == nil {
		return 0
	}
	return forward.Date
}

func messageReplyMetadata(reply *channel.ReplyRef) map[string]any {
	if reply == nil {
		return nil
	}
	result := map[string]any{}
	if v := strings.TrimSpace(reply.MessageID); v != "" {
		result["message_id"] = v
	}
	if v := strings.TrimSpace(reply.Sender); v != "" {
		result["sender"] = v
	}
	if v := strings.TrimSpace(reply.Preview); v != "" {
		result["preview"] = v
	}
	if attachments := channelAttachmentMetadata(reply.Attachments); len(attachments) > 0 {
		result["attachments"] = attachments
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func channelAttachmentMetadata(attachments []channel.Attachment) []map[string]any {
	if len(attachments) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(attachments))
	for _, att := range attachments {
		item := channel.BundleFromAttachment(att).ToMap()
		if len(item) > 0 {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func messageForwardMetadata(forward *channel.ForwardRef) map[string]any {
	if forward == nil {
		return nil
	}
	result := map[string]any{}
	if v := strings.TrimSpace(forward.MessageID); v != "" {
		result["message_id"] = v
	}
	if v := strings.TrimSpace(forward.FromUserID); v != "" {
		result["from_user_id"] = v
	}
	if v := strings.TrimSpace(forward.FromConversationID); v != "" {
		result["from_conversation_id"] = v
	}
	if v := strings.TrimSpace(forward.Sender); v != "" {
		result["sender"] = v
	}
	if forward.Date > 0 {
		result["date"] = forward.Date
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func isDirectConversationType(conversationType string) bool {
	return channel.IsPrivateConversationType(conversationType)
}

func metadataBool(metadata map[string]any, key string) bool {
	if metadata == nil {
		return false
	}
	raw, ok := metadata[key]
	if !ok {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// persistPassiveMessage writes a user message directly into bot_history_messages
// for group conversations where the bot was not @mentioned. This replaces the
// old inbox system — the message is stored in the route's active session so it
// becomes part of the conversation history the next time the agent is triggered.
func (p *ChannelInboundProcessor) persistPassiveMessage(
	ctx context.Context,
	ident InboundIdentity,
	msg channel.InboundMessage,
	text string,
	attachments []turn.Attachment,
	routeID, sessionID, eventID string,
) {
	if p.message == nil {
		return
	}
	botID := strings.TrimSpace(ident.BotID)
	if botID == "" {
		return
	}
	trimmedText := strings.TrimSpace(text)
	if trimmedText == "" && len(attachments) == 0 {
		return
	}

	var attachmentPaths []string
	for _, att := range attachments {
		if ap := strings.TrimSpace(att.Path); ap != "" {
			attachmentPaths = append(attachmentPaths, ap)
		}
	}

	headerifiedText := turn.FormatUserHeader(turn.UserMessageHeaderInput{
		MessageID:         strings.TrimSpace(msg.Message.ID),
		ChannelIdentityID: strings.TrimSpace(ident.ChannelIdentityID),
		DisplayName:       strings.TrimSpace(ident.DisplayName),
		Channel:           msg.Channel.String(),
		ConversationType:  strings.TrimSpace(msg.Conversation.Type),
		ConversationName:  strings.TrimSpace(msg.Conversation.Name),
		Target:            strings.TrimSpace(msg.ReplyTarget),
		AttachmentPaths:   attachmentPaths,
		Time:              time.Now().UTC(),
	}, trimmedText)

	modelMsg := turn.ModelMessage{Role: "user", Content: turn.NewTextContent(headerifiedText)}
	serialized, err := json.Marshal(modelMsg)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("marshal passive message failed", slog.Any("error", err))
		}
		return
	}

	meta := map[string]any{
		"route_id": strings.TrimSpace(routeID),
		"platform": msg.Channel.String(),
	}
	if reply := messageReplyMetadata(msg.Message.Reply); reply != nil {
		meta["reply"] = reply
	}
	if forward := messageForwardMetadata(msg.Message.Forward); forward != nil {
		meta["forward"] = forward
	}

	var assets []messagepkg.AssetRef
	for i, att := range attachments {
		ch := strings.TrimSpace(att.ContentHash)
		if ch == "" {
			continue
		}
		ref := messagepkg.AssetRef{
			ContentHash: ch,
			Role:        "attachment",
			Ordinal:     i,
			Mime:        strings.TrimSpace(att.Mime),
			SizeBytes:   att.Size,
			Name:        strings.TrimSpace(att.Name),
			Metadata:    att.Metadata,
		}
		if att.Metadata != nil {
			if sk, ok := att.Metadata["storage_key"].(string); ok {
				ref.StorageKey = sk
			}
		}
		assets = append(assets, ref)
	}

	if _, err := p.message.Persist(ctx, messagepkg.PersistInput{
		BotID:                   botID,
		SessionID:               sessionID,
		SenderChannelIdentityID: strings.TrimSpace(ident.ChannelIdentityID),
		SenderUserID:            strings.TrimSpace(ident.UserID),
		ExternalMessageID:       strings.TrimSpace(msg.Message.ID),
		SourceReplyToMessageID:  inboundReplyMessageID(msg.Message.Reply),
		Role:                    "user",
		Content:                 serialized,
		Metadata:                meta,
		Assets:                  assets,
		EventID:                 eventID,
		DisplayText:             trimmedText,
	}); err != nil && p.logger != nil {
		p.logger.Warn("persist passive message failed", slog.Any("error", err), slog.String("bot_id", botID))
	}
}

func buildChannelMessage(output turn.AssistantOutput, capabilities channel.ChannelCapabilities) channel.Message {
	msg := channel.Message{}
	if strings.TrimSpace(output.Content) != "" {
		msg.Text = strings.TrimSpace(output.Content)
		if channel.ContainsMarkdown(msg.Text) && (capabilities.Markdown || capabilities.RichText) {
			msg.Format = channel.MessageFormatMarkdown
		}
	}
	if len(output.Parts) == 0 {
		return msg
	}
	if capabilities.RichText && hasNonTrivialContentPart(output.Parts) {
		parts := make([]channel.MessagePart, 0, len(output.Parts))
		for _, part := range output.Parts {
			if !contentPartHasValue(part) {
				continue
			}
			partType := normalizeContentPartType(part.Type)
			parts = append(parts, channel.MessagePart{
				Type:              partType,
				Text:              part.Text,
				URL:               part.URL,
				Styles:            normalizeContentPartStyles(part.Styles),
				Language:          part.Language,
				ChannelIdentityID: part.ChannelIdentityID,
				Emoji:             part.Emoji,
			})
		}
		if len(parts) > 0 {
			msg.Text = ""
			msg.Parts = parts
			msg.Format = channel.MessageFormatRich
		}
		return msg
	}
	textParts := make([]string, 0, len(output.Parts))
	for _, part := range output.Parts {
		if !contentPartHasValue(part) {
			continue
		}
		textParts = append(textParts, strings.TrimSpace(contentPartText(part)))
	}
	if len(textParts) > 0 {
		msg.Text = strings.Join(textParts, "\n")
		if msg.Format == "" && channel.ContainsMarkdown(msg.Text) && (capabilities.Markdown || capabilities.RichText) {
			msg.Format = channel.MessageFormatMarkdown
		}
	}
	return msg
}

func hasNonTrivialContentPart(parts []turn.ContentPart) bool {
	for _, part := range parts {
		if !contentPartHasValue(part) {
			continue
		}
		if normalizeContentPartType(part.Type) != channel.MessagePartText {
			return true
		}
		if len(normalizeContentPartStyles(part.Styles)) > 0 {
			return true
		}
		if strings.TrimSpace(part.URL) != "" ||
			strings.TrimSpace(part.Language) != "" ||
			strings.TrimSpace(part.ChannelIdentityID) != "" ||
			strings.TrimSpace(part.Emoji) != "" {
			return true
		}
	}
	return false
}

func contentPartHasValue(part turn.ContentPart) bool {
	if strings.TrimSpace(part.Text) != "" {
		return true
	}
	if strings.TrimSpace(part.URL) != "" {
		return true
	}
	if strings.TrimSpace(part.Emoji) != "" {
		return true
	}
	return false
}

func contentPartText(part turn.ContentPart) string {
	if strings.TrimSpace(part.Text) != "" {
		return part.Text
	}
	if strings.TrimSpace(part.URL) != "" {
		return part.URL
	}
	if strings.TrimSpace(part.Emoji) != "" {
		return part.Emoji
	}
	return ""
}

// agentStreamEnvelope is the JSON shape produced by internal/agent.StreamEvent.
type agentStreamEnvelope struct {
	Type     string              `json:"type"`
	Delta    string              `json:"delta"`
	Error    string              `json:"error"`
	Message  string              `json:"message"`
	Data     json.RawMessage     `json:"data"`
	Messages []turn.ModelMessage `json:"messages"`

	ToolName    string          `json:"toolName"`
	ToolCallID  string          `json:"toolCallId"`
	ApprovalID  string          `json:"approvalId"`
	UserInputID string          `json:"userInputId"`
	ShortID     int             `json:"shortId"`
	Status      string          `json:"status"`
	Input       json.RawMessage `json:"input"`
	Result      json.RawMessage `json:"result"`
	Metadata    json.RawMessage `json:"metadata"`
	Attachments json.RawMessage `json:"attachments"`
	Reactions   json.RawMessage `json:"reactions"`
	Speeches    json.RawMessage `json:"speeches"`
}

func mapStreamChunkToChannelEvents(chunk json.RawMessage) ([]channel.StreamEvent, []turn.ModelMessage, error) {
	if len(chunk) == 0 {
		return nil, nil, nil
	}
	var envelope agentStreamEnvelope
	if err := json.Unmarshal(chunk, &envelope); err != nil {
		return nil, nil, err
	}
	finalMessages := make([]turn.ModelMessage, 0, len(envelope.Messages))
	finalMessages = append(finalMessages, envelope.Messages...)
	eventType := strings.ToLower(strings.TrimSpace(envelope.Type))
	switch eventType {
	case "text_delta":
		if envelope.Delta == "" {
			return nil, finalMessages, nil
		}
		return []channel.StreamEvent{
			{
				Type:  channel.StreamEventDelta,
				Delta: envelope.Delta,
				Phase: channel.StreamPhaseText,
			},
		}, finalMessages, nil
	case "reasoning_delta":
		if envelope.Delta == "" {
			return nil, finalMessages, nil
		}
		return []channel.StreamEvent{
			{
				Type:  channel.StreamEventDelta,
				Delta: envelope.Delta,
				Phase: channel.StreamPhaseReasoning,
			},
		}, finalMessages, nil
	case "tool_call_start":
		return []channel.StreamEvent{
			{
				Type: channel.StreamEventToolCallStart,
				ToolCall: &channel.StreamToolCall{
					Name:   strings.TrimSpace(envelope.ToolName),
					CallID: strings.TrimSpace(envelope.ToolCallID),
					Input:  parseRawJSON(envelope.Input),
				},
			},
		}, finalMessages, nil
	case "tool_call_end":
		return []channel.StreamEvent{
			{
				Type: channel.StreamEventToolCallEnd,
				ToolCall: &channel.StreamToolCall{
					Name:   strings.TrimSpace(envelope.ToolName),
					CallID: strings.TrimSpace(envelope.ToolCallID),
					Input:  parseRawJSON(envelope.Input),
					Result: parseRawJSON(envelope.Result),
				},
			},
		}, finalMessages, nil
	case "tool_approval_request":
		return []channel.StreamEvent{
			{
				Type: channel.StreamEventToolCallStart,
				ToolCall: &channel.StreamToolCall{
					Name:       strings.TrimSpace(envelope.ToolName),
					CallID:     strings.TrimSpace(envelope.ToolCallID),
					Input:      parseRawJSON(envelope.Input),
					ApprovalID: strings.TrimSpace(envelope.ApprovalID),
					ShortID:    envelope.ShortID,
					Actions: []channel.Action{
						{Type: "tool_approval", Label: "Approve", Value: "approve:" + strings.TrimSpace(envelope.ApprovalID)},
						{Type: "tool_approval", Label: "Reject", Value: "reject:" + strings.TrimSpace(envelope.ApprovalID)},
					},
				},
			},
		}, finalMessages, nil
	case "user_input_request":
		userInputID := strings.TrimSpace(envelope.UserInputID)
		if userInputID == "" {
			userInputID = strings.TrimSpace(envelope.ApprovalID)
		}
		payload := canonicalUserInputPayload(envelope.Metadata, envelope.Input)
		input := map[string]any{
			"user_input_id": userInputID,
			"short_id":      envelope.ShortID,
			"status":        strings.TrimSpace(envelope.Status),
			"payload":       parseRawJSON(payload),
		}
		actions := userInputActions(userInputID)
		return []channel.StreamEvent{
			{
				Type: channel.StreamEventToolCallStart,
				ToolCall: &channel.StreamToolCall{
					Name:    strings.TrimSpace(envelope.ToolName),
					CallID:  strings.TrimSpace(envelope.ToolCallID),
					Input:   input,
					ShortID: envelope.ShortID,
					Actions: actions,
				},
			},
		}, finalMessages, nil
	case "reasoning_start":
		return []channel.StreamEvent{
			{Type: channel.StreamEventPhaseStart, Phase: channel.StreamPhaseReasoning},
		}, finalMessages, nil
	case "reasoning_end":
		return []channel.StreamEvent{
			{Type: channel.StreamEventPhaseEnd, Phase: channel.StreamPhaseReasoning},
		}, finalMessages, nil
	case "text_start":
		return []channel.StreamEvent{
			{Type: channel.StreamEventPhaseStart, Phase: channel.StreamPhaseText},
		}, finalMessages, nil
	case "text_end":
		return []channel.StreamEvent{
			{Type: channel.StreamEventPhaseEnd, Phase: channel.StreamPhaseText},
		}, finalMessages, nil
	case "attachment_delta":
		attachments := parseAttachmentDelta(envelope.Attachments)
		if len(attachments) == 0 {
			return nil, finalMessages, nil
		}
		return []channel.StreamEvent{
			{Type: channel.StreamEventAttachment, Attachments: attachments},
		}, finalMessages, nil
	case "reaction_delta":
		reactions := parseReactionDelta(envelope.Reactions)
		if len(reactions) == 0 {
			return nil, finalMessages, nil
		}
		return []channel.StreamEvent{
			{Type: channel.StreamEventReaction, Reactions: reactions},
		}, finalMessages, nil
	case "speech_delta":
		speeches := parseSpeechDelta(envelope.Speeches)
		if len(speeches) == 0 {
			return nil, finalMessages, nil
		}
		return []channel.StreamEvent{
			{Type: channel.StreamEventSpeech, Speeches: speeches},
		}, finalMessages, nil
	case "agent_start":
		return []channel.StreamEvent{
			{
				Type: channel.StreamEventAgentStart,
				Metadata: map[string]any{
					"input": parseRawJSON(envelope.Input),
					"data":  parseRawJSON(envelope.Data),
				},
			},
		}, finalMessages, nil
	case "agent_end":
		return []channel.StreamEvent{
			{
				Type: channel.StreamEventAgentEnd,
				Metadata: map[string]any{
					"result": parseRawJSON(envelope.Result),
					"data":   parseRawJSON(envelope.Data),
				},
			},
		}, finalMessages, nil
	case "processing_started":
		return []channel.StreamEvent{
			{Type: channel.StreamEventProcessingStarted},
		}, finalMessages, nil
	case "processing_completed":
		return []channel.StreamEvent{
			{Type: channel.StreamEventProcessingCompleted},
		}, finalMessages, nil
	case "processing_failed":
		streamError := strings.TrimSpace(envelope.Error)
		if streamError == "" {
			streamError = strings.TrimSpace(envelope.Message)
		}
		return []channel.StreamEvent{
			{
				Type:  channel.StreamEventProcessingFailed,
				Error: streamError,
			},
		}, finalMessages, nil
	case "error":
		streamError := strings.TrimSpace(envelope.Error)
		if streamError == "" {
			streamError = strings.TrimSpace(envelope.Message)
		}
		if streamError == "" {
			streamError = "stream error"
		}
		return []channel.StreamEvent{
			{
				Type:  channel.StreamEventError,
				Error: streamError,
			},
		}, finalMessages, nil
	default:
		return nil, finalMessages, nil
	}
}

func canonicalUserInputPayload(metadata, fallback json.RawMessage) json.RawMessage {
	var deferred struct {
		UIPayload json.RawMessage `json:"ui_payload"`
	}
	if err := json.Unmarshal(metadata, &deferred); err == nil && len(deferred.UIPayload) > 0 && string(deferred.UIPayload) != "null" {
		return deferred.UIPayload
	}
	return fallback
}

// userInputActions emits the single marker action for a pending ask_user
// request. The marker's Type ("user_input") is what keeps the tool-call
// filter from dropping the prompt; its Value is only ever consumed by the
// Telegram legacy respond: parser as a silent no-op. Adapters with native
// controls (Telegram) rebuild the real interactive UI from the payload —
// the shared layer must not fabricate per-option keyboards here, because no
// other adapter renders user_input buttons and Telegram replaces them anyway.
func userInputActions(userInputID string) []channel.Action {
	userInputID = strings.TrimSpace(userInputID)
	if userInputID == "" {
		return nil
	}
	return []channel.Action{{Type: "user_input", Label: "Reply", Value: "respond:" + userInputID}}
}

func normalizeContentPartType(raw string) channel.MessagePartType {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "link":
		return channel.MessagePartLink
	case "code_block":
		return channel.MessagePartCodeBlock
	case "mention":
		return channel.MessagePartMention
	case "emoji":
		return channel.MessagePartEmoji
	default:
		return channel.MessagePartText
	}
}

func normalizeContentPartStyles(styles []string) []channel.MessageTextStyle {
	if len(styles) == 0 {
		return nil
	}
	result := make([]channel.MessageTextStyle, 0, len(styles))
	for _, style := range styles {
		switch strings.TrimSpace(strings.ToLower(style)) {
		case "bold":
			result = append(result, channel.MessageStyleBold)
		case "italic":
			result = append(result, channel.MessageStyleItalic)
		case "strikethrough", "lineThrough":
			result = append(result, channel.MessageStyleStrikethrough)
		case "code":
			result = append(result, channel.MessageStyleCode)
		default:
			continue
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

type sendMessageToolArgs struct {
	Platform          string           `json:"platform"`
	Target            string           `json:"target"`
	ChannelIdentityID string           `json:"channel_identity_id"`
	Text              string           `json:"text"`
	Message           *channel.Message `json:"message"`
}

func collectMessageToolContext(registry *channel.Registry, messages []turn.ModelMessage, channelType channel.ChannelType, replyTarget string) ([]string, bool) {
	if len(messages) == 0 {
		return nil, false
	}
	var sentTexts []string
	suppressReplies := false
	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name != "send" && tc.Function.Name != "send_message" {
				continue
			}
			var args sendMessageToolArgs
			if !parseToolArguments(tc.Function.Arguments, &args) {
				continue
			}
			if text := strings.TrimSpace(extractSendMessageText(args)); text != "" {
				sentTexts = append(sentTexts, text)
			}
			if shouldSuppressForToolCall(registry, args, channelType, replyTarget) {
				suppressReplies = true
			}
		}
	}
	return sentTexts, suppressReplies
}

func parseToolArguments(raw string, out any) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	if err := json.Unmarshal([]byte(raw), out); err == nil {
		return true
	}
	var decoded string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return false
	}
	if strings.TrimSpace(decoded) == "" {
		return false
	}
	return json.Unmarshal([]byte(decoded), out) == nil
}

func extractSendMessageText(args sendMessageToolArgs) string {
	if strings.TrimSpace(args.Text) != "" {
		return strings.TrimSpace(args.Text)
	}
	if args.Message == nil {
		return ""
	}
	return strings.TrimSpace(args.Message.PlainText())
}

func shouldSuppressForToolCall(registry *channel.Registry, args sendMessageToolArgs, channelType channel.ChannelType, replyTarget string) bool {
	platform := strings.TrimSpace(args.Platform)
	if platform == "" {
		platform = string(channelType)
	}
	if !strings.EqualFold(platform, string(channelType)) {
		return false
	}
	target := strings.TrimSpace(args.Target)
	if target == "" && strings.TrimSpace(args.ChannelIdentityID) == "" {
		target = replyTarget
	}
	if strings.TrimSpace(target) == "" || strings.TrimSpace(replyTarget) == "" {
		return false
	}
	normalizedTarget := normalizeReplyTarget(registry, channelType, target)
	normalizedReply := normalizeReplyTarget(registry, channelType, replyTarget)
	if normalizedTarget == "" || normalizedReply == "" {
		return false
	}
	return normalizedTarget == normalizedReply
}

func normalizeReplyTarget(registry *channel.Registry, channelType channel.ChannelType, target string) string {
	if registry == nil {
		return strings.TrimSpace(target)
	}
	normalized, ok := registry.NormalizeTarget(channelType, target)
	if ok && strings.TrimSpace(normalized) != "" {
		return strings.TrimSpace(normalized)
	}
	return strings.TrimSpace(target)
}

func isSilentReplyText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	token := []rune(silentReplyToken)
	value := []rune(trimmed)
	if len(value) < len(token) {
		return false
	}
	if hasTokenPrefix(value, token) {
		return true
	}
	if hasTokenSuffix(value, token) {
		return true
	}
	return false
}

func hasTokenPrefix(value []rune, token []rune) bool {
	if len(value) < len(token) {
		return false
	}
	for i := range token {
		if value[i] != token[i] {
			return false
		}
	}
	if len(value) == len(token) {
		return true
	}
	return !isWordChar(value[len(token)])
}

func hasTokenSuffix(value []rune, token []rune) bool {
	if len(value) < len(token) {
		return false
	}
	start := len(value) - len(token)
	for i := range token {
		if value[start+i] != token[i] {
			return false
		}
	}
	if start == 0 {
		return true
	}
	return !isWordChar(value[start-1])
}

func isWordChar(value rune) bool {
	return value == '_' || unicode.IsLetter(value) || unicode.IsDigit(value)
}

func normalizeTextForComparison(text string) string {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if trimmed == "" {
		return ""
	}
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(trimmed, " "))
}

func isMessagingToolDuplicate(text string, sentTexts []string) bool {
	if len(sentTexts) == 0 {
		return false
	}
	normalized := normalizeTextForComparison(text)
	if len(normalized) < minDuplicateTextLength {
		return false
	}
	for _, sent := range sentTexts {
		sentNormalized := normalizeTextForComparison(sent)
		if len(sentNormalized) < minDuplicateTextLength {
			continue
		}
		if strings.Contains(normalized, sentNormalized) || strings.Contains(sentNormalized, normalized) {
			return true
		}
	}
	return false
}

// requireIdentity resolves identity for the current message.
// It first checks whether the middleware chain already resolved and stored an
// IdentityState in the context (via IdentityResolver.Middleware), and reuses
// that result to avoid a redundant round-trip to the identity store. If no
// cached state is found it falls back to a fresh Resolve call.
func (p *ChannelInboundProcessor) requireIdentity(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage) (IdentityState, error) {
	if p.identity == nil {
		return IdentityState{}, errors.New("identity resolver not configured")
	}
	if state, ok := IdentityStateFromContext(ctx); ok {
		return state, nil
	}
	return p.identity.Resolve(ctx, cfg, msg)
}

func (p *ChannelInboundProcessor) resolveProcessingStatusNotifier(channelType channel.ChannelType) channel.ProcessingStatusNotifier {
	if p == nil || p.registry == nil {
		return nil
	}
	notifier, ok := p.registry.GetProcessingStatusNotifier(channelType)
	if !ok {
		return nil
	}
	return notifier
}

func (*ChannelInboundProcessor) notifyProcessingStarted(
	ctx context.Context,
	notifier channel.ProcessingStatusNotifier,
	cfg channel.ChannelConfig,
	msg channel.InboundMessage,
	info channel.ProcessingStatusInfo,
) (channel.ProcessingStatusHandle, error) {
	if notifier == nil {
		return channel.ProcessingStatusHandle{}, nil
	}
	statusCtx, cancel := context.WithTimeout(ctx, processingStatusTimeout)
	defer cancel()
	return notifier.ProcessingStarted(statusCtx, cfg, msg, info)
}

func (*ChannelInboundProcessor) notifyProcessingCompleted(
	ctx context.Context,
	notifier channel.ProcessingStatusNotifier,
	cfg channel.ChannelConfig,
	msg channel.InboundMessage,
	info channel.ProcessingStatusInfo,
	handle channel.ProcessingStatusHandle,
) error {
	if notifier == nil {
		return nil
	}
	statusCtx, cancel := context.WithTimeout(ctx, processingStatusTimeout)
	defer cancel()
	return notifier.ProcessingCompleted(statusCtx, cfg, msg, info, handle)
}

func (*ChannelInboundProcessor) notifyProcessingFailed(
	ctx context.Context,
	notifier channel.ProcessingStatusNotifier,
	cfg channel.ChannelConfig,
	msg channel.InboundMessage,
	info channel.ProcessingStatusInfo,
	handle channel.ProcessingStatusHandle,
	cause error,
) error {
	if notifier == nil {
		return nil
	}
	statusCtx, cancel := context.WithTimeout(ctx, processingStatusTimeout)
	defer cancel()
	return notifier.ProcessingFailed(statusCtx, cfg, msg, info, handle, cause)
}

func (p *ChannelInboundProcessor) logProcessingStatusError(
	stage string,
	msg channel.InboundMessage,
	identity InboundIdentity,
	err error,
) {
	if p == nil || p.logger == nil || err == nil {
		return
	}
	p.logger.Warn(
		"processing status notify failed",
		slog.String("stage", stage),
		slog.String("channel", msg.Channel.String()),
		slog.String("channel_identity_id", identity.ChannelIdentityID),
		slog.String("user_id", identity.UserID),
		slog.Any("error", err),
	)
}

// parseRawJSON converts raw JSON bytes to a typed value for StreamToolCall fields.
func parseRawJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func (p *ChannelInboundProcessor) ingestInboundAttachments(
	ctx context.Context,
	cfg channel.ChannelConfig,
	msg channel.InboundMessage,
	botID string,
	attachments []channel.Attachment,
) []channel.Attachment {
	if len(attachments) == 0 || p == nil || p.mediaService == nil || strings.TrimSpace(botID) == "" {
		return attachments
	}
	result := make([]channel.Attachment, 0, len(attachments))
	for _, att := range attachments {
		item := att
		if strings.TrimSpace(item.ContentHash) != "" {
			result = append(result, item)
			continue
		}
		payload, err := p.loadInboundAttachmentPayload(ctx, cfg, msg, item)
		if err != nil {
			if p.logger != nil {
				p.logger.Warn(
					"inbound attachment ingest skipped",
					slog.Any("error", err),
					slog.String("attachment_type", strings.TrimSpace(string(item.Type))),
					slog.String("attachment_url", strings.TrimSpace(item.URL)),
					slog.String("platform_key", strings.TrimSpace(item.PlatformKey)),
				)
			}
			result = append(result, item)
			continue
		}
		sourceMime := attachment.NormalizeMime(item.Mime)
		if sourceMime == "" {
			sourceMime = attachment.NormalizeMime(payload.mime)
		}
		if strings.TrimSpace(item.Name) == "" {
			item.Name = strings.TrimSpace(payload.name)
		}
		if item.Size == 0 && payload.size > 0 {
			item.Size = payload.size
		}
		mediaType := attachment.MapMediaType(string(item.Type))
		preparedReader, finalMime, err := attachment.PrepareReaderAndMime(payload.reader, mediaType, sourceMime)
		if err != nil {
			if payload.reader != nil {
				_ = payload.reader.Close()
			}
			if p.logger != nil {
				p.logger.Warn(
					"inbound attachment mime prepare failed",
					slog.Any("error", err),
					slog.String("attachment_type", strings.TrimSpace(string(item.Type))),
					slog.String("attachment_url", strings.TrimSpace(item.URL)),
					slog.String("platform_key", strings.TrimSpace(item.PlatformKey)),
				)
			}
			result = append(result, item)
			continue
		}
		item.Mime = finalMime
		maxBytes := media.MaxAssetBytes
		asset, err := p.mediaService.Ingest(ctx, media.IngestInput{
			BotID:       botID,
			Mime:        strings.TrimSpace(item.Mime),
			Reader:      preparedReader,
			MaxBytes:    maxBytes,
			OriginalExt: filepath.Ext(strings.TrimSpace(item.Name)),
		})
		if payload.reader != nil {
			_ = payload.reader.Close()
		}
		if err != nil {
			if p.logger != nil {
				p.logger.Warn(
					"inbound attachment ingest failed",
					slog.Any("error", err),
					slog.String("attachment_type", strings.TrimSpace(string(item.Type))),
					slog.String("attachment_url", strings.TrimSpace(item.URL)),
					slog.String("platform_key", strings.TrimSpace(item.PlatformKey)),
				)
			}
			result = append(result, item)
			continue
		}
		item = channel.AttachmentFromBundle(channel.BundleFromAttachment(item).WithAssetAccess(
			botID,
			asset,
			p.mediaService.AccessPath(ctx, asset),
		))
		result = append(result, item)
	}
	return result
}

type inboundAttachmentPayload struct {
	reader io.ReadCloser
	mime   string
	name   string
	size   int64
}

func (p *ChannelInboundProcessor) loadInboundAttachmentPayload(
	ctx context.Context,
	cfg channel.ChannelConfig,
	msg channel.InboundMessage,
	att channel.Attachment,
) (inboundAttachmentPayload, error) {
	rawURL := strings.TrimSpace(att.URL)
	if rawURL != "" {
		payload, err := openInboundAttachmentURL(ctx, rawURL)
		if err == nil {
			if strings.TrimSpace(att.Mime) != "" {
				payload.mime = strings.TrimSpace(att.Mime)
			}
			if strings.TrimSpace(payload.name) == "" {
				payload.name = strings.TrimSpace(att.Name)
			}
			return payload, nil
		}
		// When URL download fails and no other source exists, return URL error.
		if strings.TrimSpace(att.PlatformKey) == "" && strings.TrimSpace(att.Base64) == "" {
			return inboundAttachmentPayload{}, err
		}
	}
	rawBase64 := strings.TrimSpace(att.Base64)
	if rawBase64 != "" {
		decoded, err := attachment.DecodeBase64(rawBase64, media.MaxAssetBytes)
		if err != nil {
			return inboundAttachmentPayload{}, fmt.Errorf("decode attachment base64: %w", err)
		}
		mimeType := strings.TrimSpace(att.Mime)
		if mimeType == "" {
			mimeType = strings.TrimSpace(attachment.MimeFromDataURL(rawBase64))
		}
		return inboundAttachmentPayload{
			reader: io.NopCloser(decoded),
			mime:   mimeType,
			name:   strings.TrimSpace(att.Name),
		}, nil
	}
	platformKey := strings.TrimSpace(att.PlatformKey)
	if platformKey == "" {
		return inboundAttachmentPayload{}, errors.New("attachment has no ingestible payload")
	}
	resolver := p.resolveAttachmentResolver(msg.Channel)
	if resolver == nil {
		return inboundAttachmentPayload{}, fmt.Errorf("attachment resolver not supported for channel: %s", msg.Channel.String())
	}
	resolved, err := resolver.ResolveAttachment(ctx, cfg, att)
	if err != nil {
		return inboundAttachmentPayload{}, fmt.Errorf("resolve attachment by platform key: %w", err)
	}
	if resolved.Reader == nil {
		return inboundAttachmentPayload{}, errors.New("resolved attachment reader is nil")
	}
	mime := strings.TrimSpace(att.Mime)
	if mime == "" {
		mime = strings.TrimSpace(resolved.Mime)
	}
	name := strings.TrimSpace(att.Name)
	if name == "" {
		name = strings.TrimSpace(resolved.Name)
	}
	return inboundAttachmentPayload{
		reader: resolved.Reader,
		mime:   mime,
		name:   name,
		size:   resolved.Size,
	}, nil
}

func (p *ChannelInboundProcessor) transcribeInboundAttachments(ctx context.Context, botID string, attachments []channel.Attachment) string {
	if p == nil || p.transcriber == nil || p.sttModelResolver == nil || p.mediaService == nil || strings.TrimSpace(botID) == "" {
		return ""
	}
	modelID, err := p.sttModelResolver.ResolveTranscriptionModelID(ctx, botID)
	if err != nil || strings.TrimSpace(modelID) == "" {
		return ""
	}
	transcripts := make([]string, 0, len(attachments))
	for _, att := range attachments {
		if att.Type != channel.AttachmentAudio && att.Type != channel.AttachmentVoice {
			continue
		}
		if strings.TrimSpace(att.ContentHash) == "" {
			continue
		}
		reader, asset, err := p.mediaService.Open(ctx, botID, strings.TrimSpace(att.ContentHash))
		if err != nil {
			if p.logger != nil {
				p.logger.Warn("open inbound audio for transcription failed", slog.Any("error", err), slog.String("bot_id", botID), slog.String("content_hash", att.ContentHash))
			}
			continue
		}
		audio, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil || len(audio) == 0 {
			if p.logger != nil {
				p.logger.Warn("read inbound audio for transcription failed", slog.Any("error", readErr), slog.String("bot_id", botID), slog.String("content_hash", att.ContentHash))
			}
			continue
		}
		filename := strings.TrimSpace(att.Name)
		if filename == "" {
			filename = "audio" + filepath.Ext(asset.StorageKey)
		}
		contentType := strings.TrimSpace(att.Mime)
		if contentType == "" {
			contentType = strings.TrimSpace(asset.Mime)
		}
		result, txErr := p.transcriber.Transcribe(ctx, modelID, audio, filename, contentType, nil)
		if txErr != nil {
			if p.logger != nil {
				p.logger.Warn("inbound attachment transcription failed", slog.Any("error", txErr), slog.String("bot_id", botID), slog.String("content_hash", att.ContentHash))
			}
			continue
		}
		text := strings.TrimSpace(result.GetText())
		if text == "" {
			continue
		}
		transcripts = append(transcripts, text)
	}
	if len(transcripts) == 0 {
		return ""
	}
	return strings.Join(transcripts, "\n\n")
}

func formatInboundTranscript(transcript string) string {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return ""
	}
	return "[Voice message transcription]\n" + transcript
}

func containsVoiceAttachment(attachments []channel.Attachment) bool {
	for _, att := range attachments {
		if att.Type == channel.AttachmentAudio || att.Type == channel.AttachmentVoice {
			return true
		}
	}
	return false
}

func formatVoiceTranscriptionUnavailableNotice(attachments []channel.Attachment) string {
	paths := make([]string, 0, len(attachments))
	for _, att := range attachments {
		if att.Type != channel.AttachmentAudio && att.Type != channel.AttachmentVoice {
			continue
		}
		if ref := strings.TrimSpace(att.URL); ref != "" {
			paths = append(paths, ref)
		}
	}
	if len(paths) == 0 {
		return "[User sent a voice message, but transcription is unavailable.]"
	}
	return "[User sent a voice message, but transcription is unavailable. Use transcribe_audio with one of these paths if needed: " + strings.Join(paths, ", ") + "]"
}

func openInboundAttachmentURL(ctx context.Context, rawURL string) (inboundAttachmentPayload, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return inboundAttachmentPayload{}, fmt.Errorf("build request: %w", err)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // G704: URL is an attachment URL provided by the inbound channel adapter
	if err != nil {
		return inboundAttachmentPayload{}, fmt.Errorf("download attachment: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_ = resp.Body.Close()
		return inboundAttachmentPayload{}, fmt.Errorf("download attachment status: %d", resp.StatusCode)
	}
	maxBytes := media.MaxAssetBytes
	if resp.ContentLength > maxBytes {
		_ = resp.Body.Close()
		return inboundAttachmentPayload{}, fmt.Errorf("%w: max %d bytes", media.ErrAssetTooLarge, maxBytes)
	}
	mime := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	return inboundAttachmentPayload{
		reader: resp.Body,
		mime:   mime,
		size:   resp.ContentLength,
	}, nil
}

func (p *ChannelInboundProcessor) resolveAttachmentResolver(channelType channel.ChannelType) channel.AttachmentResolver {
	if p == nil || p.registry == nil {
		return nil
	}
	resolver, ok := p.registry.GetAttachmentResolver(channelType)
	if !ok {
		return nil
	}
	return resolver
}

// ingestOutboundAttachments persists LLM-generated attachment data URLs via the
// media service, replacing ephemeral data URLs with stable asset references.
// For container-internal paths (non-HTTP), it attempts to resolve the existing
// asset by matching the storage key extracted from the path.
func (p *ChannelInboundProcessor) ingestOutboundAttachments(ctx context.Context, botID string, channelType channel.ChannelType, attachments []channel.Attachment) []channel.Attachment {
	if len(attachments) == 0 || p.mediaService == nil || strings.TrimSpace(botID) == "" {
		return attachments
	}
	prepared, err := channel.PrepareStreamEvent(ctx, p.mediaService, channel.ChannelConfig{
		BotID:       botID,
		ChannelType: channelType,
	}, channel.StreamEvent{
		Type:        channel.StreamEventAttachment,
		Attachments: attachments,
	})
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("prepare outbound attachments failed", slog.Any("error", err))
		}
		return attachments
	}
	return prepared.LogicalEvent().Attachments
}

func isDataURL(raw string) bool {
	return channel.IsDataURL(raw)
}

func isHTTPURL(raw string) bool {
	return channel.IsHTTPURL(raw)
}

// extractStorageKey derives the media storage key from a container-internal
// access path. The expected path format is /data/.memoh/media/<storage_key>.
func extractStorageKey(accessPath string, _ string) string {
	return attachment.ExtractStorageKey(accessPath)
}

// isLocalChannelType returns true for channels that already publish to RouteHub
// natively (e.g. web). Wrapping these with a tee would cause duplicate events.
func isLocalChannelType(ct channel.ChannelType) bool {
	s := strings.ToLower(strings.TrimSpace(string(ct)))
	return s == "web" || s == "cli"
}

// replayPipelineSession loads persisted events from the DB and replays them
// into the pipeline. Called lazily on first access per session after cold start.
func (p *ChannelInboundProcessor) replayPipelineSession(ctx context.Context, sessionID string) {
	if p.eventStore == nil || p.pipeline == nil {
		return
	}
	events, err := p.eventStore.LoadEvents(ctx, sessionID)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("pipeline replay failed", slog.String("session_id", sessionID), slog.Any("error", err))
		}
		return
	}
	if len(events) > 0 {
		p.pipeline.ReplaySession(sessionID, events)
		if p.logger != nil {
			p.logger.Info("pipeline session replayed", slog.String("session_id", sessionID), slog.Int("events", len(events)))
		}
	}
}

// broadcastInboundMessage notifies the observer about the user's inbound
// message so WebUI subscribers see the full conversation, not just the bot reply.
func (p *ChannelInboundProcessor) broadcastInboundMessage(
	ctx context.Context,
	botID string,
	msg channel.InboundMessage,
	text string,
	identity InboundIdentity,
	resolvedAttachments []channel.Attachment,
) {
	if p.observer == nil || strings.TrimSpace(botID) == "" {
		return
	}
	inboundMsg := channel.Message{
		Text:        text,
		Attachments: resolvedAttachments,
		Reply:       msg.Message.Reply,
		Forward:     msg.Message.Forward,
		Metadata: map[string]any{
			"external_message_id": strings.TrimSpace(msg.Message.ID),
			"sender_display_name": strings.TrimSpace(identity.DisplayName),
		},
	}
	p.observer.OnStreamEvent(ctx, botID, msg.Channel, channel.StreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.StreamFinalizePayload{
			Message: inboundMsg,
		},
		Metadata: map[string]any{
			"source_channel": string(msg.Channel),
			"role":           "user",
			"sender_user_id": strings.TrimSpace(identity.UserID),
		},
	})
}

// channelAttachmentsToAssetRefs converts channel Attachments to message AssetRefs
// for denormalized persistence. Only attachments with a non-empty ContentHash are
// included. StorageKey is extracted from Metadata when present.
func channelAttachmentsToAssetRefs(attachments []channel.Attachment, role string) []messagepkg.AssetRef {
	if len(attachments) == 0 {
		return nil
	}
	refs := make([]messagepkg.AssetRef, 0, len(attachments))
	for idx, att := range attachments {
		contentHash := strings.TrimSpace(att.ContentHash)
		if contentHash == "" {
			continue
		}
		ref := messagepkg.AssetRef{
			ContentHash: contentHash,
			Role:        role,
			Ordinal:     idx,
			Mime:        strings.TrimSpace(att.Mime),
			SizeBytes:   att.Size,
			Name:        strings.TrimSpace(att.Name),
			Metadata:    att.Metadata,
		}
		ref.StorageKey = attachment.MetadataString(att.Metadata, attachment.MetadataKeyStorageKey)
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

func mapChannelToChatAttachments(attachments []channel.Attachment) []turn.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	result := make([]turn.Attachment, 0, len(attachments))
	for _, att := range attachments {
		if att.Type == channel.AttachmentAudio || att.Type == channel.AttachmentVoice {
			continue
		}
		bundle := channel.BundleFromAttachment(att)
		ca := turn.AttachmentFromBundle(bundle)
		switch {
		case strings.TrimSpace(bundle.ContentHash) != "" && bundle.Path != "":
			ca.Path = bundle.Path
			ca.URL = ""
		case bundle.URL != "":
			ca.URL = bundle.URL
		case bundle.Path != "":
			ca.URL = bundle.Path
		}
		result = append(result, ca)
	}
	return result
}

// parseAttachmentDelta converts raw JSON attachment data to channel Attachments.
func parseAttachmentDelta(raw json.RawMessage) []channel.Attachment {
	if len(raw) == 0 {
		return nil
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	attachments := make([]channel.Attachment, 0, len(items))
	for _, item := range items {
		bundle := attachment.BundleFromMap(item)
		attachments = append(attachments, channel.AttachmentFromBundle(bundle))
	}
	return attachments
}

// synthesizeAndPushVoice handles speech_delta events by synthesizing audio
// and pushing the resulting voice attachment into the outbound stream.
// assetTracker keeps ordinal bookkeeping for outbound asset refs and
// forwards each batch into the running turn so the resolver can attach
// them at persist time.
type assetTracker struct {
	mu   sync.Mutex
	refs []turn.OutboundAssetRef
	run  turn.RunHandle
}

func (t *assetTracker) add(attachments []channel.Attachment) {
	t.mu.Lock()
	refs := buildAssetRefs(attachments, len(t.refs))
	t.refs = append(t.refs, refs...)
	t.mu.Unlock()
	if len(refs) > 0 && t.run != nil {
		t.run.AddOutboundAssets(refs)
	}
}

func (p *ChannelInboundProcessor) synthesizeAndPushVoice(
	ctx context.Context,
	botID string,
	channelType channel.ChannelType,
	speeches []channel.SpeechRequest,
	stream channel.OutboundStream,
	assets *assetTracker,
) {
	if p.speechService == nil || p.speechModelResolver == nil {
		if p.logger != nil {
			p.logger.Warn("speech_delta received but TTS service not configured")
		}
		return
	}
	modelID, err := p.speechModelResolver.ResolveSpeechModelID(ctx, botID)
	if err != nil || strings.TrimSpace(modelID) == "" {
		if p.logger != nil {
			p.logger.Warn("speech_delta: bot has no TTS model configured", slog.String("bot_id", botID))
		}
		return
	}
	for _, speech := range speeches {
		text := strings.TrimSpace(speech.Text)
		if text == "" {
			continue
		}
		audioData, contentType, synthErr := p.speechService.Synthesize(ctx, modelID, text, nil)
		if synthErr != nil {
			if p.logger != nil {
				p.logger.Warn("speech synthesis failed", slog.String("bot_id", botID), slog.Any("error", synthErr))
			}
			continue
		}
		dataURL := encodeDataURL(contentType, audioData)
		voiceEvent := channel.StreamEvent{
			Type: channel.StreamEventAttachment,
			Attachments: []channel.Attachment{
				{
					Type: channel.AttachmentVoice,
					URL:  dataURL,
					Mime: contentType,
					Size: int64(len(audioData)),
				},
			},
		}
		ingested := p.ingestOutboundAttachments(ctx, botID, channelType, voiceEvent.Attachments)
		voiceEvent.Attachments = ingested
		assets.add(ingested)
		if pushErr := stream.Push(ctx, voiceEvent); pushErr != nil {
			if p.logger != nil {
				p.logger.Warn("push voice attachment failed", slog.String("bot_id", botID), slog.Any("error", pushErr))
			}
			return
		}
	}
}

// parseSpeechDelta converts raw JSON speech data to SpeechRequest values.
func parseSpeechDelta(raw json.RawMessage) []channel.SpeechRequest {
	if len(raw) == 0 {
		return nil
	}
	var items []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	speeches := make([]channel.SpeechRequest, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		speeches = append(speeches, channel.SpeechRequest{Text: text})
	}
	return speeches
}

func buildAssetRefs(attachments []channel.Attachment, startOrdinal int) []turn.OutboundAssetRef {
	var refs []turn.OutboundAssetRef
	for _, att := range attachments {
		contentHash := strings.TrimSpace(att.ContentHash)
		if contentHash == "" {
			continue
		}
		ref := turn.OutboundAssetRef{
			ContentHash: contentHash,
			Role:        "attachment",
			Ordinal:     startOrdinal + len(refs),
			Mime:        strings.TrimSpace(att.Mime),
			SizeBytes:   att.Size,
			Name:        strings.TrimSpace(att.Name),
			Metadata:    att.Metadata,
		}
		ref.StorageKey = attachment.MetadataString(att.Metadata, attachment.MetadataKeyStorageKey)
		refs = append(refs, ref)
	}
	return refs
}

func encodeDataURL(mime string, data []byte) string {
	encoded := base64Encode(data)
	return "data:" + mime + ";base64," + encoded
}

func base64Encode(data []byte) string {
	return base64Std.EncodeToString(data)
}

// parseReactionDelta converts raw JSON reaction data to channel ReactRequests.
func parseReactionDelta(raw json.RawMessage) []channel.ReactRequest {
	if len(raw) == 0 {
		return nil
	}
	var items []struct {
		Emoji string `json:"emoji"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	reactions := make([]channel.ReactRequest, 0, len(items))
	for _, item := range items {
		emoji := strings.TrimSpace(item.Emoji)
		if emoji == "" {
			continue
		}
		reactions = append(reactions, channel.ReactRequest{
			Emoji: emoji,
		})
	}
	return reactions
}

// dispatchReactions sends emoji reactions to the channel for the source message.
func (p *ChannelInboundProcessor) dispatchReactions(
	ctx context.Context,
	botID string,
	channelType channel.ChannelType,
	target string,
	sourceMessageID string,
	reactions []channel.ReactRequest,
) {
	if p.reactor == nil {
		return
	}
	target = strings.TrimSpace(target)
	sourceMessageID = strings.TrimSpace(sourceMessageID)
	if target == "" || sourceMessageID == "" {
		if p.logger != nil {
			p.logger.Warn("cannot dispatch reactions: missing target or source message ID",
				slog.String("bot_id", botID),
				slog.String("channel", channelType.String()),
			)
		}
		return
	}
	for _, reaction := range reactions {
		req := channel.ReactRequest{
			Target:    target,
			MessageID: sourceMessageID,
			Emoji:     reaction.Emoji,
		}
		if err := p.reactor.React(ctx, strings.TrimSpace(botID), channelType, req); err != nil {
			if p.logger != nil {
				p.logger.Warn("inline reaction failed",
					slog.String("bot_id", botID),
					slog.String("channel", channelType.String()),
					slog.String("emoji", reaction.Emoji),
					slog.String("message_id", sourceMessageID),
					slog.Any("error", err),
				)
			}
		}
	}
}

// buildRouteMetadata extracts user/conversation information for route metadata persistence.
func buildRouteMetadata(msg channel.InboundMessage, identity InboundIdentity) map[string]any {
	m := make(map[string]any)

	if v := strings.TrimSpace(identity.DisplayName); v != "" {
		m["sender_display_name"] = v
	}
	if v := strings.TrimSpace(identity.AvatarURL); v != "" {
		m["sender_avatar_url"] = v
	}
	if v := strings.TrimSpace(msg.Sender.SubjectID); v != "" {
		m["sender_id"] = v
	}
	if v := strings.TrimSpace(msg.Conversation.Name); v != "" {
		m["conversation_name"] = v
	}

	for k, v := range msg.Sender.Attributes {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if k == "username" {
			m["sender_username"] = v
		}
	}
	if mentions, ok := msg.Metadata["mentions"]; ok && mentions != nil {
		m["mentions"] = mentions
	}
	if targets, ok := msg.Metadata["mentioned_targets"]; ok && targets != nil {
		m["mentioned_targets"] = targets
	}

	return m
}

// enrichConversationAvatar resolves group-level metadata (avatar, handle) via
// the directory adapter and stores them in the route metadata map.
func (p *ChannelInboundProcessor) enrichConversationAvatar(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, meta map[string]any) {
	convType := strings.TrimSpace(msg.Conversation.Type)
	if convType != "group" && convType != "supergroup" && convType != "channel" {
		return
	}
	if p.registry == nil {
		return
	}
	directoryAdapter, ok := p.registry.DirectoryAdapter(msg.Channel)
	if !ok || directoryAdapter == nil {
		return
	}
	convID := strings.TrimSpace(msg.Conversation.ID)
	if convID == "" {
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	entry, err := directoryAdapter.ResolveEntry(lookupCtx, cfg, convID, channel.DirectoryEntryGroup)
	if err != nil {
		if p.logger != nil {
			p.logger.Debug("resolve conversation directory entry failed",
				slog.String("channel", msg.Channel.String()),
				slog.String("conversation_id", convID),
				slog.Any("error", err),
			)
		}
		return
	}
	if v := strings.TrimSpace(entry.Name); v != "" {
		meta["conversation_name"] = v
	}
	if v := strings.TrimSpace(entry.AvatarURL); v != "" {
		meta["conversation_avatar_url"] = v
	}
	if v := strings.TrimSpace(entry.Handle); v != "" {
		meta["conversation_handle"] = v
	}
}

// handleStopCommand resolves the route for the current conversation and
// cancels any active agent stream, effectively aborting the generation.
func (p *ChannelInboundProcessor) handleStopCommand(
	ctx context.Context,
	cfg channel.ChannelConfig,
	msg channel.InboundMessage,
	sender channel.StreamReplySender,
	identity InboundIdentity,
) error {
	target := strings.TrimSpace(msg.ReplyTarget)
	if target == "" {
		return errors.New("reply target missing for /stop command")
	}
	loc := p.localizer(ctx, identity.BotID)
	caps := p.channelCaps(msg.Channel)

	if p.routeResolver == nil {
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(friendlyOps(loc, "ops.verb.stopReply"), caps),
		})
	}

	threadID := extractThreadID(msg)
	routeMetadata := buildRouteMetadata(msg, identity)
	p.enrichConversationAvatar(ctx, cfg, msg, routeMetadata)
	resolved, err := p.routeResolver.ResolveConversation(ctx, route.ResolveInput{
		BotID:                  identity.BotID,
		Platform:               msg.Channel.String(),
		ExternalConversationID: msg.Conversation.ID,
		ExternalThreadID:       threadID,
		ConversationType:       msg.Conversation.Type,
		ChannelConfigID:        identity.ChannelConfigID,
		ReplyTarget:            target,
		Metadata:               routeMetadata,
	})
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("resolve route for /stop command failed", slog.Any("error", err))
		}
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(friendlyOps(loc, "ops.verb.stopReply"), caps),
		})
	}

	streamKey := strings.TrimSpace(identity.BotID) + ":" + strings.TrimSpace(resolved.RouteID)
	cancelVal, loaded := p.activeStreams.LoadAndDelete(streamKey)
	if !loaded {
		// No active stream — silently ignore.
		return nil
	}

	cancelFn, ok := cancelVal.(context.CancelFunc)
	if !ok {
		return nil
	}

	cancelFn()
	if p.logger != nil {
		p.logger.Info("agent stream aborted via /stop command",
			slog.String("bot_id", strings.TrimSpace(identity.BotID)),
			slog.String("route_id", strings.TrimSpace(resolved.RouteID)),
			slog.String("channel", msg.Channel.String()),
		)
	}
	return nil
}

// handleStartCommand replies with a lightweight welcome. Unlike /new it does not
// reset the active session or show configuration — it only opens the door.
func (p *ChannelInboundProcessor) handleStartCommand(
	ctx context.Context,
	msg channel.InboundMessage,
	sender channel.StreamReplySender,
	identity InboundIdentity,
) error {
	target := strings.TrimSpace(msg.ReplyTarget)
	if target == "" {
		return errors.New("reply target missing for /start command")
	}
	loc := p.localizer(ctx, identity.BotID)
	caps := p.channelCaps(msg.Channel)
	botUsername := telegramGroupBotUsername(msg)

	out := applyMessageFormat(channel.Message{Text: formatStartWelcomeMessage(loc, botUsername)}, caps)
	if mid := strings.TrimSpace(msg.Message.ID); mid != "" {
		out.Reply = &channel.ReplyRef{MessageID: mid}
	}
	return sender.Send(ctx, channel.OutboundMessage{Target: target, Message: out})
}

func (p *ChannelInboundProcessor) handleToolApprovalCommand(ctx context.Context, msg channel.InboundMessage, sender channel.StreamReplySender, identity InboundIdentity, routeID, sessionID string, invocation command.Invocation) error {
	loc := p.localizer(ctx, identity.BotID)
	caps := p.channelCaps(msg.Channel)
	if p.turnSvc == nil {
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  strings.TrimSpace(msg.ReplyTarget),
			Message: applyMessageFormat(channel.Message{Text: loc.T("cmd.toolApproval.unavailable")}, caps),
		})
	}
	approvalRunner := ToolApprovalRunner(p.turnSvc)
	parsed := invocation.Parsed
	explicitID := ""
	reason := ""
	replyExternalID := ""
	if msg.Message.Reply != nil {
		replyExternalID = strings.TrimSpace(msg.Message.Reply.MessageID)
	}
	actionText := strings.TrimSpace(parsed.Action)
	if parsed.Resource == "reject" && replyExternalID != "" && actionText != "" && !looksLikeApprovalID(actionText) {
		reason = strings.TrimSpace(strings.Join(append([]string{actionText}, parsed.Args...), " "))
	} else {
		explicitID = actionText
		reason = strings.TrimSpace(strings.Join(parsed.Args, " "))
	}
	return p.streamToolApprovalCommand(ctx, msg, sender, identity, routeID, approvalRunner, turn.ToolApprovalResponse{
		BotID:                  strings.TrimSpace(identity.BotID),
		ThreadID:               strings.TrimSpace(sessionID),
		ActorChannelIdentityID: strings.TrimSpace(identity.ChannelIdentityID),
		ActorUserID:            strings.TrimSpace(identity.UserID),
		ExplicitID:             explicitID,
		ReplyExternalMessageID: replyExternalID,
		Decision:               parsed.Resource,
		Reason:                 reason,
		ChatToken:              p.issueChatToken(identity, routeID, msg),
	})
}

func (p *ChannelInboundProcessor) handleUserInputResponseCommand(ctx context.Context, msg channel.InboundMessage, sender channel.StreamReplySender, identity InboundIdentity, routeID, sessionID string, invocation command.Invocation) error {
	loc := p.localizer(ctx, identity.BotID)
	caps := p.channelCaps(msg.Channel)
	if p.turnSvc == nil {
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  strings.TrimSpace(msg.ReplyTarget),
			Message: applyMessageFormat(channel.Message{Text: loc.T("cmd.userInput.unavailable")}, caps),
		})
	}
	userInputRunner := UserInputRunner(p.turnSvc)
	replyExternalID := ""
	if msg.Message.Reply != nil {
		replyExternalID = strings.TrimSpace(msg.Message.Reply.MessageID)
	}
	explicitID, answerText, err := parseUserInputResponseInvocation(invocation, replyExternalID != "")
	if err != nil {
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  strings.TrimSpace(msg.ReplyTarget),
			Message: applyMessageFormat(channel.Message{Text: loc.T("cmd.userInput.parseFailed")}, caps),
		})
	}
	if callbackID, _ := msg.Metadata["user_input_id"].(string); strings.TrimSpace(callbackID) != "" {
		explicitID = strings.TrimSpace(callbackID)
	}
	// Platform adapters (Telegram multi-step wizard) may attach fully structured
	// answers after collecting every question. Prefer those over free-text
	// parsing so multi-question replies do not depend on resolver text limits.
	answers := userInputAnswersFromMetadata(msg.Metadata)
	return p.streamUserInputResponseCommand(ctx, msg, sender, identity, routeID, userInputRunner, turn.UserInputResponse{
		BotID:                  strings.TrimSpace(identity.BotID),
		ThreadID:               strings.TrimSpace(sessionID),
		ActorChannelIdentityID: strings.TrimSpace(identity.ChannelIdentityID),
		ActorUserID:            strings.TrimSpace(identity.UserID),
		ExplicitID:             explicitID,
		ReplyExternalMessageID: replyExternalID,
		Answers:                answers,
		TextAnswer:             answerText,
		ChatToken:              p.issueChatToken(identity, routeID, msg),
	})
}

// userInputAnswersFromMetadata extracts structured ask_user answers attached by
// platform adapters (e.g. Telegram's multi-step wizard). Returns nil when the
// metadata has no usable answers so the resolver can fall back to TextAnswer.
func userInputAnswersFromMetadata(meta map[string]any) []turn.QuestionAnswer {
	if len(meta) == 0 {
		return nil
	}
	raw, ok := meta["user_input_answers"]
	if !ok || raw == nil {
		return nil
	}
	// Accept both []map[string]any (from adapters) and JSON-shaped []any.
	var entries []any
	switch v := raw.(type) {
	case []any:
		entries = v
	case []map[string]any:
		for _, item := range v {
			entries = append(entries, item)
		}
	default:
		// Best-effort JSON round-trip for unexpected concrete types.
		data, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil
		}
	}
	out := make([]turn.QuestionAnswer, 0, len(entries))
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			// After JSON round-trip keys are still map[string]any.
			if typed, ok := entry.(map[string]interface{}); ok {
				m = typed
			} else {
				continue
			}
		}
		qID := strings.TrimSpace(fmt.Sprint(m["question_id"]))
		if qID == "" || qID == "<nil>" {
			continue
		}
		answer := turn.QuestionAnswer{QuestionID: qID}
		if skipped, ok := m["skipped"].(bool); ok {
			answer.Skipped = skipped
		}
		if text, ok := m["text"].(string); ok {
			answer.Text = strings.TrimSpace(text)
		}
		if custom, ok := m["custom_text"].(string); ok {
			answer.CustomText = strings.TrimSpace(custom)
		}
		switch ids := m["option_ids"].(type) {
		case []string:
			answer.OptionIDs = append([]string{}, ids...)
		case []any:
			for _, id := range ids {
				if s, ok := id.(string); ok && strings.TrimSpace(s) != "" {
					answer.OptionIDs = append(answer.OptionIDs, strings.TrimSpace(s))
				}
			}
		}
		out = append(out, answer)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func turnQuestionAnswers(answers []userinput.QuestionAnswer) []turn.QuestionAnswer {
	if answers == nil {
		return nil
	}
	out := make([]turn.QuestionAnswer, len(answers))
	for i := range answers {
		out[i] = turn.QuestionAnswer{
			QuestionID: answers[i].QuestionID,
			OptionIDs:  answers[i].OptionIDs,
			CustomText: answers[i].CustomText,
			Text:       answers[i].Text,
			Skipped:    answers[i].Skipped,
		}
	}
	return out
}

func parseUserInputResponseInvocation(invocation command.Invocation, hasReplyTarget bool) (explicitID, answerText string, err error) {
	if invocation.Parsed.Resource != "respond" {
		return "", "", errors.New("not a respond command")
	}
	if hasReplyTarget {
		return "", strings.TrimSpace(invocation.Rest), nil
	}
	explicitID, answerText = splitFirstCommandField(invocation.Rest)
	return strings.TrimSpace(explicitID), strings.TrimSpace(answerText), nil
}

func splitFirstCommandField(text string) (head, tail string) {
	text = strings.TrimSpace(text)
	for idx, r := range text {
		if unicode.IsSpace(r) {
			return text[:idx], strings.TrimSpace(text[idx:])
		}
	}
	return text, ""
}

func (p *ChannelInboundProcessor) streamToolApprovalCommand(ctx context.Context, msg channel.InboundMessage, sender channel.StreamReplySender, identity InboundIdentity, routeID string, approvalRunner ToolApprovalRunner, input turn.ToolApprovalResponse) error {
	return p.streamContinuationCommand(ctx, msg, sender, identity, routeID, func(runCtx context.Context, eventCh chan<- json.RawMessage) error {
		return approvalRunner.RespondToolApproval(runCtx, input, eventCh)
	})
}

func (p *ChannelInboundProcessor) streamUserInputResponseCommand(ctx context.Context, msg channel.InboundMessage, sender channel.StreamReplySender, identity InboundIdentity, routeID string, userInputRunner UserInputRunner, input turn.UserInputResponse) error {
	return p.streamContinuationCommand(ctx, msg, sender, identity, routeID, func(runCtx context.Context, eventCh chan<- json.RawMessage) error {
		return userInputRunner.RespondUserInput(runCtx, input, eventCh)
	})
}

type streamContinuationFunc func(context.Context, chan<- json.RawMessage) error

func (p *ChannelInboundProcessor) streamContinuationCommand(ctx context.Context, msg channel.InboundMessage, sender channel.StreamReplySender, identity InboundIdentity, routeID string, run streamContinuationFunc) error {
	target := strings.TrimSpace(msg.ReplyTarget)
	if target == "" {
		return errors.New("reply target missing")
	}
	routeID = strings.TrimSpace(routeID)
	if routeID != "" && p.dispatcher != nil && !isLocalChannelType(msg.Channel) {
		p.dispatcher.MarkActive(routeID)
		defer p.drainQueue(context.WithoutCancel(ctx), routeID)
	}
	sourceMessageID := strings.TrimSpace(msg.Message.ID)
	replyRef := &channel.ReplyRef{Target: target}
	if sourceMessageID != "" {
		replyRef.MessageID = sourceMessageID
	}
	stream, err := sender.OpenStream(ctx, target, channel.StreamOptions{
		Reply:           replyRef,
		SourceMessageID: sourceMessageID,
		Metadata: map[string]any{
			"conversation_type": msg.Conversation.Type,
		},
	})
	if err != nil {
		return err
	}
	streamClosed := false
	closeStream := func() error {
		if streamClosed {
			return nil
		}
		streamClosed = true
		return stream.Close(context.WithoutCancel(ctx))
	}
	defer func() { _ = closeStream() }()

	if !isLocalChannelType(msg.Channel) && !p.shouldShowToolCallsInIM(ctx, identity.BotID) {
		stream = channel.NewToolCallDroppingStream(stream)
	}
	if err := stream.Push(ctx, channel.StreamEvent{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted}); err != nil {
		return err
	}

	eventCh := make(chan json.RawMessage, 64)
	errCh := make(chan error, 1)
	go func() {
		defer close(eventCh)
		errCh <- run(ctx, eventCh)
		close(errCh)
	}()

	var finalMessages []turn.ModelMessage
	for eventCh != nil || errCh != nil {
		select {
		case chunk, ok := <-eventCh:
			if !ok {
				eventCh = nil
				continue
			}
			events, messages, parseErr := mapStreamChunkToChannelEvents(chunk)
			if parseErr != nil {
				if p.logger != nil {
					p.logger.Warn("approval stream chunk parse failed", slog.Any("error", parseErr))
				}
				continue
			}
			if len(messages) > 0 {
				finalMessages = messages
			}
			for _, event := range events {
				if isUserInputEvent(&event) {
					event.ToolCall.Locale = p.localizer(ctx, identity.BotID).Locale()
				}
				// Approval continuations should not flash transient "running"
				// tool messages in IM. If tool visibility is enabled, the
				// completed tool state may still be shown on tool_call_end.
				if event.Type == channel.StreamEventToolCallStart &&
					(event.ToolCall == nil || (strings.TrimSpace(event.ToolCall.ApprovalID) == "" && !hasUserInputAction(event.ToolCall.Actions))) {
					continue
				}
				if event.Type == channel.StreamEventReaction && len(event.Reactions) > 0 {
					p.dispatchReactions(ctx, identity.BotID, msg.Channel, target, sourceMessageID, event.Reactions)
					continue
				}
				if err := stream.Push(ctx, event); err != nil {
					return err
				}
			}
		case runErr, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if runErr != nil {
				_ = stream.Push(ctx, channel.StreamEvent{Type: channel.StreamEventError, Error: runErr.Error()})
				return runErr
			}
		}
	}

	sentTexts, suppressReplies := collectMessageToolContext(p.registry, finalMessages, msg.Channel, target)
	if !suppressReplies {
		outputs := turn.ExtractAssistantOutputs(finalMessages)
		for _, output := range outputs {
			outMessage := buildChannelMessage(output, channel.ChannelCapabilities{Text: true, Markdown: true, Reply: true})
			if outMessage.IsEmpty() {
				continue
			}
			plainText := strings.TrimSpace(outMessage.PlainText())
			if isSilentReplyText(plainText) || isMessagingToolDuplicate(plainText, sentTexts) {
				continue
			}
			if outMessage.Reply == nil && sourceMessageID != "" {
				outMessage.Reply = &channel.ReplyRef{Target: target, MessageID: sourceMessageID}
			}
			if err := stream.Push(ctx, channel.StreamEvent{
				Type:  channel.StreamEventFinal,
				Final: &channel.StreamFinalizePayload{Message: outMessage},
			}); err != nil {
				return err
			}
		}
	}
	if err := stream.Push(ctx, channel.StreamEvent{Type: channel.StreamEventStatus, Status: channel.StreamStatusCompleted}); err != nil {
		return err
	}
	return closeStream()
}

func hasUserInputAction(actions []channel.Action) bool {
	for _, action := range actions {
		if strings.TrimSpace(action.Type) == "user_input" {
			return true
		}
	}
	return false
}

func (p *ChannelInboundProcessor) issueChatToken(identity InboundIdentity, routeID string, msg channel.InboundMessage) string {
	if p.jwtSecret == "" || strings.TrimSpace(msg.ReplyTarget) == "" {
		return ""
	}
	signed, _, err := auth.GenerateChatToken(auth.ChatToken{
		BotID:             identity.BotID,
		ChatID:            identity.BotID,
		RouteID:           strings.TrimSpace(routeID),
		UserID:            identity.UserID,
		ChannelIdentityID: identity.ChannelIdentityID,
	}, p.jwtSecret, p.tokenTTL)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("issue approval chat token failed", slog.Any("error", err))
		}
		return ""
	}
	return signed
}

func (p *ChannelInboundProcessor) issueChannelBearerToken(ctx context.Context, identity InboundIdentity, fallbackChatToken string) string {
	if p.jwtSecret == "" {
		if strings.TrimSpace(fallbackChatToken) != "" {
			return "Bearer " + strings.TrimSpace(fallbackChatToken)
		}
		return ""
	}
	tokenUserID := strings.TrimSpace(identity.UserID)
	if p.policy != nil {
		if ownerID, err := p.policy.BotOwnerUserID(ctx, identity.BotID); err == nil && strings.TrimSpace(ownerID) != "" {
			tokenUserID = strings.TrimSpace(ownerID)
		} else if p.logger != nil {
			p.logger.Warn("resolve bot owner for token failed, falling back to caller identity",
				slog.String("bot_id", identity.BotID), slog.Any("error", err))
		}
	}
	if tokenUserID != "" {
		signed, _, err := auth.GenerateToken(tokenUserID, p.jwtSecret, p.tokenTTL)
		if err != nil {
			if p.logger != nil {
				p.logger.Warn("issue channel token failed", slog.Any("error", err))
			}
		} else {
			return "Bearer " + signed
		}
	}
	if strings.TrimSpace(fallbackChatToken) != "" {
		return "Bearer " + strings.TrimSpace(fallbackChatToken)
	}
	return ""
}

func (p *ChannelInboundProcessor) issueSessionBearerToken(ctx context.Context, identity InboundIdentity, sess SessionResult, runtimeOwnerAccountID, fallbackChatToken string) string {
	if sessionUsesACPRuntime(sess) {
		return p.issueRuntimeBearerToken(ctx, identity, runtimeOwnerAccountID, fallbackChatToken)
	}
	return p.issueChannelBearerToken(ctx, identity, fallbackChatToken)
}

func (p *ChannelInboundProcessor) issueRuntimeBearerToken(_ context.Context, identity InboundIdentity, runtimeOwnerAccountID, fallbackChatToken string) string {
	if p.jwtSecret == "" {
		if strings.TrimSpace(fallbackChatToken) != "" {
			return "Bearer " + strings.TrimSpace(fallbackChatToken)
		}
		return ""
	}
	tokenUserID := strings.TrimSpace(runtimeOwnerAccountID)
	if tokenUserID == "" {
		tokenUserID = strings.TrimSpace(identity.UserID)
	}
	if tokenUserID != "" {
		signed, _, err := auth.GenerateToken(tokenUserID, p.jwtSecret, p.tokenTTL)
		if err != nil {
			if p.logger != nil {
				p.logger.Warn("issue ACP runtime token failed", slog.Any("error", err))
			}
		} else {
			return "Bearer " + signed
		}
	}
	if strings.TrimSpace(fallbackChatToken) != "" {
		return "Bearer " + strings.TrimSpace(fallbackChatToken)
	}
	return ""
}

func acpMCPToolsURLFromEnv(botID string) string {
	if raw := strings.TrimSpace(os.Getenv("MEMOH_ACP_MCP_HTTP_URL")); raw != "" {
		if strings.Contains(raw, "{bot_id}") {
			return strings.ReplaceAll(raw, "{bot_id}", url.PathEscape(strings.TrimSpace(botID)))
		}
		return raw
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("MEMOH_ACP_MCP_HTTP_BASE_URL")), "/")
	if base == "" {
		return ""
	}
	return base + "/bots/" + url.PathEscape(strings.TrimSpace(botID)) + "/tools"
}

func looksLikeApprovalID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return strings.Contains(value, "-")
		}
	}
	return true
}

// resolveNewSessionSpecParsed determines the session mode/runtime for /new.
// /new chat → chat+model; a named Agent selects either its direct runtime or
// the generic ACP runtime while the explicit/default channel mode stays intact.
func resolveNewSessionSpecParsed(parsed command.ParsedCommand, msg channel.InboundMessage, profiles turn.ACPProfileResolver) (NewSessionSpec, error) {
	operands := newSessionOperands(parsed)
	explicit := ""
	var args []string
	if len(operands) > 0 {
		explicit = strings.ToLower(strings.TrimSpace(operands[0]))
		args = operands[1:]
	}
	mode := ""
	agentID := ""
	switch explicit {
	case "chat":
		mode = sessionpkg.TypeChat
		agentID = firstNewSessionAgentArg(args)
		if agentID != "" && isGroupConversation(msg) {
			return NewSessionSpec{}, groupChatACPUnsupportedFeedback()
		}
	case "discuss":
		if isLocalChannelType(msg.Channel) {
			return NewSessionSpec{}, errors.New("discuss mode is not supported via WebUI — use a channel adapter (Telegram, Discord, etc.)")
		}
		mode = sessionpkg.TypeDiscuss
		agentID = firstNewSessionAgentArg(args)
	case "":
		// Default: local → chat, group → discuss, DM → chat.
		switch {
		case isLocalChannelType(msg.Channel), channel.IsPrivateConversationType(msg.Conversation.Type):
			mode = sessionpkg.TypeChat
		default:
			mode = sessionpkg.TypeDiscuss
		}
	default:
		if direct := normalizeACPAgentID(explicit); sessionpkg.IsDirectRuntimeType(direct) {
			// A bare direct external agent name ("/new codex") is a valid
			// operand even though it has no ACP profile.
			agentID = direct
			switch {
			case isLocalChannelType(msg.Channel), channel.IsPrivateConversationType(msg.Conversation.Type):
				mode = sessionpkg.TypeChat
			default:
				mode = sessionpkg.TypeDiscuss
			}
			break
		}
		profile := resolveACPProfile(profiles, explicit)
		if !profile.Known {
			return NewSessionSpec{}, fmt.Errorf("unknown session type %q — use /new, /new chat, or /new discuss", explicit)
		}
		agentID = profile.ID
		switch {
		case isLocalChannelType(msg.Channel), channel.IsPrivateConversationType(msg.Conversation.Type):
			mode = sessionpkg.TypeChat
		default:
			mode = sessionpkg.TypeDiscuss
		}
	}
	if mode == "" {
		mode = sessionpkg.TypeChat
	}
	spec := NewSessionSpec{
		Mode:    mode,
		Runtime: sessionpkg.RuntimeModel,
		Type:    mode,
	}
	agentID = normalizeACPAgentID(agentID)
	if agentID == "" {
		return spec, nil
	}
	// Direct external agents (codex, claude-code) are addressed by their
	// runtime name and never live in the ACP profile registry.
	if sessionpkg.IsDirectRuntimeType(agentID) {
		spec.Runtime = agentID
		if mode != sessionpkg.TypeChat {
			spec.Type = sessionpkg.TypeDiscuss
		}
		return spec, nil
	}
	profile := resolveACPProfile(profiles, agentID)
	if !profile.Known {
		return NewSessionSpec{}, agentfeedback.New(
			agentfeedback.CodeAgentNotFound,
			"unknown_agent",
			http.StatusBadRequest,
			"chat.externalAgent.agentNotFound",
			fmt.Sprintf("Unknown ACP agent %q.", agentID),
			map[string]string{"agent_id": agentID},
		)
	}
	agentID = profile.ID
	spec.Runtime = sessionpkg.RuntimeACPAgent
	spec.Metadata = sessionpkg.ApplyACPMetadataDefaults(map[string]any{"acp_agent_id": agentID})
	if mode == sessionpkg.TypeChat {
		spec.Type = sessionpkg.TypeACPAgent
	} else {
		spec.Type = sessionpkg.TypeDiscuss
	}
	return spec, nil
}

// newSessionOperands applies /new's grammar after the shared syntax parser.
// Mentions address chat participants rather than naming a session mode or
// Agent, and callback flags control execution rather than session semantics.
func newSessionOperands(parsed command.ParsedCommand) []string {
	values := make([]string, 0, 1+len(parsed.Args))
	values = append(values, parsed.Action)
	values = append(values, parsed.Args...)
	operands := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || strings.HasPrefix(value, "-") || isMentionArgument(value) {
			continue
		}
		operands = append(operands, value)
	}
	return operands
}

func isMentionArgument(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "@") ||
		(strings.HasPrefix(value, "<@") && strings.HasSuffix(value, ">"))
}

func firstNewSessionAgentArg(args []string) string {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		return normalizeACPAgentID(arg)
	}
	return ""
}

// handleNewSessionCommand resolves the route for the current message and
// creates a brand-new active session, effectively starting a fresh
// conversation in the same IM thread/chat.
// Supports: /new (default), /new chat, /new discuss.
func (p *ChannelInboundProcessor) handleNewSessionCommand(
	ctx context.Context,
	cfg channel.ChannelConfig,
	msg channel.InboundMessage,
	sender channel.StreamReplySender,
	identity InboundIdentity,
	invocation command.Invocation,
) error {
	target := strings.TrimSpace(msg.ReplyTarget)
	if target == "" {
		return errors.New("reply target missing for /new command")
	}
	loc := p.localizer(ctx, identity.BotID)
	caps := p.channelCaps(msg.Channel)

	parsed := invocation.Parsed
	spec, err := resolveNewSessionSpecParsed(parsed, msg, p.acpProfiles)
	if err != nil {
		if feedback := acpFeedbackFromError(err); feedback != nil {
			return p.sendACPFeedbackError(ctx, sender, msg, identity, feedback)
		}
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(loc.T("newSession.usage"), caps),
		})
	}
	spec, err = p.applyDefaultChatRuntimeToNewSessionSpec(ctx, identity, msg, spec)
	if err != nil {
		if feedback := acpFeedbackFromError(err); feedback != nil {
			return p.sendACPFeedbackError(ctx, sender, msg, identity, feedback)
		}
		return err
	}
	if spec.Runtime == sessionpkg.RuntimeACPAgent {
		if err := p.validateACPNewSessionSpec(ctx, identity, spec); err != nil {
			if feedback := acpFeedbackFromError(err); feedback != nil {
				return p.sendACPFeedbackError(ctx, sender, msg, identity, feedback)
			}
			return err
		}
	}
	if spec.Runtime == sessionpkg.RuntimeACPAgent || sessionpkg.IsDirectRuntimeType(spec.Runtime) {
		if err := p.requireWorkspaceExecForACP(ctx, identity); err != nil {
			if feedback := acpFeedbackFromError(err); feedback != nil {
				return p.sendACPFeedbackError(ctx, sender, msg, identity, feedback)
			}
			return err
		}
	}

	// /new discards history, so on button-capable channels gate it behind a
	// Confirm/Cancel keyboard. Tapping Confirm re-dispatches "/new <mode>
	// --confirm" which lands back here with newCommandConfirmed == true and
	// performs the reset. Non-button channels reset immediately (unchanged).
	modeText := newSessionConfirmModeText(spec)
	modeLabel := newSessionDisplayModeLabel(loc, spec, p.acpProfiles)
	if caps.Buttons && !newCommandConfirmedParsed(parsed) {
		return p.sendNewConfirmation(ctx, msg, sender, loc, modeText, modeLabel, caps)
	}

	if p.routeResolver == nil {
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(friendlyOps(loc, "ops.verb.startSession"), caps),
		})
	}
	if p.sessionEnsurer == nil {
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(friendlyOps(loc, "ops.verb.startSession"), caps),
		})
	}

	threadID := extractThreadID(msg)
	routeMetadata := buildRouteMetadata(msg, identity)
	p.enrichConversationAvatar(ctx, cfg, msg, routeMetadata)
	resolved, err := p.routeResolver.ResolveConversation(ctx, route.ResolveInput{
		BotID:                  identity.BotID,
		Platform:               msg.Channel.String(),
		ExternalConversationID: msg.Conversation.ID,
		ExternalThreadID:       threadID,
		ConversationType:       msg.Conversation.Type,
		ChannelConfigID:        identity.ChannelConfigID,
		ReplyTarget:            target,
		Metadata:               routeMetadata,
	})
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("resolve route for /new command failed", slog.Any("error", err))
		}
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(friendlyOps(loc, "ops.verb.startSession"), caps),
		})
	}

	if spec.Runtime == sessionpkg.RuntimeACPAgent || sessionpkg.IsDirectRuntimeType(spec.Runtime) {
		spec.RuntimeOwnerAccountID = acpRuntimeOwnerPrincipal(identity, spec.RuntimeOwnerAccountID)
	}
	if strings.TrimSpace(spec.CreatedByUserID) == "" {
		spec.CreatedByUserID = strings.TrimSpace(identity.UserID)
	} else {
		spec.CreatedByUserID = strings.TrimSpace(spec.CreatedByUserID)
	}
	sess, err := p.sessionEnsurer.CreateNewSession(ctx, identity.BotID, resolved.RouteID, msg.Channel.String(), spec)
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("create new session via /new command failed", slog.Any("error", err))
		}
		if feedback := acpFeedbackFromError(err); feedback != nil {
			return p.sendACPFeedbackError(ctx, sender, msg, identity, feedback)
		}
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(friendlyOps(loc, "ops.verb.startSession"), caps),
		})
	}
	p.cancelActiveStreamForRoute(identity.BotID, resolved.RouteID, "new session created")

	modeLabel = newSessionDisplayModeLabel(loc, spec, p.acpProfiles)
	if p.logger != nil {
		p.logger.Info("new session created via /new command",
			slog.String("bot_id", strings.TrimSpace(identity.BotID)),
			slog.String("route_id", strings.TrimSpace(resolved.RouteID)),
			slog.String("session_id", strings.TrimSpace(sess.ID)),
			slog.String("session_type", sess.Type),
			slog.String("session_mode", spec.Mode),
			slog.String("runtime_type", spec.Runtime),
			slog.String("channel", msg.Channel.String()),
		)
	}
	text := loc.T("newSession.title", map[string]any{"mode": modeLabel})
	botUsername := telegramGroupBotUsername(msg)
	renderedSessionDetails := false
	if p.commandHandler != nil {
		if cc, err := p.commandHandler.CurrentContext(ctx, identity.BotID); err == nil {
			cc = currentContextForNewSessionSpec(cc, spec, p.acpProfiles)
			text = formatNewSessionMessage(loc, modeLabel, cc, botUsername)
			renderedSessionDetails = true
		}
	}
	if botUsername != "" && !renderedSessionDetails {
		var b strings.Builder
		b.WriteString(text)
		appendTelegramGroupCommandTip(&b, loc, botUsername)
		text = b.String()
	}
	out := applyMessageFormat(channel.Message{Text: text}, p.channelCaps(msg.Channel))
	// When confirmed via the inline button, edit the confirmation message into
	// the result card; otherwise reply to (quote) the /new command.
	if editID, ok := msg.Metadata["edit_message_id"].(string); ok && strings.TrimSpace(editID) != "" && p.channelCaps(msg.Channel).Edit {
		out.ID = strings.TrimSpace(editID)
	} else if mid := strings.TrimSpace(msg.Message.ID); mid != "" {
		out.Reply = &channel.ReplyRef{MessageID: mid}
	}
	return sender.Send(ctx, channel.OutboundMessage{Target: target, Message: out})
}

func (p *ChannelInboundProcessor) cancelActiveStreamForRoute(botID, routeID, reason string) bool {
	streamKey := strings.TrimSpace(botID) + ":" + strings.TrimSpace(routeID)
	cancelVal, loaded := p.activeStreams.LoadAndDelete(streamKey)
	if !loaded {
		return false
	}
	cancelFn, ok := cancelVal.(context.CancelFunc)
	if !ok {
		return false
	}
	cancelFn()
	if p.logger != nil {
		p.logger.Info("agent stream aborted",
			slog.String("bot_id", strings.TrimSpace(botID)),
			slog.String("route_id", strings.TrimSpace(routeID)),
			slog.String("reason", strings.TrimSpace(reason)),
		)
	}
	return true
}

func newSessionConfirmModeText(spec NewSessionSpec) string {
	mode := strings.TrimSpace(spec.Mode)
	if mode == "" {
		mode = sessionpkg.TypeChat
	}
	if spec.Runtime == sessionpkg.RuntimeACPAgent {
		if agentID := acpNewSessionAgentID(spec); agentID != "" {
			return mode + " " + agentID
		}
	}
	if sessionpkg.IsDirectRuntimeType(spec.Runtime) {
		return mode + " " + spec.Runtime
	}
	return mode
}

func newSessionModeKey(spec NewSessionSpec) string {
	if spec.Mode == sessionpkg.TypeDiscuss {
		return "newSession.modeDiscussion"
	}
	return "newSession.modeChat"
}

func newSessionDisplayModeLabel(loc *i18n.Localizer, spec NewSessionSpec, profiles turn.ACPProfileResolver) string {
	mode := loc.T(newSessionModeKey(spec))
	runtime := ""
	switch {
	case spec.Runtime == sessionpkg.RuntimeACPAgent:
		runtime = newSessionACPRuntimeLabel(spec, profiles)
		if runtime == "" {
			runtime = "ACP"
		}
	case sessionpkg.IsDirectRuntimeType(spec.Runtime):
		runtime = spec.Runtime
	default:
		return mode
	}
	return loc.T("newSession.modeWithRuntime", map[string]any{
		"mode":    mode,
		"runtime": runtime,
	})
}

func (p *ChannelInboundProcessor) defaultSessionSpecForInbound(ctx context.Context, identity InboundIdentity, msg channel.InboundMessage) (NewSessionSpec, bool, error) {
	mode := sessionpkg.TypeChat
	if isGroupConversation(msg) {
		mode = sessionpkg.TypeDiscuss
	}
	spec := NewSessionSpec{
		Mode:            mode,
		Runtime:         sessionpkg.RuntimeModel,
		Type:            mode,
		CreatedByUserID: strings.TrimSpace(identity.UserID),
	}
	if mode == sessionpkg.TypeDiscuss {
		return spec, true, nil
	}
	spec, err := p.applyDefaultChatRuntimeToNewSessionSpec(ctx, identity, msg, spec)
	if err != nil {
		return NewSessionSpec{}, false, err
	}
	return spec, true, nil
}

func (p *ChannelInboundProcessor) applyDefaultChatRuntimeToNewSessionSpec(ctx context.Context, identity InboundIdentity, msg channel.InboundMessage, spec NewSessionSpec) (NewSessionSpec, error) {
	if spec.Runtime == sessionpkg.RuntimeACPAgent {
		return p.applyDefaultACPProjectToExplicitSpec(ctx, identity, spec)
	}
	if spec.Mode != sessionpkg.TypeChat || isGroupConversation(msg) {
		return spec, nil
	}
	if p.defaultChatRuntime == nil {
		return spec, nil
	}
	defaults, err := p.defaultChatRuntime.DefaultChatRuntime(ctx, identity.BotID)
	if err != nil {
		return NewSessionSpec{}, err
	}
	defaultRuntime := strings.TrimSpace(defaults.Runtime)
	directDefault := runtimekind.IsDirect(defaultRuntime)
	if !directDefault && defaultRuntime != sessionpkg.RuntimeACPAgent {
		return spec, nil
	}
	agentID := normalizeACPAgentID(defaults.ACPAgentID)
	if directDefault {
		// A direct default is fully described by its runtime.
		agentID = defaultRuntime
	}
	if agentID == "" {
		return NewSessionSpec{}, agentfeedback.New(
			agentfeedback.CodeAgentNotConfigured,
			"missing_agent_id",
			http.StatusBadRequest,
			"chat.externalAgent.agentNotConfigured",
			"External agent is selected as the default chat runtime, but no agent is configured.",
			nil,
		)
	}
	if p.permissionChecker == nil {
		return NewSessionSpec{}, p.missingWorkspaceExecFeedback("permission_checker_unavailable", "Current identity cannot be verified for workspace execution.")
	}
	if err := p.requireWorkspaceExecForACP(ctx, identity); err != nil {
		return NewSessionSpec{}, err
	}
	projectPath := strings.TrimSpace(defaults.ProjectPath)
	if projectPath == "" {
		projectPath = sessionpkg.DefaultACPProjectPath
	}

	// Direct external agents have no ACP profile to resolve; only real ACP
	// providers go through the profile registry.
	if directDefault {
		spec.Runtime = defaultRuntime
		spec.Type = sessionpkg.TypeChat
		spec.BotAgentID = strings.TrimSpace(defaults.BotAgentID)
		spec.RuntimeOwnerAccountID = acpRuntimeOwnerPrincipal(identity, "")
		spec.Metadata = map[string]any{
			"project_path": projectPath,
		}
		return spec, nil
	}

	profile := resolveACPProfile(p.acpProfiles, agentID)
	if !profile.Known {
		return NewSessionSpec{}, agentfeedback.New(
			agentfeedback.CodeAgentNotFound,
			"unknown_agent",
			http.StatusBadRequest,
			"chat.externalAgent.agentNotFound",
			"Configured ACP agent was not found.",
			map[string]string{"agent_id": agentID},
		)
	}
	agentID = profile.ID
	projectMode := strings.TrimSpace(defaults.ProjectMode)
	if projectMode == "" {
		projectMode = sessionpkg.DefaultACPProjectMode
	}
	spec.Runtime = sessionpkg.RuntimeACPAgent
	spec.Type = sessionpkg.TypeACPAgent
	spec.BotAgentID = strings.TrimSpace(defaults.BotAgentID)
	spec.RuntimeOwnerAccountID = acpRuntimeOwnerPrincipal(identity, "")
	spec.Metadata = sessionpkg.ApplyACPMetadataDefaults(map[string]any{
		"acp_agent_id":     agentID,
		"project_path":     projectPath,
		"acp_project_mode": projectMode,
	})
	return spec, nil
}

func (p *ChannelInboundProcessor) applyDefaultACPProjectToExplicitSpec(ctx context.Context, identity InboundIdentity, spec NewSessionSpec) (NewSessionSpec, error) {
	if p == nil || p.defaultChatRuntime == nil || spec.Runtime != sessionpkg.RuntimeACPAgent {
		return spec, nil
	}
	defaults, err := p.defaultChatRuntime.DefaultChatRuntime(ctx, identity.BotID)
	if err != nil {
		return NewSessionSpec{}, err
	}
	if strings.TrimSpace(defaults.Runtime) != sessionpkg.RuntimeACPAgent {
		return spec, nil
	}
	agentID := acpNewSessionAgentID(spec)
	defaultAgentID := normalizeACPAgentID(defaults.ACPAgentID)
	if agentID == "" || agentID != defaultAgentID {
		return spec, nil
	}
	metadata := make(map[string]any, len(spec.Metadata)+3)
	for key, value := range spec.Metadata {
		metadata[key] = value
	}
	metadata["acp_agent_id"] = agentID
	currentProjectPath := strings.TrimSpace(metadataString(metadata, "project_path"))
	if currentProjectPath == "" || currentProjectPath == sessionpkg.DefaultACPProjectPath {
		projectPath := strings.TrimSpace(defaults.ProjectPath)
		if projectPath == "" {
			projectPath = sessionpkg.DefaultACPProjectPath
		}
		metadata["project_path"] = projectPath
	}
	currentProjectMode := strings.TrimSpace(metadataString(metadata, "acp_project_mode"))
	if currentProjectMode == "" || currentProjectMode == sessionpkg.DefaultACPProjectMode {
		projectMode := strings.TrimSpace(defaults.ProjectMode)
		if projectMode == "" {
			projectMode = sessionpkg.DefaultACPProjectMode
		}
		metadata["acp_project_mode"] = projectMode
	}
	spec.Metadata = metadata
	return spec, nil
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return ""
	}
}

func (p *ChannelInboundProcessor) validateACPNewSessionSpec(ctx context.Context, identity InboundIdentity, spec NewSessionSpec) error {
	agentID := acpNewSessionAgentID(spec)
	if agentID == "" {
		return agentfeedback.New(
			agentfeedback.CodeAgentNotConfigured,
			"missing_agent_id",
			http.StatusBadRequest,
			"chat.externalAgent.agentNotConfigured",
			"ACP agent id is required for external-agent sessions.",
			nil,
		)
	}
	profile := resolveACPProfile(p.acpProfiles, agentID)
	if !profile.Known {
		return agentfeedback.New(
			agentfeedback.CodeAgentNotFound,
			"unknown_agent",
			http.StatusBadRequest,
			"chat.externalAgent.agentNotFound",
			"Configured ACP agent was not found.",
			map[string]string{"agent_id": agentID},
		)
	}
	projectPath := strings.TrimSpace(metadataString(spec.Metadata, "project_path"))
	if projectPath == "" {
		projectPath = sessionpkg.DefaultACPProjectPath
	}
	if !strings.HasPrefix(projectPath, "/") {
		return agentfeedback.New(
			agentfeedback.CodeProjectPathInvalid,
			"project_path_must_be_absolute",
			http.StatusBadRequest,
			"chat.externalAgent.projectPathInvalid",
			"ACP project path must be absolute.",
			map[string]string{"agent_id": agentID},
		)
	}
	projectMode := strings.TrimSpace(metadataString(spec.Metadata, "acp_project_mode"))
	if projectMode == "" {
		projectMode = sessionpkg.DefaultACPProjectMode
	}
	switch projectMode {
	case sessionpkg.DefaultACPProjectMode:
	case "none":
		return agentfeedback.New(
			agentfeedback.CodeProjectModeInvalid,
			"none_not_supported_for_new_session",
			http.StatusBadRequest,
			"chat.externalAgent.projectModeInvalid",
			"acp_project_mode=none is not supported for channel-created ACP sessions.",
			map[string]string{"agent_id": agentID, "project_mode": projectMode},
		)
	default:
		return agentfeedback.New(
			agentfeedback.CodeProjectModeInvalid,
			"unknown_project_mode",
			http.StatusBadRequest,
			"chat.externalAgent.projectModeInvalid",
			"Unknown ACP project mode.",
			map[string]string{"agent_id": agentID, "project_mode": projectMode},
		)
	}
	if p == nil || p.acpAgentSetup == nil {
		return nil
	}
	metadata, err := p.acpAgentSetup.ACPAgentSetupMetadata(ctx, identity.BotID)
	if err != nil {
		return err
	}
	setup := p.acpProfiles.ResolveACPSetupPreflight(profile.ID, metadata)
	if strings.TrimSpace(spec.BotAgentID) == "" && !setup.Enabled {
		return agentfeedback.New(
			agentfeedback.CodeAgentNotEnabled,
			"agent_not_enabled",
			http.StatusForbidden,
			"chat.externalAgent.agentNotEnabled",
			"ACP agent is not enabled for this bot.",
			map[string]string{"agent_id": agentID},
		)
	}
	if field := setup.MissingManagedField; field != nil {
		return agentfeedback.New(
			agentfeedback.CodeAgentNotConfigured,
			"missing_managed_field",
			http.StatusBadRequest,
			"chat.externalAgent.agentNotConfigured",
			"ACP agent setup is incomplete.",
			map[string]string{"agent_id": agentID, "field_id": field.ID, "field_label": field.Label},
		)
	}
	return nil
}

func (p *ChannelInboundProcessor) requireWorkspaceExecForACP(ctx context.Context, identity InboundIdentity) error {
	return p.requireWorkspaceExecForACPPrincipal(ctx, identity.BotID, acpRuntimeOwnerPrincipal(identity, ""))
}

func (p *ChannelInboundProcessor) requireWorkspaceExecForACPPrincipal(ctx context.Context, botID, accountUserID string) error {
	if p.permissionChecker == nil {
		return p.missingWorkspaceExecFeedback("permission_checker_unavailable", "Current identity cannot be verified for workspace execution.")
	}
	accountUserID = strings.TrimSpace(accountUserID)
	if accountUserID == "" {
		return p.missingWorkspaceExecFeedback("account_user_unbound", "Current identity is not linked to an account with workspace execution permission.")
	}
	allowed, err := p.permissionChecker.HasBotPermission(ctx, strings.TrimSpace(botID), accountUserID, bots.PermissionWorkspaceExec)
	if err != nil {
		return err
	}
	if !allowed {
		return p.missingWorkspaceExecFeedback("missing_workspace_exec", "Current identity does not have workspace execution permission, so it cannot use an external agent as the chat runtime.")
	}
	return nil
}

func (p *ChannelInboundProcessor) requireACPRuntimeActor(_ context.Context, identity InboundIdentity, runtimeOwnerAccountID string) error {
	actorUserID := strings.TrimSpace(identity.UserID)
	runtimeOwnerAccountID = strings.TrimSpace(runtimeOwnerAccountID)
	if runtimeOwnerAccountID == "" {
		return sessionpkg.ErrACPRuntimeOwnerMissing
	}
	if actorUserID == "" {
		return p.missingWorkspaceExecFeedback("account_user_unbound", "Current identity is not linked to an account with workspace execution permission.")
	}
	if actorUserID == runtimeOwnerAccountID {
		return nil
	}
	return p.missingWorkspaceExecFeedback("runtime_owner_mismatch", "This ACP runtime belongs to another user.")
}

// sessionUsesACPRuntime reports a session that runs on an agent runtime with
// workspace access — ACP or a direct external agent — and therefore needs the
// runtime-owner workspace-exec gate before a turn starts. The name predates
// the direct runtimes; every caller wants "agent runtime", not "ACP".
func sessionUsesACPRuntime(sess SessionResult) bool {
	return runtimekind.RequiresWorkspaceExec(sess.Runtime) ||
		strings.TrimSpace(sess.Type) == sessionpkg.TypeACPAgent
}

func sessionSupportsRequestedSkills(sess SessionResult) bool {
	return sessionpkg.SupportsSkillActivation("", sess.Type, sess.Runtime)
}

func newSessionSpecSupportsRequestedSkills(spec NewSessionSpec) bool {
	typ := strings.TrimSpace(spec.Type)
	if typ == "" {
		typ = strings.TrimSpace(spec.Mode)
	}
	return sessionpkg.SupportsSkillActivation("", typ, spec.Runtime)
}

func sessionRequiresACPRuntimeActor(sess SessionResult) bool {
	if !sessionUsesACPRuntime(sess) {
		return false
	}
	return strings.TrimSpace(sess.Type) != sessionpkg.TypeDiscuss
}

func acpRuntimeOwnerPrincipal(identity InboundIdentity, explicitOwner string) string {
	if owner := strings.TrimSpace(explicitOwner); owner != "" {
		return owner
	}
	return strings.TrimSpace(identity.UserID)
}

func isGroupConversation(msg channel.InboundMessage) bool {
	return !isLocalChannelType(msg.Channel) && !channel.IsPrivateConversationType(msg.Conversation.Type)
}

func groupChatACPUnsupportedFeedback() *agentfeedback.Error {
	return agentfeedback.New(
		agentfeedback.CodeGroupChatUnsupported,
		"group_chat_acp_unsupported",
		http.StatusBadRequest,
		"chat.externalAgent.groupChatUnsupported",
		"Group chats cannot create a chat-mode external-agent session. Use /new codex or /new discuss codex to create a discuss external-agent session.",
		nil,
	)
}

func (*ChannelInboundProcessor) missingWorkspaceExecFeedback(reason, message string) *agentfeedback.Error {
	return agentfeedback.New(
		agentfeedback.CodeNoWorkspaceExec,
		reason,
		http.StatusForbidden,
		"chat.externalAgent.noWorkspaceExec",
		message,
		nil,
	)
}

func (p *ChannelInboundProcessor) sendACPFeedbackError(ctx context.Context, sender channel.StreamReplySender, msg channel.InboundMessage, identity InboundIdentity, err error) error {
	feedback := acpFeedbackFromError(err)
	if feedback == nil {
		return err
	}
	target := strings.TrimSpace(msg.ReplyTarget)
	if target == "" {
		return err
	}
	loc := p.localizer(ctx, identity.BotID)
	out := renderResult(&command.Result{
		Text:          strings.TrimSpace(feedback.Message),
		Locale:        loc.Locale(),
		FeedbackError: feedback,
	}, RenderContext{Caps: p.channelCaps(msg.Channel), T: loc})
	if mid := strings.TrimSpace(msg.Message.ID); mid != "" {
		out.Reply = &channel.ReplyRef{MessageID: mid}
	}
	return sender.Send(ctx, channel.OutboundMessage{Target: target, Message: out})
}

func acpFeedbackFromError(err error) *agentfeedback.Error {
	var feedback *agentfeedback.Error
	if errors.As(err, &feedback) {
		return feedback
	}
	switch {
	case errors.Is(err, sessionpkg.ErrACPAgentIDRequired):
		return agentfeedback.New(agentfeedback.CodeAgentNotConfigured, "missing_agent_id", http.StatusBadRequest, "chat.externalAgent.agentNotConfigured", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPUnknownAgent):
		return agentfeedback.New(agentfeedback.CodeAgentNotFound, "unknown_agent", http.StatusBadRequest, "chat.externalAgent.agentNotFound", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPAgentNotEnabled):
		return agentfeedback.New(agentfeedback.CodeAgentNotEnabled, "agent_not_enabled", http.StatusForbidden, "chat.externalAgent.agentNotEnabled", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPAgentNotConfigured):
		return agentfeedback.New(agentfeedback.CodeAgentNotConfigured, "agent_not_configured", http.StatusBadRequest, "chat.externalAgent.agentNotConfigured", err.Error(), nil)
	case errors.Is(err, sessionpkg.ErrACPRuntimeOwnerMissing):
		return agentfeedback.New(agentfeedback.CodeRuntimeOwnerMissing, "missing_runtime_owner", http.StatusForbidden, "chat.externalAgent.runtimeOwnerMissing", err.Error(), nil)
	default:
		return nil
	}
}

func currentContextForNewSessionSpec(cc command.CurrentContext, spec NewSessionSpec, profiles turn.ACPProfileResolver) command.CurrentContext {
	if sessionpkg.IsDirectRuntimeType(spec.Runtime) {
		cc.ChatModel = spec.Runtime
		return cc
	}
	if spec.Runtime != sessionpkg.RuntimeACPAgent {
		return cc
	}
	label := newSessionACPRuntimeLabel(spec, profiles)
	if label == "" {
		cc.ChatModel = "ACP agent"
		return cc
	}
	cc.ChatModel = label
	return cc
}

func newSessionACPRuntimeLabel(spec NewSessionSpec, profiles turn.ACPProfileResolver) string {
	agentID := acpNewSessionAgentID(spec)
	if agentID == "" {
		return ""
	}
	if profile := resolveACPProfile(profiles, agentID); profile.Known && strings.TrimSpace(profile.DisplayName) != "" {
		return profile.DisplayName + " / ACP"
	}
	return agentID + " / ACP"
}

func acpNewSessionAgentID(spec NewSessionSpec) string {
	if spec.Runtime != sessionpkg.RuntimeACPAgent {
		return ""
	}
	return normalizeACPAgentID(newSessionMetadataString(spec.Metadata, "acp_agent_id"))
}

func normalizeACPAgentID(agentID string) string {
	return strings.ToLower(strings.TrimSpace(agentID))
}

func resolveACPProfile(profiles turn.ACPProfileResolver, agentID string) turn.ACPAgentProfile {
	agentID = normalizeACPAgentID(agentID)
	if profiles == nil {
		return turn.ACPAgentProfile{ID: agentID}
	}
	profile := profiles.ResolveACPProfile(agentID)
	profile.ID = normalizeACPAgentID(profile.ID)
	if profile.ID == "" {
		profile.ID = agentID
	}
	return profile
}

func newSessionMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func newCommandConfirmedParsed(parsed command.ParsedCommand) bool {
	for _, a := range parsed.Args {
		if a == "--confirm" {
			return true
		}
	}
	return false
}

// sendNewConfirmation posts the Confirm/Cancel gate for /new. Confirm carries a
// callback that re-dispatches "/new <mode> --confirm"; Cancel dismisses (deletes)
// the prompt.
func (*ChannelInboundProcessor) sendNewConfirmation(
	ctx context.Context,
	msg channel.InboundMessage,
	sender channel.StreamReplySender,
	loc *i18n.Localizer,
	modeText string,
	modeLabel string,
	caps channel.ChannelCapabilities,
) error {
	text := command.MdBold(loc.T("newSession.confirmTitle")) +
		"\n\n" + loc.T("newSession.confirmBody", map[string]any{"mode": modeLabel})
	out := applyMessageFormat(channel.Message{Text: text}, caps)
	out.Actions = []channel.Action{
		{Type: actionTypeCallback, Label: loc.T("newSession.action.confirm"), Value: command.EncodeConfirmNewCallback(modeText), Row: 0},
		{Type: actionTypeCallback, Label: loc.T("newSession.action.cancel"), Value: command.DismissCallback(), Row: 0},
	}
	if mid := strings.TrimSpace(msg.Message.ID); mid != "" {
		out.Reply = &channel.ReplyRef{MessageID: mid}
	}
	return sender.Send(ctx, channel.OutboundMessage{Target: strings.TrimSpace(msg.ReplyTarget), Message: out})
}

func (p *ChannelInboundProcessor) handleStatusCommand(
	ctx context.Context,
	cfg channel.ChannelConfig,
	msg channel.InboundMessage,
	sender channel.StreamReplySender,
	identity InboundIdentity,
	invocation command.Invocation,
) error {
	target := strings.TrimSpace(msg.ReplyTarget)
	if target == "" {
		return errors.New("reply target missing for /status command")
	}
	loc := p.localizer(ctx, identity.BotID)
	caps := p.channelCaps(msg.Channel)
	if p.routeResolver == nil {
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(friendlyOps(loc, "ops.verb.loadStatus"), caps),
		})
	}
	if p.commandHandler == nil {
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(friendlyOps(loc, "ops.verb.loadStatus"), caps),
		})
	}

	threadID := extractThreadID(msg)
	routeMetadata := buildRouteMetadata(msg, identity)
	p.enrichConversationAvatar(ctx, cfg, msg, routeMetadata)
	resolved, err := p.routeResolver.ResolveConversation(ctx, route.ResolveInput{
		BotID:                  identity.BotID,
		Platform:               msg.Channel.String(),
		ExternalConversationID: msg.Conversation.ID,
		ExternalThreadID:       threadID,
		ConversationType:       msg.Conversation.Type,
		ChannelConfigID:        identity.ChannelConfigID,
		ReplyTarget:            target,
		Metadata:               routeMetadata,
	})
	if err != nil {
		if p.logger != nil {
			p.logger.Warn("resolve route for /status command failed", slog.Any("error", err))
		}
		return sender.Send(ctx, channel.OutboundMessage{
			Target:  target,
			Message: plainTextMessage(friendlyOps(loc, "ops.verb.loadStatus"), caps),
		})
	}

	sessionID := ""
	if p.sessionEnsurer != nil {
		sess, sessErr := p.sessionEnsurer.GetActiveSession(ctx, resolved.RouteID)
		if sessErr == nil {
			sessionID = strings.TrimSpace(sess.ID)
		} else if p.logger != nil {
			p.logger.Debug("resolve active session for /status command failed", slog.Any("error", sessErr))
		}
	}

	reply, execErr := p.commandHandler.ExecuteWithInput(ctx, command.ExecuteInput{
		BotID:             strings.TrimSpace(identity.BotID),
		ChannelIdentityID: strings.TrimSpace(identity.ChannelIdentityID),
		UserID:            strings.TrimSpace(identity.UserID),
		Text:              rawTextForCommand(msg, strings.TrimSpace(msg.Message.PlainText())),
		Invocation:        &invocation,
		ChannelType:       msg.Channel.String(),
		ConversationType:  strings.TrimSpace(msg.Conversation.Type),
		ConversationID:    strings.TrimSpace(msg.Conversation.ID),
		ThreadID:          threadID,
		RouteID:           strings.TrimSpace(resolved.RouteID),
		SessionID:         sessionID,
	})
	if execErr != nil {
		if p.logger != nil {
			p.logger.Warn("execute /status command failed", slog.Any("error", execErr))
		}
		reply = friendlyOps(loc, "ops.verb.loadStatus")
	}

	statusOut := applyMessageFormat(channel.Message{Text: reply}, p.channelCaps(msg.Channel))
	if mid := strings.TrimSpace(msg.Message.ID); mid != "" {
		statusOut.Reply = &channel.ReplyRef{MessageID: mid}
	}
	return sender.Send(ctx, channel.OutboundMessage{
		Target:  target,
		Message: statusOut,
	})
}
