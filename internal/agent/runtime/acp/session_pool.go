// Package acp manages long-lived Agent Control Protocol runtimes.
//
// Architecture note: this is an in-memory runtime pool for a single server
// instance only. A runtime is an OS process plus protocol state; it is
// identified by a server-generated runtime ID and optionally *bound* to one
// chat session. Sessions live in the database and survive restarts; runtimes
// do not - after a restart the next prompt simply cold-starts a fresh
// runtime. "First-class" here means code abstraction and lifecycle ownership,
// not persistence.
package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	toolapproval "github.com/memohai/memoh/internal/agent/decision/approval"
	"github.com/memohai/memoh/internal/agent/decision/feedback"
	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	"github.com/memohai/memoh/internal/agent/event"
	"github.com/memohai/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
	"github.com/memohai/memoh/internal/agent/sessionmode"
	"github.com/memohai/memoh/internal/agentcredential"
	"github.com/memohai/memoh/internal/bots"
	"github.com/memohai/memoh/internal/mcp"
	"github.com/memohai/memoh/internal/runtimefence"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

const (
	// boundRuntimeIdleTimeout reaps runtimes attached to a session after
	// prolonged inactivity.
	boundRuntimeIdleTimeout = 30 * time.Minute
	// unboundRuntimeIdleTimeout reaps runtimes that were created for the
	// pre-session model picker but never bound to a session.
	unboundRuntimeIdleTimeout = 5 * time.Minute
	// maxUnboundRuntimesPerBot bounds pre-session runtimes per bot so a
	// single caller cannot spawn unbounded agent processes.
	maxUnboundRuntimesPerBot = 4

	runtimeIDPrefix = "rt_"
)

var (
	// ErrRuntimeNotFound reports that no runtime with the given ID is owned
	// by the calling bot. Cross-bot references intentionally behave exactly
	// like missing runtimes: no side effects, no existence leak.
	ErrRuntimeNotFound = errors.New("ACP runtime not found")
	// ErrRuntimeBindRejected reports that a runtime cannot be bound to the
	// session (already bound, still starting, closed, or agent/project
	// mismatch). Callers should fall back to a cold start.
	ErrRuntimeBindRejected = errors.New("ACP runtime cannot be bound to this session")
	// ErrTooManyRuntimes reports that the per-bot budget for unbound
	// runtimes is exhausted and every slot is busy.
	ErrTooManyRuntimes = errors.New("too many unbound ACP runtimes for this bot")
	// ErrRuntimeConfigUpdateFailed reports a transport or protocol failure
	// while applying the model/reasoning values requested for a turn.
	ErrRuntimeConfigUpdateFailed = errors.New("ACP runtime configuration update failed")
)

const (
	stateStarting = "starting"
	stateIdle     = "idle"
	stateActive   = "active"
	stateClosed   = "closed"
)

// SessionPool owns every ACP runtime in the process. Runtimes are keyed by a
// server-generated runtime ID; bySession is a secondary index from a bound
// chat session to its runtime.
//
// Lock order: handle.op serializes operations on one runtime (prompt, model,
// bind, close). handle.state is the innermost leaf guarding the mutable
// snapshot fields. p.mu guards the maps; it may be held while taking
// handle.state (budget scans), but handle.state is never held while taking
// p.mu, and p.mu is never held while taking handle.op.
type SessionPool struct {
	logger      *slog.Logger
	runner      sessionRunner
	bots        botGetter
	store       SessionDescriptorReader
	tools       *mcp.ToolGatewayService
	contexts    *mcp.ToolSessionContextStore
	approval    client.ToolApprovalService
	userInput   pendingUserInputCanceller
	credentials *agentcredential.Service
	timeout     time.Duration

	adapterMu                  sync.Mutex
	adapterStates              map[string]*adapterUpgradeState
	dynamicAdapterStartTimeout time.Duration

	mu        sync.RWMutex
	runtimes  map[string]*runtimeHandle
	bySession map[string]string
}

type sessionRunner interface {
	WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error)
	StartSession(ctx context.Context, req client.StartRequest, sink client.EventSink) (*client.Session, error)
}

type workspaceClientRunner interface {
	MCPClient(ctx context.Context, botID string) (*bridge.Client, error)
}

type pendingUserInputCanceller interface {
	CancelPendingForSession(context.Context, string, string, string) ([]userinput.Request, error)
}

type botGetter interface {
	Get(ctx context.Context, botID string) (bots.Bot, error)
}

// SessionDescriptor contains the minimal persisted session metadata required
// to launch an ACP runtime. The Chat domain supplies it through an adapter.
type SessionDescriptor struct {
	BotID           string
	SessionType     string
	Metadata        map[string]any
	RuntimeMetadata map[string]any
	IsACP           bool
}

// SessionDescriptorReader resolves runtime metadata without exposing Chat
// domain types to the ACP runtime.
type SessionDescriptorReader interface {
	Get(ctx context.Context, sessionID string) (SessionDescriptor, error)
}

// runtimeHandle is the single owner of one agent process. All internal code
// operates on handles resolved through the pool's tenancy gate - never on
// bare string IDs - so cleanup can only ever touch the runtime it resolved.
type runtimeHandle struct {
	// Stable identity, fixed at creation.
	id                    string
	toolToken             string
	botID                 string
	agentID               string
	projectPath           string
	runtimeOwnerAccountID string
	credentialID          string
	credentialVersion     int64

	// op serializes operations (start, prompt, runtime config, bind, close).
	op sync.Mutex

	// state guards the mutable snapshot below. Leaf lock: never acquire
	// other locks while holding it.
	state                    sync.Mutex
	session                  *client.Session
	status                   string
	lastActive               time.Time
	boundSession             string
	defaultModelID           string
	active                   *client.ToolSessionContext
	persistenceFence         runtimefence.Fence
	startCancel              context.CancelFunc
	closed                   bool
	hadPrompt                bool
	decisionPreCleanupOnce   sync.Once
	decisionFinalCleanupOnce sync.Once
}

// PromptInput carries one prompt (or runtime control call) for a chat
// session. Session metadata (agent, project path) is resolved from the
// session store when available.
type PromptInput struct {
	BotID                    string
	ChatID                   string
	SessionID                string
	RunID                    string
	SessionType              string
	RouteID                  string
	AgentID                  string
	CredentialID             string
	ProjectPath              string
	ModelID                  string
	ReasoningEffort          string
	Prompt                   string
	Images                   []client.PromptImage
	AttachmentReferences     []string
	CanFallbackImagesToFiles bool
	ChannelIdentityID        string
	// SessionToken is consumed only by Prompt, where it flows into the
	// per-prompt tool context overlay. Ensure and SetModel ignore it.
	SessionToken          string //nolint:gosec // runtime session credential, not a hardcoded secret.
	CurrentPlatform       string
	ReplyTarget           string
	ConversationType      string
	CanRequestUserInput   bool
	SupportsImageInput    bool
	ToolOutputLimit       client.ToolOutputLimit
	ToolHTTPURL           string
	ContextURI            string
	ContextMarkdown       string
	RuntimeOwnerAccountID string
	ForceFreshRuntime     bool
	Sink                  client.EventSink
	RuntimeGuard          func(context.Context) error
}

// CreateRuntimeInput describes a pre-session runtime creation request.
type CreateRuntimeInput struct {
	BotID                 string
	AgentID               string
	CredentialID          string
	ProjectPath           string
	RuntimeOwnerAccountID string
	ToolHTTPURL           string
	Sink                  client.EventSink
}

// RuntimeStatus describes the live state of a pooled ACP runtime as exposed
// over the HTTP API.
type RuntimeStatus struct {
	RuntimeID             string                 `json:"runtime_id,omitempty"`
	SessionID             string                 `json:"session_id,omitempty"`
	AgentID               string                 `json:"agent_id,omitempty"`
	CredentialID          string                 `json:"agent_credential_id,omitempty"`
	ProjectPath           string                 `json:"project_path,omitempty"`
	RuntimeOwnerAccountID string                 `json:"-"`
	State                 string                 `json:"state"`
	ACPSession            string                 `json:"acp_session_id,omitempty"`
	Models                *client.ModelState     `json:"models,omitempty"`
	Reasoning             *client.ReasoningState `json:"reasoning,omitempty"`
	DefaultModelID        string                 `json:"default_model_id,omitempty"`
} // @name acpagent.RuntimeStatus

func NewSessionPool(log *slog.Logger, runner *client.Runner, botService *bots.Service, sessionServices ...SessionDescriptorReader) *SessionPool {
	var sessionService SessionDescriptorReader
	if len(sessionServices) > 0 {
		sessionService = sessionServices[0]
	}
	return newSessionPool(log, runner, botService, sessionService)
}

func newSessionPool(log *slog.Logger, runner sessionRunner, botService botGetter, sessionServices ...SessionDescriptorReader) *SessionPool {
	if log == nil {
		log = slog.Default()
	}
	var sessionService SessionDescriptorReader
	if len(sessionServices) > 0 {
		sessionService = sessionServices[0]
	}
	return &SessionPool{
		logger:    log.With(slog.String("service", "acp_session_pool")),
		runner:    runner,
		bots:      botService,
		store:     sessionService,
		timeout:   boundRuntimeIdleTimeout,
		runtimes:  map[string]*runtimeHandle{},
		bySession: map[string]string{},
	}
}

func (p *SessionPool) SetToolGateway(gateway *mcp.ToolGatewayService) {
	if p != nil {
		p.tools = gateway
	}
}

func (p *SessionPool) SetToolSessionContextStore(store *mcp.ToolSessionContextStore) {
	if p != nil {
		p.contexts = store
	}
}

func (p *SessionPool) SetToolApprovalService(service client.ToolApprovalService) {
	if p != nil {
		p.approval = service
	}
}

func (p *SessionPool) SetUserInputService(service pendingUserInputCanceller) {
	if p != nil {
		p.userInput = service
	}
}

func (p *SessionPool) SetCredentialService(service *agentcredential.Service) {
	if p != nil {
		p.credentials = service
	}
}

func newRuntimeID() string {
	return runtimeIDPrefix + uuid.NewString()
}

func newRuntimeToolToken() string {
	return uuid.NewString()
}

// owned is the single tenancy gate: every runtime-scoped operation resolves
// through here, and a cross-bot reference behaves exactly like a missing
// runtime - zero side effects.
func (p *SessionPool) owned(botID, runtimeID string) (*runtimeHandle, error) {
	botID = strings.TrimSpace(botID)
	runtimeID = strings.TrimSpace(runtimeID)
	if p == nil || botID == "" || runtimeID == "" {
		return nil, ErrRuntimeNotFound
	}
	p.mu.RLock()
	h := p.runtimes[runtimeID]
	p.mu.RUnlock()
	if h == nil || h.botID != botID {
		return nil, ErrRuntimeNotFound
	}
	return h, nil
}

// CreateRuntime starts an unbound runtime for the pre-session model picker.
// The runtime ID is server-generated; clients can never choose it.
func (p *SessionPool) CreateRuntime(ctx context.Context, input CreateRuntimeInput) (RuntimeStatus, error) {
	if p == nil || p.runner == nil || p.bots == nil {
		return RuntimeStatus{}, errors.New("ACP session pool is not configured")
	}
	botID := strings.TrimSpace(input.BotID)
	if botID == "" {
		return RuntimeStatus{}, errors.New("bot_id is required")
	}
	agentID := acpprofile.NormalizeAgentID(input.AgentID)
	if agentID == "" {
		agentID = acpprofile.AgentCodexID
	}
	projectPath := strings.TrimSpace(input.ProjectPath)
	runtimeOwnerAccountID := strings.TrimSpace(input.RuntimeOwnerAccountID)
	if runtimeOwnerAccountID == "" {
		return RuntimeStatus{}, runtimeOwnerMissingError()
	}

	p.reapIdle(time.Now()) //nolint:contextcheck // reaper close uses its own background ctx.

	h := &runtimeHandle{
		id:                    newRuntimeID(),
		toolToken:             newRuntimeToolToken(),
		botID:                 botID,
		agentID:               agentID,
		credentialID:          strings.TrimSpace(input.CredentialID),
		projectPath:           projectPath,
		runtimeOwnerAccountID: runtimeOwnerAccountID,
		status:                stateStarting,
		lastActive:            time.Now(),
	}
	p.mu.Lock()
	victims, err := p.unboundBudgetLocked(botID)
	if err != nil {
		p.mu.Unlock()
		return RuntimeStatus{}, err
	}
	p.runtimes[h.id] = h
	p.mu.Unlock()
	for _, victim := range victims {
		p.logger.Info("evicting oldest unbound ACP runtime",
			slog.String("runtime_id", victim.id), slog.String("bot_id", botID))
		p.tryCloseIdle(victim, 0) //nolint:contextcheck // lifecycle close uses background ctx.
	}

	h.op.Lock()
	err = p.startRuntime(ctx, h, startOptions{
		ToolHTTPURL: input.ToolHTTPURL,
		Sink:        input.Sink,
	})
	h.op.Unlock()
	if err != nil {
		return RuntimeStatus{}, err
	}
	return p.statusOf(h), nil
}

// unboundBudgetLocked enforces the per-bot unbound runtime budget. Must be
// called with p.mu held. Returns the idle victims to evict (closed by the
// caller outside the lock); errors when the budget is full and every slot is
// busy starting or serving a request.
func (p *SessionPool) unboundBudgetLocked(botID string) ([]*runtimeHandle, error) {
	count := 0
	var oldest *runtimeHandle
	var oldestActive time.Time
	for _, h := range p.runtimes {
		if h == nil || h.botID != botID {
			continue
		}
		h.state.Lock()
		unbound := h.boundSession == "" && !h.closed
		idle := h.status == stateIdle
		last := h.lastActive
		h.state.Unlock()
		if !unbound {
			continue
		}
		count++
		if !idle {
			continue
		}
		if oldest == nil || last.Before(oldestActive) {
			oldest, oldestActive = h, last
		}
	}
	if count < maxUnboundRuntimesPerBot {
		return nil, nil
	}
	if oldest == nil {
		return nil, fmt.Errorf("%w (limit %d)", ErrTooManyRuntimes, maxUnboundRuntimesPerBot)
	}
	return []*runtimeHandle{oldest}, nil
}

// BindRuntime attaches an unbound runtime to a freshly created chat session.
// After binding, the runtime uses the normal (bound) idle timeout and the
// session's prompts reuse the warm process. Returns ErrRuntimeBindRejected
// when the runtime cannot serve this session; callers fall back to a cold
// start and must not treat that as fatal.
func (p *SessionPool) BindRuntime(botID, runtimeID, sessionID, agentID, projectPath, runtimeOwnerAccountID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session_id is required")
	}
	runtimeOwnerAccountID = strings.TrimSpace(runtimeOwnerAccountID)
	if runtimeOwnerAccountID == "" {
		return ErrRuntimeBindRejected
	}
	h, err := p.owned(botID, runtimeID)
	if err != nil {
		return err
	}
	normalizedAgent := acpprofile.NormalizeAgentID(agentID)
	if normalizedAgent == "" {
		normalizedAgent = acpprofile.AgentCodexID
	}
	projectPath = strings.TrimSpace(projectPath)

	// Waits out an in-flight model change on the runtime.
	h.op.Lock()
	defer h.op.Unlock()

	h.state.Lock()
	ok := !h.closed && h.session != nil && h.boundSession == "" &&
		h.agentID == normalizedAgent && h.projectPath == projectPath &&
		h.runtimeOwnerAccountID == runtimeOwnerAccountID
	h.state.Unlock()
	if !ok {
		return ErrRuntimeBindRejected
	}

	p.mu.Lock()
	if existing, taken := p.bySession[sessionID]; taken && existing != h.id {
		p.mu.Unlock()
		return ErrRuntimeBindRejected
	}
	p.bySession[sessionID] = h.id
	p.mu.Unlock()

	h.state.Lock()
	h.boundSession = sessionID
	h.lastActive = time.Now()
	h.state.Unlock()
	return nil
}

// RuntimeStatusByID reports the live state of an owned runtime.
func (p *SessionPool) RuntimeStatusByID(botID, runtimeID string) (RuntimeStatus, error) {
	h, err := p.owned(botID, runtimeID)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return p.statusOf(h), nil
}

// SetRuntimeModel switches the runtime's model. An empty modelID resets the
// runtime to the agent default captured at startup.
func (p *SessionPool) SetRuntimeModel(ctx context.Context, botID, runtimeID, modelID string) (RuntimeStatus, error) {
	h, err := p.owned(botID, runtimeID)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return p.setModelOnHandle(ctx, h, modelID)
}

// SetRuntimeReasoning switches the runtime's reasoning effort.
func (p *SessionPool) SetRuntimeReasoning(ctx context.Context, botID, runtimeID, effort string) (RuntimeStatus, error) {
	h, err := p.owned(botID, runtimeID)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return p.setReasoningOnHandle(ctx, h, effort)
}

func (p *SessionPool) setModelOnHandle(ctx context.Context, h *runtimeHandle, modelID string) (RuntimeStatus, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		h.state.Lock()
		modelID = h.defaultModelID
		h.state.Unlock()
	}
	if modelID == "" {
		// Reset requested but the agent never reported a default; nothing to do.
		return p.statusOf(h), nil
	}
	return p.updateConfigOnHandle(ctx, h,
		func(sess *client.Session) bool {
			return strings.TrimSpace(sess.ModelState().CurrentModelID) == modelID
		},
		func(ctx context.Context, sess *client.Session) error {
			_, err := sess.SetModel(ctx, modelID)
			return err
		},
	)
}

func (p *SessionPool) setReasoningOnHandle(ctx context.Context, h *runtimeHandle, effort string) (RuntimeStatus, error) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return RuntimeStatus{}, client.ErrReasoningEffortRequired
	}
	return p.updateConfigOnHandle(ctx, h,
		func(sess *client.Session) bool {
			return strings.TrimSpace(sess.ReasoningState().CurrentEffort) == effort
		},
		func(ctx context.Context, sess *client.Session) error {
			_, err := sess.SetReasoningEffort(ctx, effort)
			return err
		},
	)
}

func (p *SessionPool) updateConfigOnHandle(
	ctx context.Context,
	h *runtimeHandle,
	matches func(*client.Session) bool,
	update func(context.Context, *client.Session) error,
) (RuntimeStatus, error) {
	h.op.Lock()
	defer h.op.Unlock()

	h.state.Lock()
	sess := h.session
	closed := h.closed
	h.state.Unlock()
	if closed || sess == nil {
		return RuntimeStatus{}, ErrRuntimeNotFound
	}
	if matches(sess) {
		return p.statusOf(h), nil
	}

	h.setStatus(stateActive)
	err := update(ctx, sess)
	if err == nil {
		h.setStatus(stateIdle)
		// Build the response before releasing h.op. Otherwise a concurrent
		// setter can win the lock and make this request return its state.
		return p.statusOf(h), nil
	}
	if isPromptConfigSelectionError(err) {
		// A stale or invalid selection did not mutate the runtime. Keep it alive
		// so the user can choose one of the newly advertised values.
		h.setStatus(stateIdle)
		return RuntimeStatus{}, err
	}
	if configUpdateCanceled(ctx, err) {
		// Caller cancellation says nothing about the health of the Agent process.
		// Public HTTP setters detach request cancellation before reaching this
		// fallback, so keep the healthy process for any other direct caller.
		h.setStatus(stateIdle)
		return RuntimeStatus{}, err
	}
	// If the setter failed at the transport/protocol layer, the Agent may have
	// accepted the value even though Memoh never received its new config
	// snapshot. The cached state is no longer trustworthy, so rebuild rather
	// than allowing the per-turn equality check to skip a required setter.
	_ = p.teardown(h) //nolint:contextcheck // lifecycle close uses background ctx.
	return RuntimeStatus{}, fmt.Errorf("%w: %w", ErrRuntimeConfigUpdateFailed, err)
}

func configUpdateCanceled(ctx context.Context, err error) bool {
	return ctx.Err() != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// CloseRuntime tears down an owned runtime, waiting out any in-flight
// operation first.
func (p *SessionPool) CloseRuntime(botID, runtimeID string) error {
	h, err := p.owned(botID, runtimeID)
	if err != nil {
		return err
	}
	return p.closeHandle(h) //nolint:contextcheck // lifecycle close uses background ctx.
}

// ResolveRuntimeToolContext resolves the trusted MCP tool context for a
// runtime referenced by its stable ID (for example from baked process
// headers). Fails closed: dead or foreign runtimes resolve to nothing.
func (p *SessionPool) ResolveRuntimeToolContext(botID, runtimeID, toolToken string) (mcp.ToolSessionContext, bool) {
	h, err := p.owned(botID, runtimeID)
	if err != nil {
		return mcp.ToolSessionContext{}, false
	}
	if strings.TrimSpace(h.toolToken) == "" || strings.TrimSpace(toolToken) != h.toolToken {
		return mcp.ToolSessionContext{}, false
	}
	h.state.Lock()
	closed := h.closed
	h.state.Unlock()
	if closed {
		return mcp.ToolSessionContext{}, false
	}
	return h.toolContext(), true
}

// prepareInput validates pool wiring and required input fields, returning
// the input with session metadata applied.
func (p *SessionPool) prepareInput(ctx context.Context, input PromptInput) (PromptInput, error) {
	if p == nil || p.runner == nil || p.bots == nil {
		return PromptInput{}, errors.New("ACP session pool is not configured")
	}
	if strings.TrimSpace(input.SessionID) == "" {
		return PromptInput{}, errors.New("session_id is required")
	}
	resolved, err := p.resolveSessionMetadata(ctx, input)
	if err != nil {
		return PromptInput{}, err
	}
	if strings.TrimSpace(resolved.BotID) == "" {
		return PromptInput{}, errors.New("bot_id is required")
	}
	if _, _, _, _, _, err := p.resolveAgentSetup(ctx, resolved.BotID, resolved.AgentID); err != nil {
		return PromptInput{}, err
	}
	return resolved, nil
}

// Prompt sends a prompt to the runtime bound to input.SessionID, cold
// starting (and binding) one when the session has no live runtime.
//
//nolint:contextcheck // lifecycle close intentionally uses background ctx.
func (p *SessionPool) Prompt(ctx context.Context, input PromptInput) (client.PromptResult, error) {
	input, err := p.prepareInput(ctx, input)
	if err != nil {
		return client.PromptResult{}, err
	}
	input.Images, err = client.NormalizePromptImages(input.Images)
	if err != nil {
		return client.PromptResult{}, err
	}
	hasAttachmentContext := len(input.AttachmentReferences) > 0 && len(promptResources(input)) > 0
	if strings.TrimSpace(input.Prompt) == "" && len(input.Images) == 0 && !hasAttachmentContext {
		return client.PromptResult{}, client.ErrPromptRequired
	}

	p.reapIdle(time.Now())
	if input.ForceFreshRuntime {
		_ = p.CloseSession(input.SessionID) //nolint:contextcheck // lifecycle close uses background ctx.
		input.ForceFreshRuntime = false
	}
	// A handle can be torn down between resolution and use (reaper, agent
	// change, a concurrent failed prompt); retry resolution a bounded number
	// of times instead of failing the user's message.
	for attempt := 0; attempt < 3; attempt++ {
		h, err := p.runtimeForSession(ctx, input)
		if err != nil {
			return client.PromptResult{}, err
		}
		result, retry, err := p.promptOnHandle(ctx, h, input)
		if retry {
			continue
		}
		return result, err
	}
	return client.PromptResult{}, errors.New("ACP runtime is restarting, retry the prompt")
}

func (p *SessionPool) promptOnHandle(ctx context.Context, h *runtimeHandle, input PromptInput) (client.PromptResult, bool, error) {
	h.op.Lock()
	defer h.op.Unlock()

	h.state.Lock()
	if h.closed || h.session == nil {
		h.state.Unlock()
		return client.PromptResult{}, true, nil
	}
	sess := h.session
	h.status = stateActive
	h.lastActive = time.Now()
	toolCtx := toolSessionContext(ctx, input, h)
	h.active = &toolCtx
	h.hadPrompt = true
	if toolCtx.RuntimeFence.Valid() {
		h.persistenceFence = toolCtx.RuntimeFence
	}
	h.state.Unlock()
	// Cleanup is defer-based so error paths can never leave a stale
	// per-prompt context or sink behind.
	defer h.clearActive()

	if err := applyPromptConfig(ctx, sess, input); err != nil {
		if isPromptConfigSelectionError(err) {
			return client.PromptResult{}, false, err
		}
		if configUpdateCanceled(ctx, err) {
			// The turn was aborted (or the client dropped) while the per-turn
			// setter was in flight. Cancellation says nothing about the Agent
			// process health; keep the runtime and let the next turn's apply
			// reconverge on the desired values.
			return client.PromptResult{}, false, err
		}
		// A transport/protocol failure while mutating session config leaves the
		// agent's effective state unknown. Drop the runtime so the next turn
		// starts from a clean session.
		_ = p.teardown(h) //nolint:contextcheck // lifecycle close uses background ctx.
		return client.PromptResult{}, false, fmt.Errorf("%w: %w", ErrRuntimeConfigUpdateFailed, err)
	}

	toolSink := newPromptToolEventSink(input.Sink, input.ToolOutputLimit)
	unregisterToolSink := p.registerToolEventSink(input, toolSink)
	defer unregisterToolSink()

	resources := promptResources(input)
	options := client.PromptOptions{
		ToolOutputLimit:   input.ToolOutputLimit,
		Images:            input.Images,
		AllowResourceOnly: len(input.AttachmentReferences) > 0 && len(resources) > 0,
	}
	result, err := sess.PromptWithToolContextOptions(ctx, input.Prompt, resources, toolCtx, options, toolSink)
	if errors.Is(err, client.ErrImagePromptUnsupported) && len(options.Images) > 0 && input.CanFallbackImagesToFiles {
		options.Images = nil
		result, err = sess.PromptWithToolContextOptions(ctx, input.Prompt, resources, toolCtx, options, toolSink)
	}
	toolSink.ApplyToResult(&result)
	if err != nil {
		if errors.Is(err, client.ErrImagePromptUnsupported) ||
			errors.Is(err, client.ErrInvalidPromptImage) ||
			errors.Is(err, client.ErrPromptRequired) {
			return result, false, err
		}
		// Prompt failures usually indicate the ACP process is in a bad state
		// (transport hang, agent crash); drop the runtime so the next call
		// starts fresh.
		_ = p.teardown(h) //nolint:contextcheck // lifecycle close uses background ctx.
		return result, false, err
	}
	return result, false, nil
}

// applyPromptConfig applies the per-turn composer selection while the caller
// holds the runtime operation lock. Model must be applied first because the
// authoritative response can replace the available reasoning options.
func applyPromptConfig(ctx context.Context, sess *client.Session, input PromptInput) error {
	desiredModel := strings.TrimSpace(input.ModelID)
	if desiredModel != "" && strings.TrimSpace(sess.ModelState().CurrentModelID) != desiredModel {
		if _, err := sess.SetModel(ctx, desiredModel); err != nil {
			return err
		}
	}

	desiredReasoning := strings.TrimSpace(input.ReasoningEffort)
	if desiredReasoning != "" && strings.TrimSpace(sess.ReasoningState().CurrentEffort) != desiredReasoning {
		if _, err := sess.SetReasoningEffort(ctx, desiredReasoning); err != nil {
			return err
		}
	}
	return nil
}

func isPromptConfigSelectionError(err error) bool {
	return errors.Is(err, client.ErrModelIDRequired) ||
		errors.Is(err, client.ErrModelSelectionUnsupported) ||
		errors.Is(err, client.ErrModelUnavailable) ||
		errors.Is(err, client.ErrReasoningEffortRequired) ||
		errors.Is(err, client.ErrReasoningSelectionUnsupported) ||
		errors.Is(err, client.ErrReasoningEffortUnavailable)
}

// Ensure starts (or reuses) the runtime for a session without prompting it.
//
//nolint:contextcheck // lifecycle close intentionally uses background ctx.
func (p *SessionPool) Ensure(ctx context.Context, input PromptInput) (RuntimeStatus, error) {
	input, err := p.prepareInput(ctx, input)
	if err != nil {
		return RuntimeStatus{}, err
	}
	p.reapIdle(time.Now())
	h, err := p.runtimeForSession(ctx, input)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return p.statusOf(h), nil
}

// SetModel switches the model of the runtime bound to a session, cold
// starting one when needed.
//
//nolint:contextcheck // lifecycle close intentionally uses background ctx.
func (p *SessionPool) SetModel(ctx context.Context, input PromptInput, modelID string) (RuntimeStatus, error) {
	if strings.TrimSpace(modelID) == "" {
		return RuntimeStatus{}, client.ErrModelIDRequired
	}
	input, err := p.prepareInput(ctx, input)
	if err != nil {
		return RuntimeStatus{}, err
	}
	p.reapIdle(time.Now())
	h, err := p.runtimeForSession(ctx, input)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return p.setModelOnHandle(ctx, h, modelID)
}

// SetReasoning switches the reasoning effort of the runtime bound to a
// session, cold starting one when needed.
//
//nolint:contextcheck // lifecycle close intentionally uses background ctx.
func (p *SessionPool) SetReasoning(ctx context.Context, input PromptInput, effort string) (RuntimeStatus, error) {
	if strings.TrimSpace(effort) == "" {
		return RuntimeStatus{}, client.ErrReasoningEffortRequired
	}
	input, err := p.prepareInput(ctx, input)
	if err != nil {
		return RuntimeStatus{}, err
	}
	p.reapIdle(time.Now())
	h, err := p.runtimeForSession(ctx, input)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return p.setReasoningOnHandle(ctx, h, effort)
}

// runtimeForSession resolves the runtime bound to a session, cold starting
// and binding a fresh one when the index misses. A bound runtime whose agent
// or project no longer matches the session metadata is replaced.
func (p *SessionPool) runtimeForSession(ctx context.Context, input PromptInput) (*runtimeHandle, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	agentID := acpprofile.NormalizeAgentID(input.AgentID)
	if agentID == "" {
		agentID = acpprofile.AgentCodexID
	}
	projectPath := strings.TrimSpace(input.ProjectPath)
	runtimeOwnerAccountID := strings.TrimSpace(input.RuntimeOwnerAccountID)
	if runtimeOwnerAccountID == "" {
		runtimeOwnerAccountID = strings.TrimSpace(input.ChannelIdentityID)
	}
	if runtimeOwnerAccountID == "" {
		return nil, runtimeOwnerMissingError()
	}

	for attempt := 0; attempt < 3; attempt++ {
		p.mu.Lock()
		var h *runtimeHandle
		if rid, ok := p.bySession[sessionID]; ok {
			h = p.runtimes[rid]
			if h == nil {
				delete(p.bySession, sessionID)
			}
		}
		if h == nil {
			// Register the starting handle and the session index atomically
			// so a concurrent caller waits on this start instead of racing a
			// second one.
			h = &runtimeHandle{
				id:                    newRuntimeID(),
				toolToken:             newRuntimeToolToken(),
				botID:                 input.BotID,
				agentID:               agentID,
				credentialID:          strings.TrimSpace(input.CredentialID),
				projectPath:           projectPath,
				runtimeOwnerAccountID: runtimeOwnerAccountID,
				status:                stateStarting,
				lastActive:            time.Now(),
				boundSession:          sessionID,
			}
			p.runtimes[h.id] = h
			p.bySession[sessionID] = h.id
			p.mu.Unlock()

			h.op.Lock()
			err := p.startRuntime(ctx, h, startOptions{
				ToolHTTPURL: input.ToolHTTPURL,
				Sink:        input.Sink,
			})
			h.op.Unlock()
			if err != nil {
				return nil, err
			}
			return h, nil
		}
		p.mu.Unlock()

		if h.botID != input.BotID {
			// resolveSessionMetadata already pins the session to the calling
			// bot, so this is purely defensive - and side-effect free.
			return nil, ErrRuntimeNotFound
		}
		h.state.Lock()
		credentialMatches := strings.TrimSpace(input.CredentialID) == "" || h.credentialID == strings.TrimSpace(input.CredentialID)
		matches := h.agentID == agentID && h.projectPath == projectPath && h.runtimeOwnerAccountID == runtimeOwnerAccountID && credentialMatches
		closed := h.closed
		if matches && !closed {
			// Resolving counts as activity: a session whose UI keeps the
			// runtime ensured (without prompting) must not be idle-reaped.
			h.lastActive = time.Now()
		}
		h.state.Unlock()
		if matches && !closed {
			return h, nil
		}
		// Agent or project changed for this session: replace the runtime.
		_ = p.closeHandle(h) //nolint:contextcheck // lifecycle close uses background ctx.
	}
	return nil, errors.New("ACP runtime is restarting, retry the request")
}

type startOptions struct {
	ToolHTTPURL string
	Sink        client.EventSink
}

// startRuntime boots the agent process for a registered handle. Must be
// called with h.op held. On failure the handle is fully torn down (process,
// maps, context) before returning.
//
//nolint:contextcheck // startup failure cleanup intentionally uses background ctx.
func (p *SessionPool) startRuntime(ctx context.Context, h *runtimeHandle, opts startOptions) error {
	startCtx, cancelStart := context.WithCancel(ctx)
	defer cancelStart()
	h.state.Lock()
	if h.closed {
		h.state.Unlock()
		return errors.New("ACP runtime was closed during startup")
	}
	h.startCancel = cancelStart
	h.state.Unlock()

	fail := func(err error) error {
		// The HTTP layer returns a short, redacted acp_runtime_start_failed body,
		// so the underlying cause must be recorded here or it is lost entirely.
		if err != nil {
			p.logger.Warn("ACP runtime start failed",
				slog.String("bot_id", h.botID),
				slog.String("agent_id", h.agentID),
				slog.String("runtime_id", h.id),
				slog.String("session_id", h.boundSession),
				slog.Any("error", err))
		}
		_ = p.teardown(h)
		return err
	}

	_, profile, setup, mode, workspaceInfo, err := p.resolveAgentSetup(startCtx, h.botID, h.agentID)
	if err != nil {
		return fail(err)
	}
	var codexOAuth *client.CodexOAuthCredentials
	var resolvedCredential agentcredential.ResolvedCredential
	if mode != client.SetupModeSelf && p.credentials != nil {
		if strings.TrimSpace(h.credentialID) == "" {
			resolvedCredential, err = p.credentials.ResolveDefault(startCtx, h.botID, h.agentID)
		} else {
			resolvedCredential, err = p.credentials.Resolve(startCtx, h.botID, h.agentID, h.credentialID)
		}
		if err != nil {
			return fail(err)
		}
		setup, mode, codexOAuth, err = applyAgentCredential(profile, setup, resolvedCredential)
		if err != nil {
			return fail(err)
		}
		h.state.Lock()
		h.credentialID = resolvedCredential.ID
		h.credentialVersion = resolvedCredential.CredentialVersion
		h.state.Unlock()
	}
	resolved, err := client.ResolveSessionContext(client.SessionContextInput{
		RuntimeID:   h.id,
		AgentID:     h.agentID,
		SetupMode:   mode,
		Backend:     workspaceInfo.Backend,
		ProjectPath: h.projectPath,
	})
	if err != nil {
		return fail(fmt.Errorf("resolve ACP session context: %w", err))
	}
	if err := p.reconcileManagedACPConfig(startCtx, h.botID, profile, setup, mode, resolved, codexOAuth); err != nil {
		return fail(fmt.Errorf("prepare %s managed config: %w", profile.DisplayName, err))
	}
	// Managed env (Claude Code BYOK tokens) is injected for every session.
	// managedProcessEnv returns nil for self mode and for Codex, which is
	// configured via CODEX_HOME files instead of env.
	var env []string
	env, err = managedProcessEnv(profile, setup.Managed, mode)
	if err != nil {
		return fail(err)
	}
	cleanEnv, unsetEnv := managedEnvControls(profile, mode, resolved.Backend)

	toolHTTPURL, err := p.resolveToolHTTPURL(opts.ToolHTTPURL, workspaceInfo)
	if err != nil {
		return fail(err)
	}
	startReq := client.StartRequest{
		RuntimeID:              h.id,
		AgentID:                h.agentID,
		BotID:                  h.botID,
		ProjectPath:            h.projectPath,
		Command:                profile.Command,
		Args:                   profile.Args,
		Env:                    env,
		CleanEnv:               cleanEnv,
		UnsetEnv:               unsetEnv,
		Resolved:               &resolved,
		SetupMode:              mode,
		SessionMode:            profile.SessionModeID,
		SessionConfigValues:    profile.SessionConfigValues,
		ReasoningConfigID:      profile.ReasoningConfigID,
		DefaultReasoningEffort: profile.DefaultReasoningEffort,
		Timeout:                0,
		ToolHTTPURL:            toolHTTPURL,
		// The handler resolves identity from the handle per request, so the
		// process configuration only ever carries stable runtime identity.
		ToolHTTPHandler: p.toolHTTPHandler(h),
		ToolGateway:     p.tools,
		ToolSession:     h.stableToolIdentity(),
		ToolApproval:    p.approval,
	}
	if codexOAuth != nil && p.credentials != nil {
		startReq.BeforeAuthCleanup = p.codexOAuthWriteback(h, resolved, resolvedCredential)
	}

	var sess *client.Session
	sess, err = p.startDynamicAdapter(startCtx, profile, workspaceInfo, startReq, opts.Sink)
	if err != nil {
		if startCtx.Err() != nil {
			return fail(err)
		}
		p.logger.Warn("dynamic ACP adapter unavailable; falling back to bundled version",
			slog.String("bot_id", h.botID),
			slog.String("agent_id", h.agentID),
			slog.String("runtime_id", h.id),
			slog.Any("error", err))
	}
	if sess == nil {
		sess, err = p.runner.StartSession(startCtx, startReq, opts.Sink)
	}
	if err != nil {
		return fail(err)
	}

	h.state.Lock()
	if h.closed {
		h.state.Unlock()
		if closeErr := sess.Close(); closeErr != nil {
			p.logger.Warn("failed to close ACP session after startup cancellation",
				slog.Any("error", closeErr), slog.String("runtime_id", h.id))
		}
		return errors.New("ACP runtime was closed during startup")
	}
	h.session = sess
	h.status = stateIdle
	h.lastActive = time.Now()
	h.startCancel = nil
	h.defaultModelID = strings.TrimSpace(sess.ModelState().CurrentModelID)
	h.state.Unlock()
	return nil
}

func (p *SessionPool) codexOAuthWriteback(h *runtimeHandle, resolved client.ResolvedSessionContext, credential agentcredential.ResolvedCredential) func(context.Context) {
	return func(ctx context.Context) {
		runner, ok := p.runner.(workspaceClientRunner)
		if !ok || p.credentials == nil {
			return
		}
		bridgeClient, err := runner.MCPClient(ctx, h.botID)
		if err != nil {
			p.logger.Warn("resolve bridge for Codex credential writeback failed", slog.String("runtime_id", h.id), slog.Any("error", err))
			return
		}
		current, err := client.ReadCodexOAuthCredentials(ctx, bridgeClient, resolved.CodexHome)
		if err != nil {
			p.logger.Warn("read Codex credential writeback failed", slog.String("runtime_id", h.id), slog.Any("error", err))
			return
		}
		if current.AccessToken == credential.Secret["access_token"] && current.RefreshToken == credential.Secret["refresh_token"] && current.IDToken == credential.Secret["id_token"] {
			return
		}
		secret := map[string]string{"access_token": current.AccessToken, "refresh_token": current.RefreshToken, "id_token": current.IDToken, "account_id": current.AccountID}
		if _, err := p.credentials.UpdateSecretCAS(ctx, credential.ID, credential.CredentialVersion, secret, credential.AccountMetadata, credential.ExpiresAt); err != nil && !errors.Is(err, agentcredential.ErrNotFound) {
			p.logger.Warn("persist Codex credential rotation failed", slog.String("runtime_id", h.id), slog.String("credential_id", credential.ID), slog.Any("error", err))
		}
	}
}

// RuntimeStatus reports the runtime state for a session, returning an idle
// skeleton when no runtime is live.
func (p *SessionPool) RuntimeStatus(sessionID, agentID, projectPath string) RuntimeStatus {
	sessionID = strings.TrimSpace(sessionID)
	idle := RuntimeStatus{
		SessionID:   sessionID,
		AgentID:     strings.TrimSpace(agentID),
		ProjectPath: strings.TrimSpace(projectPath),
		State:       stateIdle,
	}
	if p == nil {
		return idle
	}
	h := p.sessionHandle(sessionID)
	if h == nil {
		return idle
	}
	status := p.statusOf(h)
	if status.SessionID == "" {
		status.SessionID = sessionID
	}
	return status
}

func (p *SessionPool) sessionHandle(sessionID string) *runtimeHandle {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	rid, ok := p.bySession[sessionID]
	if !ok {
		return nil
	}
	return p.runtimes[rid]
}

func (*SessionPool) statusOf(h *runtimeHandle) RuntimeStatus {
	h.state.Lock()
	sess := h.session
	status := RuntimeStatus{
		RuntimeID:             h.id,
		SessionID:             h.boundSession,
		AgentID:               h.agentID,
		CredentialID:          h.credentialID,
		ProjectPath:           h.projectPath,
		RuntimeOwnerAccountID: h.runtimeOwnerAccountID,
		State:                 h.status,
		DefaultModelID:        h.defaultModelID,
	}
	h.state.Unlock()
	switch status.State {
	case stateStarting:
		status.State = stateActive
	case stateClosed, "":
		status.State = stateIdle
	}
	if sess != nil {
		status.ACPSession = sess.ID()
		modelState, reasoningState := sess.ConfigurationState()
		status.Models = &modelState
		status.Reasoning = &reasoningState
	}
	return status
}

// IsSessionActive reports whether the session's runtime is currently serving
// an operation.
func (p *SessionPool) IsSessionActive(sessionID string) bool {
	if p == nil {
		return false
	}
	h := p.sessionHandle(sessionID)
	if h == nil {
		return false
	}
	if !h.op.TryLock() {
		return true
	}
	h.op.Unlock()
	h.state.Lock()
	active := h.status == stateActive
	h.state.Unlock()
	return active
}

func (p *SessionPool) StartReaper(ctx context.Context) {
	if p == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.reapIdle(time.Now()) //nolint:contextcheck // reaper close uses its own background ctx.
			case <-ctx.Done():
				return
			}
		}
	}()
}

// CloseSession tears down the runtime bound to a session (used when the
// session is deleted or its agent changes). Session IDs reaching this path
// are database-validated by the caller.
//
//nolint:contextcheck // lifecycle close intentionally uses background ctx so cleanup runs after caller cancels.
func (p *SessionPool) CloseSession(sessionID string) error {
	if p == nil {
		return nil
	}
	h := p.sessionHandle(sessionID)
	if h == nil {
		return nil
	}
	return p.closeHandle(h)
}

// closeHandle destroys the runtime. It first marks the handle closed and
// cancels any active prompt/start before waiting for the serialized operation
// lock, so a prompt blocked on ACP approval or user input can unwind promptly.
func (p *SessionPool) closeHandle(h *runtimeHandle) error {
	h.state.Lock()
	if !h.closed {
		h.closed = true
	}
	h.status = stateClosed
	sess := h.session
	cancel := h.startCancel
	h.startCancel = nil
	bound := h.boundSession
	activeSession := ""
	fence := h.persistenceFence
	if h.active != nil {
		activeSession = strings.TrimSpace(h.active.SessionID)
		if h.active.RuntimeFence.Valid() {
			fence = h.active.RuntimeFence
		}
		h.active = nil
	}
	h.state.Unlock()

	if cancel != nil {
		cancel()
	}
	sessionID := strings.TrimSpace(bound)
	if sessionID == "" {
		sessionID = activeSession
	}
	p.cancelHandlePendingDecisions(h, sessionID, fence, decisionCleanupPre, "decision cancelled: ACP runtime closed before a response arrived")
	var closeErr error
	if sess != nil {
		closeErr = sess.Close()
	}

	h.op.Lock()
	defer h.op.Unlock()
	if err := p.teardown(h); err != nil {
		if closeErr != nil {
			return fmt.Errorf("%w; teardown after close: %w", closeErr, err)
		}
		return err
	}
	return closeErr
}

// tryCloseIdle closes the handle only when it is idle and has been inactive
// for at least minIdle. Never blocks: a runtime that is busy (or becomes
// busy) is skipped, which is what makes it safe for the reaper and the
// eviction path.
func (p *SessionPool) tryCloseIdle(h *runtimeHandle, minIdle time.Duration) bool {
	if !h.op.TryLock() {
		return false
	}
	defer h.op.Unlock()
	h.state.Lock()
	eligible := !h.closed && h.status == stateIdle &&
		(minIdle <= 0 || time.Since(h.lastActive) > minIdle)
	h.state.Unlock()
	if !eligible {
		return false
	}
	if err := p.teardown(h); err != nil {
		p.logger.Warn("failed to close idle ACP runtime",
			slog.Any("error", err), slog.String("runtime_id", h.id))
	}
	return true
}

// teardown is the single destruction path for a runtime: it marks the handle
// closed, cancels a pending start, kills the agent process, and removes the
// handle from both pool indexes. Idempotent - and it always re-runs the map
// cleanup, because a handle can be marked closed (aborted start) before its
// registration is removed.
func (p *SessionPool) teardown(h *runtimeHandle) error {
	h.state.Lock()
	h.closed = true
	h.status = stateClosed
	sess := h.session
	h.session = nil
	cancel := h.startCancel
	h.startCancel = nil
	bound := h.boundSession
	activeSession := ""
	fence := h.persistenceFence
	if h.active != nil {
		activeSession = strings.TrimSpace(h.active.SessionID)
		if h.active.RuntimeFence.Valid() {
			fence = h.active.RuntimeFence
		}
	}
	h.active = nil
	h.state.Unlock()
	sessionID := strings.TrimSpace(bound)
	if sessionID == "" {
		sessionID = activeSession
	}
	p.cancelHandlePendingDecisions(h, sessionID, fence, decisionCleanupPre, "decision cancelled: ACP runtime closed before a response arrived")

	if cancel != nil {
		cancel()
	}
	var closeErr error
	if sess != nil {
		closeErr = sess.Close()
	}
	p.cancelHandlePendingDecisions(h, sessionID, fence, decisionCleanupFinal, "decision cancelled: ACP runtime closed before a response arrived")

	p.mu.Lock()
	delete(p.runtimes, h.id)
	if bound != "" && p.bySession[bound] == h.id {
		delete(p.bySession, bound)
	}
	p.mu.Unlock()
	return closeErr
}

type decisionCleanupPhase uint8

const (
	decisionCleanupPre decisionCleanupPhase = iota
	decisionCleanupFinal
)

func (p *SessionPool) cancelHandlePendingDecisions(h *runtimeHandle, sessionID string, fence runtimefence.Fence, phase decisionCleanupPhase, reason string) {
	if p == nil || h == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	h.state.Lock()
	hadPrompt := h.hadPrompt
	h.state.Unlock()
	if !fence.Valid() && !hadPrompt {
		return
	}
	p.mu.RLock()
	currentID := p.bySession[sessionID]
	p.mu.RUnlock()
	if currentID != "" && currentID != h.id {
		return
	}
	var once *sync.Once
	switch phase {
	case decisionCleanupPre:
		once = &h.decisionPreCleanupOnce
	case decisionCleanupFinal:
		once = &h.decisionFinalCleanupOnce
	default:
		return
	}
	once.Do(func() {
		ctx := context.Background()
		if fence.Valid() {
			ctx = runtimefence.WithContext(ctx, fence)
		}
		p.cancelPendingDecisions(ctx, h.botID, sessionID, reason)
	})
}

//nolint:contextcheck // lifecycle cleanup is detached from the closed prompt but retains its fence value.
func (p *SessionPool) cancelPendingDecisions(parent context.Context, botID, sessionID, reason string) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if p == nil || botID == "" || sessionID == "" {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	var cleanup sync.WaitGroup
	if approval, ok := p.approval.(interface {
		CancelPendingForSession(context.Context, string, string, string) ([]toolapproval.Request, error)
	}); ok {
		cleanup.Add(1)
		go func() {
			defer cleanup.Done()
			ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
			defer cancel()
			if _, err := approval.CancelPendingForSession(ctx, botID, sessionID, reason); err != nil && !errors.Is(err, runtimefence.ErrStale) {
				p.logger.Warn("cancel pending ACP approvals failed",
					slog.Any("error", err),
					slog.String("bot_id", botID),
					slog.String("session_id", sessionID))
			}
		}()
	}
	if p.userInput != nil {
		cleanup.Add(1)
		go func() {
			defer cleanup.Done()
			ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
			defer cancel()
			if _, err := p.userInput.CancelPendingForSession(ctx, botID, sessionID, reason); err != nil && !errors.Is(err, runtimefence.ErrStale) {
				p.logger.Warn("cancel pending ACP user inputs failed",
					slog.Any("error", err),
					slog.String("bot_id", botID),
					slog.String("session_id", sessionID))
			}
		}()
	}
	cleanup.Wait()
}

func (p *SessionPool) CloseAll() {
	if p == nil {
		return
	}
	p.mu.RLock()
	handles := make([]*runtimeHandle, 0, len(p.runtimes))
	for _, h := range p.runtimes {
		if h != nil {
			handles = append(handles, h)
		}
	}
	p.mu.RUnlock()
	for _, h := range handles {
		// Shutdown must not wait for in-flight prompts: teardown directly,
		// the op holder unwinds via the closed flag and its erroring session.
		if err := p.teardown(h); err != nil {
			p.logger.Warn("failed to close ACP runtime",
				slog.Any("error", err), slog.String("runtime_id", h.id))
		}
	}
}

func (p *SessionPool) CloseBotAgentRuntimes(botID, agentID string) error {
	if p == nil {
		return nil
	}
	botID = strings.TrimSpace(botID)
	agentID = acpprofile.NormalizeAgentID(agentID)
	if botID == "" {
		return nil
	}
	p.mu.RLock()
	handles := make([]*runtimeHandle, 0)
	for _, h := range p.runtimes {
		if h == nil || h.botID != botID {
			continue
		}
		if agentID != "" && h.agentID != agentID {
			continue
		}
		handles = append(handles, h)
	}
	p.mu.RUnlock()

	var firstErr error
	for _, h := range handles {
		// Bot metadata updates must not wait for an active prompt that may itself
		// be waiting on user input or tool approval. Closing the session directly
		// cancels the in-flight prompt and lets its op holder unwind.
		if err := p.teardown(h); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (p *SessionPool) reapIdle(now time.Time) int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	handles := make([]*runtimeHandle, 0, len(p.runtimes))
	for _, h := range p.runtimes {
		if h != nil {
			handles = append(handles, h)
		}
	}
	p.mu.RUnlock()

	reaped := 0
	for _, h := range handles {
		h.state.Lock()
		limit := unboundRuntimeIdleTimeout
		if h.boundSession != "" {
			limit = p.timeout
		}
		stale := !h.closed && h.status == stateIdle && !h.lastActive.IsZero() &&
			limit > 0 && now.Sub(h.lastActive) > limit
		h.state.Unlock()
		if !stale {
			continue
		}
		if p.tryCloseIdle(h, limit) {
			reaped++
		}
	}
	return reaped
}

func (p *SessionPool) resolveSessionMetadata(ctx context.Context, input PromptInput) (PromptInput, error) {
	if p == nil || p.store == nil {
		return input, nil
	}
	sess, err := p.store.Get(ctx, input.SessionID)
	if err != nil {
		return input, fmt.Errorf("load ACP session metadata: %w", err)
	}
	if !sess.IsACP {
		return input, fmt.Errorf("session %s is not an ACP agent session", input.SessionID)
	}
	if input.BotID != "" && sess.BotID != "" && input.BotID != sess.BotID {
		return input, fmt.Errorf("session %s does not belong to bot %s", input.SessionID, input.BotID)
	}
	if input.BotID == "" {
		input.BotID = sess.BotID
	}
	input.SessionType = sess.SessionType
	if sess.Metadata == nil {
		sess.Metadata = map[string]any{}
	}
	runtimeMeta := sess.RuntimeMetadata
	if len(runtimeMeta) > 0 {
		for _, key := range []string{"acp_agent_id", "project_path", "acp_project_mode", "runtime_owner_account_id", "agent_credential_id"} {
			if value, ok := runtimeMeta[key]; ok {
				sess.Metadata[key] = value
			}
		}
	}
	if agentID := metadataString(sess.Metadata, "acp_agent_id"); agentID != "" {
		input.AgentID = agentID
	}
	if projectPath := metadataString(sess.Metadata, "project_path"); projectPath != "" {
		input.ProjectPath = projectPath
	}
	if ownerID := metadataString(sess.Metadata, "runtime_owner_account_id"); ownerID != "" {
		input.RuntimeOwnerAccountID = ownerID
	}
	if credentialID := metadataString(sess.Metadata, "agent_credential_id"); credentialID != "" {
		input.CredentialID = credentialID
	}
	return input, nil
}

func (p *SessionPool) resolveAgentSetup(ctx context.Context, botID, agentID string) (bots.Bot, acpprofile.Profile, acpprofile.AgentSetup, client.SetupMode, bridge.WorkspaceInfo, error) {
	agentID = acpprofile.NormalizeAgentID(agentID)
	profile, ok := acpprofile.Lookup(agentID)
	if !ok {
		return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, feedback.New(
			feedback.CodeAgentNotFound,
			"unknown_agent",
			http.StatusBadRequest,
			"chat.acp.agentNotFound",
			fmt.Sprintf("Unknown ACP agent %q", agentID),
			map[string]string{"agent_id": agentID},
		)
	}
	bot, err := p.bots.Get(ctx, botID)
	if err != nil {
		return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, fmt.Errorf("load bot ACP setup: %w", err)
	}
	setup := acpprofile.ParseAgentSetup(bot.Metadata, agentID)
	if !setup.Enabled {
		return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, feedback.New(
			feedback.CodeAgentNotEnabled,
			"agent_not_enabled",
			http.StatusForbidden,
			"chat.acp.agentNotEnabled",
			fmt.Sprintf("ACP agent %q is not enabled for this bot", agentID),
			map[string]string{"agent_id": agentID},
		)
	}
	workspaceInfo, err := p.runner.WorkspaceInfo(ctx, botID)
	if err != nil {
		return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, fmt.Errorf("resolve workspace: %w", err)
	}
	mode := client.SetupMode(setup.Mode)
	if !setup.ModeSet {
		// Legacy bots created before setup_mode was introduced default to
		// api_key to preserve the original validation behaviour.
		mode = client.SetupModeAPIKey
	}
	if !profileSupportsSetupMode(profile, mode) {
		reason := fmt.Sprintf("does not support setup mode %q", mode)
		return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, feedback.New(
			feedback.CodeAgentNotConfigured,
			reason,
			http.StatusBadRequest,
			"chat.acp.agentNotConfigured",
			fmt.Sprintf("%s %s", profile.DisplayName, reason),
			map[string]string{"agent_id": agentID, "setup_mode": string(mode)},
		)
	}
	if !profileSupportsBackend(profile, workspaceInfo.Backend) {
		reason := fmt.Sprintf("does not support workspace backend %q", workspaceInfo.Backend)
		return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, feedback.New(
			feedback.CodeAgentNotConfigured,
			reason,
			http.StatusBadRequest,
			"chat.acp.agentNotConfigured",
			fmt.Sprintf("%s %s", profile.DisplayName, reason),
			map[string]string{"agent_id": agentID, "workspace_backend": workspaceInfo.Backend},
		)
	}
	if mode != client.SetupModeSelf {
		if err := validateManagedFields(profile, setup.Managed, mode); err != nil {
			return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, feedback.New(
				feedback.CodeAgentNotConfigured,
				"missing_managed_field",
				http.StatusBadRequest,
				"chat.acp.agentNotConfigured",
				err.Error(),
				map[string]string{"agent_id": agentID},
			)
		}
	}
	return bot, profile, setup, mode, workspaceInfo, nil
}

func validateManagedFields(profile acpprofile.Profile, managed map[string]string, mode client.SetupMode) error {
	field, missing := acpprofile.MissingRequiredManagedField(profile, acpprofile.AgentSetup{
		AgentID: profile.ID,
		Enabled: true,
		Mode:    string(mode),
		ModeSet: true,
		Managed: managed,
	})
	if !missing {
		return nil
	}
	id := strings.TrimSpace(field.ID)
	if id == "" {
		id = "managed field"
	}
	return fmt.Errorf("%s required", id)
}

// stableToolIdentity is the only identity baked into the agent process
// configuration (MCP HTTP headers): the runtime ID never changes for the
// life of the process, so binding the runtime to a session later requires no
// re-configuration.
func (h *runtimeHandle) stableToolIdentity() client.ToolSessionContext {
	return client.ToolSessionContext{
		BotID:        h.botID,
		ChatID:       h.botID,
		RuntimeID:    h.id,
		RuntimeToken: h.toolToken,
		SessionType:  sessionmode.ACPAgent,
	}
}

// toolContext resolves the trusted MCP tool context for the runtime: stable
// identity plus, while a prompt is running, the per-prompt fields (stream,
// token, chat, reply target...).
func (h *runtimeHandle) toolContext() mcp.ToolSessionContext {
	h.state.Lock()
	defer h.state.Unlock()
	ctx := mcp.ToolSessionContext{
		BotID:            h.botID,
		ChatID:           h.botID,
		RuntimeID:        h.id,
		SessionID:        h.boundSession,
		SessionType:      sessionmode.ACPAgent,
		CanListUserInput: true,
	}
	if h.active == nil {
		return ctx
	}
	ctx.RuntimeActive = true
	overlay := func(dst *string, value string) {
		if value = strings.TrimSpace(value); value != "" {
			*dst = value
		}
	}
	overlay(&ctx.ChatID, h.active.ChatID)
	overlay(&ctx.SessionID, h.active.SessionID)
	overlay(&ctx.RunID, h.active.RunID)
	overlay(&ctx.SessionType, h.active.SessionType)
	overlay(&ctx.RouteID, h.active.RouteID)
	overlay(&ctx.ChannelIdentityID, h.active.ChannelIdentityID)
	overlay(&ctx.SessionToken, h.active.SessionToken)
	overlay(&ctx.CurrentPlatform, h.active.CurrentPlatform)
	overlay(&ctx.ReplyTarget, h.active.ReplyTarget)
	overlay(&ctx.ConversationType, h.active.ConversationType)
	if h.active.CanRequestUserInput {
		ctx.CanRequestUserInput = true
	}
	if h.active.SupportsImageInput {
		ctx.SupportsImageInput = true
	}
	if h.active.RuntimeFence.Valid() {
		ctx.RuntimeFence = h.active.RuntimeFence
	}
	ctx.RunContext = h.active.RunContext
	ctx.RuntimeGuard = h.active.RuntimeGuard
	return ctx
}

func (h *runtimeHandle) clearActive() {
	h.state.Lock()
	h.active = nil
	if !h.closed {
		h.status = stateIdle
	}
	h.lastActive = time.Now()
	h.state.Unlock()
}

func (h *runtimeHandle) setStatus(status string) {
	h.state.Lock()
	if !h.closed {
		h.status = status
	}
	h.lastActive = time.Now()
	h.state.Unlock()
}

func toolSessionContext(ctx context.Context, input PromptInput, h *runtimeHandle) client.ToolSessionContext {
	fence, _ := runtimefence.FromContext(ctx)
	return client.ToolSessionContext{
		BotID:               h.botID,
		ChatID:              firstNonEmpty(input.ChatID, h.botID),
		RuntimeID:           h.id,
		SessionID:           strings.TrimSpace(input.SessionID),
		RunID:               strings.TrimSpace(input.RunID),
		SessionType:         firstNonEmpty(input.SessionType, sessionmode.ACPAgent),
		RouteID:             input.RouteID,
		ChannelIdentityID:   input.ChannelIdentityID,
		SessionToken:        input.SessionToken,
		CurrentPlatform:     input.CurrentPlatform,
		ReplyTarget:         input.ReplyTarget,
		ConversationType:    input.ConversationType,
		CanRequestUserInput: input.CanRequestUserInput,
		IsSubagent:          false,
		SupportsImageInput:  input.SupportsImageInput,
		RuntimeFence:        fence,
		RunContext:          ctx,
		RuntimeGuard:        input.RuntimeGuard,
	}
}

func promptResources(input PromptInput) []client.PromptResource {
	markdown := strings.TrimSpace(input.ContextMarkdown)
	if markdown == "" {
		return nil
	}
	uri := strings.TrimSpace(input.ContextURI)
	if uri == "" {
		uri = "memoh://context/current-turn"
	}
	return []client.PromptResource{{
		URI:      uri,
		MimeType: "text/markdown",
		Text:     markdown,
	}}
}

func (p *SessionPool) registerToolEventSink(input PromptInput, sink *promptToolEventSink) func() {
	if p == nil || p.contexts == nil || sink == nil {
		return func() {}
	}
	return p.contexts.RegisterToolEventSink(client.ToolSessionContext{
		BotID:     input.BotID,
		SessionID: input.SessionID,
		RunID:     input.RunID,
	}, sink.EmitToolStreamEvent)
}

func (p *SessionPool) resolveToolHTTPURL(inputURL string, workspaceInfo bridge.WorkspaceInfo) (string, error) {
	if p == nil || p.tools == nil {
		return "", nil
	}
	backend := strings.TrimSpace(workspaceInfo.Backend)
	if backend == "" || backend == bridge.WorkspaceBackendContainer {
		return strings.TrimSpace(workspaceInfo.ACPToolsHTTPURL), nil
	}
	return strings.TrimSpace(inputURL), nil
}

// toolHTTPHandler serves the runtime's MCP tool requests. Identity comes
// from the handle (stable identity plus the live per-prompt context), never
// from request headers, so a runtime can be bound to a session after start
// without any re-configuration.
func (p *SessionPool) toolHTTPHandler(h *runtimeHandle) http.Handler {
	if p == nil || p.tools == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mcp.ServeToolMCPHTTPWithoutContextMerge(w, req, p.logger, p.tools, p.contexts, h.toolContext())
	})
}

func (p *SessionPool) reconcileManagedACPConfig(ctx context.Context, botID string, profile acpprofile.Profile, setup acpprofile.AgentSetup, mode client.SetupMode, resolved client.ResolvedSessionContext, codexOAuth *client.CodexOAuthCredentials) error {
	if mode == client.SetupModeSelf {
		return nil
	}
	runner, hasWorkspaceClient := p.runner.(workspaceClientRunner)
	if !hasWorkspaceClient {
		return nil
	}
	return client.WriteManagedACPConfig(ctx, client.ManagedACPConfigRequest{
		Profile:    profile,
		Setup:      setup,
		Mode:       mode,
		Resolved:   resolved,
		CodexOAuth: codexOAuth,
	}, func() (*bridge.Client, error) {
		return runner.MCPClient(ctx, botID)
	})
}

func applyAgentCredential(profile acpprofile.Profile, setup acpprofile.AgentSetup, credential agentcredential.ResolvedCredential) (acpprofile.AgentSetup, client.SetupMode, *client.CodexOAuthCredentials, error) {
	if setup.Managed == nil {
		setup.Managed = map[string]string{}
	}
	switch credential.AuthKind {
	case agentcredential.AuthKindOpenAIAPIKey, agentcredential.AuthKindAnthropicAPIKey, agentcredential.AuthKindGoogleAPIKey, agentcredential.AuthKindOpenRouterAPIKey:
		setup.Managed["api_key"] = credential.Secret["api_key"]
		if profile.ID == acpprofile.AgentHermesID && strings.TrimSpace(setup.Managed["provider"]) == "" {
			setup.Managed["provider"] = credential.Provider
		}
		return setup, client.SetupModeAPIKey, nil, nil
	case agentcredential.AuthKindClaudeCodeOAuth:
		setup.Managed["oauth_token"] = credential.Secret["oauth_token"]
		return setup, client.SetupModeOAuth, nil, nil
	case agentcredential.AuthKindOpenAICodexOAuth:
		expiresAt := time.Time{}
		if credential.ExpiresAt != nil {
			expiresAt = *credential.ExpiresAt
		}
		return setup, client.SetupModeOAuth, &client.CodexOAuthCredentials{
			AccessToken: credential.Secret["access_token"], IDToken: credential.Secret["id_token"],
			RefreshToken: credential.Secret["refresh_token"], AccountID: credential.Secret["account_id"],
			BaseURL: setup.Managed["base_url"], ExpiresAt: expiresAt,
		}, nil
	default:
		return setup, "", nil, agentcredential.ErrIncompatible
	}
}

func runtimeOwnerMissingError() *feedback.Error {
	return feedback.New(
		feedback.CodeRuntimeOwnerMissing,
		"missing_runtime_owner",
		http.StatusConflict,
		"chat.acp.runtimeOwnerMissing",
		"ACP runtime owner is missing; recreate or reauthorize the ACP session",
		nil,
	)
}

type promptToolEventSink struct {
	mu         sync.Mutex
	next       client.EventSink
	events     []event.StreamEvent
	transcript *client.TranscriptRecorder
	limit      client.ToolOutputLimit
}

func newPromptToolEventSink(next client.EventSink, limits ...client.ToolOutputLimit) *promptToolEventSink {
	var limit client.ToolOutputLimit
	if len(limits) > 0 {
		limit = limits[0]
	}
	return &promptToolEventSink{
		next:       next,
		transcript: client.NewTranscriptRecorder(limit),
		limit:      limit,
	}
}

func (s *promptToolEventSink) EmitStreamEvent(ev event.StreamEvent) {
	if s == nil {
		return
	}
	ev = client.LimitStreamEvent(ev, s.limit)
	s.mu.Lock()
	s.events = appendBoundedPromptEvents(s.events, ev)
	if s.transcript != nil {
		s.transcript.Add(ev)
	}
	s.mu.Unlock()
	if s.next != nil {
		s.next.EmitStreamEvent(ev)
	}
}

func (s *promptToolEventSink) EmitToolStreamEvent(toolEvent mcp.ToolStreamEvent) {
	if s == nil {
		return
	}
	if ev, ok := toolEvent.ToAgentStreamEvent(); ok {
		s.EmitStreamEvent(ev)
	}
}

func (s *promptToolEventSink) Events() []event.StreamEvent {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.StreamEvent(nil), s.events...)
}

func (s *promptToolEventSink) ApplyToResult(result *client.PromptResult) {
	if s == nil || result == nil {
		return
	}
	events := s.Events()
	if len(events) == 0 {
		return
	}
	result.Events = events
	if s.transcript != nil {
		result.Output = s.transcript.Messages(result.Text)
	}
}

func appendBoundedPromptEvents(events []event.StreamEvent, incoming ...event.StreamEvent) []event.StreamEvent {
	if len(incoming) == 0 {
		return events
	}
	events = append(events, incoming...)
	if len(events) <= maxCollectedPromptToolEvents {
		return events
	}
	return append([]event.StreamEvent(nil), events[len(events)-maxCollectedPromptToolEvents:]...)
}

const maxCollectedPromptToolEvents = 4096

func managedEnvControls(profile acpprofile.Profile, mode client.SetupMode, backend client.WorkspaceBackend) (bool, []string) {
	if profile.ID != acpprofile.AgentHermesID || mode == client.SetupModeSelf {
		return false, nil
	}
	return backend == client.WorkspaceBackendContainer, client.HermesManagedUnsetEnvKeys()
}

func profileSupportsSetupMode(profile acpprofile.Profile, mode client.SetupMode) bool {
	if len(profile.SetupModes) == 0 {
		return true
	}
	for _, supported := range profile.SetupModes {
		if strings.EqualFold(strings.TrimSpace(supported), string(mode)) {
			return true
		}
	}
	return false
}

func profileSupportsBackend(profile acpprofile.Profile, backend string) bool {
	if len(profile.SupportedBackends) == 0 {
		return true
	}
	normalized := strings.TrimSpace(backend)
	if normalized == "" {
		normalized = bridge.WorkspaceBackendContainer
	}
	for _, supported := range profile.SupportedBackends {
		if strings.EqualFold(strings.TrimSpace(supported), normalized) {
			return true
		}
	}
	return false
}

func managedProcessEnv(profile acpprofile.Profile, values map[string]string, mode client.SetupMode) ([]string, error) {
	switch profile.ID {
	case acpprofile.AgentClaudeCodeID:
		env := []string{
			"ANTHROPIC_AUTH_TOKEN=",
			"CLAUDE_CODE_USE_BEDROCK=",
			"CLAUDE_CODE_USE_VERTEX=",
			"CLAUDE_CODE_USE_FOUNDRY=",
			// Claude Code does not think unless given a budget; this is the
			// counterpart of Codex's model_reasoning_effort in config.toml so
			// managed sessions stream reasoning by default.
			"MAX_THINKING_TOKENS=16000",
		}
		switch mode {
		case client.SetupModeAPIKey:
			apiKey := strings.TrimSpace(values["api_key"])
			if apiKey == "" {
				return nil, fmt.Errorf("api_key required for %s api_key setup", profile.DisplayName)
			}
			env = append(env,
				"CLAUDE_CODE_OAUTH_TOKEN=",
				"ANTHROPIC_API_KEY="+apiKey,
			)
		case client.SetupModeOAuth:
			token := strings.TrimSpace(values["oauth_token"])
			if token == "" {
				return nil, fmt.Errorf("oauth_token required for %s oauth setup", profile.DisplayName)
			}
			env = append(env,
				"ANTHROPIC_API_KEY=",
				"CLAUDE_CODE_OAUTH_TOKEN="+token,
			)
		default:
			return nil, nil
		}
		if baseURL := strings.TrimSpace(values["base_url"]); baseURL != "" {
			env = append(env, "ANTHROPIC_BASE_URL="+baseURL)
		}
		return env, nil
	default:
		return nil, nil
	}
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
