package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/reasoning"
	"github.com/felinics/memoh/internal/settings"
)

// SpawnAgent is the interface the subagent control tools use to run tasks.
// It is satisfied by *agent.Agent and avoids an import cycle.
type SpawnAgent interface {
	Generate(ctx context.Context, cfg SpawnRunConfig) (*SpawnResult, error)
	GenerateWithWatchdog(ctx context.Context, cfg SpawnRunConfig, touchFn func()) (*SpawnResult, error)
}

// SpawnAttemptDisposition names the authoritative outcome of one native
// attempt. Failed attempts can end the admitted run, retry inside it, or
// reflect owning cancellation; a clean end is recorded as completed.
type SpawnAttemptDisposition uint8

const (
	SpawnAttemptFailure SpawnAttemptDisposition = iota
	SpawnAttemptRetry
	SpawnAttemptAbort
	SpawnAttemptCompleted
)

// SpawnRunConfig mirrors agent.RunConfig fields needed by subagent controls.
type SpawnRunConfig struct {
	RunID         string
	Model         *sdk.Model
	ModelUUID     string
	ModelID       string
	ModelProvider string
	System        string
	Query         string
	SessionType   string
	Identity      SpawnIdentity
	LoopDetection SpawnLoopConfig
	Messages      []sdk.Message
	// ReasoningConfig is the thinking decision resolved for the subagent's own
	// model. It replaces a lone effort string that was never assigned, which is
	// how subagents came to run with no reasoning configuration at all (#983).
	ReasoningConfig *models.ReasoningConfig
	// Keep the unresolved parent-turn inputs as well, so a nested subagent can
	// resolve the same override against its own selected model.
	ReasoningStoredEffort     string
	ReasoningRequestedEffort  string
	PromptCacheTTL            string
	ChatCompletionsCompat     string
	SupportsImageInput        bool
	SupportsFileInput         bool
	SupportsToolCall          bool
	Skills                    map[string]SkillDetail
	BackgroundManager         *background.Manager
	ContextBudgetMaxTokens    int
	ContextToolExchangePolicy *contextfrag.ToolExchangePolicy
	// TurnRequestMessageID is the persisted task user message this run's
	// assistant and tool rows bind to, so incremental step persistence files
	// them into the same history turn the runtime view names.
	TurnRequestMessageID string
	// OnStepPersisted, if set, is called after a complete or interrupted step of
	// this run has been durably persisted. The spawn provider uses it to stop
	// retrying an attempt that has already produced durable output (and possibly
	// real side effects): replaying the turn from the top would do that work twice.
	OnStepPersisted func()
	// Attempt is one-based and MaxAttempts is the total outer-attempt budget.
	// ResolveAttempt is called at most once after a failed native attempt. It is
	// the spawn provider's authoritative disposition: retry keeps the admitted
	// run live, abort records owning cancellation, and failure ends the run.
	// ResolveCompletion arbitrates a clean native end against concurrent owning
	// cancellation before that end is published; it returns Completed, Abort for
	// an explicit stop, or Failure for another owning-context failure.
	// ReconcileTerminal applies the outcome selected by the session runtime when
	// terminal publication races a routed control such as AbortControl.
	Attempt           int
	MaxAttempts       int
	ResolveAttempt    func(error) SpawnAttemptDisposition
	ResolveCompletion func() SpawnAttemptDisposition
	ReconcileTerminal func(SpawnAttemptDisposition)
}

// SpawnIdentity mirrors agent.SessionContext fields needed by subagent controls.
type SpawnIdentity struct {
	BotID               string
	ChatID              string
	SessionID           string
	UserID              string
	ChannelIdentityID   string
	CurrentPlatform     string
	ReplyTarget         string
	ConversationType    string
	SessionToken        string //nolint:gosec // #nosec G117 -- session identifier, not a secret
	WorkspaceTargetID   string
	WorkspaceTargetKind string
	WorkspaceTargetName string
	WorkdirPath         string
	TimezoneLocation    *time.Location
	IsSubagent          bool
}

// SpawnLoopConfig mirrors agent.LoopDetectionConfig.
type SpawnLoopConfig struct {
	Enabled bool
}

// SpawnResult mirrors agent.GenerateResult.
type SpawnResult struct {
	Messages         []sdk.Message
	Text             string
	Usage            *sdk.Usage
	ContextLifecycle *contextfrag.LifecycleSnapshot
	// Persisted reports that incremental step persistence owned this run's
	// history, so the caller must not persist the result again.
	Persisted bool
}

const (
	// subagentTimeout caps total execution time as a safety net per attempt.
	subagentTimeout = 10 * time.Minute
	// spawnProgressInterval keeps the parent stream active during foreground waits.
	spawnProgressInterval   = 30 * time.Second
	subagentMaxRetries      = 3
	subagentRetryBaseDelay  = 2 * time.Second
	subagentWatchdogTimeout = 3 * time.Minute

	agentControlVersion = "v2"
)

// ErrWatchdogTimedOut is returned when the subagent watchdog fires.
var ErrWatchdogTimedOut = errors.New("subagent watchdog: no activity within timeout")

var (
	err429Pattern    = regexp.MustCompile(`(^|[^0-9])429($|[^0-9])`)
	errEOFPattern    = regexp.MustCompile(`(?i)connection (reset|refused)|EOF$`)
	serverErrPattern = regexp.MustCompile(`api error 5\d{2}`)
	agentIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	errAgentNotFound = errors.New("agent not found")
)

// SubagentWatchdog implements an activity-based timeout for subagent execution.
type SubagentWatchdog struct {
	timeout time.Duration
	touchCh chan struct{}
	cancel  context.CancelCauseFunc
	done    chan struct{}
	logger  *slog.Logger
}

func NewSubagentWatchdog(parentCtx context.Context, timeout time.Duration, logger *slog.Logger) (context.Context, *SubagentWatchdog) {
	if timeout <= 0 {
		timeout = subagentWatchdogTimeout
	}
	ctx, cancel := context.WithCancelCause(parentCtx)
	wd := &SubagentWatchdog{
		timeout: timeout,
		touchCh: make(chan struct{}, 1),
		cancel:  cancel,
		done:    make(chan struct{}),
		logger:  logger,
	}
	go wd.run(ctx)
	return ctx, wd
}

func (w *SubagentWatchdog) Touch() {
	select {
	case w.touchCh <- struct{}{}:
	default:
	}
}

func (w *SubagentWatchdog) Stop() {
	w.cancel(context.Canceled)
	<-w.done
}

func (w *SubagentWatchdog) run(ctx context.Context) {
	defer close(w.done)
	timer := time.NewTimer(w.timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.touchCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.timeout)
		case <-timer.C:
			w.logger.Warn("subagent watchdog fired", slog.Duration("timeout", w.timeout))
			w.cancel(ErrWatchdogTimedOut)
			return
		}
	}
}

type agentSessionService interface {
	CreateSubagent(ctx context.Context, input sessionpkg.CreateSubagentInput) (sessionpkg.Thread, sessionpkg.SubagentConfig, error)
	GetSubagentConfig(ctx context.Context, sessionID string) (sessionpkg.SubagentConfig, error)
	ListSubagentForkContext(ctx context.Context, sessionID string) ([]sessionpkg.SubagentForkContextMessage, error)
	ListSubagentsByParent(ctx context.Context, parentSessionID string) ([]sessionpkg.Thread, error)
}

type resolvedSubagentModel struct {
	Model        *sdk.Model
	UUID         string
	ModelID      string
	ProviderName string
	// ReasoningConfig is resolved against this model, not inherited from the
	// parent: a subagent may run a different model, whose advertised tiers and
	// off-ability differ. Before #983 it was neither inherited nor resolved, so
	// subagents ran with no thinking configuration at all.
	ReasoningConfig        *models.ReasoningConfig
	PromptCacheTTL         string
	ChatCompletionsCompat  string
	SupportsImageInput     bool
	SupportsFileInput      bool
	SupportsToolCall       bool
	ContextBudgetMaxTokens int
}

type subagentModelCatalogItem struct {
	UUID         string
	ModelID      string
	ProviderName string
	Description  string
	// EffortLevels is the model's supported reasoning effort tiers (empty
	// when the capability registry has no data for the model).
	EffortLevels []string
}

type subagentModelResolver func(
	ctx context.Context,
	session SessionContext,
	modelUUID string,
	modelID string,
	providerName string,
) (resolvedSubagentModel, error)

// SpawnProvider exposes managed subagent control tools.
type SpawnProvider struct {
	agent          SpawnAgent
	settings       *settings.Service
	models         *models.Service
	queries        dbstore.Queries
	sessionService agentSessionService
	messageService messagepkg.Service
	systemPromptFn func(sessionType string) string
	bgManager      *background.Manager
	hookService    *hooks.Service
	admitter       SubagentAdmitter
	modelResolver  subagentModelResolver
	coord          *agentCoordinator
	logger         *slog.Logger
}

func NewSpawnProvider(
	log *slog.Logger,
	settingsSvc *settings.Service,
	modelsSvc *models.Service,
	queries dbstore.Queries,
	sessionService *sessionpkg.Service,
	bgManager *background.Manager,
) *SpawnProvider {
	if log == nil {
		log = slog.Default()
	}
	p := &SpawnProvider{
		settings:       settingsSvc,
		models:         modelsSvc,
		queries:        queries,
		sessionService: sessionService,
		bgManager:      bgManager,
		coord:          newAgentCoordinator(),
		logger:         log.With(slog.String("tool", "agent_control")),
	}
	p.modelResolver = p.resolveModel
	return p
}

func (p *SpawnProvider) SetAgent(a SpawnAgent) {
	p.agent = a
}

func (p *SpawnProvider) SetMessageService(w messagepkg.Service) {
	p.messageService = w
}

func (p *SpawnProvider) SetSystemPromptFunc(fn func(sessionType string) string) {
	p.systemPromptFn = fn
}

func (p *SpawnProvider) SetHookService(h *hooks.Service) {
	p.hookService = h
}

// Usage frames how the available agent-control tools are meant to be used.
func (*SpawnProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	var parts []string
	canStartBackground := false
	if spawnRef, ok := available.Ref(ToolSpawnAgent()); ok {
		canStartBackground = true
		parts = append(parts,
			"Use "+spawnRef+" to create a managed subagent for an independent task.",
			"Subagents can use the bot's configured tools, including workspace, web, memory, skills, browser, email, media, and MCP tools. They cannot ask the user, send direct chat messages or reactions, or create more subagents.",
			"Choose `model_id` when another enabled chat model is better for the task. Add `provider` only when the same model_id exists under multiple providers. Omit `model_id` to reuse the current session model.",
			"Set `fork: true` when the worker needs the parent model's current message context; otherwise it starts with only the assigned task.",
			"Use subagents when work benefits from isolated context or can proceed while you continue. Don't use one for simple single-step work — just do it directly.",
		)
	}
	if ref, ok := available.Ref(ToolListModels()); ok {
		parts = append(parts, "Use "+ref+" to inspect the enabled chat models, provider names, and descriptions before choosing a model for a subagent.")
	}
	if ref, ok := available.Ref(ToolSendMessage()); ok {
		canStartBackground = true
		parts = append(parts, "Use "+ref+" to continue an existing agent with a follow-up.")
	}
	if backgroundTools := available.Refs(ToolWaitUntil(), ToolGetBackgroundStatus(), ToolListBackground(), ToolKillBackground()); len(backgroundTools) > 0 {
		if canStartBackground {
			parts = append(parts, "For long work, set `run_in_background: true`. The call returns a task ID immediately. Use `wait_until(task_id)`, then `get_background_status(task_id)` to read `result`.")
		}
		parts = append(parts, "Manage running agent tasks with "+joinRefs(backgroundTools, "and")+".")
	}
	if ref, ok := available.Ref(ToolListAgents()); ok {
		parts = append(parts, "Use "+ref+" to see agents created in the current session.")
	}
	if ref, ok := available.Ref(ToolSearchMessages()); ok {
		parts = append(parts, "Read a finished task's full transcript with "+ref+" using the session ID returned by get_background_status when the brief result is not enough.")
	}
	if len(parts) == 0 {
		return ""
	}
	return usageSection("Subagents", parts)
}

func (p *SpawnProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	if session.IsSubagent || p.agent == nil {
		return nil, nil
	}
	sess := session
	spawnDescription := "Create one managed subagent for an independent task. Returns a memorable agent_id."
	if catalog, err := p.listModelCatalog(ctx); err == nil {
		spawnDescription = appendModelCatalogToSpawnDescription(spawnDescription, catalog, session)
	}
	return []sdk.Tool{
		{
			Name:        ToolSpawnAgent().String(),
			Description: spawnDescription,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Optional memorable agent id. If omitted, an id like agent_1 is assigned. Must be lowercase letters, digits, underscore, or hyphen.",
					},
					"task": map[string]any{
						"type":        "string",
						"description": "Task instruction for the new agent.",
					},
					"model_id": map[string]any{
						"type":        "string",
						"description": "Optional external model name from list_models. Omit to use the current session model.",
					},
					"provider": map[string]any{
						"type":        "string",
						"description": "Optional provider name used only to disambiguate duplicate model_id values.",
					},
					"fork": map[string]any{
						"type":        "boolean",
						"description": "If true, inherit the parent model's current message context while keeping the subagent system prompt and tools.",
					},
					"run_in_background": map[string]any{
						"type":        "boolean",
						"description": "If true, return immediately with a task_id. Use wait_until(task_id), then get_background_status(task_id) to inspect result.",
					},
				},
				"required": []string{"task"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execSpawnAgent(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolSendMessage().String(),
			Description: "Send a follow-up message to an existing managed subagent. Messages to a busy agent are queued and run serially.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Existing agent id returned when the agent was created or listed.",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "Follow-up instruction for the agent.",
					},
					"run_in_background": map[string]any{
						"type":        "boolean",
						"description": "If true, return immediately with a task_id. If the agent is busy, the message is queued regardless of this value. Use wait_until(task_id), then get_background_status(task_id) to inspect result.",
					},
				},
				"required": []string{"id", "message"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execSendMessage(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolListAgents().String(),
			Description: "List managed subagents created in the current session only.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execListAgents(ctx.Context, sess, inputAsMap(input))
			},
		},
		{
			Name:        ToolListModels().String(),
			Description: "List enabled chat models available for managed subagents, including model_id, provider, description, and the current session model marker.",
			Parameters:  emptyObjectSchema(),
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execListModels(ctx.Context, sess, inputAsMap(input))
			},
		},
	}, nil
}

type agentRecord struct {
	AgentID   string
	SessionID string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type agentRunResult struct {
	AgentID          string                         `json:"agent_id"`
	SessionID        string                         `json:"session_id,omitempty"`
	TaskID           string                         `json:"task_id,omitempty"`
	ModelID          string                         `json:"model_id,omitempty"`
	Provider         string                         `json:"provider,omitempty"`
	Fork             bool                           `json:"fork,omitempty"`
	Status           string                         `json:"status"`
	Message          string                         `json:"message,omitempty"`
	Text             string                         `json:"text,omitempty"`
	Error            string                         `json:"error,omitempty"`
	QueuePosition    int                            `json:"queue_position,omitempty"`
	QueueRemaining   int                            `json:"queue_remaining,omitempty"`
	TimedOut         bool                           `json:"timed_out,omitempty"`
	ContextLifecycle *contextfrag.LifecycleSnapshot `json:"-"`
	Cause            error                          `json:"-"`
	AttemptResolved  bool                           `json:"-"`
	AttemptOutcome   SpawnAttemptDisposition        `json:"-"`
}

type agentRequest struct {
	taskID           string
	agentID          string
	agentSessionID   string
	message          string
	messagePersisted bool
	parentSession    SessionContext
	config           sessionpkg.SubagentConfig
	runtime          resolvedSubagentModel
	systemPrompt     string
	// admission is the run identity the durable gate allocated; persistence
	// files this task's messages under admission.TurnID.
	admission SubagentAdmission
	// requestMessageID is the persisted task user message this run's assistant
	// and tool rows bind to, so the whole task lands in one history turn.
	requestMessageID string
}

type agentCoordinator struct {
	mu     sync.Mutex
	states map[string]*agentState
}

type agentState struct {
	botID           string
	parentSessionID string
	agentID         string
	agentSessionID  string
	runningTaskID   string
	queue           []*agentRequest
	last            agentRunResult
}

type agentStateSnapshot struct {
	RunningTaskID string
	QueuedTaskIDs []string
	Last          agentRunResult
}

func newAgentCoordinator() *agentCoordinator {
	return &agentCoordinator{states: make(map[string]*agentState)}
}

func agentStateKey(botID, parentSessionID, agentID string) string {
	return botID + "\x00" + parentSessionID + "\x00" + agentID
}

func (c *agentCoordinator) ensure(botID, parentSessionID, agentID, agentSessionID string) *agentState {
	key := agentStateKey(botID, parentSessionID, agentID)
	st := c.states[key]
	if st == nil {
		st = &agentState{
			botID:           botID,
			parentSessionID: parentSessionID,
			agentID:         agentID,
			agentSessionID:  agentSessionID,
		}
		c.states[key] = st
	}
	if st.agentSessionID == "" {
		st.agentSessionID = agentSessionID
	}
	return st
}

func (c *agentCoordinator) snapshot(botID, parentSessionID, agentID string) agentStateSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.states[agentStateKey(botID, parentSessionID, agentID)]
	if st == nil {
		return agentStateSnapshot{}
	}
	queued := make([]string, 0, len(st.queue))
	for _, req := range st.queue {
		queued = append(queued, req.taskID)
	}
	return agentStateSnapshot{
		RunningTaskID: st.runningTaskID,
		QueuedTaskIDs: queued,
		Last:          st.last,
	}
}

func (p *SpawnProvider) execSpawnAgent(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	if err := validateParentSession(session); err != nil {
		return nil, err
	}
	task := strings.TrimSpace(StringArg(args, "task"))
	if task == "" {
		return nil, errors.New("task is required")
	}
	agentID, err := p.resolveNewAgentID(ctx, session, StringArg(args, "id"))
	if err != nil {
		return nil, err
	}
	if existing, err := p.findAgent(ctx, session, agentID); err == nil && existing.AgentID != "" {
		return nil, fmt.Errorf("agent %q already exists; choose a different id or continue the existing agent if follow-up messaging is available", agentID)
	} else if err != nil && !errors.Is(err, errAgentNotFound) {
		return nil, err
	}
	requestedModelID := strings.TrimSpace(StringArg(args, "model_id"))
	requestedProvider := strings.TrimSpace(StringArg(args, "provider"))
	if requestedModelID == "" && requestedProvider != "" {
		return nil, errors.New("provider requires model_id")
	}
	runtime, err := p.modelResolver(context.WithoutCancel(ctx), session, "", requestedModelID, requestedProvider)
	if err != nil {
		return nil, fmt.Errorf("resolve subagent model: %w", err)
	}
	forked, _, _ := BoolArg(args, "fork")
	var forkContext []sessionpkg.SubagentForkContextMessage
	if forked {
		if session.ForkContext == nil {
			return nil, errors.New("fork context is not available for this session")
		}
		entries, snapshotErr := session.ForkContext.Entries()
		if snapshotErr != nil {
			return nil, fmt.Errorf("read fork context: %w", snapshotErr)
		}
		forkContext = make([]sessionpkg.SubagentForkContextMessage, 0, len(entries))
		for i, entry := range entries {
			content, marshalErr := json.Marshal(entry.Message)
			if marshalErr != nil {
				return nil, fmt.Errorf("marshal fork context message %d: %w", i, marshalErr)
			}
			forkContext = append(forkContext, sessionpkg.SubagentForkContextMessage{
				SourceMessageID: strings.TrimSpace(entry.SourceMessageID),
				Role:            string(entry.Message.Role),
				Message:         content,
			})
		}
	}
	rec, config, err := p.createAgentSession(context.WithoutCancel(ctx), session, agentID, task, runtime, forked, forkContext)
	if err != nil {
		return nil, err
	}
	runInBackground, _, _ := BoolArg(args, "run_in_background")
	return p.submitAgentTask(ctx, session, rec, config, task, runInBackground)
}

func (p *SpawnProvider) execSendMessage(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	if err := validateParentSession(session); err != nil {
		return nil, err
	}
	agentID, err := normalizeAgentID(StringArg(args, "id"))
	if err != nil {
		return nil, err
	}
	message := strings.TrimSpace(StringArg(args, "message"))
	if message == "" {
		return nil, errors.New("message is required")
	}
	rec, err := p.findAgent(ctx, session, agentID)
	if err != nil {
		return nil, err
	}
	config, err := p.loadSubagentConfig(context.WithoutCancel(ctx), rec)
	if err != nil {
		return nil, err
	}
	runInBackground, _, _ := BoolArg(args, "run_in_background")
	return p.submitAgentTask(ctx, session, rec, config, message, runInBackground)
}

func (p *SpawnProvider) execListModels(ctx context.Context, session SessionContext, _ map[string]any) (any, error) {
	catalog, err := p.listModelCatalog(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(catalog, func(i, j int) bool {
		return catalog[i].UUID == session.CurrentModelUUID && catalog[j].UUID != session.CurrentModelUUID
	})
	items := make([]map[string]any, 0, len(catalog))
	for _, item := range catalog {
		items = append(items, map[string]any{
			"model_id": item.ModelID,
			// model_uuid is the stable reference used where a model is
			// stored by id, e.g. create_schedule's model_id parameter.
			"model_uuid":        item.UUID,
			"provider":          item.ProviderName,
			"description":       item.Description,
			"reasoning_efforts": item.EffortLevels,
			"current":           item.UUID == session.CurrentModelUUID,
		})
	}
	return map[string]any{
		"current_model_id": session.CurrentModelID,
		"current_provider": session.CurrentModelProvider,
		"models":           items,
		"count":            len(items),
	}, nil
}

func (p *SpawnProvider) execListAgents(ctx context.Context, session SessionContext, _ map[string]any) (any, error) {
	if err := validateParentSession(session); err != nil {
		return nil, err
	}
	agents, err := p.listAgentRecords(ctx, session)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(agents))
	for _, rec := range agents {
		snap := p.coord.snapshot(session.BotID, session.SessionID, rec.AgentID)
		status := "idle"
		switch {
		case snap.RunningTaskID != "":
			status = string(background.TaskRunning)
		case len(snap.QueuedTaskIDs) > 0:
			status = string(background.TaskQueued)
		case snap.Last.Status != "":
			status = snap.Last.Status
		}
		item := map[string]any{
			"agent_id":     rec.AgentID,
			"session_id":   rec.SessionID,
			"title":        rec.Title,
			"status":       status,
			"queued_count": len(snap.QueuedTaskIDs),
			"created_at":   session.FormatTime(rec.CreatedAt),
			"updated_at":   session.FormatTime(rec.UpdatedAt),
		}
		if config, configErr := p.sessionService.GetSubagentConfig(ctx, rec.SessionID); configErr == nil {
			item["model_id"] = config.ModelID
			item["provider"] = config.ProviderName
			item["fork"] = config.Forked
		}
		if snap.RunningTaskID != "" {
			item["current_task_id"] = snap.RunningTaskID
		}
		if snap.Last.TaskID != "" {
			item["last_task_id"] = snap.Last.TaskID
		}
		items = append(items, item)
	}
	return map[string]any{"agents": items, "count": len(items)}, nil
}

func (p *SpawnProvider) submitAgentTask(ctx context.Context, session SessionContext, rec agentRecord, config sessionpkg.SubagentConfig, message string, runInBackground bool) (any, error) {
	if p.bgManager == nil {
		return nil, errors.New("background task manager not available")
	}
	// Checked before a task record exists, so an unwired runtime is one clean
	// error instead of a task that is created only to be failed.
	if p.admitter == nil {
		return nil, errors.New("session runtime is not available")
	}
	runtime, err := p.modelResolver(context.WithoutCancel(ctx), session, config.ModelUUID, config.ModelID, config.ProviderName)
	if err != nil {
		return nil, fmt.Errorf("resolve model: %w", err)
	}
	systemPrompt := ""
	if p.systemPromptFn != nil {
		systemPrompt = p.systemPromptFn(sessionpkg.TypeSubagent)
	}

	req := &agentRequest{
		agentID:        rec.AgentID,
		agentSessionID: rec.SessionID,
		message:        message,
		parentSession:  session,
		config:         config,
		runtime:        runtime,
		systemPrompt:   systemPrompt,
	}
	description := truncateTitle(fmt.Sprintf("%s: %s", rec.AgentID, message), 120)

	key := agentStateKey(session.BotID, session.SessionID, rec.AgentID)
	p.coord.mu.Lock()
	st := p.coord.ensure(session.BotID, session.SessionID, rec.AgentID, rec.SessionID)
	if st.runningTaskID != "" {
		taskID, _, err := p.bgManager.StartAgentTask(ctx, session.BotID, session.SessionID, rec.AgentID, rec.SessionID, message, description, true)
		if err != nil {
			p.coord.mu.Unlock()
			return nil, err
		}
		req.taskID = taskID
		st.queue = append(st.queue, req)
		queuePosition := len(st.queue)
		p.coord.mu.Unlock()
		return map[string]any{
			"agent_id":       rec.AgentID,
			"session_id":     rec.SessionID,
			"task_id":        taskID,
			"model_id":       config.ModelID,
			"provider":       config.ProviderName,
			"fork":           config.Forked,
			"status":         string(background.TaskQueued),
			"description":    description,
			"queue_position": queuePosition,
			"message":        "Agent is currently running. Message queued. Use wait_until(task_id), then get_background_status(task_id) to inspect result.",
		}, nil
	}

	taskID, taskCtx, err := p.bgManager.StartAgentTask(context.WithoutCancel(ctx), session.BotID, session.SessionID, rec.AgentID, rec.SessionID, message, description, false)
	if err != nil {
		p.coord.mu.Unlock()
		return nil, err
	}
	req.taskID = taskID
	st.runningTaskID = taskID
	p.coord.mu.Unlock()

	if runInBackground {
		go p.runAgentRequest(taskCtx, key, req)
		return map[string]any{
			"agent_id":    rec.AgentID,
			"session_id":  rec.SessionID,
			"task_id":     taskID,
			"model_id":    config.ModelID,
			"provider":    config.ProviderName,
			"fork":        config.Forked,
			"status":      "background_started",
			"description": description,
			"message":     fmt.Sprintf("Agent %s started in background with task ID: %s. Use wait_until(task_id), then get_background_status(task_id) to inspect result.", rec.AgentID, taskID),
		}, nil
	}

	progressCtx, progressCancel := context.WithCancel(ctx)
	defer progressCancel()
	p.startSpawnProgress(progressCtx, session)
	result := p.runAgentRequest(taskCtx, key, req)
	return agentResultMap(result), nil
}

func (p *SpawnProvider) runAgentRequest(ctx context.Context, key string, req *agentRequest) agentRunResult {
	runCtx, finishRun, admitErr := p.admitAgentRun(ctx, req)
	if admitErr != nil {
		// Nothing was started and nothing was persisted, but the task record has
		// to close anyway: a caller waiting on it would otherwise wait on a run
		// that will never exist.
		return p.completeAgentRequest(ctx, key, req, rejectedAgentRun(req, admitErr))
	}
	requestMessageID, persisted := p.persistUserMessage(context.WithoutCancel(runCtx), req)
	req.messagePersisted = persisted
	if persisted {
		req.requestMessageID = requestMessageID
	}
	result := p.runSubagentTask(runCtx, req)
	if result.AttemptResolved && result.AttemptOutcome == SpawnAttemptAbort {
		if p.agentTaskStopRequested(req.taskID) {
			result.Status = string(background.TaskKilled)
			result.Error = "stopped by the user"
		}
		if result.Status != string(background.TaskKilled) &&
			runCtx.Err() != nil && ctx.Err() == nil {
			result.Status = string(background.TaskKilled)
			result.Error = "stopped by the user"
		}
	} else if !result.AttemptResolved {
		if p.agentTaskStopRequested(req.taskID) {
			result.Status = string(background.TaskKilled)
			result.Error = "stopped by the user"
		}
		if result.Status != string(background.TaskKilled) &&
			runCtx.Err() != nil && ctx.Err() == nil &&
			errors.Is(context.Cause(runCtx), context.Canceled) {
			// The run context died while the parent task is still alive: the child
			// run itself was aborted (stop control on the subagent session). That
			// is a deliberate stop, not a failure, so it is recorded exactly like a
			// kill — and the wording tells the parent what happened rather than
			// handing it an opaque cancellation.
			result.Status = string(background.TaskKilled)
			result.Error = "stopped by the user"
		}
	}
	// Release the thread's slot before the queue promotes the next message, or
	// the successor's admission finds this run still active and is told the
	// agent is busy.
	finishRun(result)
	return p.completeAgentRequest(ctx, key, req, result)
}

func (p *SpawnProvider) agentTaskStopRequested(taskID string) bool {
	return p != nil && p.bgManager != nil && p.bgManager.AgentTaskStopRequested(taskID)
}

// completeAgentRequest closes the background record and hands the agent's queue
// to whatever is next. Both the executed and the refused path end here, so a
// task can never be left running in the manager's view.
func (p *SpawnProvider) completeAgentRequest(ctx context.Context, key string, req *agentRequest, result agentRunResult) agentRunResult {
	status := background.TaskCompleted
	switch {
	case result.Status == string(background.TaskKilled):
		status = background.TaskKilled
	case result.Error != "":
		status = background.TaskFailed
		result.Status = string(background.TaskFailed)
	default:
		result.Status = string(background.TaskCompleted)
	}
	p.bgManager.CompleteAgentTask(req.taskID, background.AgentTaskResult{
		AgentID:        req.agentID,
		AgentSessionID: req.agentSessionID,
		Message:        req.message,
		ModelID:        req.config.ModelID,
		Provider:       req.config.ProviderName,
		Fork:           req.config.Forked,
		Status:         status,
		Report:         result.Text,
		Error:          result.Error,
	})
	p.finishAgentRequest(ctx, key, result)
	return result
}

func (p *SpawnProvider) finishAgentRequest(ctx context.Context, key string, result agentRunResult) {
	var next *agentRequest
	p.coord.mu.Lock()
	st := p.coord.states[key]
	if st != nil {
		st.runningTaskID = ""
		st.last = result
		for len(st.queue) > 0 {
			candidate := st.queue[0]
			st.queue = st.queue[1:]
			task := p.bgManager.Get(candidate.taskID)
			if task != nil && task.Snapshot().Status == background.TaskKilled {
				continue
			}
			next = candidate
			st.runningTaskID = candidate.taskID
			break
		}
	}
	p.coord.mu.Unlock()
	if next == nil {
		return
	}
	runCtx, ok, err := p.bgManager.MarkAgentTaskRunning(ctx, next.taskID)
	if err != nil {
		p.logger.Warn("start queued agent task failed", slog.String("task_id", next.taskID), slog.Any("error", err))
		p.finishAgentRequest(ctx, key, agentRunResult{
			AgentID:   next.agentID,
			SessionID: next.agentSessionID,
			TaskID:    next.taskID,
			ModelID:   next.config.ModelID,
			Provider:  next.config.ProviderName,
			Fork:      next.config.Forked,
			Status:    string(background.TaskFailed),
			Message:   next.message,
			Error:     err.Error(),
		})
		return
	}
	if !ok {
		p.finishAgentRequest(ctx, key, agentRunResult{
			AgentID:   next.agentID,
			SessionID: next.agentSessionID,
			TaskID:    next.taskID,
			ModelID:   next.config.ModelID,
			Provider:  next.config.ProviderName,
			Fork:      next.config.Forked,
			Status:    string(background.TaskKilled),
			Message:   next.message,
		})
		return
	}
	go p.runAgentRequest(runCtx, key, next)
}

func (p *SpawnProvider) runSubagentTask(ctx context.Context, req *agentRequest) agentRunResult {
	res := agentRunResult{
		AgentID:   req.agentID,
		SessionID: req.agentSessionID,
		TaskID:    req.taskID,
		ModelID:   req.config.ModelID,
		Provider:  req.config.ProviderName,
		Fork:      req.config.Forked,
		Message:   req.message,
	}
	runtime, err := p.modelResolver(
		context.WithoutCancel(ctx),
		req.parentSession,
		req.config.ModelUUID,
		req.config.ModelID,
		req.config.ProviderName,
	)
	if err != nil {
		res.Error = fmt.Sprintf("resolve pinned subagent model: %v", err)
		res.Cause = err
		res.Status = string(background.TaskFailed)
		return res
	}
	req.runtime = runtime
	if err := p.runSubagentHook(ctx, hooks.EventSubagentStart, req, res); err != nil {
		res.Error = err.Error()
		res.Cause = err
		res.Status = string(background.TaskFailed)
		return res
	}
	defer func() {
		if err := p.runSubagentHook(context.WithoutCancel(ctx), hooks.EventSubagentStop, req, res); err != nil && p.logger != nil {
			p.logger.Warn("subagent stop hook failed",
				slog.String("bot_id", req.parentSession.BotID),
				slog.String("agent_id", req.agentID),
				slog.Any("error", err),
			)
		}
	}()
	history := p.loadAgentMessages(context.WithoutCancel(ctx), req.agentSessionID)
	if req.messagePersisted {
		history = dropLatestMatchingUserMessage(history, req.message)
	}
	if req.config.Forked {
		parentMessages, loadErr := p.loadAgentForkContext(context.WithoutCancel(ctx), req.agentSessionID)
		if loadErr != nil {
			res.Error = fmt.Sprintf("load fork context: %v", loadErr)
			res.Cause = loadErr
			res.Status = string(background.TaskFailed)
			return res
		}
		combined := make([]sdk.Message, 0, len(parentMessages)+len(history))
		combined = append(combined, parentMessages...)
		combined = append(combined, history...)
		history = combined
	}
	contextBudgetMaxTokens := req.runtime.ContextBudgetMaxTokens
	if contextBudgetMaxTokens <= 0 {
		contextBudgetMaxTokens = req.parentSession.ContextBudgetMaxTokens
	}
	cfg := SpawnRunConfig{
		RunID:                     strings.TrimSpace(req.admission.RunID),
		Model:                     req.runtime.Model,
		ModelUUID:                 req.runtime.UUID,
		ModelID:                   req.runtime.ModelID,
		ModelProvider:             req.runtime.ProviderName,
		ReasoningConfig:           req.runtime.ReasoningConfig,
		ReasoningStoredEffort:     req.parentSession.ReasoningStoredEffort,
		ReasoningRequestedEffort:  req.parentSession.ReasoningRequestedEffort,
		System:                    req.systemPrompt,
		Query:                     req.message,
		SessionType:               sessionpkg.TypeSubagent,
		PromptCacheTTL:            req.runtime.PromptCacheTTL,
		ChatCompletionsCompat:     req.runtime.ChatCompletionsCompat,
		SupportsImageInput:        req.runtime.SupportsImageInput,
		SupportsFileInput:         req.runtime.SupportsFileInput,
		SupportsToolCall:          req.runtime.SupportsToolCall,
		Messages:                  history,
		Skills:                    req.parentSession.Skills,
		BackgroundManager:         p.bgManager,
		ContextBudgetMaxTokens:    contextBudgetMaxTokens,
		ContextToolExchangePolicy: req.parentSession.ContextToolExchangePolicy,
		TurnRequestMessageID:      req.requestMessageID,
		Identity: SpawnIdentity{
			BotID:               req.parentSession.BotID,
			ChatID:              req.parentSession.ChatID,
			SessionID:           req.agentSessionID,
			UserID:              req.parentSession.UserID,
			ChannelIdentityID:   req.parentSession.ChannelIdentityID,
			CurrentPlatform:     req.parentSession.CurrentPlatform,
			ReplyTarget:         req.parentSession.ReplyTarget,
			ConversationType:    req.parentSession.ConversationType,
			SessionToken:        req.parentSession.SessionToken,
			WorkspaceTargetID:   req.parentSession.WorkspaceTargetID,
			WorkspaceTargetKind: req.parentSession.WorkspaceTargetKind,
			WorkspaceTargetName: req.parentSession.WorkspaceTargetName,
			// A subagent works in its parent's working directory.
			WorkdirPath:      req.parentSession.WorkdirPath,
			TimezoneLocation: req.parentSession.TimezoneLocation,
			IsSubagent:       true,
		},
		LoopDetection: SpawnLoopConfig{Enabled: true},
	}
	// stepPersisted flips once any complete step or interrupted checkpoint has
	// durably committed. From then on the attempt loop stops retrying: the agent
	// already produced durable output and possibly real side effects, and
	// replaying the turn from the top would do that work twice.
	var stepPersisted atomic.Bool
	cfg.OnStepPersisted = func() { stepPersisted.Store(true) }

	var lastErr error
	for attempt := 0; attempt <= subagentMaxRetries; attempt++ {
		if attempt > 0 {
			delay := subagentRetryBaseDelay * time.Duration(attempt)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				res.Error = fmt.Sprintf("parent cancelled: %v", ctx.Err())
				res.Cause = context.Cause(ctx)
				res.AttemptResolved = true
				res.AttemptOutcome = SpawnAttemptFailure
				if errors.Is(context.Cause(ctx), context.Canceled) {
					res.AttemptOutcome = SpawnAttemptAbort
				}
				return res
			}
		}

		safetyCtx, safetyCancel := context.WithTimeout(ctx, subagentTimeout)
		wdCtx, wd := NewSubagentWatchdog(safetyCtx, subagentWatchdogTimeout, p.logger)
		cfg.Attempt = attempt + 1
		cfg.MaxAttempts = subagentMaxRetries + 1
		attemptDisposition := SpawnAttemptFailure
		dispositionResolved := false
		cfg.ResolveAttempt = func(attemptErr error) SpawnAttemptDisposition {
			if p.agentTaskStopRequested(req.taskID) {
				attemptDisposition = SpawnAttemptAbort
			} else {
				attemptDisposition = subagentAttemptDisposition(
					ctx,
					attemptErr,
					stepPersisted.Load(),
					attempt < subagentMaxRetries,
				)
			}
			dispositionResolved = true
			return attemptDisposition
		}
		cfg.ResolveCompletion = func() SpawnAttemptDisposition {
			switch {
			case p.agentTaskStopRequested(req.taskID), errors.Is(context.Cause(ctx), context.Canceled):
				attemptDisposition = SpawnAttemptAbort
			case ctx.Err() != nil:
				attemptDisposition = SpawnAttemptFailure
			default:
				attemptDisposition = SpawnAttemptCompleted
			}
			dispositionResolved = true
			return attemptDisposition
		}
		cfg.ReconcileTerminal = func(outcome SpawnAttemptDisposition) {
			attemptDisposition = outcome
			dispositionResolved = true
		}
		genResult, err := p.agent.GenerateWithWatchdog(wdCtx, cfg, wd.Touch)
		wd.Stop()
		safetyCancel()

		if genResult != nil && genResult.ContextLifecycle != nil {
			res.ContextLifecycle = genResult.ContextLifecycle
		}
		if err == nil {
			if !dispositionResolved {
				attemptDisposition = cfg.ResolveCompletion()
			}
			res.AttemptResolved = true
			res.AttemptOutcome = attemptDisposition
			if attemptDisposition == SpawnAttemptAbort {
				cause := context.Cause(ctx)
				if cause == nil {
					cause = context.Canceled
				}
				res.Error = fmt.Sprintf("parent cancelled: %v", cause)
				res.Cause = cause
				return res
			}
			if attemptDisposition != SpawnAttemptCompleted {
				cause := context.Cause(ctx)
				if cause == nil {
					cause = errors.New("agent run failed")
				}
				res.Error = cause.Error()
				res.Cause = cause
				return res
			}
			res.Text = genResult.Text
			if !genResult.Persisted && p.messageService != nil && req.agentSessionID != "" {
				p.persistMessages(context.WithoutCancel(ctx), req, genResult, !req.messagePersisted)
			}
			return res
		}
		lastErr = err
		if !dispositionResolved {
			if p.agentTaskStopRequested(req.taskID) {
				attemptDisposition = SpawnAttemptAbort
			} else {
				attemptDisposition = subagentAttemptDisposition(
					ctx,
					err,
					stepPersisted.Load(),
					attempt < subagentMaxRetries,
				)
			}
		}
		if attemptDisposition == SpawnAttemptRetry {
			continue
		}
		res.AttemptResolved = true
		res.AttemptOutcome = attemptDisposition
		if attemptDisposition == SpawnAttemptAbort {
			cause := context.Cause(ctx)
			if cause == nil {
				cause = context.Canceled
			}
			res.Error = fmt.Sprintf("parent cancelled: %v", cause)
			res.Cause = cause
			return res
		}
		if stepPersisted.Load() {
			res.Error = fmt.Sprintf("%v (progress from this attempt is saved; send a follow-up message to continue)", err)
			return res
		}
		if (errors.Is(err, ErrWatchdogTimedOut) || isRetryableSubagentError(err)) &&
			attempt == subagentMaxRetries {
			break
		}
		res.Error = err.Error()
		res.Cause = err
		return res
	}
	res.Error = fmt.Sprintf("all %d attempts failed (last: %v)", subagentMaxRetries+1, lastErr)
	res.Cause = lastErr
	return res
}

func subagentAttemptDisposition(
	ctx context.Context,
	err error,
	stepPersisted bool,
	attemptsRemain bool,
) SpawnAttemptDisposition {
	if errors.Is(context.Cause(ctx), context.Canceled) {
		return SpawnAttemptAbort
	}
	if ctx != nil && ctx.Err() != nil {
		return SpawnAttemptFailure
	}
	if err != nil && !stepPersisted && attemptsRemain &&
		(errors.Is(err, ErrWatchdogTimedOut) || isRetryableSubagentError(err)) {
		return SpawnAttemptRetry
	}
	return SpawnAttemptFailure
}

func (p *SpawnProvider) runSubagentHook(ctx context.Context, eventName string, req *agentRequest, result agentRunResult) error {
	if p == nil || p.hookService == nil || req == nil {
		return nil
	}
	extra := map[string]any{
		"agent_id":         req.agentID,
		"agent_session_id": req.agentSessionID,
		"task_id":          req.taskID,
		"message":          req.message,
	}
	if strings.TrimSpace(result.Status) != "" {
		extra["status"] = result.Status
	}
	if strings.TrimSpace(result.Error) != "" {
		extra["error"] = result.Error
	}
	if strings.TrimSpace(result.Text) != "" {
		extra["text_bytes"] = len(result.Text)
	}
	hookCWD := strings.TrimSpace(req.parentSession.WorkdirPath)
	if hookCWD == "" {
		hookCWD = hooks.DefaultWorkDir
	}
	hreq := hooks.Request{
		Version:   1,
		Event:     eventName,
		BotID:     req.parentSession.BotID,
		SessionID: req.parentSession.SessionID,
		ChatID:    req.parentSession.ChatID,
		Workspace: hooks.WorkspaceInfo{CWD: hookCWD},
		Extra:     extra,
	}
	res, err := p.hookService.Run(ctx, hreq, nil)
	if err != nil {
		return err
	}
	if res.Decision == hooks.DecisionDeny {
		return hooks.ErrDenied
	}
	return nil
}

func (p *SpawnProvider) createAgentSession(
	ctx context.Context,
	parent SessionContext,
	agentID, task string,
	runtime resolvedSubagentModel,
	forked bool,
	forkContext []sessionpkg.SubagentForkContextMessage,
) (agentRecord, sessionpkg.SubagentConfig, error) {
	if p.sessionService == nil {
		return agentRecord{}, sessionpkg.SubagentConfig{}, errors.New("session service not available")
	}
	sess, config, err := p.sessionService.CreateSubagent(ctx, sessionpkg.CreateSubagentInput{
		Thread: sessionpkg.CreateInput{
			BotID:           parent.BotID,
			Type:            sessionpkg.TypeSubagent,
			Title:           truncateTitle(task, 100),
			ParentThreadID:  parent.SessionID,
			CreatedByUserID: strings.TrimSpace(parent.UserID),
			Metadata: map[string]any{
				"agent_id":              agentID,
				"agent_control_version": agentControlVersion,
				// The pinned runtime, mirrored where a reader of the session
				// can see it: a client that lets a human talk to this subagent
				// directly has to offer the model the agent was spawned on,
				// otherwise the first direct message silently moves it onto the
				// parent bot's default. subagent_configs stays authoritative.
				"model_uuid":     runtime.UUID,
				"model_id":       runtime.ModelID,
				"model_provider": runtime.ProviderName,
			},
		},
		ModelUUID:    runtime.UUID,
		ModelID:      runtime.ModelID,
		ProviderName: runtime.ProviderName,
		Forked:       forked,
		ForkContext:  forkContext,
	})
	if err != nil {
		return agentRecord{}, sessionpkg.SubagentConfig{}, err
	}
	return agentRecord{
		AgentID:   agentID,
		SessionID: sess.ID,
		Title:     sess.Title,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
	}, config, nil
}

func (p *SpawnProvider) loadSubagentConfig(ctx context.Context, rec agentRecord) (sessionpkg.SubagentConfig, error) {
	if p.sessionService == nil {
		return sessionpkg.SubagentConfig{}, errors.New("session service not available")
	}
	config, err := p.sessionService.GetSubagentConfig(ctx, rec.SessionID)
	if err != nil {
		return sessionpkg.SubagentConfig{}, fmt.Errorf("load subagent config: %w", err)
	}
	return config, nil
}

func (p *SpawnProvider) resolveNewAgentID(ctx context.Context, session SessionContext, raw string) (string, error) {
	if strings.TrimSpace(raw) != "" {
		return normalizeAgentID(raw)
	}
	agents, err := p.listAgentRecords(ctx, session)
	if err != nil {
		return "", err
	}
	used := make(map[string]struct{}, len(agents))
	for _, rec := range agents {
		used[rec.AgentID] = struct{}{}
	}
	for i := 1; ; i++ {
		id := "agent_" + strconv.Itoa(i)
		if _, ok := used[id]; !ok {
			return id, nil
		}
	}
}

func (p *SpawnProvider) findAgent(ctx context.Context, session SessionContext, agentID string) (agentRecord, error) {
	agents, err := p.listAgentRecords(ctx, session)
	if err != nil {
		return agentRecord{}, err
	}
	for _, rec := range agents {
		if rec.AgentID == agentID {
			return rec, nil
		}
	}
	return agentRecord{}, fmt.Errorf("%w: %q in current session", errAgentNotFound, agentID)
}

func (p *SpawnProvider) listAgentRecords(ctx context.Context, session SessionContext) ([]agentRecord, error) {
	if p.sessionService == nil {
		return nil, errors.New("session service not available")
	}
	sessions, err := p.sessionService.ListSubagentsByParent(ctx, session.SessionID)
	if err != nil {
		return nil, err
	}
	records := make([]agentRecord, 0, len(sessions))
	for _, sess := range sessions {
		if sess.Type != sessionpkg.TypeSubagent {
			continue
		}
		agentID, _ := sess.Metadata["agent_id"].(string)
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			continue
		}
		records = append(records, agentRecord{
			AgentID:   agentID,
			SessionID: sess.ID,
			Title:     sess.Title,
			CreatedAt: sess.CreatedAt,
			UpdatedAt: sess.UpdatedAt,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	return records, nil
}

func (p *SpawnProvider) loadAgentForkContext(ctx context.Context, sessionID string) ([]sdk.Message, error) {
	if p.sessionService == nil {
		return nil, errors.New("session service not available")
	}
	rows, err := p.sessionService.ListSubagentForkContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]sdk.Message, 0, len(rows))
	for _, row := range rows {
		converted, ok := sdkMessageFromPersisted(messagepkg.Message{Role: row.Role, Content: row.Message})
		if !ok {
			return nil, fmt.Errorf("invalid fork context message role %q", row.Role)
		}
		messages = append(messages, converted)
	}
	return messages, nil
}

func (p *SpawnProvider) loadAgentMessages(ctx context.Context, sessionID string) []sdk.Message {
	if p.messageService == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	msgs, err := p.messageService.ListBySession(ctx, sessionID)
	if err != nil {
		p.logger.Warn("load subagent messages failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil
	}
	out := make([]sdk.Message, 0, len(msgs))
	for _, msg := range msgs {
		if converted, ok := sdkMessageFromPersisted(msg); ok {
			out = append(out, converted)
		}
	}
	return out
}

func sdkMessageFromPersisted(msg messagepkg.Message) (sdk.Message, bool) {
	var full sdk.Message
	if err := json.Unmarshal(msg.Content, &full); err == nil && (full.Role != "" || len(full.Content) > 0) {
		if full.Role == "" {
			full.Role = sdk.MessageRole(msg.Role)
		}
		return full, true
	}

	var envelope struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(msg.Content, &envelope); err == nil {
		role := envelope.Role
		if role == "" {
			role = msg.Role
		}
		var text string
		if err := json.Unmarshal(envelope.Content, &text); err == nil {
			return sdk.Message{
				Role:    sdk.MessageRole(role),
				Content: []sdk.MessagePart{sdk.TextPart{Text: text}},
			}, true
		}
		wrapped, _ := json.Marshal(struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}{Role: role, Content: envelope.Content})
		if err := json.Unmarshal(wrapped, &full); err == nil {
			return full, true
		}
	}

	var text string
	if err := json.Unmarshal(msg.Content, &text); err == nil {
		return sdk.Message{
			Role:    sdk.MessageRole(msg.Role),
			Content: []sdk.MessagePart{sdk.TextPart{Text: text}},
		}, true
	}
	return sdk.Message{}, false
}

func dropLatestMatchingUserMessage(messages []sdk.Message, query string) []sdk.Message {
	needle := strings.TrimSpace(query)
	if needle == "" || len(messages) == 0 {
		return messages
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != sdk.MessageRoleUser {
			continue
		}
		if strings.TrimSpace(messageTextContent(msg)) != needle {
			continue
		}
		out := make([]sdk.Message, 0, len(messages)-1)
		out = append(out, messages[:i]...)
		out = append(out, messages[i+1:]...)
		return out
	}
	return messages
}

func messageTextContent(msg sdk.Message) string {
	var b strings.Builder
	for _, part := range msg.Content {
		if text, ok := part.(sdk.TextPart); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func validateParentSession(session SessionContext) error {
	if strings.TrimSpace(session.BotID) == "" {
		return errors.New("bot_id is required")
	}
	if strings.TrimSpace(session.SessionID) == "" {
		return errors.New("session_id is required")
	}
	return nil
}

func normalizeAgentID(raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if id == "" {
		return "", errors.New("id is required")
	}
	if !agentIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid agent id %q: expected lowercase slug matching %s", raw, agentIDPattern.String())
	}
	return id, nil
}

func agentResultMap(res agentRunResult) map[string]any {
	out := map[string]any{
		"agent_id":   res.AgentID,
		"session_id": res.SessionID,
		"task_id":    res.TaskID,
		"status":     res.Status,
	}
	if res.ModelID != "" {
		out["model_id"] = res.ModelID
	}
	if res.Provider != "" {
		out["provider"] = res.Provider
	}
	out["fork"] = res.Fork
	if res.Message != "" {
		out["message"] = res.Message
	}
	if res.Text != "" {
		out["text"] = res.Text
	}
	if res.Error != "" {
		out["error"] = res.Error
	}
	if res.QueuePosition > 0 {
		out["queue_position"] = res.QueuePosition
	}
	if res.QueueRemaining > 0 {
		out["queue_remaining"] = res.QueueRemaining
	}
	if res.TimedOut {
		out["timed_out"] = true
	}
	return out
}

func (*SpawnProvider) startSpawnProgress(ctx context.Context, session SessionContext) {
	emitter := session.Emitter
	if emitter == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(spawnProgressInterval)
		defer ticker.Stop()
		runSpawnProgress(ctx, emitter, ticker.C)
	}()
}

func runSpawnProgress(ctx context.Context, emitter StreamEmitter, ticks <-chan time.Time) {
	if emitter == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if ctx.Err() != nil {
				return
			}
			emitter(ToolStreamEvent{Type: StreamEventSpawnProgress})
		}
	}
}

func isRetryableSubagentError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	if strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "rate_limit") {
		return true
	}
	if err429Pattern.MatchString(errStr) || serverErrPattern.MatchString(errStr) {
		return true
	}
	if errEOFPattern.MatchString(errStr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (p *SpawnProvider) persistMessages(
	ctx context.Context,
	req *agentRequest,
	result *SpawnResult,
	includeUser bool,
) {
	if includeUser {
		id, ok := p.persistUserMessage(ctx, req)
		if ok {
			req.requestMessageID = id
		}
	}

	lastAssistantIdx := -1
	for i, msg := range result.Messages {
		if msg.Role == sdk.MessageRoleAssistant {
			lastAssistantIdx = i
		}
	}

	for i, msg := range result.Messages {
		if msg.Role == sdk.MessageRoleUser {
			continue
		}
		content, err := historyfrag.MarshalStoredSDKMessage(msg)
		if err != nil {
			continue
		}
		var usage json.RawMessage
		if msg.Usage != nil {
			usage, _ = json.Marshal(msg.Usage)
		}
		var metadata map[string]any
		if i == lastAssistantIdx && result.ContextLifecycle != nil {
			metadata = map[string]any{
				contextfrag.MetadataContextLifecycleKey: *result.ContextLifecycle,
			}
		}
		persisted, err := p.messageService.Persist(ctx, messagepkg.PersistInput{
			BotID:     req.parentSession.BotID,
			SessionID: req.agentSessionID,
			Role:      string(msg.Role),
			Content:   content,
			Metadata:  metadata,
			Usage:     usage,
			ModelID:   req.runtime.UUID,
			// Bind to the task's request row so the whole task is one history
			// turn; the run id is traceability, exactly like the chat path.
			RunID:                strings.TrimSpace(req.admission.RunID),
			TurnRequestMessageID: strings.TrimSpace(req.requestMessageID),
		})
		if err != nil {
			p.logger.Warn("persist subagent message failed", slog.Any("error", err))
			continue
		}
		if i == lastAssistantIdx && result.ContextLifecycle != nil {
			result.ContextLifecycle.AssistantMessageID = persisted.ID
		}
	}
}

// persistUserMessage files the task message as the admitted turn's request row
// (SR-TURN-001): history and the live runtime view then agree on which turn
// this run writes into. It returns the persisted message id so assistant and
// tool rows can bind to it.
func (p *SpawnProvider) persistUserMessage(ctx context.Context, req *agentRequest) (string, bool) {
	if p.messageService == nil || strings.TrimSpace(req.agentSessionID) == "" {
		return "", false
	}
	userContent, _ := historyfrag.MarshalStoredSDKMessage(sdk.UserMessage(req.message))
	input := messagepkg.PersistInput{
		BotID:     req.parentSession.BotID,
		SessionID: req.agentSessionID,
		Role:      "user",
		Content:   userContent,
		RunID:     strings.TrimSpace(req.admission.RunID),
	}
	if turnID := strings.TrimSpace(req.admission.TurnID); turnID != "" {
		position := req.admission.TurnPosition
		input.TurnID = turnID
		input.TurnPosition = &position
	}
	persisted, err := p.messageService.Persist(ctx, input)
	if err != nil {
		p.logger.Warn("persist subagent user message failed", slog.Any("error", err))
		return "", false
	}
	return persisted.ID, true
}

func (p *SpawnProvider) listModelCatalog(ctx context.Context) ([]subagentModelCatalogItem, error) {
	if p.models == nil || p.queries == nil {
		return nil, errors.New("model catalog services not configured")
	}
	modelList, err := p.models.ListEnabledByType(ctx, models.ModelTypeChat)
	if err != nil {
		return nil, err
	}
	items := make([]subagentModelCatalogItem, 0, len(modelList))
	for _, model := range modelList {
		provider, fetchErr := models.FetchProviderByID(ctx, p.queries, model.ProviderID)
		if fetchErr != nil {
			return nil, fetchErr
		}
		description := ""
		if model.Config.Description != nil {
			description = strings.TrimSpace(*model.Config.Description)
		}
		items = append(items, subagentModelCatalogItem{
			UUID:         model.ID,
			ModelID:      model.ModelID,
			ProviderName: provider.Name,
			Description:  description,
			EffortLevels: append([]string(nil), model.Config.ReasoningEfforts...),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ProviderName != items[j].ProviderName {
			return items[i].ProviderName < items[j].ProviderName
		}
		if items[i].ModelID != items[j].ModelID {
			return items[i].ModelID < items[j].ModelID
		}
		return items[i].UUID < items[j].UUID
	})
	return items, nil
}

func appendModelCatalogToSpawnDescription(base string, catalog []subagentModelCatalogItem, session SessionContext) string {
	if len(catalog) == 0 {
		return base + " No enabled chat models are currently available."
	}
	lines := make([]string, 0, len(catalog)+2)
	lines = append(lines, base, "Enabled subagent models (model_id | provider | description):")
	for _, item := range catalog {
		marker := ""
		if item.UUID == session.CurrentModelUUID {
			marker = " [current]"
		}
		description := strings.Join(strings.Fields(item.Description), " ")
		if description == "" {
			description = "No description"
		}
		lines = append(lines, fmt.Sprintf("- %s | %s | %s%s", item.ModelID, item.ProviderName, description, marker))
	}
	lines = append(lines, "Use list_models for the same catalog as structured data.")
	return strings.Join(lines, "\n")
}

func (p *SpawnProvider) resolveModel(
	ctx context.Context,
	session SessionContext,
	modelUUID string,
	requestedModelID string,
	requestedProvider string,
) (resolvedSubagentModel, error) {
	if p.models == nil || p.queries == nil {
		return resolvedSubagentModel{}, errors.New("model resolution services not configured")
	}
	modelUUID = strings.TrimSpace(modelUUID)
	requestedModelID = strings.TrimSpace(requestedModelID)
	requestedProvider = strings.TrimSpace(requestedProvider)

	var modelInfo models.GetResponse
	var err error
	switch {
	case modelUUID != "":
		modelInfo, err = p.models.GetByID(ctx, modelUUID)
		if err != nil {
			return resolvedSubagentModel{}, fmt.Errorf("pinned model %s (%s) is unavailable: %w", requestedModelID, requestedProvider, err)
		}
	case requestedModelID != "":
		catalog, catalogErr := p.listModelCatalog(ctx)
		if catalogErr != nil {
			return resolvedSubagentModel{}, catalogErr
		}
		matches := make([]subagentModelCatalogItem, 0, 1)
		for _, item := range catalog {
			if item.ModelID == requestedModelID && (requestedProvider == "" || item.ProviderName == requestedProvider) {
				matches = append(matches, item)
			}
		}
		if len(matches) == 0 {
			if requestedProvider != "" {
				return resolvedSubagentModel{}, fmt.Errorf("enabled chat model %q was not found for provider %q", requestedModelID, requestedProvider)
			}
			return resolvedSubagentModel{}, fmt.Errorf("enabled chat model %q was not found", requestedModelID)
		}
		if len(matches) > 1 {
			providers := make([]string, 0, len(matches))
			for _, match := range matches {
				providers = append(providers, match.ProviderName)
			}
			return resolvedSubagentModel{}, fmt.Errorf("model_id %q is ambiguous; specify provider as one of: %s", requestedModelID, strings.Join(providers, ", "))
		}
		modelInfo, err = p.models.GetByID(ctx, matches[0].UUID)
	default:
		defaultModelUUID := strings.TrimSpace(session.CurrentModelUUID)
		if defaultModelUUID == "" {
			if p.settings == nil {
				return resolvedSubagentModel{}, errors.New("no current model and bot settings service is not configured")
			}
			botSettings, settingsErr := p.settings.GetBot(ctx, session.BotID)
			if settingsErr != nil {
				return resolvedSubagentModel{}, settingsErr
			}
			defaultModelUUID = strings.TrimSpace(botSettings.ChatModelID)
		}
		if defaultModelUUID == "" {
			return resolvedSubagentModel{}, errors.New("no current or default chat model is configured")
		}
		modelInfo, err = p.models.GetByID(ctx, defaultModelUUID)
	}
	if err != nil {
		return resolvedSubagentModel{}, err
	}
	if modelInfo.Type != models.ModelTypeChat {
		return resolvedSubagentModel{}, fmt.Errorf("model %s is not a chat model", modelInfo.ModelID)
	}
	if !modelInfo.Enable || (modelInfo.Config.CatalogAvailable != nil && !*modelInfo.Config.CatalogAvailable) {
		return resolvedSubagentModel{}, fmt.Errorf("subagent chat model %s is disabled or unavailable", modelInfo.ModelID)
	}
	provider, err := models.FetchProviderByID(ctx, p.queries, modelInfo.ProviderID)
	if err != nil {
		return resolvedSubagentModel{}, err
	}
	if !provider.Enable {
		return resolvedSubagentModel{}, fmt.Errorf("subagent model provider %s is disabled", provider.Name)
	}
	if requestedProvider != "" && provider.Name != requestedProvider {
		return resolvedSubagentModel{}, fmt.Errorf("pinned model provider changed from %q to %q", requestedProvider, provider.Name)
	}
	if requestedModelID != "" && modelUUID != "" && modelInfo.ModelID != requestedModelID {
		return resolvedSubagentModel{}, fmt.Errorf("pinned model id changed from %q to %q", requestedModelID, modelInfo.ModelID)
	}
	authResolver := providers.NewService(nil, p.queries, "")
	authCtx := oauthctx.WithUserID(ctx, strings.TrimSpace(session.UserID))
	creds, err := authResolver.ResolveModelCredentials(authCtx, provider)
	if err != nil {
		return resolvedSubagentModel{}, err
	}
	baseURL := providers.ProviderConfigString(provider, "base_url")
	chatCompletionsCompat := models.ResolveChatCompletionsCompat(
		baseURL,
		providers.ProviderConfigString(provider, models.ChatCompletionsCompatConfigKey),
	)
	reasoningConfig, err := p.resolveSubagentReasoning(ctx, session, modelInfo, provider.ClientType)
	if err != nil {
		return resolvedSubagentModel{}, fmt.Errorf("resolve subagent reasoning: %w", err)
	}
	contextWindow := modelInfo.Config.ContextBudgetMaxTokens()
	if contextWindow <= 0 {
		contextWindow = session.ContextBudgetMaxTokens
	}

	sdkModel := models.NewSDKChatModel(models.SDKModelConfig{
		ModelID:               modelInfo.ModelID,
		ClientType:            provider.ClientType,
		APIKey:                creds.APIKey,
		CodexAccountID:        creds.CodexAccountID,
		BaseURL:               baseURL,
		ChatCompletionsCompat: chatCompletionsCompat,
		ReasoningConfig:       reasoningConfig,
		ReasoningDialect:      modelInfo.Config.ReasoningDialect,
		ReasoningOffSupport:   modelInfo.Config.ReasoningOffSupport,
		ReasoningDefaultOn:    modelInfo.Config.ReasoningDefaultOn,
		ThinkingBudgetMin:     modelInfo.Config.ThinkingBudgetMin,
		ThinkingBudgetMax:     modelInfo.Config.ThinkingBudgetMax,
		ContextWindow:         contextWindow,
	})
	return resolvedSubagentModel{
		Model:                  sdkModel,
		ReasoningConfig:        reasoningConfig,
		UUID:                   modelInfo.ID,
		ModelID:                modelInfo.ModelID,
		ProviderName:           provider.Name,
		PromptCacheTTL:         providers.ProviderConfigString(provider, "prompt_cache_ttl"),
		ChatCompletionsCompat:  chatCompletionsCompat,
		SupportsImageInput:     modelInfo.HasCompatibility(models.CompatVision),
		SupportsToolCall:       modelInfo.HasCompatibility(models.CompatToolCall),
		ContextBudgetMaxTokens: contextWindow,
	}, nil
}

func truncateTitle(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-3]) + "..."
}

// resolveSubagentReasoning resolves the thinking decision for a subagent against
// the model the subagent will actually run, using the bot's stored effort as the
// on/off source. It deliberately does not inherit the parent's resolved config: a
// subagent may run a different model, and a tier the parent's model advertises
// may not exist on this one.
//
// A missing settings service retains the old nil-config fallback for direct or
// test-only callers that do not carry the parent inputs. When a requested value
// arrives without the stored fallback (as it does across the ACP tool bridge), a
// configured settings service still loads stored: the child model may reject the
// requested tier or Off and ResolveConfig must then fall back to the bot setting.
// Read failures are returned instead of silently changing provider-wire behavior.
func (p *SpawnProvider) resolveSubagentReasoning(
	ctx context.Context,
	session SessionContext,
	modelInfo models.GetResponse,
	clientType string,
) (*models.ReasoningConfig, error) {
	stored := strings.TrimSpace(session.ReasoningStoredEffort)
	requested := strings.TrimSpace(session.ReasoningRequestedEffort)
	if stored == "" {
		if p.settings == nil {
			if requested == "" {
				return nil, nil
			}
		} else {
			botSettings, err := p.settings.GetBot(ctx, session.BotID)
			if err != nil {
				return nil, fmt.Errorf("load bot reasoning settings: %w", err)
			}
			stored = botSettings.ReasoningEffort
		}
	}
	return reasoning.ResolveConfig(
		modelInfo.ResolveThinkingMode(),
		modelInfo.Config.ReasoningEfforts,
		modelInfo.ReasoningOptions(clientType),
		stored,
		requested,
		clientType,
	), nil
}
