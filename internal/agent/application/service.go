// Package application orchestrates agent turns, including context assembly,
// persistence, memory, compaction, approvals, and runtime invocation.
package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	turnpkg "github.com/felinics/memoh/internal/agent/turn"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/hooks"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/reasoning"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/workspace"
)

const (
	defaultMaxContextMinutes = 24 * 60
)

// SkillEntry represents a skill loaded from the container.
type SkillEntry struct {
	Name        string
	Description string
	Content     string
	Path        string
	Metadata    map[string]any
}

// SkillLoader loads skills for a given bot from its container.
type SkillLoader interface {
	LoadSkills(ctx context.Context, botID string) ([]SkillEntry, error)
}

// gatewayAssetLoader resolves content_hash references to binary payloads for gateway dispatch.
type gatewayAssetLoader interface {
	OpenForGateway(ctx context.Context, botID, contentHash string) (reader io.ReadCloser, mime string, err error)
	AccessPathForGateway(ctx context.Context, botID, contentHash string) (string, error)
}

// PlatformIdentity is the Agent-owned projection of a connected platform
// account used while assembling the system prompt.
type PlatformIdentity struct {
	ID               string
	Platform         string
	ExternalIdentity string
	SelfIdentity     map[string]any
}

// PlatformIdentitySource supplies connected platform identities without
// exposing Channel configuration types to the application layer.
type PlatformIdentitySource interface {
	ListPlatformIdentities(ctx context.Context, botID string) ([]PlatformIdentity, error)
}

type botPermissionChecker interface {
	HasBotPermission(ctx context.Context, botID, accountID, permission string) (bool, error)
}

type workspaceTargetResolver interface {
	ResolveWorkspaceTarget(ctx context.Context, botID, targetID string) (workspace.ResolvedWorkspaceTarget, error)
}

type compactionRunner interface {
	RunCompactionSync(context.Context, compaction.TriggerConfig) (compaction.Result, error)
}

// Service orchestrates chat with the internal agent.
type Service struct {
	agent                  *native.Agent
	modelsService          *models.Service
	queries                dbstore.Queries
	memoryRegistry         *memprovider.Registry
	messageService         messagepkg.Service
	settingsService        *settings.Service
	accountService         *accounts.Service
	sessionService         SessionService
	acpPool                acpPrompter
	compactionService      compactionRunner
	eventPublisher         messageevent.Publisher
	skillLoader            SkillLoader
	assetLoader            gatewayAssetLoader
	platformIdentities     PlatformIdentitySource
	botPermissions         botPermissionChecker
	workspaceTargets       workspaceTargetResolver
	workdirs               sessionWorkdirResolver
	pipeline               *timeline.Pipeline
	streamHTTPClient       *http.Client
	nonStreamingHTTPClient *http.Client
	streamIdleTimeout      time.Duration
	streamIdleTimeoutMax   time.Duration
	bgManager              *background.Manager
	toolApproval           *toolapproval.Service
	userInput              userInputService
	hookService            *hooks.Service
	memoryContextMu        sync.Mutex
	memoryContextCache     *memprovider.MemoryContextCache
	acpPromptMu            sync.Mutex
	acpPromptHubs          map[string]*acpActivePromptHub
	// continueUserInputFn overrides the application resume after a user input
	// response; nil means storeUserInputResultAndContinue. Test seam.
	continueUserInputFn               func(ctx context.Context, req userinput.Request, input UserInputResponseInput, result sdk.ToolResultPart, eventCh chan<- WSStreamEvent) error
	sessionCompactionMu               sync.Mutex
	sessionCompactions                map[string]*sessionCompactionGate
	timeout                           time.Duration
	memorySearchTimeout               time.Duration
	contextAbsoluteCapTokens          int
	clockLocation                     *time.Location
	logger                            *slog.Logger
	allowedTeam                       string
	sessionRuntime                    turnAdmitter
	decisionRuntime                   *sessionruntime.Manager
	abortRuntime                      runtimeAbortController
	contextLifecycles                 contextLifecycleStore
	contextLifecyclePersistenceErrors atomic.Uint64
	contextLifecycleCandidatesMu      sync.Mutex
	contextLifecycleCandidates        map[contextLifecycleCandidateKey]contextLifecycleCandidate
	publishTurnEvent                  func(context.Context, sessionruntime.RunHandle, native.StreamEvent) error
	turnHooks                         *turnRuntimeHooks
}

// NewService creates an application service backed by the native agent.
func NewService(
	log *slog.Logger,
	modelsService *models.Service,
	queries dbstore.Queries,
	messageService messagepkg.Service,
	settingsService *settings.Service,
	accountService *accounts.Service,
	a *native.Agent,
	clockLocation *time.Location,
	timeout time.Duration,
) *Service {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if clockLocation == nil {
		clockLocation = time.UTC
	}
	// Streaming requests keep transport establishment bounded, while the
	// application idle watchdog owns first-byte and between-event silence. A
	// client-wide or response-header deadline would otherwise preempt that
	// policy and turn one intentional timeout into repeated transport retries.
	streamTransport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	streamHTTPClient := &http.Client{
		Transport: streamTransport,
	}
	nonStreamingTransport := streamTransport.Clone()
	nonStreamingTransport.ResponseHeaderTimeout = 30 * time.Second
	nonStreamingHTTPClient := &http.Client{
		Transport: nonStreamingTransport,
		Timeout:   10 * time.Minute,
	}

	return &Service{
		agent:                  a,
		modelsService:          modelsService,
		queries:                queries,
		contextLifecycles:      queries,
		messageService:         messageService,
		settingsService:        settingsService,
		accountService:         accountService,
		streamHTTPClient:       streamHTTPClient,
		nonStreamingHTTPClient: nonStreamingHTTPClient,
		timeout:                timeout,
		memorySearchTimeout:    defaultMemorySearchTimeout,
		clockLocation:          clockLocation,
		logger:                 log.With(slog.String("service", "agent/application")),
	}
}

// SetContextAbsoluteMaxTokens sets the server-wide context admission cap
// (CM-ADM-001). Zero keeps the shared default; the cap is never disabled.
func (s *Service) SetContextAbsoluteMaxTokens(v int) {
	s.contextAbsoluteCapTokens = v
}

// contextAbsoluteMaxTokens resolves the effective admission cap.
func (s *Service) contextAbsoluteMaxTokens() int {
	if s.contextAbsoluteCapTokens > 0 {
		return s.contextAbsoluteCapTokens
	}
	return contextfrag.DefaultAbsoluteCapTokens
}

// effectiveContextTokenBudget derives the per-turn context budget as
// min(model context window, absolute cap), falling back to the cap alone
// when the model has no configured window. A missing window therefore
// degrades to a smaller bounded context, never to an unbounded one.
func (s *Service) effectiveContextTokenBudget(chatModel models.GetResponse) int {
	budget := contextBudgetFromChatModel(chatModel)
	capTokens := s.contextAbsoluteMaxTokens()
	if budget <= 0 {
		s.logger.Warn("context budget: model context window missing, applying absolute cap",
			slog.String("model_id", chatModel.ID),
			slog.Int("absolute_cap_tokens", capTokens))
		return capTokens
	}
	return min(budget, capTokens)
}

// SetMemoryRegistry sets the provider registry for memory operations.
func (s *Service) SetMemoryRegistry(registry *memprovider.Registry) {
	s.memoryRegistry = registry
}

// SetSkillLoader sets the skill loader used to populate usable skills in gateway requests.
func (s *Service) SetSkillLoader(sl SkillLoader) {
	s.skillLoader = sl
}

// SetGatewayAssetLoader configures optional asset loading used to inline
// attachments before calling the agent gateway.
func (s *Service) SetGatewayAssetLoader(loader gatewayAssetLoader) {
	s.assetLoader = loader
}

func (s *Service) SetBotPermissionChecker(checker botPermissionChecker) {
	s.botPermissions = checker
}

// SetWorkspaceTargetResolver configures request-scoped Computer resolution.
func (s *Service) SetWorkspaceTargetResolver(resolver workspaceTargetResolver) {
	s.workspaceTargets = resolver
}

// SetPlatformIdentitySource configures the neutral source used to load
// platform identity metadata for system prompt generation.
func (s *Service) SetPlatformIdentitySource(source PlatformIdentitySource) {
	s.platformIdentities = source
}

// SetCompactionService configures the compaction service for context compaction.
func (s *Service) SetCompactionService(service *compaction.Service) {
	if service == nil {
		s.compactionService = nil
		return
	}
	s.compactionService = service
}

// SetBackgroundManager configures the background task manager used for task
// summaries and background status tooling.
func (s *Service) SetBackgroundManager(m *background.Manager) {
	s.bgManager = m
}

func (s *Service) SetToolApprovalService(service *toolapproval.Service) {
	s.toolApproval = service
}

func (s *Service) SetHookService(service *hooks.Service) {
	s.hookService = service
}

func (s *Service) SetUserInputService(service *userinput.Service) {
	if service == nil {
		s.userInput = nil
		return
	}
	s.userInput = service
}

// SetPipeline configures the DCP pipeline for RC-based context assembly.
// When set, resolve() will use RC from the pipeline instead of loading
// history from bot_history_messages for sessions that have pipeline data.
func (s *Service) SetPipeline(p *timeline.Pipeline) {
	s.pipeline = p
}

// Pipeline returns the configured pipeline, or nil.
func (s *Service) Pipeline() *timeline.Pipeline {
	return s.pipeline
}

// InlineImageAttachments resolves image content hashes to sdk.ImagePart values
// using the configured asset loader. Intended for the discuss driver to inline
// images from new RC segments before calling the LLM.
func (s *Service) InlineImageAttachments(ctx context.Context, botID string, refs []timeline.ImageAttachmentRef) []sdk.ImagePart {
	if s == nil || s.assetLoader == nil || len(refs) == 0 {
		return nil
	}
	var parts []sdk.ImagePart
	for _, ref := range refs {
		contentHash := strings.TrimSpace(ref.ContentHash)
		if contentHash == "" {
			continue
		}
		dataURL, mime, err := s.inlineAssetAsDataURL(ctx, botID, contentHash, "image", strings.TrimSpace(ref.Mime))
		if err != nil {
			if s.logger != nil {
				s.logger.Warn(
					"inline discuss image attachment failed",
					slog.Any("error", err),
					slog.String("bot_id", botID),
					slog.String("content_hash", contentHash),
				)
			}
			continue
		}
		parts = append(parts, sdk.ImagePart{
			Image:     dataURL,
			MediaType: mime,
		})
	}
	return parts
}

type resolvedContext struct {
	runConfig                   native.RunConfig
	model                       models.GetResponse
	provider                    sqlc.Provider
	query                       string // headerified persistable query
	userMessageAlreadyInContext bool
	injectedRecords             *[]InjectedMessageRecord
	estimatedTokens             int // estimated input token count for compaction
	compactableTokens           int // raw history eligible for compaction
	compactableTokensKnown      bool
	contextTokenBudget          int // token budget used to clamp compaction triggers
}

func runIDForChatRequest(admittedRunID string) string {
	if runID := strings.TrimSpace(admittedRunID); runID != "" {
		return runID
	}
	return uuid.NewString()
}

func contextBudgetFromChatModel(chatModel models.GetResponse) int {
	return chatModel.Config.ContextBudgetMaxTokens()
}

func markRequiredHistoryMessageCurrent(cfg *native.RunConfig, requiredMessageID string) {
	if cfg == nil {
		return
	}
	cfg.ContextCurrentUserMessageIndex = nil
	requiredMessageID = strings.TrimSpace(requiredMessageID)
	if requiredMessageID == "" {
		return
	}
	for index, sourceMessageID := range cfg.ForkContextSourceMessageIDs {
		if strings.TrimSpace(sourceMessageID) != requiredMessageID {
			continue
		}
		if index >= len(cfg.Messages) || cfg.Messages[index].Role != sdk.MessageRoleUser {
			return
		}
		cfg.ContextCurrentUserMessageIndex = intPointer(index)
		return
	}
}

func defaultToolExchangePolicy() *contextfrag.ToolExchangePolicy {
	return contextfrag.DefaultToolExchangePolicy()
}

// resolve builds the run context for one turn and returns the effective
// request alongside it. Resolution fills in defaults the caller's copy does not
// have — a direct turn on a subagent thread learns its session type, pinned
// model, and skip flags here — so everything that runs after resolution (title
// generation, the step committer, terminal persistence) must use the returned
// request, not the one that went in. Query is deliberately untouched: callers
// that persist a headerified user message still take it from resolvedContext.
func (s *Service) resolve(ctx context.Context, req ChatRequest) (resolvedContext, ChatRequest, error) {
	return s.resolveWithHTTPClient(ctx, req, nil)
}

func (s *Service) resolveWithHTTPClient(ctx context.Context, req ChatRequest, modelHTTPClient *http.Client) (resolvedContext, ChatRequest, error) {
	modelQuery := modelQueryText(req)
	if strings.TrimSpace(modelQuery) == "" && len(req.Attachments) == 0 {
		return resolvedContext{}, req, errors.New("query or attachments is required")
	}
	if strings.TrimSpace(req.BotID) == "" {
		return resolvedContext{}, req, errors.New("bot id is required")
	}
	if strings.TrimSpace(req.ChatID) == "" {
		return resolvedContext{}, req, errors.New("chat id is required")
	}
	if err := s.rejectRequestedSkillsIfUnsupportedContext(ctx, req); err != nil {
		return resolvedContext{}, req, err
	}
	req = s.applySubagentThreadDefaults(ctx, req)

	runCfg, chatModel, provider, err := s.buildBaseRunConfig(ctx, baseRunConfigParams{
		BotID:             req.BotID,
		ChatID:            req.ChatID,
		SessionID:         req.ThreadID,
		RouteID:           req.RouteID,
		UserID:            req.UserID,
		ChannelIdentityID: req.SourceChannelIdentityID,
		CurrentPlatform:   req.CurrentChannel,
		ReplyTarget:       req.ReplyTarget,
		ConversationType:  req.ConversationType,
		SessionToken:      req.ChatToken,
		SessionType:       req.SessionType,
		Model:             req.Model,
		Provider:          req.Provider,
		ReasoningEffort:   req.ReasoningEffort,
		HTTPClient:        modelHTTPClient,
	})
	if err != nil {
		s.logger.Error("resolve: buildBaseRunConfig failed",
			slog.String("bot_id", req.BotID),
			slog.Any("error", err),
		)
		return resolvedContext{}, req, err
	}
	if strings.EqualFold(strings.TrimSpace(req.SessionType), sessionpkg.TypeSubagent) {
		// A direct turn on a subagent thread runs as the subagent, not as a
		// chat turn that happens to share its history: same restricted tool
		// surface (no nested spawns), same prompt mode.
		runCfg.Identity.IsSubagent = true
	}
	runCfg.RunID = runIDForChatRequest(req.RunID)
	memoryMsg := s.loadMemoryContextMessage(ctx, req)
	reqMessages := pruneMessagesForGateway(nonNilModelMessages(req.Messages))
	if memoryMsg != nil {
		pruned, _ := pruneMessageForGateway(*memoryMsg)
		memoryMsg = &pruned
	}

	// When the DCP pipeline has data for this session, build context from
	// the rendered event stream (RC) + bot turn responses (TR) instead of
	// loading raw history from bot_history_messages. The current inbound
	// message is already in the RC, so it must not be appended again.
	usePipeline := s.pipeline != nil &&
		strings.TrimSpace(req.ThreadID) != "" &&
		strings.TrimSpace(req.HistoryCutoffBeforeMessageID) == "" &&
		len(req.RequestedSkills) == 0
	if usePipeline {
		if _, loaded := s.pipeline.GetIC(strings.TrimSpace(req.ThreadID)); !loaded {
			usePipeline = false
		}
	}

	contextTokenBudget := s.effectiveContextTokenBudget(chatModel)
	runCfg.ContextBudgetMaxTokens = contextTokenBudget

	var messages []ModelMessage
	var historyRecords []historyfrag.HistoryRecord
	var estimatedTokens int
	var compactableTokens int
	var compactableTokensKnown bool
	var currentMessageIndex *int
	if usePipeline {
		messages = s.buildMessagesFromPipeline(ctx, req, contextTokenBudget)
		currentMessageIndex = latestModelUserMessageIndex(messages)
	} else {
		historyFallback := historyScopeFallbackFromChatRequest(req)
		prepared, loadErr := s.prepareHistoryContext(ctx, req, historyFallback, contextTokenBudget)
		if loadErr != nil {
			s.logger.Error("resolve: prepare history context failed",
				slog.String("bot_id", req.BotID),
				slog.String("stage", "initial"),
				slog.Any("error", loadErr),
			)
			return resolvedContext{}, req, loadErr
		}
		messages = prepared.messages
		historyRecords = prepared.records
		estimatedTokens = prepared.estimatedTokens
		compactableTokens = prepared.compactableTokens
		compactableTokensKnown = true
		// The trigger only counts raw (compactable) rows: active summaries can
		// never be compacted away, so including them would make the trigger
		// self-sustaining once accumulated summaries cross the threshold.
		if syncCompactionShouldRun(compactableTokens, contextTokenBudget) {
			compactionThreshold := hardCompactionThreshold(contextTokenBudget)
			s.logger.Warn("resolve: context reached compaction threshold, running synchronous compaction",
				slog.String("bot_id", req.BotID),
				slog.Int("estimated_tokens", estimatedTokens),
				slog.Int("compactable_tokens", compactableTokens),
				slog.Int("context_token_budget", contextTokenBudget),
				slog.Int("compaction_threshold", compactionThreshold),
			)
			// Reload and post-process only when this run actually produced a
			// summary. A noop (cooldown, in-flight, nothing markable) keeps
			// this turn's context untouched — possibly still above the
			// threshold — and the next turn re-evaluates.
			if res := s.runCompactionSync(ctx, req, compactableTokens, contextTokenBudget, chatModel.ID); res.Status == compaction.StatusOK {
				prepared, loadErr = s.prepareHistoryContext(ctx, req, historyFallback, contextTokenBudget)
				if loadErr != nil {
					s.logger.Error("resolve: prepare history context failed",
						slog.String("bot_id", req.BotID),
						slog.String("stage", "post_compaction"),
						slog.Any("error", loadErr),
					)
					return resolvedContext{}, req, loadErr
				}
				messages = prepared.messages
				historyRecords = prepared.records
				estimatedTokens = prepared.estimatedTokens
				compactableTokens = prepared.compactableTokens
				// Remove tool messages from the recent context — they are large
				// and unnecessary when we already have a summary. Keep only
				// user/assistant conversation turns.
				messages = stripToolMessagesWhenCompactionSummaryIsActive(messages, historyRecords)
			}
		}
	}
	if forkContext := s.subagentForkContextModelMessages(ctx, req); len(forkContext) > 0 {
		// The inherited parent snapshot precedes the thread's own transcript,
		// exactly as parent-driven subagent tasks assemble it.
		messages, currentMessageIndex = prependContextMessages(forkContext, messages, currentMessageIndex)
	}
	historyMessageCount := len(messages)
	if notice := s.currentWorkspaceContextMessage(ctx, req); notice != nil {
		messages = append(messages, *notice)
	}
	var memoryMessageIndex *int
	if memoryMsg != nil {
		messages = append(messages, *memoryMsg)
		memoryMessageIndex = intPointer(len(messages) - 1)
	}
	if requestedSkillMsg := buildRequestedSkillContextMessage(req.RequestedSkills); requestedSkillMsg != nil {
		messages = append(messages, *requestedSkillMsg)
	}
	if !usePipeline && !req.ReusePersistedUserMessage {
		messages = append(messages, reqMessages...)
	}
	trimmableMessages := normalizedContextPrefixLength(messages, historyMessageCount)
	messages, currentMessageIndex, memoryMessageIndex = normalizeContextMessages(
		messages,
		currentMessageIndex,
		memoryMessageIndex,
	)
	runCfg.ContextHistoryTokenEstimates = make([]int, len(messages))
	for index := range messages {
		runCfg.ContextHistoryTokenEstimates[index] = estimateMessageTokens(messages[index])
	}
	runCfg.ContextTrimmableMessages = min(trimmableMessages, len(messages))
	if runCfg.ContextToolExchangePolicy == nil {
		runCfg.ContextToolExchangePolicy = defaultToolExchangePolicy()
	}

	displayName := s.resolveDisplayName(ctx, req)
	mergedAttachments := s.routeAndMergeAttachments(ctx, chatModel, req)

	tz := runCfg.Identity.TimezoneLocation
	if tz == nil {
		tz = time.UTC
	}
	headerInput := turnpkg.UserMessageHeaderInput{
		MessageID:         strings.TrimSpace(req.ExternalMessageID),
		ChannelIdentityID: strings.TrimSpace(req.SourceChannelIdentityID),
		DisplayName:       displayName,
		Channel:           req.CurrentChannel,
		ConversationType:  strings.TrimSpace(req.ConversationType),
		ConversationName:  strings.TrimSpace(req.ConversationName),
		Target:            strings.TrimSpace(req.ReplyTarget),
		AttachmentPaths:   extractAttachmentPaths(mergedAttachments),
		Time:              time.Now().In(tz),
		Timezone:          runCfg.Identity.Timezone,
	}
	headerifiedQuery := ""
	if userQueryNeedsHeader(req, len(mergedAttachments)) {
		headerifiedQuery = turnpkg.FormatUserHeader(headerInput, req.Query)
	}
	headerifiedModelQuery := headerifiedQuery
	if strings.TrimSpace(modelQuery) != strings.TrimSpace(req.Query) {
		headerifiedModelQuery = turnpkg.FormatUserHeader(headerInput, modelQuery)
	}
	runCfg.ContextFrags = historyContextFragsForMessages(messages, historyRecords)
	forkMessages := nonNilModelMessages(messages)
	runCfg.ForkContextSourceMessageIDs = historySourceMessageIDsForMessages(forkMessages, historyRecords)
	runCfg.Messages = modelMessagesToSDKMessages(forkMessages)
	runCfg.ContextMemoryMessageIndex = memoryMessageIndex
	if usePipeline {
		runCfg.ContextCurrentUserMessageIndex = currentMessageIndex
	} else if req.ReusePersistedUserMessage {
		markRequiredHistoryMessageCurrent(&runCfg, req.RequiredHistoryMessageID)
	}
	// When using the pipeline the user message is already in the RC;
	// don't send it to the LLM again. headerifiedQuery is still kept
	// for storeRound so the user message gets persisted.
	if !usePipeline && !req.ReusePersistedUserMessage {
		runCfg.Query = headerifiedModelQuery
	}
	runCfg.InlineImages = extractNativeImageParts(mergedAttachments)
	runCfg.InlineAttachments = extractNativeAttachmentParts(mergedAttachments)
	runCfg.ContextScope = buildContextFragScope(req, displayName, runCfg.Identity)
	runCfg = runCfg.RefreshContextFrag()

	var injectedRecords *[]InjectedMessageRecord
	if req.InjectCh != nil {
		agentInjectCh := make(chan native.InjectMessage, cap(req.InjectCh))
		go func() {
			for msg := range req.InjectCh {
				agentMsg := native.InjectMessage{
					Text:            msg.Text,
					HeaderifiedText: msg.HeaderifiedText,
				}
				// Inline any image attachments from the injected message so the
				// model receives them as vision input alongside the text.
				if runCfg.SupportsImageInput && len(msg.Attachments) > 0 {
					agentMsg.ImageParts = s.inlineInjectAttachments(ctx, req.BotID, msg.Attachments)
				}
				agentInjectCh <- agentMsg
			}
			close(agentInjectCh)
		}()
		runCfg.InjectCh = agentInjectCh

		records := make([]InjectedMessageRecord, 0)
		injectedRecords = &records
		var recMu sync.Mutex
		runCfg.InjectedRecorder = func(headerifiedText string, insertAfter int) {
			recMu.Lock()
			*injectedRecords = append(*injectedRecords, InjectedMessageRecord{
				HeaderifiedText: headerifiedText,
				InsertAfter:     insertAfter,
			})
			recMu.Unlock()
		}
	}

	return resolvedContext{
		runConfig:                   runCfg,
		model:                       chatModel,
		provider:                    provider,
		query:                       headerifiedQuery,
		userMessageAlreadyInContext: usePipeline,
		injectedRecords:             injectedRecords,
		estimatedTokens:             estimatedTokens,
		compactableTokens:           compactableTokens,
		compactableTokensKnown:      compactableTokensKnown,
		contextTokenBudget:          contextTokenBudget,
	}, req, nil
}

// Chat sends a synchronous chat request and stores the result.
func (s *Service) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if err := rejectReservedSkillMetadataIfPresent(req); err != nil {
		return ChatResponse{}, err
	}
	if err := s.rejectRequestedSkillsIfUnsupportedContext(ctx, req); err != nil {
		return ChatResponse{}, err
	}
	var err error
	ctx, req, err = s.prepareWorkspaceRequest(ctx, req)
	if err != nil {
		return ChatResponse{}, err
	}
	if _, err := s.isACPAgentSession(ctx, req); err != nil {
		return ChatResponse{}, err
	}

	if req.RawQuery == "" {
		req.RawQuery = strings.TrimSpace(req.Query)
	}
	if !req.UserMessagePersisted {
		req, err = s.applyUserMessageHook(ctx, req)
		if err != nil {
			return ChatResponse{}, err
		}
	}
	rc, req, err := s.resolveWithHTTPClient(ctx, req, s.nonStreamingHTTPClient)
	if err != nil {
		return ChatResponse{}, err
	}
	req.Query = rc.query
	req.RunID = rc.runConfig.RunID

	go s.maybeGenerateSessionTitle(context.WithoutCancel(ctx), req, req.RawQuery)

	cfg := rc.runConfig
	stepCommitter := s.newAgentStepCommitter(ctx, req, rc)
	if stepCommitter != nil {
		cfg.OnStepCommitted = stepCommitter.commit
	}
	cfg = s.prepareRunConfig(ctx, cfg)
	terminal := s.contextLifecycleTerminal(ctx, cfg)
	var lifecycleCause error
	defer func() { terminal(lifecycleCause) }()

	result, err := s.agent.Generate(ctx, cfg)
	lifecycleCause = err
	if err != nil {
		if stepCommitter != nil {
			_ = stepCommitter.finish(ctx, rc.estimatedTokens)
		}
		return ChatResponse{}, err
	}

	outputMessages := sdkMessagesToModelMessages(result.Messages)
	storeReq := req
	if stepCommitter != nil {
		inputTokens := 0
		if result.Usage != nil {
			inputTokens = result.Usage.InputTokens
		}
		if err := stepCommitter.finish(ctx, inputTokens); err != nil {
			lifecycleCause = err
			return ChatResponse{}, err
		}
	} else {
		roundMessages := prependTurnUserMessage(storeReq, outputMessages)
		if err := s.storeRoundWithOptions(ctx, storeReq, roundMessages, rc.model.ID, storeRoundOptions{
			SkipMemory:       storeReq.SkipMemoryExtraction,
			ContextLifecycle: cfg.ContextLifecycle,
		}); err != nil {
			lifecycleCause = err
			return ChatResponse{}, err
		}
		if err := s.persistSessionWorkspaceTarget(ctx, storeReq); err != nil {
			lifecycleCause = err
			return ChatResponse{}, err
		}
		if result.Usage != nil {
			go s.maybeCompact(context.WithoutCancel(ctx), req, rc, result.Usage.InputTokens)
		}
	}

	return ChatResponse{
		Messages: outputMessages,
		Model:    rc.model.ModelID,
		Provider: rc.provider.ClientType,
	}, nil
}

// baseRunConfigParams holds parameters for buildBaseRunConfig that differ
// between chat and discuss callers.
type baseRunConfigParams struct {
	BotID             string
	ChatID            string
	SessionID         string
	RouteID           string
	UserID            string
	ChannelIdentityID string
	CurrentPlatform   string
	ReplyTarget       string
	ConversationType  string
	SessionToken      string //nolint:gosec // session credential material, not a hardcoded secret
	SessionType       string
	Model             string
	Provider          string
	ReasoningEffort   string // caller-provided override (empty = use bot default)
	HTTPClient        *http.Client
}

// buildBaseRunConfig creates a RunConfig with model, credentials, skills,
// identity and system prompt — everything except Messages/Query/InlineImages.
// Both resolve() and ResolveRunConfig() delegate to this shared builder.
func (s *Service) buildBaseRunConfig(ctx context.Context, p baseRunConfigParams) (native.RunConfig, models.GetResponse, sqlc.Provider, error) {
	botSettings, err := s.loadBotSettings(ctx, p.BotID)
	if err != nil {
		return native.RunConfig{}, models.GetResponse{}, sqlc.Provider{}, err
	}
	botInfo, loopDetectionEnabled := s.loadBotRuntimeInfo(ctx, p.BotID)
	userTimezoneName, userClockLocation := s.resolveTimezone(ctx, p.BotID, p.UserID)

	chatID := p.ChatID
	if chatID == "" {
		chatID = p.BotID
	}

	req := buildModelSelectionRequest(p, chatID)

	chatModel, provider, err := s.selectChatModel(ctx, req, botSettings)
	if err != nil {
		return native.RunConfig{}, models.GetResponse{}, sqlc.Provider{}, err
	}

	authService := providers.NewService(nil, s.queries, "")
	authCtx := oauthctx.WithUserID(ctx, p.UserID)
	creds, err := authService.ResolveModelCredentials(authCtx, provider)
	if err != nil {
		return native.RunConfig{}, models.GetResponse{}, sqlc.Provider{}, fmt.Errorf("resolve provider credentials: %w", err)
	}

	baseURL := providers.ProviderConfigString(provider, "base_url")
	chatCompletionsCompat := models.ResolveChatCompletionsCompat(
		baseURL,
		providers.ProviderConfigString(provider, models.ChatCompletionsCompatConfigKey),
	)

	reasoningConfig := resolveReasoningConfig(chatModel, botSettings, p.ReasoningEffort, provider.ClientType)

	modelHTTPClient := p.HTTPClient
	if modelHTTPClient == nil {
		modelHTTPClient = s.streamHTTPClient
	}
	sdkModel := models.NewSDKChatModel(models.SDKModelConfig{
		ModelID:               chatModel.ModelID,
		ClientType:            provider.ClientType,
		APIKey:                creds.APIKey,
		CodexAccountID:        creds.CodexAccountID,
		BaseURL:               baseURL,
		ChatCompletionsCompat: chatCompletionsCompat,
		HTTPClient:            modelHTTPClient,
		ReasoningConfig:       reasoningConfig,
		ReasoningDialect:      chatModel.Config.ReasoningDialect,
		ReasoningOffSupport:   chatModel.Config.ReasoningOffSupport,
		ReasoningDefaultOn:    chatModel.Config.ReasoningDefaultOn,
		ThinkingBudgetMin:     chatModel.Config.ThinkingBudgetMin,
		ThinkingBudgetMax:     chatModel.Config.ThinkingBudgetMax,
		ContextWindow:         contextBudgetFromChatModel(chatModel),
	})

	var agentSkills []native.SkillEntry
	if s.skillLoader != nil {
		entries, skillErr := s.skillLoader.LoadSkills(ctx, p.BotID)
		if skillErr != nil {
			s.logger.Warn("failed to load skills", slog.String("bot_id", p.BotID), slog.Any("error", skillErr))
		} else {
			for _, e := range entries {
				if skill, ok := normalizeGatewaySkill(e); ok {
					agentSkills = append(agentSkills, skill)
				}
			}
		}
	}
	if agentSkills == nil {
		agentSkills = []native.SkillEntry{}
	}

	cfg := native.RunConfig{
		Model:                    sdkModel,
		CurrentModelUUID:         chatModel.ID,
		CurrentModelID:           chatModel.ModelID,
		CurrentModelProvider:     provider.Name,
		ReasoningConfig:          reasoningConfig,
		ReasoningStoredEffort:    strings.TrimSpace(botSettings.ReasoningEffort),
		ReasoningRequestedEffort: strings.TrimSpace(p.ReasoningEffort),
		ChatCompletionsCompat:    chatCompletionsCompat,
		PromptCacheTTL:           providers.ProviderConfigString(provider, "prompt_cache_ttl"),
		SessionType:              p.SessionType,
		SupportsImageInput:       supportsImageInputForModel(chatModel),
		SupportsFileInput:        supportsFileInputForModel(chatModel),
		SupportsToolCall:         chatModel.HasCompatibility(models.CompatToolCall),
		Identity: native.SessionContext{
			BotID:             p.BotID,
			ChatID:            chatID,
			SessionID:         p.SessionID,
			UserID:            strings.TrimSpace(p.UserID),
			ChannelIdentityID: strings.TrimSpace(p.ChannelIdentityID),
			CurrentPlatform:   p.CurrentPlatform,
			ReplyTarget:       strings.TrimSpace(p.ReplyTarget),
			ConversationType:  strings.TrimSpace(p.ConversationType),
			Timezone:          userTimezoneName,
			TimezoneLocation:  userClockLocation,
			SessionToken:      p.SessionToken,
		},
		Bot:               botInfo,
		Skills:            agentSkills,
		LoopDetection:     native.LoopDetectionConfig{Enabled: loopDetectionEnabled},
		BackgroundManager: s.bgManager,
		ContextLifecycle:  contextfrag.NewLifecycleHolder(),
		ContextScope: contextfrag.Scope{
			BotID:             p.BotID,
			ChatID:            chatID,
			SessionID:         p.SessionID,
			ChannelIdentityID: strings.TrimSpace(p.ChannelIdentityID),
			Platform:          strings.TrimSpace(p.CurrentPlatform),
			ConversationType:  strings.TrimSpace(p.ConversationType),
			ReplyTarget:       strings.TrimSpace(p.ReplyTarget),
		},
	}
	if s.toolApproval != nil || s.userInput != nil {
		cfg.ToolApprovalHandler = s.buildToolApprovalHandler(p)
	}
	if bound, hasWorkdir, workdirErr := s.resolveSessionWorkdirBinding(ctx, p.BotID, p.SessionID); workdirErr != nil {
		return native.RunConfig{}, models.GetResponse{}, sqlc.Provider{}, workdirErr
	} else if hasWorkdir {
		cfg.Identity.WorkdirPath = bound.WorkDir
		// Pin the workdir's target so the resolution below runs against it —
		// including its error path when the target is unreachable.
		ctx = workspace.WithWorkspaceTarget(ctx, bound.TargetID)
	}
	if s.workspaceTargets != nil {
		if target, targetErr := s.workspaceTargets.ResolveWorkspaceTarget(ctx, p.BotID, ""); targetErr == nil {
			cfg.Identity.WorkspaceTargetID = strings.TrimSpace(target.TargetID)
			cfg.Identity.WorkspaceTargetKind = strings.TrimSpace(target.Kind)
			cfg.Identity.WorkspaceTargetName = strings.TrimSpace(target.Name)
		} else if workspace.WorkspaceTargetFromContext(ctx) != "" {
			return native.RunConfig{}, models.GetResponse{}, sqlc.Provider{}, targetErr
		}
	}

	return cfg, chatModel, provider, nil
}

func (s *Service) canDeliverUserInputStream() bool {
	return s.userInput != nil
}

func (s *Service) canDeliverUserInputWS(eventCh chan<- WSStreamEvent) bool {
	return s.userInput != nil && eventCh != nil
}

func supportsImageInputForModel(m models.GetResponse) bool {
	return m.HasCompatibility(models.CompatVision)
}

func supportsFileInputForModel(m models.GetResponse) bool {
	return m.HasCompatibility(models.CompatFileInput)
}

// resolveReasoningConfig makes the single reasoning decision for a call. The
// decision itself lives in internal/reasoning, which also answers what a picker
// may offer — the two share internals so the options a user sees and the value a
// call sends cannot disagree.
func resolveReasoningConfig(chatModel models.GetResponse, botSettings settings.Settings, requestedEffort, clientType string) *models.ReasoningConfig {
	return reasoning.ResolveConfig(
		chatModel.ResolveThinkingMode(),
		chatModel.Config.ReasoningEfforts,
		chatModel.ReasoningOptions(clientType),
		botSettings.ReasoningEffort,
		requestedEffort,
		clientType,
	)
}

func (s *Service) buildToolApprovalHandler(p baseRunConfigParams) func(context.Context, sdk.ToolCall) (sdk.ToolApprovalResult, error) {
	return func(ctx context.Context, call sdk.ToolCall) (sdk.ToolApprovalResult, error) {
		if strings.TrimSpace(call.ToolName) == userinput.ToolNameAskUser {
			if err := userinput.ValidateAskUserInput(call.Input); err != nil {
				// Let the tool's Execute handler return an instructional tool result
				// to the model instead of creating a fake pending request.
				return sdk.ToolApprovalResult{Decision: sdk.ToolApprovalDecisionApproved}, nil
			}
			if s.userInput == nil {
				return s.limitToolApprovalResult(sdk.ToolApprovalResult{
					Decision: sdk.ToolApprovalDecisionRejected,
					Reason:   "user input service is not configured",
				}, call.ToolName), nil
			}
			if !isInteractiveApprovalSession(p.SessionType) {
				return s.limitToolApprovalResult(sdk.ToolApprovalResult{
					Decision: sdk.ToolApprovalDecisionRejected,
					Reason:   "user input requested in a non-interactive session",
				}, call.ToolName), nil
			}
			// No ExpiresAt here: application requests have no in-process
			// waiter — the run pauses and resumes whenever the user answers,
			// even much later. Only waiter-backed (ACP/MCP) requests expire.
			req, err := s.userInput.CreatePending(ctx, userinput.CreatePendingInput{
				BotID:                        p.BotID,
				SessionID:                    p.SessionID,
				RouteID:                      p.RouteID,
				ChannelIdentityID:            p.ChannelIdentityID,
				RequestedByChannelIdentityID: p.ChannelIdentityID,
				ToolCallID:                   call.ToolCallID,
				ToolName:                     call.ToolName,
				Input:                        call.Input,
				SourcePlatform:               p.CurrentPlatform,
				ReplyTarget:                  p.ReplyTarget,
				ConversationType:             p.ConversationType,
				WorkspaceTargetID:            workspace.WorkspaceTargetFromContext(ctx),
			})
			if err != nil {
				return sdk.ToolApprovalResult{}, err
			}
			if req.Status != userinput.StatusPending {
				return s.limitToolApprovalResult(sdk.ToolApprovalResult{
					Decision:   sdk.ToolApprovalDecisionRejected,
					ApprovalID: req.ID,
					Reason:     "ask_user request is already " + req.Status,
					Metadata:   userinput.DeferredMetadata(req),
				}, call.ToolName), nil
			}
			return sdk.ToolApprovalResult{
				Decision:   sdk.ToolApprovalDecisionDeferred,
				ApprovalID: req.ID,
				Metadata:   userinput.DeferredMetadata(req),
			}, nil
		}
		input := toolapproval.CreatePendingInput{
			BotID:                        p.BotID,
			SessionID:                    p.SessionID,
			RouteID:                      p.RouteID,
			ChannelIdentityID:            p.ChannelIdentityID,
			RequestedByChannelIdentityID: p.ChannelIdentityID,
			ToolCallID:                   call.ToolCallID,
			ToolName:                     call.ToolName,
			ToolInput:                    call.Input,
			SourcePlatform:               p.CurrentPlatform,
			ReplyTarget:                  p.ReplyTarget,
			ConversationType:             p.ConversationType,
			WorkspaceTargeted:            isWorkspaceTargetTool(call.ToolName),
			WorkspaceTargetID:            workspace.WorkspaceTargetFromContext(ctx),
		}
		forcedApprovalReason, forcedApproval := native.HookForcedApprovalReason(ctx)
		if s.toolApproval == nil {
			if forcedApproval {
				return s.limitToolApprovalResult(sdk.ToolApprovalResult{
					Decision: sdk.ToolApprovalDecisionRejected,
					Reason:   firstNonEmpty(forcedApprovalReason, "hook requested approval but tool approval is not configured"),
				}, call.ToolName), nil
			}
			return sdk.ToolApprovalResult{Decision: sdk.ToolApprovalDecisionApproved}, nil
		}
		eval, err := s.toolApproval.EvaluatePolicy(ctx, input)
		if err != nil {
			if input.WorkspaceTargeted && errors.Is(err, workspace.ErrWorkspaceTargetNotFound) {
				requestedTargetID := ""
				if args, ok := call.Input.(map[string]any); ok {
					requestedTargetID = strings.TrimSpace(readAnyString(args["target_id"]))
				}
				if s.logger != nil {
					s.logger.Warn("workspace tool target not found during approval",
						slog.String("bot_id", p.BotID),
						slog.String("tool_name", call.ToolName),
						slog.String("tool_call_id", call.ToolCallID),
						slog.String("requested_target_id", requestedTargetID),
					)
				}
				// Twilight treats approval handler errors as fatal to the whole run.
				// A missing target is invalid model input, so let the tool execute
				// and return its normal, instructional error instead. This is safe
				// only for not-found targets: they cannot execute or bypass policy.
				return sdk.ToolApprovalResult{Decision: sdk.ToolApprovalDecisionApproved}, nil
			}
			return sdk.ToolApprovalResult{}, err
		}
		input.ExecutionLocation = eval.ExecutionLocation
		locationMetadata := executionLocationResultMetadata(eval.ExecutionLocation)
		if eval.Decision == toolapproval.DecisionDeny {
			return s.limitToolApprovalResult(sdk.ToolApprovalResult{
				Decision: sdk.ToolApprovalDecisionRejected,
				Reason:   toolapproval.PolicyDeniedReason,
				Metadata: locationMetadata,
			}, call.ToolName), nil
		}
		if eval.Decision == toolapproval.DecisionBypass && !forcedApproval {
			return sdk.ToolApprovalResult{
				Decision: sdk.ToolApprovalDecisionApproved,
				Metadata: locationMetadata,
			}, nil
		}
		if !isInteractiveApprovalSession(p.SessionType) {
			req, err := s.toolApproval.CreatePending(ctx, input)
			if err != nil {
				return sdk.ToolApprovalResult{}, err
			}
			reason := "tool execution requires approval, but this session type cannot request approval"
			rejected, err := s.toolApproval.Reject(ctx, req.ID, p.ChannelIdentityID, reason)
			if err != nil {
				return sdk.ToolApprovalResult{}, err
			}
			return s.limitToolApprovalResult(sdk.ToolApprovalResult{
				Decision:   sdk.ToolApprovalDecisionRejected,
				ApprovalID: rejected.ID,
				Reason:     reason,
				Metadata:   approvalResultMetadata(rejected),
			}, call.ToolName), nil
		}
		req, err := s.toolApproval.CreatePending(ctx, input)
		if err != nil {
			return sdk.ToolApprovalResult{}, err
		}
		return sdk.ToolApprovalResult{
			Decision:   sdk.ToolApprovalDecisionDeferred,
			ApprovalID: req.ID,
			Metadata:   approvalResultMetadata(req),
		}, nil
	}
}

func isWorkspaceTargetTool(toolName string) bool {
	_, ok := toolapproval.OperationForTool(toolName)
	return ok
}

func approvalResultMetadata(req toolapproval.Request) map[string]any {
	metadata := map[string]any{
		"short_id":     req.ShortID,
		"status":       req.Status,
		"tool_name":    req.ToolName,
		"operation":    req.Operation,
		"tool_call_id": req.ToolCallID,
		// The nested approval payload is what the stream converter reads for
		// option-aware cards; without it the committed-decision publish path
		// drops the agent's options and the recorded selection.
		"approval": toolapproval.RequestMetadata(req),
	}
	if req.ExecutionLocation != nil {
		metadata[toolapproval.ExecutionLocationMetadataKey] = req.ExecutionLocation
	}
	return metadata
}

func executionLocationResultMetadata(location *toolapproval.ExecutionLocation) map[string]any {
	if location == nil {
		return nil
	}
	return map[string]any{toolapproval.ExecutionLocationMetadataKey: location}
}

func isInteractiveApprovalSession(sessionType string) bool {
	return sessionmode.IsInteractive(sessionType)
}

func (s *Service) resolveRunConfigSessionDescriptor(ctx context.Context, sessionID string) (string, string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s == nil || s.sessionService == nil {
		return sessionpkg.TypeChat, sessionpkg.RuntimeModel
	}
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("ResolveRunConfig: session lookup failed; falling back to chat session type",
				slog.String("session_id", sessionID),
				slog.Any("error", err),
			)
		}
		return sessionpkg.TypeChat, sessionpkg.RuntimeModel
	}
	typ := strings.TrimSpace(sess.Type)
	if typ == "" {
		typ = sessionpkg.TypeChat
	}
	runtimeType := strings.TrimSpace(sess.RuntimeType)
	if runtimeType == "" {
		if sessionpkg.IsACPRuntime(sess) {
			runtimeType = sessionpkg.RuntimeACPAgent
		} else {
			runtimeType = sessionpkg.RuntimeModel
		}
	}
	return typ, runtimeType
}

func (s *Service) resolveRunConfigSessionType(ctx context.Context, sessionID string) string {
	typ, _ := s.resolveRunConfigSessionDescriptor(ctx, sessionID)
	return typ
}

func buildModelSelectionRequest(p baseRunConfigParams, chatID string) ChatRequest {
	return ChatRequest{
		BotID:          p.BotID,
		ChatID:         chatID,
		ThreadID:       p.SessionID,
		CurrentChannel: p.CurrentPlatform,
		Model:          p.Model,
		Provider:       p.Provider,
	}
}

// ResolveRunConfig builds a complete RunConfig (model, system prompt, tools,
// identity) for a bot+session without loading messages or requiring a query.
// The caller is responsible for filling RunConfig.Messages.
// Used by discuss turns to reuse the service's model, tools, and prompt pipeline.
func (s *Service) ResolveRunConfig(ctx context.Context, botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken string) (ResolveRunConfigResult, error) {
	if strings.TrimSpace(botID) == "" {
		return ResolveRunConfigResult{}, errors.New("bot id is required")
	}

	sessionType, runtimeType := s.resolveRunConfigSessionDescriptor(ctx, sessionID)
	if runtimeType == sessionpkg.RuntimeACPAgent {
		cfg := native.RunConfig{
			SessionType: sessionType,
			Identity: native.SessionContext{
				BotID:             botID,
				ChatID:            botID,
				SessionID:         sessionID,
				ChannelIdentityID: strings.TrimSpace(channelIdentityID),
				CurrentPlatform:   currentPlatform,
				ReplyTarget:       strings.TrimSpace(replyTarget),
				ConversationType:  strings.TrimSpace(conversationType),
				SessionToken:      chatToken,
			},
		}
		return ResolveRunConfigResult{
			RunConfig:   cfg,
			RuntimeType: runtimeType,
		}, nil
	}
	cfg, chatModel, _, err := s.buildBaseRunConfig(ctx, baseRunConfigParams{
		BotID:             botID,
		SessionID:         sessionID,
		ChannelIdentityID: channelIdentityID,
		CurrentPlatform:   currentPlatform,
		ReplyTarget:       replyTarget,
		ConversationType:  conversationType,
		SessionToken:      chatToken,
		SessionType:       sessionType,
	})
	if err != nil {
		return ResolveRunConfigResult{}, err
	}

	contextBudget := s.effectiveContextTokenBudget(chatModel)
	cfg.ContextBudgetMaxTokens = contextBudget
	cfg = s.prepareRunConfig(ctx, cfg)
	return ResolveRunConfigResult{
		RunConfig:              cfg,
		ModelID:                chatModel.ID,
		RuntimeType:            runtimeType,
		ContextBudgetMaxTokens: contextBudget,
	}, nil
}

// prepareRunConfig generates the system prompt and appends the user message.
func (s *Service) prepareRunConfig(ctx context.Context, cfg native.RunConfig) native.RunConfig {
	cfg.ContextHookText = ""
	beforePromptResult := s.runPromptHook(ctx, agentRunConfigView{
		BotID:        cfg.Identity.BotID,
		SessionID:    cfg.Identity.SessionID,
		ChatID:       cfg.Identity.ChatID,
		SessionType:  cfg.SessionType,
		MessageCount: len(cfg.Messages),
	}, hooks.EventBeforePromptBuild)
	beforePromptContext := beforePromptResult.AppendContext
	var files []native.SystemFile
	limits := native.DefaultLimits()
	if s.agent != nil {
		limits = s.agent.Limits()
		nowFn := time.Now
		if cfg.Identity.TimezoneLocation != nil {
			nowFn = func() time.Time { return time.Now().In(cfg.Identity.TimezoneLocation) }
		}
		fs := native.NewFSClient(s.agent.BridgeProvider(), cfg.Identity.BotID, nowFn)
		files = fs.LoadSystemFiles(ctx)
	}

	platformIdentitiesSection := ""
	var platformIdentityItems []native.SystemPromptItem
	if s.platformIdentities != nil {
		identities, err := s.platformIdentities.ListPlatformIdentities(ctx, cfg.Identity.BotID)
		if err != nil {
			s.logger.Warn("load bot platform identities failed",
				slog.String("bot_id", cfg.Identity.BotID),
				slog.Any("error", err),
			)
		} else {
			platformIdentityItems = buildPlatformIdentityPromptItems(identities)
			platformIdentitiesSection = buildPlatformIdentitiesSectionFromItems(platformIdentityItems)
		}
	}
	systemParams := native.SystemPromptParams{
		SessionType:               cfg.SessionType,
		Bot:                       cfg.Bot,
		Skills:                    cfg.Skills,
		Files:                     files,
		MaxFilesBytes:             limits.SystemFilesMaxBytes,
		Timezone:                  cfg.Identity.Timezone,
		PlatformIdentitiesSection: platformIdentitiesSection,
		PlatformIdentities:        platformIdentityItems,
	}
	cfg.System = native.GenerateSystemPrompt(systemParams)
	var promptHookTexts []string
	if beforePromptContext != "" {
		text := formatServiceHookContext(hooks.EventBeforePromptBuild, beforePromptContext)
		promptHookTexts = append(promptHookTexts, text)
	}
	beforePromptObservedTexts := append([]string(nil), promptHookTexts...)
	beforePromptObservedTexts = append(beforePromptObservedTexts, hookSystemSectionTexts(beforePromptResult)...)
	afterPromptResult := s.runPromptHook(ctx, agentRunConfigView{
		BotID:        cfg.Identity.BotID,
		SessionID:    cfg.Identity.SessionID,
		ChatID:       cfg.Identity.ChatID,
		SessionType:  cfg.SessionType,
		MessageCount: len(cfg.Messages),
		SystemBytes:  afterPromptHookSystemBytes(cfg.System, beforePromptObservedTexts),
	}, hooks.EventAfterPromptBuild)
	afterPromptContext := afterPromptResult.AppendContext
	if afterPromptContext != "" {
		text := formatServiceHookContext(hooks.EventAfterPromptBuild, afterPromptContext)
		promptHookTexts = append(promptHookTexts, text)
	}
	cfg.ContextHookText = strings.Join(promptHookTexts, "\n\n")

	if cfg.Query != "" {
		var extra []sdk.MessagePart
		for _, img := range cfg.InlineImages {
			if strings.TrimSpace(img.Image) != "" {
				extra = append(extra, img)
			}
		}
		extra = append(extra, cfg.InlineAttachments...)
		cfg.Messages = append(cfg.Messages, sdk.UserMessage(cfg.Query, extra...))
		cfg.ForkContextSourceMessageIDs = append(cfg.ForkContextSourceMessageIDs, "")
		index := len(cfg.Messages) - 1
		cfg.ContextCurrentUserMessageIndex = &index
		cfg.ContextQueryMaterialized = true
	} else if len(cfg.InlineImages) > 0 || len(cfg.InlineAttachments) > 0 {
		// Pipeline path: the user query is already embedded in the RC messages,
		// but media parts are not rendered by the pipeline renderer. Inject the
		// inline media into the last user message so the model receives them.
		imageParts := make([]sdk.MessagePart, 0, len(cfg.InlineImages)+len(cfg.InlineAttachments))
		for _, img := range cfg.InlineImages {
			if strings.TrimSpace(img.Image) != "" {
				imageParts = append(imageParts, img)
			}
		}
		imageParts = append(imageParts, cfg.InlineAttachments...)
		if len(imageParts) > 0 {
			currentIndex := validCurrentUserMessageIndex(
				cfg.Messages,
				cfg.ContextCurrentUserMessageIndex,
				cfg.ContextMemoryMessageIndex,
			)
			injected := false
			for i := len(cfg.Messages) - 1; i >= 0; i-- {
				if cfg.Messages[i].Role == sdk.MessageRoleUser {
					cfg.Messages[i].Content = append(cfg.Messages[i].Content, imageParts...)
					if i < len(cfg.ForkContextSourceMessageIDs) {
						cfg.ForkContextSourceMessageIDs[i] = ""
					}
					injected = true
					break
				}
			}
			if !injected {
				cfg.Messages = append(cfg.Messages, sdk.UserMessage("", imageParts...))
				cfg.ForkContextSourceMessageIDs = append(cfg.ForkContextSourceMessageIDs, "")
				index := len(cfg.Messages) - 1
				currentIndex = &index
			}
			cfg.ContextCurrentUserMessageIndex = currentIndex
			cfg.ContextQueryMaterialized = true
		}
	}

	hookBuild := buildHookSystemSections([]promptHookOutput{
		{Event: hooks.EventBeforePromptBuild, Result: beforePromptResult},
		{Event: hooks.EventAfterPromptBuild, Result: afterPromptResult},
	}, cfg.ContextScope)
	cfg.ContextSourceFrags = buildProviderSourceFrags(
		ctx,
		cfg,
		native.GenerateSystemSections(systemParams),
		hookBuild.Frags,
	)
	cfg.ContextSourceWarnings = hookBuild.Warnings
	return cfg.RefreshContextFrag()
}

func normalizeContextMessages(messages []ModelMessage, currentIndex, memoryIndex *int) ([]ModelMessage, *int, *int) {
	cleaned := sanitizeMessages(messages)
	stripTools := len(cleaned) > 10
	if stripTools {
		// Tool outputs are large and waste tokens; the LLM doesn't need raw
		// tool results when summaries and memory tools are available for lookup.
		cleaned = stripToolMessages(cleaned)
	}
	cleaned = repairToolCallClosures(cleaned, syntheticToolClosureError)
	return cleaned,
		remapContextMessageIndex(messages, currentIndex, stripTools),
		remapContextMessageIndex(messages, memoryIndex, stripTools)
}

func prependContextMessages(prefix, messages []ModelMessage, trackedIndex *int) ([]ModelMessage, *int) {
	if len(prefix) == 0 {
		return messages, trackedIndex
	}
	if trackedIndex != nil {
		if *trackedIndex < 0 || *trackedIndex >= len(messages) {
			trackedIndex = nil
		} else {
			trackedIndex = intPointer(*trackedIndex + len(prefix))
		}
	}
	return append(prefix, messages...), trackedIndex
}

func normalizedContextPrefixLength(messages []ModelMessage, rawPrefixLength int) int {
	if rawPrefixLength <= 0 || len(messages) == 0 {
		return 0
	}
	rawPrefixLength = min(rawPrefixLength, len(messages))
	stripTools := len(sanitizeMessages(messages)) > 10
	prefix := sanitizeMessages(messages[:rawPrefixLength])
	if stripTools {
		prefix = stripToolMessages(prefix)
	}
	prefix = repairToolCallClosures(prefix, syntheticToolClosureError)
	return len(prefix)
}

func remapContextMessageIndex(messages []ModelMessage, index *int, stripTools bool) *int {
	if index == nil || *index < 0 || *index >= len(messages) || !strings.EqualFold(strings.TrimSpace(messages[*index].Role), "user") {
		return nil
	}
	prefix := sanitizeMessages(messages[:*index+1])
	if stripTools {
		prefix = stripToolMessages(prefix)
	}
	prefix = repairToolCallClosures(prefix, syntheticToolClosureError)
	if len(prefix) == 0 || !strings.EqualFold(strings.TrimSpace(prefix[len(prefix)-1].Role), "user") {
		return nil
	}
	return intPointer(len(prefix) - 1)
}

func latestModelUserMessageIndex(messages []ModelMessage) *int {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "user") {
			return intPointer(i)
		}
	}
	return nil
}

func validCurrentUserMessageIndex(messages []sdk.Message, currentIndex, memoryIndex *int) *int {
	if currentIndex != nil && *currentIndex >= 0 && *currentIndex < len(messages) &&
		messages[*currentIndex].Role == sdk.MessageRoleUser && (memoryIndex == nil || *currentIndex != *memoryIndex) {
		return intPointer(*currentIndex)
	}
	return latestUserMessageIndex(messages, optionalIndex(memoryIndex)...)
}

func latestUserMessageIndex(messages []sdk.Message, excluded ...int) *int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sdk.MessageRoleUser && !containsIndex(excluded, i) {
			return intPointer(i)
		}
	}
	return nil
}

func optionalIndex(index *int) []int {
	if index == nil {
		return nil
	}
	return []int{*index}
}

func containsIndex(indexes []int, target int) bool {
	for _, index := range indexes {
		if index == target {
			return true
		}
	}
	return false
}

func intPointer(value int) *int { return &value }

func normalizeGatewaySkill(entry SkillEntry) (native.SkillEntry, bool) {
	name := strings.TrimSpace(entry.Name)
	if name == "" {
		return native.SkillEntry{}, false
	}
	description := strings.TrimSpace(entry.Description)
	if description == "" {
		description = name
	}
	content := strings.TrimSpace(entry.Content)
	if content == "" {
		content = description
	}
	return native.SkillEntry{
		Name:        name,
		Description: description,
		Content:     content,
		Path:        strings.TrimSpace(entry.Path),
		Metadata:    entry.Metadata,
	}, true
}

func normalizeUserMessageContent(msg ModelMessage) ModelMessage {
	if !strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
		return msg
	}
	normalized, changed := normalizeUserContentParts(msg.Content)
	if !changed {
		return msg
	}
	msg.Content = normalized
	return msg
}

func normalizeUserContentParts(content json.RawMessage) (json.RawMessage, bool) {
	if len(content) == 0 {
		return nil, false
	}
	var parts []map[string]any
	if err := json.Unmarshal(content, &parts); err != nil || len(parts) == 0 {
		return nil, false
	}

	changed := false
	rebuilt := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		partType := strings.TrimSpace(strings.ToLower(readAnyString(part["type"])))
		switch partType {
		case "image":
			normalized, ok, didChange := normalizeUserImagePart(part)
			if didChange {
				changed = true
			}
			if ok {
				rebuilt = append(rebuilt, normalized)
			}
		default:
			rebuilt = append(rebuilt, part)
		}
	}
	if !changed {
		return nil, false
	}
	if len(rebuilt) == 0 {
		rebuilt = append(rebuilt, map[string]any{
			"type": "text",
			"text": "[User sent an attachment]",
		})
	}
	data, err := json.Marshal(rebuilt)
	if err != nil {
		return nil, false
	}
	return data, true
}

func normalizeUserImagePart(part map[string]any) (map[string]any, bool, bool) {
	raw, ok := part["image"]
	if !ok {
		return nil, false, true
	}
	if image, ok := raw.(string); ok && strings.TrimSpace(image) != "" {
		return part, true, false
	}
	bytes, ok := anyIndexedByteObject(raw)
	if !ok {
		return nil, false, true
	}
	cloned := cloneAnyMap(part)
	mediaType := strings.TrimSpace(readAnyString(cloned["mediaType"]))
	encoded := base64.StdEncoding.EncodeToString(bytes)
	if mediaType != "" {
		cloned["image"] = "data:" + mediaType + ";base64," + encoded
	} else {
		cloned["image"] = encoded
	}
	return cloned, true, true
}

func cloneAnyMap(input map[string]any) map[string]any {
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func readAnyString(value any) string {
	text, _ := value.(string)
	return text
}

func anyIndexedByteObject(value any) ([]byte, bool) {
	obj, ok := value.(map[string]any)
	if !ok || len(obj) == 0 {
		return nil, false
	}
	indexes := make([]int, 0, len(obj))
	values := make(map[int]byte, len(obj))
	for key, raw := range obj {
		idx, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil || idx < 0 {
			return nil, false
		}
		byteValue, ok := anyNumberToByte(raw)
		if !ok {
			return nil, false
		}
		indexes = append(indexes, idx)
		values[idx] = byteValue
	}
	sort.Ints(indexes)
	if indexes[len(indexes)-1]+1 != len(indexes) {
		return nil, false
	}
	bytes := make([]byte, len(indexes))
	for _, idx := range indexes {
		bytes[idx] = values[idx]
	}
	return bytes, true
}

func anyNumberToByte(value any) (byte, bool) {
	floatValue, ok := value.(float64)
	if !ok || math.IsNaN(floatValue) || math.IsInf(floatValue, 0) {
		return 0, false
	}
	if floatValue < 0 || floatValue > 255 || math.Trunc(floatValue) != floatValue {
		return 0, false
	}
	parsed, err := strconv.ParseUint(strconv.FormatFloat(floatValue, 'f', 0, 64), 10, 8)
	if err != nil {
		return 0, false
	}
	return byte(parsed), true
}

// userQueryNeedsHeader reports whether resolve should wrap req.Query in the
// user message header (sender/channel/time/attachment paths). Text always
// gets a header. An attachment-only message (no caption) must too: the header
// is the only thing telling the model who sent what file, and the headerified
// query is what makes the user turn (with its asset links) persist to history.
// The one deliberate exception is a no-prompt skill activation, whose stored
// user content must stay empty (its model text travels via ModelQuery, and
// display state via skill_activation metadata).
func userQueryNeedsHeader(req ChatRequest, attachmentCount int) bool {
	if strings.TrimSpace(req.Query) != "" {
		return true
	}
	return attachmentCount > 0 && req.UserMessageKind != UserMessageKindSkillActivation
}

// extractAttachmentPaths collects container file paths from ALL gateway
// attachments — both tool_file_ref (fallback) and native images that carry a
// FallbackPath. This ensures the YAML user header always lists every
// attachment the user sent, regardless of whether the model consumes the
// image natively or via the read_media tool.
func extractAttachmentPaths(attachments []any) []string {
	var paths []string
	for _, att := range attachments {
		ga, ok := att.(gatewayAttachment)
		if !ok {
			continue
		}
		if ga.Transport == gatewayTransportToolFileRef && strings.TrimSpace(ga.Payload) != "" {
			paths = append(paths, ga.Payload)
		} else if strings.TrimSpace(ga.FallbackPath) != "" {
			paths = append(paths, ga.FallbackPath)
		}
	}
	return paths
}

// extractNativeImageParts returns sdk.ImagePart entries for attachments that
// the model can consume as inline multimodal input (vision-capable images with
// an inline data URL or public URL payload).
func extractNativeImageParts(attachments []any) []sdk.ImagePart {
	var parts []sdk.ImagePart
	for _, att := range attachments {
		ga, ok := att.(gatewayAttachment)
		if !ok || ga.Type != "image" {
			continue
		}
		transport := strings.ToLower(strings.TrimSpace(ga.Transport))
		if transport != gatewayTransportInlineDataURL && transport != gatewayTransportPublicURL {
			continue
		}
		payload := strings.TrimSpace(ga.Payload)
		if payload == "" {
			continue
		}
		parts = append(parts, sdk.ImagePart{
			Image:     payload,
			MediaType: strings.TrimSpace(ga.Mime),
		})
	}
	return parts
}
