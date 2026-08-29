// Package acp manages long-lived Agent Control Protocol runtimes.
//
// Architecture note: this is an in-memory runtime pool for a single server
// instance only. A runtime is an OS process identified by a server-generated
// runtime ID and optionally *bound* to one chat session. Processes never
// survive a server restart. For supported profiles, however, the adapter's
// native ACP session ID and JSONL files are checkpointed separately in the
// database and restored into the next process-owned runtime directory.
package acp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/decision/feedback"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
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

	// decisionQuiesceTimeout bounds how long a cancelled prompt waits for
	// in-flight permission/Form callbacks to unwind before the runtime is
	// reused; past it the runtime is recycled instead (see promptOnHandle).
	decisionQuiesceTimeout = 3 * time.Second

	runtimeIDPrefix = "rt_"
	// sessionStateIOTimeout bounds 512 MiB bridge/database streaming while
	// allowing 200-300 MiB transcripts on ordinary container storage.
	sessionStateIOTimeout = 10 * time.Minute
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
	logger         *slog.Logger
	runner         sessionRunner
	bots           botGetter
	store          SessionDescriptorReader
	stateStore     SessionStateStore
	sessionRuntime sessionRuntimeCoordinator
	tools          *mcp.ToolGatewayService
	contexts       *mcp.ToolSessionContextStore
	approval       client.ToolApprovalService
	userInput      sessionUserInputService
	timeout        time.Duration

	mu        sync.RWMutex
	runtimes  map[string]*runtimeHandle
	bySession map[string]string
	// History-reset gates linearize runtime teardown with the database clear.
	// A session may not cold-start from the old published checkpoint while its
	// canonical history is being cleared.
	historyResetSessions map[string]historyResetSessionGate
	historyResetBots     map[string]chan struct{}
}

type sessionRuntimeCoordinator interface {
	WaitForHistoryReset(ctx context.Context, botID, sessionID string) error
	BeginSessionHistoryReset(ctx context.Context, botID, sessionID string) (context.Context, func(), error)
	BeginBotHistoryReset(ctx context.Context, botID string) (context.Context, func(), error)
}

type historyResetSessionGate struct {
	botID string
	done  chan struct{}
}

type sessionRunner interface {
	WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error)
	StartSession(ctx context.Context, req client.StartRequest, sink client.EventSink) (*client.Session, error)
}

type workspaceClientRunner interface {
	MCPClient(ctx context.Context, botID string) (*bridge.Client, error)
}

type sessionUserInputService interface {
	client.UserInputService
	CancelPendingForSession(context.Context, string, string, string) ([]userinput.Request, error)
}

type botGetter interface {
	Get(ctx context.Context, botID string) (bots.Bot, error)
}

// SessionDescriptor contains the minimal persisted session metadata required
// to launch an ACP runtime. The Chat domain supplies it through an adapter.
type SessionDescriptor struct {
	BotID               string
	SessionType         string
	Metadata            map[string]any
	RuntimeMetadata     map[string]any
	WorkspaceTargetID   string
	WorkspaceTargetKind string
	WorkdirPath         string
	IsACP               bool
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
	cwd                   string
	runtimeOwnerAccountID string
	disableSessionState   bool
	sessionStateSupported bool
	sessionStateLocator   acpprofile.RuntimeSessionLocator
	sessionStateCursor    client.SessionStateCursor
	runtimeConfigEpoch    RuntimeConfigEpoch
	// ownerCtx is a value-only context retained for detached runtime cleanup.
	// Its cancellation and deadline do not describe request liveness.
	ownerCtx            context.Context
	workspaceTargetID   string
	workspaceTargetKind string
	workspaceTargetName string

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
	stagingCancel            context.CancelFunc
	closed                   bool
	hadPrompt                bool
	decisionPreCleanupOnce   sync.Once
	decisionFinalCleanupOnce sync.Once
	// decisionFallbackOnce keeps the malformed-handle report to one log line
	// even though closeHandle and teardown both reach the cleanup path.
	decisionFallbackOnce sync.Once
	closeStarted         bool
	closeDone            chan struct{}
	closeErr             error
	// nativeHead names the publication head this process's native conversation
	// corresponds to. It advances locally the moment a turn's state is staged
	// (checkpoint) or a snapshot-incapable turn completes (reset). The database
	// is the authority: before every prompt the pool compares nativeHead with
	// the durable head, and any divergence — a round that never committed,
	// another server's turn, a history clear — destroys this warm generation.
	nativeHead      SessionPublicationHead
	nativeHeadFound bool
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
	WorkspaceTargetID     string
	WorkspaceTargetKind   string
	WorkspaceTargetName   string
	ForceFreshRuntime     bool
	Sink                  client.EventSink
	RuntimeGuard          func(context.Context) error
	// RequiredCommand is the exact agent-command selector the admission layer
	// matched against a live runtime. After applying per-prompt configuration,
	// the client Session re-validates it against its latest command snapshot at
	// the dispatch boundary: a runtime replaced or updated between admission
	// and prompt must still advertise the command, or the turn fails with
	// ErrAgentCommandUnavailable instead of delivering stale slash text.
	RequiredCommand string

	disableSessionState bool
}

// ErrAgentCommandUnavailable reports that PromptInput.RequiredCommand is not
// advertised by the session that would actually receive the prompt.
var ErrAgentCommandUnavailable = client.ErrAgentCommandUnavailable

// CreateRuntimeInput describes a pre-session runtime creation request.
type CreateRuntimeInput struct {
	BotID                 string
	AgentID               string
	ProjectPath           string
	RuntimeOwnerAccountID string
	ToolHTTPURL           string
	Sink                  client.EventSink
}

// RuntimeStatus describes the live state of a pooled ACP runtime as exposed
// over the HTTP API.
type RuntimeStatus struct {
	RuntimeID             string                        `json:"runtime_id,omitempty"`
	SessionID             string                        `json:"session_id,omitempty"`
	AgentID               string                        `json:"agent_id,omitempty"`
	ProjectPath           string                        `json:"project_path,omitempty"`
	RuntimeOwnerAccountID string                        `json:"-"`
	WorkspaceTargetID     string                        `json:"-"`
	WorkspaceTargetKind   string                        `json:"-"`
	State                 string                        `json:"state"`
	ACPSession            string                        `json:"acp_session_id,omitempty"`
	Models                *client.ModelState            `json:"models,omitempty"`
	Reasoning             *client.ReasoningState        `json:"reasoning,omitempty"`
	Modes                 *client.ModeState             `json:"modes,omitempty"`
	AvailableCommands     []client.AvailableCommandInfo `json:"available_commands,omitempty"`
	DefaultModelID        string                        `json:"default_model_id,omitempty"`
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
		logger:               log.With(slog.String("service", "acp_session_pool")),
		runner:               runner,
		bots:                 botService,
		store:                sessionService,
		timeout:              boundRuntimeIdleTimeout,
		runtimes:             map[string]*runtimeHandle{},
		bySession:            map[string]string{},
		historyResetSessions: map[string]historyResetSessionGate{},
		historyResetBots:     map[string]chan struct{}{},
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

func (p *SessionPool) SetUserInputService(service sessionUserInputService) {
	if p != nil {
		p.userInput = service
	}
}

// SetSessionStateStore enables durable adapter-native ACP session checkpoints.
// It remains optional so embedders and focused pool tests do not need a
// PostgreSQL dependency.
func (p *SessionPool) SetSessionStateStore(store SessionStateStore) {
	if p != nil {
		p.stateStore = store
	}
}

// SetSessionRuntime connects the process-local ACP close boundary to the
// cross-instance reset coordinator without introducing a package cycle.
func (p *SessionPool) SetSessionRuntime(manager *sessionruntime.Manager) {
	if p == nil || manager == nil {
		return
	}
	p.sessionRuntime = manager
	manager.SetHistoryResetHandler(func(_ context.Context, scope sessionruntime.ResetScope) error {
		if scope.SessionID != "" {
			return p.CloseSession(scope.SessionID)
		}
		// The pool close API owns a detached bounded lifecycle context; the
		// routed reset context is only the acknowledgement boundary.
		//nolint:contextcheck
		return p.CloseBotAgentRuntimes(scope.BotID, "")
	})
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
	if p.sessionRuntime != nil {
		if err := p.sessionRuntime.WaitForHistoryReset(ctx, botID, ""); err != nil {
			return RuntimeStatus{}, err
		}
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
	_, _, _, _, workspaceInfo, err := p.resolveAgentSetup(ctx, botID, agentID)
	if err != nil {
		return RuntimeStatus{}, err
	}

	p.reapIdle(time.Now()) //nolint:contextcheck // reaper uses each handle's owner context.

	h := &runtimeHandle{
		id:                    newRuntimeID(),
		toolToken:             newRuntimeToolToken(),
		botID:                 botID,
		agentID:               agentID,
		projectPath:           projectPath,
		runtimeOwnerAccountID: runtimeOwnerAccountID,
		ownerCtx:              context.WithoutCancel(ctx),
		workspaceTargetID:     strings.TrimSpace(workspaceInfo.TargetID),
		workspaceTargetKind:   strings.TrimSpace(workspaceInfo.TargetKind),
		workspaceTargetName:   strings.TrimSpace(workspaceInfo.TargetName),
		status:                stateStarting,
		lastActive:            time.Now(),
	}
	var victims []*runtimeHandle
	for {
		p.mu.Lock()
		resetDone := p.historyResetBots[botID]
		if resetDone == nil {
			victims, err = p.unboundBudgetLocked(botID)
			if err == nil {
				p.runtimes[h.id] = h
			}
			p.mu.Unlock()
			if err != nil {
				return RuntimeStatus{}, err
			}
			break
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return RuntimeStatus{}, ctx.Err()
		case <-resetDone:
			// Re-enter under p.mu. The bot may have been deleted or its ACP
			// setup changed while the reset gate was held; startRuntime resolves
			// the authoritative setup only after registration is admitted.
		}
	}
	for _, victim := range victims {
		p.logger.Info("evicting oldest unbound ACP runtime",
			slog.String("runtime_id", victim.id), slog.String("bot_id", botID))
		p.tryCloseIdle(victim, 0) //nolint:contextcheck // lifecycle close uses the handle owner context.
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
func (p *SessionPool) BindRuntime(ctx context.Context, botID, runtimeID, sessionID, agentID, projectPath, workspaceTargetID, runtimeOwnerAccountID string) error {
	if ctx == nil {
		return errors.New("runtime bind context is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session_id is required")
	}
	bindTimeout := p.timeout
	if bindTimeout <= 0 {
		bindTimeout = 30 * time.Second
	}
	// Binding is part of the synchronous create-session request: cancellation
	// should stop reset/config waits. The stored owner context below detaches
	// cancellation separately so later runtime cleanup can retain its values.
	opCtx, cancel := context.WithTimeout(ctx, bindTimeout)
	defer cancel()
	if p.sessionRuntime != nil {
		if err := p.sessionRuntime.WaitForHistoryReset(opCtx, botID, sessionID); err != nil {
			return err
		}
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
	workspaceTargetID = strings.TrimSpace(workspaceTargetID)

	// Waits out an in-flight model change on the runtime.
	h.op.Lock()
	defer h.op.Unlock()
	actualEpoch, err := p.loadRuntimeConfigEpoch(opCtx, h.botID, sessionID)
	if err != nil {
		return err
	}

	h.state.Lock()
	epochMatches := h.runtimeConfigEpoch.Bot == actualEpoch.Bot
	targetMatches := workspaceTargetID == "" || h.workspaceTargetID == workspaceTargetID
	ok := !h.closed && h.session != nil && h.boundSession == "" &&
		h.agentID == normalizedAgent && h.projectPath == projectPath &&
		targetMatches && h.runtimeOwnerAccountID == runtimeOwnerAccountID &&
		epochMatches
	if ok {
		// Publish the binding on the handle before indexing it. A reset that
		// begins immediately after the p.mu admission below can then tear down
		// both the process and the correct bySession entry without observing a
		// half-bound handle.
		h.boundSession = sessionID
		h.runtimeConfigEpoch = actualEpoch
		h.ownerCtx = context.WithoutCancel(ctx)
		h.lastActive = time.Now()
	}
	h.state.Unlock()
	if !ok {
		if !epochMatches {
			_ = p.teardown(h) //nolint:contextcheck // stale unbound process must not remain reusable.
		}
		return ErrRuntimeBindRejected
	}
	revertBinding := func() {
		h.state.Lock()
		if !h.closed && h.boundSession == sessionID {
			h.boundSession = ""
		}
		h.state.Unlock()
	}

	p.mu.Lock()
	_, sessionReset := p.historyResetSessions[sessionID]
	if p.historyResetBots[h.botID] != nil || sessionReset {
		p.mu.Unlock()
		revertBinding()
		return ErrRuntimeBindRejected
	}
	if existing, taken := p.bySession[sessionID]; taken && existing != h.id {
		p.mu.Unlock()
		revertBinding()
		return ErrRuntimeBindRejected
	}
	p.bySession[sessionID] = h.id
	p.mu.Unlock()
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

// SetRuntimeMode switches the mode of an unbound runtime before its first
// chat message creates and binds a Session.
func (p *SessionPool) SetRuntimeMode(ctx context.Context, botID, runtimeID, modeID string) (RuntimeStatus, error) {
	h, err := p.owned(botID, runtimeID)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return p.setModeOnHandle(ctx, h, modeID)
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

func (p *SessionPool) setModeOnHandle(ctx context.Context, h *runtimeHandle, modeID string) (RuntimeStatus, error) {
	if strings.TrimSpace(modeID) == "" {
		return RuntimeStatus{}, client.ErrModeIDRequired
	}
	return p.updateConfigOnHandle(ctx, h,
		func(sess *client.Session) bool {
			return sess.ModeState().CurrentModeID == modeID
		},
		func(ctx context.Context, sess *client.Session) error {
			_, err := sess.SetMode(ctx, modeID)
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
	_ = p.teardown(h) //nolint:contextcheck // lifecycle close uses the handle owner context.
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
	return p.closeHandle(h) //nolint:contextcheck // lifecycle close uses the handle owner context.
}

// ResolveRuntimeToolContext resolves the trusted MCP tool context for a
// runtime referenced by its stable ID (for example from baked process
// headers). Fails closed: dead or foreign runtimes resolve to nothing.
func (p *SessionPool) ResolveRuntimeToolContext(botID, runtimeID, toolToken string) (mcp.ToolSessionContext, bool) {
	h, err := p.owned(botID, runtimeID)
	if err != nil {
		return mcp.ToolSessionContext{}, false
	}
	expectedToken := strings.TrimSpace(h.toolToken)
	providedToken := strings.TrimSpace(toolToken)
	if expectedToken == "" || subtle.ConstantTimeCompare([]byte(providedToken), []byte(expectedToken)) != 1 {
		return mcp.ToolSessionContext{}, false
	}
	h.state.Lock()
	closed := h.closed
	sess := h.session
	h.state.Unlock()
	if closed || sessionProcessExited(sess) {
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
	setupCtx := contextWithWorkspaceTarget(ctx, resolved.WorkspaceTargetID)
	_, _, _, _, workspaceInfo, err := p.resolveAgentSetup(setupCtx, resolved.BotID, resolved.AgentID)
	if err != nil {
		return PromptInput{}, err
	}
	requestedTargetID := strings.TrimSpace(resolved.WorkspaceTargetID)
	actualTargetID := strings.TrimSpace(workspaceInfo.TargetID)
	if requestedTargetID != "" && requestedTargetID != actualTargetID {
		return PromptInput{}, fmt.Errorf("resolved workspace target %q does not match requested target %q", actualTargetID, requestedTargetID)
	}
	resolved.WorkspaceTargetID = actualTargetID
	resolved.WorkspaceTargetKind = strings.TrimSpace(workspaceInfo.TargetKind)
	resolved.WorkspaceTargetName = strings.TrimSpace(workspaceInfo.TargetName)
	return resolved, nil
}

// Prompt sends a prompt to the runtime bound to input.SessionID, cold
// starting (and binding) one when the session has no live runtime.
//
//nolint:contextcheck // lifecycle close uses the handle owner context.
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
		_ = p.CloseSession(input.SessionID) //nolint:contextcheck // lifecycle close uses the handle owner context.
		// Discuss turns inject a complete bounded context into a fresh process.
		// Resuming or checkpointing that temporary native session would duplicate
		// context and could replace a normal chat checkpoint. Note the full
		// consequence: a completed fresh-runtime turn still publishes a RESET
		// head for its session (canonical history advanced past anything
		// resumable), so any prior checkpoint on the same session stops being
		// cold-resumable by design.
		input.disableSessionState = true
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
	h.state.Unlock()

	current, err := p.publicationHeadMatches(ctx, h)
	if err != nil {
		return client.PromptResult{}, false, err
	}
	if !current {
		// Another server process (or a history reset) moved the canonical head.
		// The in-memory native conversation can no longer be advanced safely.
		_ = p.teardown(h) //nolint:contextcheck // stale warm generation must be destroyed before retry.
		return client.PromptResult{}, true, nil
	}

	h.state.Lock()
	if h.closed || h.session != sess {
		h.state.Unlock()
		return client.PromptResult{}, true, nil
	}
	h.status = stateActive
	h.lastActive = time.Now()
	toolCtx := toolSessionContext(ctx, input, h)
	h.active = &toolCtx
	h.ownerCtx = context.WithoutCancel(ctx)
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
		_ = p.teardown(h) //nolint:contextcheck // lifecycle close uses the handle owner context.
		return client.PromptResult{}, false, fmt.Errorf("%w: %w", ErrRuntimeConfigUpdateFailed, err)
	}

	toolSink := newPromptToolEventSink(input.Sink, input.ToolOutputLimit)
	unregisterToolSink := p.registerToolEventSink(input, toolSink)
	defer unregisterToolSink()

	// An aborted turn triggers the ACP cancellation handshake, but the agent
	// cannot finish a cancelled prompt while one of its permission requests is
	// still blocked on a user decision. Cancelling the pending rows wakes those
	// waiters (per ACP, pending request_permission resolves with the cancelled
	// outcome once the turn is cancelled), letting the handshake complete
	// inside its grace window so the runtime survives the Stop.
	promptDone := make(chan struct{})
	defer close(promptDone)
	go func() {
		select {
		case <-ctx.Done():
		case <-promptDone:
			// SendRequest may return immediately after cancellation while an ACP
			// permission/Form callback is still unwinding. If both channels are
			// ready, select is intentionally nondeterministic; recheck the context
			// so the promptDone arm cannot skip waiter cleanup.
			if ctx.Err() == nil {
				return
			}
		}
		p.cancelPendingDecisions(context.WithoutCancel(ctx), toolCtx.BotID, toolCtx.SessionID,
			"decision cancelled: the turn was aborted before a response arrived")
	}()

	resources := promptResources(input)
	options := client.PromptOptions{
		ToolOutputLimit:   input.ToolOutputLimit,
		Images:            input.Images,
		AllowResourceOnly: len(input.AttachmentReferences) > 0 && len(resources) > 0,
		RequiredCommand:   input.RequiredCommand,
	}
	result, err := sess.PromptWithToolContextOptions(ctx, input.Prompt, resources, toolCtx, options, toolSink)
	if errors.Is(err, client.ErrImagePromptUnsupported) && len(options.Images) > 0 && input.CanFallbackImagesToFiles {
		options.Images = nil
		result, err = sess.PromptWithToolContextOptions(ctx, input.Prompt, resources, toolCtx, options, toolSink)
	}
	// Stop MCP deliveries and wait for any event already inside the store's
	// read-side critical section before taking the durable result snapshot.
	unregisterToolSink()
	toolSink.ApplyToResult(&result)
	if err != nil {
		if errors.Is(err, client.ErrImagePromptUnsupported) ||
			errors.Is(err, client.ErrInvalidPromptImage) ||
			errors.Is(err, client.ErrPromptRequired) ||
			errors.Is(err, client.ErrAgentCommandUnavailable) {
			return result, false, err
		}
		if ctx.Err() != nil {
			// The caller aborted (user Stop / turn cancel). Cancellation says
			// nothing about the Agent process health - the same principle the
			// config-apply path applies - so keep the runtime: connection.Prompt
			// already sent session/cancel to wind the turn down, and the next
			// prompt reuses the warm session instead of a cold restart that
			// would lose the agent-side conversation. Genuine transport/agent
			// failures (ctx still live) fall through to teardown below.
			//
			// Reuse is gated on quiescence: ACP dispatches permission/Form
			// callbacks on connection-scoped goroutines that survive prompt
			// cancellation, so wait for them to unwind while h.op is still
			// held. Waiting here also extends the between-turns window in
			// which a late-dispatched stale callback auto-cancels instead of
			// attaching to the next turn. A callback that outlives the grace
			// window means the runtime's unwinding state is unknown - recycle
			// it rather than hand the next turn a session with a live stale
			// callback.
			if errors.Is(err, client.ErrPromptCancellationUnconfirmed) ||
				!sess.WaitDecisionCallbacksIdle(decisionQuiesceTimeout) {
				// An unknown cancellation state can include a SendNotification
				// blocked in the connection write lock. Break the transport first;
				// graceful teardown would otherwise block trying session/close behind
				// that same writer and never reach the process close.
				_ = sess.ForceClose() //nolint:contextcheck // forced lifecycle teardown must outlive the cancelled turn.
				_ = p.teardown(h)     //nolint:contextcheck // lifecycle close uses the handle owner context.
			}
			return result, false, err
		}
		// Prompt failures usually indicate the ACP process is in a bad state
		// (transport hang, agent crash); drop the runtime so the next call
		// starts fresh.
		_ = p.teardown(h) //nolint:contextcheck // lifecycle close uses the handle owner context.
		return result, false, err
	}
	staged, persistErr := p.persistSessionState(ctx, h, sess, input.RunID, toolCtx.RuntimeFence, result.StateReceipt)
	if persistErr != nil {
		p.logger.Error("failed to stage ACP session state",
			slog.String("bot_id", h.botID),
			slog.String("session_id", h.boundSession),
			slog.String("run_id", input.RunID),
			slog.String("runtime_id", h.id),
			slog.Any("error", persistErr))
		// A successful native turn without a durable staged snapshot cannot be
		// published to canonical chat history: a cold restart would otherwise
		// resume an older native context. Drop the warm process for every staging
		// failure and return the partial result with an error so the application
		// persists an explicit failed turn rather than a false success watermark.
		_ = p.teardown(h) //nolint:contextcheck // failed checkpoint makes the warm native history unusable.
		return result, false, fmt.Errorf("stage ACP session checkpoint: %w", persistErr)
	}
	result.CheckpointStaged = staged
	// The native conversation has advanced past this run whether or not a
	// snapshot was staged. Record the head this process now corresponds to;
	// the application commits the matching durable head with the round, and
	// the pre-prompt head comparison destroys this generation if it never does.
	if p.stateStore != nil && strings.TrimSpace(h.boundSession) != "" {
		if runID, parseErr := uuid.Parse(strings.TrimSpace(input.RunID)); parseErr == nil {
			kind := SessionPublicationReset
			if staged {
				kind = SessionPublicationCheckpoint
			}
			h.state.Lock()
			h.nativeHead = SessionPublicationHead{RunID: runID.String(), Kind: kind}
			h.nativeHeadFound = true
			h.state.Unlock()
		}
	}
	return result, false, nil
}

func (p *SessionPool) publicationHeadMatches(ctx context.Context, h *runtimeHandle) (bool, error) {
	if p == nil || h == nil {
		return true, nil
	}
	h.state.Lock()
	sessionID := strings.TrimSpace(h.boundSession)
	expectedEpoch := h.runtimeConfigEpoch
	expected := h.nativeHead
	expectedFound := h.nativeHeadFound
	h.state.Unlock()
	actualEpoch, err := p.loadRuntimeConfigEpoch(ctx, h.botID, sessionID)
	if err != nil {
		return false, err
	}
	if expectedEpoch != actualEpoch {
		p.logger.Info("ACP warm runtime config epoch changed; restarting before prompt",
			slog.String("bot_id", h.botID),
			slog.String("session_id", sessionID),
			slog.String("runtime_id", h.id),
			slog.Int64("expected_bot_epoch", expectedEpoch.Bot),
			slog.Int64("actual_bot_epoch", actualEpoch.Bot),
			slog.Int64("expected_session_epoch", expectedEpoch.Session),
			slog.Int64("actual_session_epoch", actualEpoch.Session))
		return false, nil
	}
	if p.stateStore == nil {
		return true, nil
	}
	if sessionID == "" {
		return true, nil
	}
	actual, actualFound, err := p.stateStore.Head(ctx, h.botID, sessionID)
	if err != nil {
		return false, fmt.Errorf("load ACP session publication head: %w", err)
	}
	if publicationHeadsEqual(expected, expectedFound, actual, actualFound) {
		return true, nil
	}
	p.logger.Info("ACP warm runtime canonical head changed; restarting before prompt",
		slog.String("bot_id", h.botID),
		slog.String("session_id", sessionID),
		slog.String("runtime_id", h.id),
		slog.Bool("expected_exists", expectedFound),
		slog.String("expected_run_id", expected.RunID),
		slog.String("expected_kind", string(expected.Kind)),
		slog.Bool("actual_exists", actualFound),
		slog.String("actual_run_id", actual.RunID),
		slog.String("actual_kind", string(actual.Kind)))
	return false, nil
}

func (p *SessionPool) loadRuntimeConfigEpoch(ctx context.Context, botID, sessionID string) (RuntimeConfigEpoch, error) {
	if p == nil || p.stateStore == nil {
		return RuntimeConfigEpoch{}, nil
	}
	epoch, err := p.stateStore.RuntimeConfigEpoch(ctx, botID, sessionID)
	if err != nil {
		return RuntimeConfigEpoch{}, fmt.Errorf("load ACP runtime config epoch: %w", err)
	}
	return epoch, nil
}

func publicationHeadsEqual(a SessionPublicationHead, aFound bool, b SessionPublicationHead, bFound bool) bool {
	if aFound != bFound {
		return false
	}
	if !aFound {
		return true
	}
	aRunID, aErr := uuid.Parse(strings.TrimSpace(a.RunID))
	bRunID, bErr := uuid.Parse(strings.TrimSpace(b.RunID))
	return aErr == nil && bErr == nil && aRunID == bRunID && a.Kind == b.Kind
}

func (p *SessionPool) persistSessionState(
	ctx context.Context,
	h *runtimeHandle,
	sess *client.Session,
	runID string,
	fence runtimefence.Fence,
	receipt *client.SessionStateReceipt,
) (bool, error) {
	if p == nil || p.stateStore == nil || h == nil || sess == nil {
		return false, nil
	}
	h.state.Lock()
	disabled := h.disableSessionState
	supported := h.sessionStateSupported
	previousCursor := h.sessionStateCursor
	locator := h.sessionStateLocator
	sessionID := strings.TrimSpace(h.boundSession)
	cwd := strings.TrimSpace(h.cwd)
	h.state.Unlock()
	if disabled || !supported || sessionID == "" || cwd == "" {
		return false, nil
	}
	if locator == acpprofile.RuntimeSessionLocatorClaudeProject && receipt == nil {
		// Claude freshness is proven by the raw SDK receipt. Without one (a
		// non-"success" result subtype, or a receipt-channel inconsistency)
		// the snapshot cannot be audited as this turn's state, so decline:
		// the turn stays successful and publishes a reset head instead of an
		// unprovable checkpoint.
		p.logger.Warn("ACP turn completed without a session-state receipt; publishing a reset instead",
			slog.String("bot_id", h.botID),
			slog.String("session_id", sessionID),
			slog.String("run_id", runID))
		return false, nil
	}
	if !fence.Valid() {
		return false, errors.New("runtime persistence fence is required for a durable ACP checkpoint")
	}
	runID = strings.TrimSpace(runID)
	if _, err := uuid.Parse(runID); err != nil {
		return false, fmt.Errorf("valid run_id is required for a durable ACP checkpoint: %w", err)
	}

	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionStateIOTimeout)
	defer cancel()
	// Staging deliberately survives the request context (a client disconnect
	// must not lose the checkpoint), but a lifecycle close must be able to
	// interrupt it: closeHandle cancels this instead of blocking on h.op for
	// up to the full staging timeout. Re-check closed under the same lock so
	// a close that already scanned for a cancel hook (and found none) cannot
	// be outrun by this installation.
	h.state.Lock()
	if h.closed {
		h.state.Unlock()
		return false, errors.New("ACP runtime closed before checkpoint staging")
	}
	h.stagingCancel = cancel
	h.state.Unlock()
	defer func() {
		h.state.Lock()
		h.stagingCancel = nil
		h.state.Unlock()
	}()
	persistCtx = runtimefence.WithContext(persistCtx, fence)
	// The canonical head's per-file record counts are the append boundaries:
	// capture snapshots a running digest at each so the store can prove the
	// stored prefix is unchanged and persist only each file's tail. This read
	// is advisory - Replace re-validates every proof inside its transaction.
	var boundaries map[string]int64
	if shapes, found, shapeErr := p.stateStore.CanonicalShape(persistCtx, h.botID, sessionID); shapeErr != nil {
		p.logger.Warn("failed to load canonical ACP state shape; staging a full snapshot",
			slog.String("bot_id", h.botID), slog.String("session_id", sessionID), slog.Any("error", shapeErr))
	} else if found {
		boundaries = make(map[string]int64, len(shapes))
		for path, shape := range shapes {
			boundaries[path] = shape.Records
		}
	}
	var snapshot *client.SessionStateSnapshot
	var captureErr error
	// Codex emits its terminal protocol notification immediately before its
	// final rollout flush barrier. Usually the barrier wins the ACP round trip,
	// but a slow filesystem can briefly expose the previous stable transcript.
	// Retry only this bounded post-success checkpoint window; never publish a
	// state until its native completion cursor advances.
	for attempt := 0; attempt < 4; attempt++ {
		snapshot, captureErr = sess.SnapshotSessionState(persistCtx, previousCursor, receipt, boundaries)
		if captureErr == nil {
			break
		}
		if attempt == 3 {
			return false, fmt.Errorf("capture advanced ACP JSONL: %w", captureErr)
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 50 * time.Millisecond)
		select {
		case <-persistCtx.Done():
			timer.Stop()
			return false, fmt.Errorf("capture advanced ACP JSONL: %w", persistCtx.Err())
		case <-timer.C:
		}
	}
	defer func() { _ = snapshot.Close() }()
	state := snapshot.State()
	records, err := snapshot.Records()
	if err != nil {
		return false, err
	}
	storeRecords := func(readCtx context.Context) (SessionStateRecord, error) {
		record, readErr := records(readCtx)
		if readErr != nil {
			return SessionStateRecord{}, readErr
		}
		return SessionStateRecord{
			FilePath: record.FilePath, LineNumber: record.LineNumber, Content: record.Content,
		}, nil
	}
	shapes := snapshot.FileShapes()
	files := make([]PersistedSessionStateFile, 0, len(shapes))
	for _, shape := range shapes {
		files = append(files, PersistedSessionStateFile{
			SessionStateFileShape: SessionStateFileShape{
				Path: shape.Path, Records: shape.Records, Digest: shape.Digest,
			},
			PrefixRecords: shape.PrefixRecords,
			PrefixDigest:  shape.PrefixDigest,
		})
	}
	if err := p.stateStore.Replace(persistCtx, h.botID, sessionID, PersistedSessionState{
		AgentID:             h.agentID,
		ACPSessionID:        state.SessionID,
		ThroughRunID:        runID,
		Cwd:                 cwd,
		TranscriptPath:      state.TranscriptPath,
		RuntimeFencingToken: fence.Token,
		FileCount:           snapshot.FileCount(),
		RecordCount:         snapshot.RecordCount(),
		Files:               files,
	}, storeRecords); err != nil {
		if errors.Is(err, ErrSessionStateDivergent) {
			// The agent rewrote or removed a canonical file, so this capture
			// cannot extend the checkpoint without risking the still-canonical
			// rows. Decline staging: the turn stays successful and publishes an
			// explicit reset head, the warm runtime keeps its full context, and
			// once the reset is canonical the next turn stages a fresh full
			// snapshot safely.
			p.logger.Warn("ACP session state diverged from the canonical checkpoint; publishing a reset instead",
				slog.String("bot_id", h.botID),
				slog.String("session_id", sessionID),
				slog.String("run_id", runID),
				slog.Any("error", err))
			return false, nil
		}
		return false, err
	}
	h.state.Lock()
	h.sessionStateCursor = snapshot.Cursor()
	h.state.Unlock()
	return true, nil
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
		errors.Is(err, client.ErrReasoningEffortUnavailable) ||
		errors.Is(err, client.ErrModeIDRequired) ||
		errors.Is(err, client.ErrModeSelectionUnsupported) ||
		errors.Is(err, client.ErrModeUnavailable)
}

// Ensure starts (or reuses) the runtime for a session without prompting it.
//
//nolint:contextcheck // lifecycle close uses the handle owner context.
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
//nolint:contextcheck // lifecycle close uses the handle owner context.
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
//nolint:contextcheck // lifecycle close uses the handle owner context.
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

// SetMode switches the agent-declared mode of the runtime bound to a session,
// cold starting one when needed. The mode remains process/session local.
//
//nolint:contextcheck // lifecycle close uses the handle owner context.
func (p *SessionPool) SetMode(ctx context.Context, input PromptInput, modeID string) (RuntimeStatus, error) {
	if strings.TrimSpace(modeID) == "" {
		return RuntimeStatus{}, client.ErrModeIDRequired
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
	return p.setModeOnHandle(ctx, h, modeID)
}

// runtimeForSession resolves the runtime bound to a session, cold starting
// and binding a fresh one when the index misses. A bound runtime whose agent
// or project no longer matches the session metadata is replaced.
func (p *SessionPool) runtimeForSession(ctx context.Context, input PromptInput) (*runtimeHandle, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if p.sessionRuntime != nil {
		if err := p.sessionRuntime.WaitForHistoryReset(ctx, input.BotID, sessionID); err != nil {
			return nil, err
		}
	}
	refreshIdentity := func() error {
		resolved, err := p.resolveSessionMetadata(ctx, input)
		if err != nil {
			return err
		}
		input.BotID = resolved.BotID
		input.AgentID = resolved.AgentID
		input.ProjectPath = resolved.ProjectPath
		input.RuntimeOwnerAccountID = resolved.RuntimeOwnerAccountID
		return nil
	}
	identity := func() (agentID, projectPath, runtimeOwnerAccountID string, err error) {
		agentID = acpprofile.NormalizeAgentID(input.AgentID)
		if agentID == "" {
			agentID = acpprofile.AgentCodexID
		}
		projectPath = strings.TrimSpace(input.ProjectPath)
		runtimeOwnerAccountID = strings.TrimSpace(input.RuntimeOwnerAccountID)
		if runtimeOwnerAccountID == "" {
			runtimeOwnerAccountID = strings.TrimSpace(input.ChannelIdentityID)
		}
		if runtimeOwnerAccountID == "" {
			err = runtimeOwnerMissingError()
		}
		return
	}
	agentID, projectPath, runtimeOwnerAccountID, identityErr := identity()
	if identityErr != nil {
		return nil, identityErr
	}
	workspaceTargetID := strings.TrimSpace(input.WorkspaceTargetID)
	workspaceTargetKind := strings.TrimSpace(input.WorkspaceTargetKind)
	workspaceTargetName := strings.TrimSpace(input.WorkspaceTargetName)

	for attempt := 0; attempt < 3; {
		p.mu.Lock()
		var resetDone <-chan struct{}
		if done := p.historyResetBots[input.BotID]; done != nil {
			resetDone = done
		} else if gate, ok := p.historyResetSessions[sessionID]; ok {
			resetDone = gate.done
		}
		if resetDone != nil {
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-resetDone:
				// A reset may soft-delete the session or replace its ACP identity.
				// Never resume admission with metadata prepared before that boundary.
				if err := refreshIdentity(); err != nil {
					return nil, fmt.Errorf("reload ACP session metadata after reset: %w", err)
				}
				agentID, projectPath, runtimeOwnerAccountID, identityErr = identity()
				if identityErr != nil {
					return nil, identityErr
				}
				continue
			}
		}
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
				projectPath:           projectPath,
				runtimeOwnerAccountID: runtimeOwnerAccountID,
				ownerCtx:              context.WithoutCancel(ctx),
				disableSessionState:   input.disableSessionState,
				workspaceTargetID:     workspaceTargetID,
				workspaceTargetKind:   workspaceTargetKind,
				workspaceTargetName:   workspaceTargetName,
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
		matches := h.agentID == agentID && h.projectPath == projectPath &&
			h.runtimeOwnerAccountID == runtimeOwnerAccountID &&
			h.disableSessionState == input.disableSessionState &&
			h.workspaceTargetID == workspaceTargetID
		closed := h.closed
		starting := h.session == nil
		sess := h.session
		exited := sessionProcessExited(sess)
		if matches && !closed {
			// Resolving counts as activity: a session whose UI keeps the
			// runtime ensured (without prompting) must not be idle-reaped.
			h.lastActive = time.Now()
		}
		h.state.Unlock()
		if matches && !closed && !exited {
			if starting {
				// A concurrent startRuntime still owns h.op and has not
				// published nativeHead/epoch yet; comparing the zero values
				// against the durable head would wrongly tear the starting
				// runtime down. Callers serialize on h.op, which startRuntime
				// holds, and re-run the head comparison themselves once the
				// start completes.
				return h, nil
			}
			current, epochErr := p.publicationHeadMatches(ctx, h)
			if epochErr != nil {
				return nil, epochErr
			}
			if !current {
				_ = p.closeHandle(h) //nolint:contextcheck // stale durable generation must be destroyed before replacement.
				attempt++
				continue
			}
			return h, nil
		}
		// Agent or project changed for this session: replace the runtime.
		_ = p.closeHandle(h) //nolint:contextcheck // lifecycle close uses the handle owner context.
		attempt++
	}
	return nil, errors.New("ACP runtime is restarting, retry the request")
}

func sessionProcessExited(sess *client.Session) bool {
	if sess == nil {
		return false
	}
	done, observable := sess.Done()
	if !observable {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

type startOptions struct {
	ToolHTTPURL string
	Sink        client.EventSink
}

// startRuntime boots the agent process for a registered handle. Must be
// called with h.op held. On failure the handle is fully torn down (process,
// maps, context) before returning.
//
//nolint:contextcheck // startup failure cleanup uses the handle owner context.
func (p *SessionPool) startRuntime(ctx context.Context, h *runtimeHandle, opts startOptions) error {
	ctx = contextWithWorkspaceTarget(ctx, h.workspaceTargetID)
	startCtx, cancelStart := context.WithCancel(ctx)
	defer cancelStart()
	h.state.Lock()
	if h.ownerCtx == nil {
		h.ownerCtx = context.WithoutCancel(ctx)
	}
	if h.closed {
		h.state.Unlock()
		return errors.New("ACP runtime was closed during startup")
	}
	h.startCancel = cancelStart
	h.state.Unlock()
	epoch, err := p.loadRuntimeConfigEpoch(startCtx, h.botID, h.boundSession)
	if err != nil {
		_ = p.teardown(h)
		return err
	}
	h.state.Lock()
	h.runtimeConfigEpoch = epoch
	h.state.Unlock()
	runtimeSyncGuard := p.runtimeSyncGuard(h.botID, epoch.Bot)

	fail := func(err error) error {
		// Public surfaces return a stable, redacted runtime-operation error, so
		// the underlying cause must be recorded here or it is lost entirely.
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
	command, arguments, err := acpprofile.ResolveLaunch(profile, setup)
	if err != nil {
		return fail(fmt.Errorf("resolve ACP launch command: %w", err))
	}
	supportsSessionState := len(profile.RuntimeStorage.SessionRoots) > 0
	if targetID := strings.TrimSpace(workspaceInfo.TargetID); h.workspaceTargetID != "" && targetID != "" && h.workspaceTargetID != targetID {
		return fail(errors.New("workspace target changed while starting ACP runtime"))
	}
	resolved, err := client.ResolveSessionContext(client.SessionContextInput{
		AgentID:        h.agentID,
		SetupMode:      mode,
		Backend:        workspaceInfo.Backend,
		OS:             workspaceInfo.OS,
		DefaultWorkDir: workspaceInfo.DefaultWorkDir,
		ProjectPath:    h.projectPath,
	})
	if err != nil {
		return fail(fmt.Errorf("resolve ACP session context: %w", err))
	}
	var env []string
	var cleanEnv bool
	var unsetEnv []string
	if resolved.Backend != client.WorkspaceBackendRemote {
		if err := p.reconcileManagedACPConfig(startCtx, h.botID, profile, setup, mode, resolved, runtimeSyncGuard); err != nil {
			return fail(fmt.Errorf("prepare %s managed config: %w", profile.DisplayName, err))
		}
		env, err = managedProcessEnv(profile, setup.Managed, mode)
		if err != nil {
			return fail(err)
		}
		cleanEnv, unsetEnv = managedEnvControls(profile, mode, resolved.Backend)
	}

	toolHTTPURL, err := p.resolveToolHTTPURL(opts.ToolHTTPURL, workspaceInfo)
	if err != nil {
		return fail(err)
	}
	startReq := client.StartRequest{
		AgentID:                h.agentID,
		BotID:                  h.botID,
		ProjectPath:            h.projectPath,
		Command:                command,
		Args:                   arguments,
		Env:                    env,
		CleanEnv:               cleanEnv,
		UnsetEnv:               unsetEnv,
		Resolved:               &resolved,
		SetupMode:              mode,
		SessionMode:            profile.SessionModeID,
		ReasoningConfigID:      profile.ReasoningConfigID,
		DefaultReasoningEffort: profile.DefaultReasoningEffort,
		Timeout:                0,
		ToolHTTPURL:            toolHTTPURL,
		// The handler resolves identity from the handle per request, so the
		// process configuration only ever carries stable runtime identity.
		ToolHTTPHandler:  p.toolHTTPHandler(h),
		ToolGateway:      p.tools,
		ToolSession:      h.stableToolIdentity(),
		ToolApproval:     p.approval,
		UserInput:        p.userInput,
		RuntimeSyncGuard: runtimeSyncGuard,
	}
	var (
		restoredCursor     client.SessionStateCursor
		canonicalHead      SessionPublicationHead
		canonicalHeadFound bool
		resumeSnapshot     *client.SessionStateSnapshot
	)
	defer func() {
		if resumeSnapshot != nil {
			_ = resumeSnapshot.Close()
		}
	}()
	boundSession := strings.TrimSpace(h.boundSession)
	if p.stateStore != nil && boundSession != "" {
		canonicalHead, canonicalHeadFound, err = p.stateStore.Head(startCtx, h.botID, boundSession)
		if err != nil {
			return fail(fmt.Errorf("load ACP session publication head: %w", err))
		}
		if canonicalHeadFound {
			if _, parseErr := uuid.Parse(strings.TrimSpace(canonicalHead.RunID)); parseErr != nil {
				return fail(fmt.Errorf("%w: canonical ACP publication has invalid run id: %w", ErrSessionStateOutOfSync, parseErr))
			}
			if canonicalHead.Kind != SessionPublicationCheckpoint && canonicalHead.Kind != SessionPublicationReset {
				return fail(fmt.Errorf("%w: canonical ACP publication has unknown kind %q", ErrSessionStateOutOfSync, canonicalHead.Kind))
			}
		}
		if canonicalHeadFound && canonicalHead.Kind == SessionPublicationCheckpoint && !h.disableSessionState {
			if !supportsSessionState {
				return fail(fmt.Errorf("%w: current ACP profile cannot restore canonical checkpoint %s", ErrSessionStateOutOfSync, canonicalHead.RunID))
			}
			loadCtx, cancelLoad := context.WithTimeout(startCtx, sessionStateIOTimeout)
			var persisted PersistedSessionState
			found, loadErr := p.stateStore.Load(loadCtx, h.botID, boundSession, func(
				consumeCtx context.Context,
				state PersistedSessionState,
				records SessionStateRecordReader,
			) error {
				persisted = state
				if strings.TrimSpace(state.ThroughRunID) != strings.TrimSpace(canonicalHead.RunID) {
					return fmt.Errorf(
						"%w: loaded ACP checkpoint %s does not match canonical head %s",
						ErrSessionStateOutOfSync,
						state.ThroughRunID,
						canonicalHead.RunID,
					)
				}
				sameAgent := acpprofile.NormalizeAgentID(state.AgentID) == h.agentID
				sameCwd := strings.TrimSpace(state.Cwd) == strings.TrimSpace(resolved.ProjectPath)
				if !sameAgent || !sameCwd {
					return fmt.Errorf(
						"%w: canonical ACP checkpoint does not match current agent/workdir (agent_matches=%t, cwd_matches=%t)",
						ErrSessionStateOutOfSync,
						sameAgent,
						sameCwd,
					)
				}
				clientRecords := func(readCtx context.Context) (client.SessionStateRecord, error) {
					record, readErr := records(readCtx)
					if readErr != nil {
						return client.SessionStateRecord{}, readErr
					}
					return client.SessionStateRecord{
						FilePath: record.FilePath, LineNumber: record.LineNumber, Content: record.Content,
					}, nil
				}
				var spoolErr error
				resumeSnapshot, spoolErr = client.SpoolSessionState(
					consumeCtx,
					profile.RuntimeStorage.SessionLocator,
					profile.RuntimeStorage.SessionRoots,
					client.SessionState{
						SessionID: state.ACPSessionID, TranscriptPath: state.TranscriptPath,
					},
					clientRecords,
					state.FileCount,
					state.RecordCount,
				)
				return spoolErr
			})
			cancelLoad()
			if loadErr != nil {
				return fail(fmt.Errorf("load ACP session checkpoint: %w", loadErr))
			}
			if !found {
				return fail(fmt.Errorf("%w: canonical ACP checkpoint %s is unavailable", ErrSessionStateOutOfSync, canonicalHead.RunID))
			}
			if resumeSnapshot == nil || persisted.ACPSessionID == "" {
				return fail(fmt.Errorf("%w: canonical ACP checkpoint did not produce a resumable snapshot", ErrSessionStateOutOfSync))
			}
			startReq.Resume = resumeSnapshot
		}
	}

	runnerCtx := startCtx
	cancelRunner := func() {}
	if startReq.Resume != nil {
		runnerCtx, cancelRunner = context.WithTimeout(startCtx, sessionStateIOTimeout)
	}
	sess, err := p.runner.StartSession(runnerCtx, startReq, opts.Sink)
	cancelRunner()
	if err != nil {
		if startCtx.Err() != nil {
			return fail(err)
		}
		if startReq.Resume != nil && sessionResumeIsOutOfSync(err) {
			return fail(fmt.Errorf("%w: restore canonical ACP checkpoint: %w", ErrSessionStateOutOfSync, err))
		}
		return fail(err)
	}
	restoredCursor = sess.RestoredSessionStateCursor()
	// Startup performs several protocol round trips after the guarded runtime
	// staging read. Revalidate both the bot write guard and the complete epoch
	// pair before publishing this process as reusable.
	if runtimeSyncGuard != nil {
		if guardErr := runtimeSyncGuard(startCtx, func(context.Context) error { return nil }); guardErr != nil {
			_ = sess.Close()
			return fail(fmt.Errorf("validate ACP runtime configuration after startup: %w", guardErr))
		}
	}
	finalEpoch, epochErr := p.loadRuntimeConfigEpoch(startCtx, h.botID, h.boundSession)
	if epochErr != nil {
		_ = sess.Close()
		return fail(epochErr)
	}
	if finalEpoch != epoch {
		_ = sess.Close()
		return fail(fmt.Errorf(
			"%w: runtime configuration changed during startup (expected=%+v, actual=%+v)",
			ErrRuntimeConfigStale,
			epoch,
			finalEpoch,
		))
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
	h.cwd = resolved.ProjectPath
	h.sessionStateSupported = supportsSessionState
	h.sessionStateLocator = profile.RuntimeStorage.SessionLocator
	h.sessionStateCursor = restoredCursor
	h.nativeHead = canonicalHead
	h.nativeHeadFound = canonicalHeadFound
	h.status = stateIdle
	h.lastActive = time.Now()
	h.startCancel = nil
	h.defaultModelID = strings.TrimSpace(sess.ModelState().CurrentModelID)
	h.state.Unlock()
	return nil
}

func sessionResumeIsOutOfSync(err error) bool {
	return errors.Is(err, client.ErrSessionStateRestoreInvalid) ||
		errors.Is(err, client.ErrSessionResumeUnsupported) ||
		errors.Is(err, client.ErrSessionResumeRejected)
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
	closed := h.closed
	status := RuntimeStatus{
		RuntimeID:             h.id,
		SessionID:             h.boundSession,
		AgentID:               h.agentID,
		ProjectPath:           h.projectPath,
		RuntimeOwnerAccountID: h.runtimeOwnerAccountID,
		WorkspaceTargetID:     h.workspaceTargetID,
		WorkspaceTargetKind:   h.workspaceTargetKind,
		State:                 h.status,
		DefaultModelID:        h.defaultModelID,
	}
	h.state.Unlock()
	// closeHandle marks the handle closed before stopping the Agent and waiting
	// for the serialized operation lock. During that interval the Session
	// pointer is intentionally still present so Close can cancel an active
	// prompt, but it is no longer authoritative runtime state. Never project its
	// ACP session ID or capabilities: callers use their presence to authorize
	// Agent-declared slash commands, and a replacement runtime may belong to a
	// different Agent or project.
	if closed {
		sess = nil
		status.DefaultModelID = ""
	}
	switch status.State {
	case stateStarting:
		status.State = stateActive
	case stateClosed, "":
		status.State = stateIdle
	}
	if sess != nil {
		status.ACPSession = sess.ID()
		modelState, reasoningState, modeState, availableCommands := sess.ConfigurationState()
		status.Models = &modelState
		status.Reasoning = &reasoningState
		status.Modes = &modeState
		status.AvailableCommands = availableCommands
		// Session configuration has its own lock, so it cannot be read while
		// holding handle.state (the handle lock is the innermost leaf). Fence the
		// completed projection instead: if closing or replacement began while the
		// Session snapshot was being copied, discard every derived capability.
		h.state.Lock()
		stillLive := !h.closed && h.session == sess
		h.state.Unlock()
		if !stillLive {
			status.ACPSession = ""
			status.DefaultModelID = ""
			status.Models = nil
			status.Reasoning = nil
			status.Modes = nil
			status.AvailableCommands = nil
		}
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
				p.reapIdle(time.Now()) //nolint:contextcheck // reaper uses each handle's owner context.
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
//nolint:contextcheck // lifecycle cleanup uses owner values after the caller cancels.
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

// BeginSessionHistoryReset blocks new runtime admission for one chat session,
// then closes the current generation. The returned release function must stay
// held until the caller's canonical-history deletion transaction completes.
// This prevents a fresh runtime from restoring the checkpoint that is about to
// be invalidated.
func (p *SessionPool) BeginSessionHistoryReset(ctx context.Context, botID, sessionID string) (context.Context, func(), error) {
	if p == nil {
		return nil, nil, sessionruntime.ErrHistoryResetUnavailable
	}
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return nil, nil, errors.New("bot_id and session_id are required for ACP history reset")
	}
	for {
		p.mu.Lock()
		var wait <-chan struct{}
		if done := p.historyResetBots[botID]; done != nil {
			wait = done
		} else if gate, ok := p.historyResetSessions[sessionID]; ok {
			wait = gate.done
		} else {
			done := make(chan struct{})
			p.historyResetSessions[sessionID] = historyResetSessionGate{botID: botID, done: done}
			p.mu.Unlock()
			release := p.sessionHistoryResetRelease(sessionID, done)
			if err := p.CloseSession(sessionID); err != nil {
				release()
				return nil, nil, err
			}
			if p.sessionRuntime == nil {
				release()
				return nil, nil, sessionruntime.ErrHistoryResetUnavailable
			}
			resetCtx, releaseDistributed, err := p.sessionRuntime.BeginSessionHistoryReset(ctx, botID, sessionID)
			if err != nil {
				release()
				return nil, nil, err
			}
			return resetCtx, joinHistoryResetReleases(releaseDistributed, release), nil
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-wait:
		}
	}
}

// BeginBotHistoryReset is the bot-wide form of BeginSessionHistoryReset. It
// excludes both new bot runtimes and narrower session resets until released.
func (p *SessionPool) BeginBotHistoryReset(ctx context.Context, botID string) (context.Context, func(), error) {
	if p == nil {
		return nil, nil, sessionruntime.ErrHistoryResetUnavailable
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return nil, nil, errors.New("bot_id is required for ACP history reset")
	}
	for {
		p.mu.Lock()
		var wait <-chan struct{}
		if done := p.historyResetBots[botID]; done != nil {
			wait = done
		} else {
			for _, gate := range p.historyResetSessions {
				if gate.botID == botID {
					wait = gate.done
					break
				}
			}
		}
		if wait == nil {
			done := make(chan struct{})
			p.historyResetBots[botID] = done
			p.mu.Unlock()
			release := p.botHistoryResetRelease(botID, done)
			if err := p.CloseBotAgentRuntimes(botID, ""); err != nil { //nolint:contextcheck // lifecycle close owns its cleanup context.
				release()
				return nil, nil, err
			}
			if p.sessionRuntime == nil {
				release()
				return nil, nil, sessionruntime.ErrHistoryResetUnavailable
			}
			resetCtx, releaseDistributed, err := p.sessionRuntime.BeginBotHistoryReset(ctx, botID)
			if err != nil {
				release()
				return nil, nil, err
			}
			return resetCtx, joinHistoryResetReleases(releaseDistributed, release), nil
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-wait:
		}
	}
}

func joinHistoryResetReleases(releases ...func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, release := range releases {
				if release != nil {
					release()
				}
			}
		})
	}
}

func (p *SessionPool) sessionHistoryResetRelease(sessionID string, done chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			gate, ok := p.historyResetSessions[sessionID]
			if ok && gate.done == done {
				delete(p.historyResetSessions, sessionID)
			}
			p.mu.Unlock()
			if ok && gate.done == done {
				close(done)
			}
		})
	}
}

func (p *SessionPool) botHistoryResetRelease(botID string, done chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			current, ok := p.historyResetBots[botID]
			if ok && current == done {
				delete(p.historyResetBots, botID)
			}
			p.mu.Unlock()
			if ok && current == done {
				close(done)
			}
		})
	}
}

// closeHandle destroys the runtime. It first marks the handle closed and
// cancels any active prompt/start before waiting for the serialized operation
// lock, so a prompt blocked on ACP approval or user input can unwind promptly.
func (p *SessionPool) closeHandle(h *runtimeHandle) error {
	h.state.Lock()
	if h.closeStarted {
		done := h.closeDone
		h.state.Unlock()
		<-done
		h.state.Lock()
		err := h.closeErr
		h.state.Unlock()
		return err
	}
	h.closeStarted = true
	h.closeDone = make(chan struct{})
	h.closed = true
	h.status = stateClosed
	sess := h.session
	cancel := h.startCancel
	h.startCancel = nil
	stagingCancel := h.stagingCancel
	bound := h.boundSession
	activeSession := ""
	fence := h.persistenceFence
	cleanupParent := h.ownerCtx
	if h.active != nil {
		activeSession = strings.TrimSpace(h.active.SessionID)
		if h.active.RunContext != nil {
			cleanupParent = h.active.RunContext
		}
		if h.active.RuntimeFence.Valid() {
			fence = h.active.RuntimeFence
		}
		h.active = nil
	}
	h.state.Unlock()

	if cancel != nil {
		cancel()
	}
	if stagingCancel != nil {
		// An in-flight checkpoint staging runs detached from the request
		// context; interrupt it so the prompt holder releases h.op promptly
		// instead of streaming a snapshot this closed generation cannot use.
		stagingCancel()
	}
	if sess != nil {
		sess.CancelPrompt()
		// Close the session before waiting on h.op: an op holder can be a
		// config setter blocked on an unresponsive agent under a detached
		// context, and only a transport close makes it fail fast. Failing an
		// in-flight checkpoint capture the same way is safe - the turn is
		// persisted as failed, the durable head never moves, and this
		// generation is being destroyed regardless.
		if closeErr := sess.Close(); closeErr != nil {
			p.logger.Debug("close ACP session ahead of operation barrier",
				slog.Any("error", closeErr), slog.String("runtime_id", h.id))
		}
	}
	sessionID := strings.TrimSpace(bound)
	if sessionID == "" {
		sessionID = activeSession
	}
	if cleanupParent == nil && sessionID != "" {
		cleanupParent = p.fallbackDecisionCleanupRoot(h, sessionID)
	}
	p.cancelHandlePendingDecisions(cleanupParent, h, sessionID, fence, decisionCleanupPre, "decision cancelled: ACP runtime closed before a response arrived")
	h.op.Lock()
	closeErr := p.teardown(h)
	h.op.Unlock()

	// Keep the closed handle as an admission tombstone until the operation
	// boundary and process teardown are complete. A resolver that finds it calls
	// closeHandle too, waits on closeDone, then retries from the newly-published
	// checkpoint rather than starting from the previous generation mid-close.
	p.mu.Lock()
	delete(p.runtimes, h.id)
	if bound != "" && p.bySession[bound] == h.id {
		delete(p.bySession, bound)
	}
	p.mu.Unlock()

	h.state.Lock()
	h.closeErr = closeErr
	close(h.closeDone)
	h.state.Unlock()
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
// registration is removed. Destroying a runtime between staging and the
// round's commit is safe: the durable publication head is the authority, and
// a successor cold-starts from whatever head that commit resolves to.
func (p *SessionPool) teardown(h *runtimeHandle) error {
	h.state.Lock()
	h.closed = true
	h.status = stateClosed
	sess := h.session
	h.session = nil
	cancel := h.startCancel
	h.startCancel = nil
	stagingCancel := h.stagingCancel
	bound := h.boundSession
	activeSession := ""
	fence := h.persistenceFence
	cleanupParent := h.ownerCtx
	if h.active != nil {
		activeSession = strings.TrimSpace(h.active.SessionID)
		if h.active.RunContext != nil {
			cleanupParent = h.active.RunContext
		}
		if h.active.RuntimeFence.Valid() {
			fence = h.active.RuntimeFence
		}
	}
	h.active = nil
	closing := h.closeStarted
	h.state.Unlock()
	if stagingCancel != nil {
		// Checkpoint staging runs detached from the request context; interrupt
		// it so the prompt holder releases h.op and its spool slot promptly
		// instead of streaming a snapshot this closed generation cannot use.
		stagingCancel()
	}
	sessionID := strings.TrimSpace(bound)
	if sessionID == "" {
		sessionID = activeSession
	}
	if cleanupParent == nil && sessionID != "" {
		cleanupParent = p.fallbackDecisionCleanupRoot(h, sessionID)
	}
	p.cancelHandlePendingDecisions(cleanupParent, h, sessionID, fence, decisionCleanupPre, "decision cancelled: ACP runtime closed before a response arrived")

	if cancel != nil {
		cancel()
	}
	var closeErr error
	if sess != nil {
		closeErr = sess.Close()
	}
	p.cancelHandlePendingDecisions(cleanupParent, h, sessionID, fence, decisionCleanupFinal, "decision cancelled: ACP runtime closed before a response arrived")

	if !closing {
		p.mu.Lock()
		delete(p.runtimes, h.id)
		if bound != "" && p.bySession[bound] == h.id {
			delete(p.bySession, bound)
		}
		p.mu.Unlock()
	}
	return closeErr
}

type decisionCleanupPhase uint8

const (
	decisionCleanupPre decisionCleanupPhase = iota
	decisionCleanupFinal
)

// fallbackDecisionCleanupRoot is the single fail-open boundary for a malformed
// handle that has pending decisions but no owner context. The cleanup loses
// values, but skipping it would leave approvals or questions stranded in the
// UI. The report is logged once per handle.
func (p *SessionPool) fallbackDecisionCleanupRoot(h *runtimeHandle, sessionID string) context.Context {
	h.decisionFallbackOnce.Do(func() {
		p.logger.Error("pending ACP decision cleanup without runtime context",
			slog.String("bot_id", h.botID), slog.String("session_id", sessionID))
	})
	return context.Background()
}

func (p *SessionPool) cancelHandlePendingDecisions(parent context.Context, h *runtimeHandle, sessionID string, fence runtimefence.Fence, phase decisionCleanupPhase, reason string) {
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
	if parent == nil {
		p.logger.Error("skip pending ACP decision cleanup without normalized context",
			slog.String("bot_id", h.botID), slog.String("session_id", sessionID))
		return
	}
	cleanupCtx := context.WithoutCancel(parent)
	if fence.Valid() {
		cleanupCtx = runtimefence.WithContext(cleanupCtx, fence)
	}
	once.Do(func() {
		p.cancelPendingDecisions(cleanupCtx, h.botID, sessionID, reason)
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
		p.logger.Error("skip pending ACP decision cleanup without normalized parent context",
			slog.String("bot_id", botID), slog.String("session_id", sessionID))
		return
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
		// Bot metadata updates must not wait for an active prompt: teardown
		// closes the session directly, cancelling the in-flight prompt (and any
		// detached checkpoint staging), and the op holder unwinds on its own.
		// An interrupted staging simply fails that turn; the durable publication
		// head stays at the last committed run, so nothing can diverge.
		if err := p.teardown(h); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CloseBotWorkspaceTargetRuntimes tears down every runtime pinned to one
// workspace target. Target deletion is rejected while a workdir still refers
// to it, but an unbound prewarm may otherwise survive after the target record
// is removed and keep running commands on a computer the user disconnected.
func (p *SessionPool) CloseBotWorkspaceTargetRuntimes(botID, targetID string) error {
	if p == nil {
		return nil
	}
	botID = strings.TrimSpace(botID)
	targetID = strings.TrimSpace(targetID)
	if botID == "" || targetID == "" {
		return nil
	}
	p.mu.RLock()
	handles := make([]*runtimeHandle, 0)
	for _, h := range p.runtimes {
		if h == nil || h.botID != botID || h.workspaceTargetID != targetID {
			continue
		}
		handles = append(handles, h)
	}
	p.mu.RUnlock()

	var firstErr error
	for _, h := range handles {
		// Like bot/agent reconfiguration, target removal must not wait for an
		// active prompt or approval. Teardown closes the process first and lets
		// the operation holder unwind through the closed handle.
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
		for _, key := range []string{"acp_agent_id", "project_path", "acp_project_mode", "runtime_owner_account_id"} {
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
	if targetID := strings.TrimSpace(sess.WorkspaceTargetID); targetID != "" {
		input.WorkspaceTargetID = targetID
		input.WorkspaceTargetKind = strings.TrimSpace(sess.WorkspaceTargetKind)
	}
	if workdirPath := strings.TrimSpace(sess.WorkdirPath); workdirPath != "" {
		input.ProjectPath = workdirPath
	}
	return input, nil
}

func contextWithWorkspaceTarget(ctx context.Context, targetID string) context.Context {
	if targetID = strings.TrimSpace(targetID); targetID != "" {
		return workspace.WithWorkspaceTarget(ctx, targetID)
	}
	return ctx
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
	if strings.TrimSpace(bot.Status) == bots.BotStatusDeleting {
		return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, fmt.Errorf("bot %s is not ready for ACP runtime (status %q)", botID, bot.Status)
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
	isRemote := strings.EqualFold(strings.TrimSpace(workspaceInfo.Backend), bridge.WorkspaceBackendRemote)
	if !isRemote && !profileSupportsSetupMode(profile, mode) {
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
	if isRemote {
		osName := strings.ToLower(strings.TrimSpace(workspaceInfo.OS))
		if osName != "darwin" && osName != "linux" {
			reason := fmt.Sprintf("does not support remote operating system %q", workspaceInfo.OS)
			return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, feedback.New(
				feedback.CodeRemoteOSUnsupported,
				reason,
				http.StatusBadRequest,
				"chat.acp.remoteOSUnsupported",
				fmt.Sprintf("%s %s", profile.DisplayName, reason),
				map[string]string{"agent_id": agentID, "workspace_backend": bridge.WorkspaceBackendRemote},
			)
		}
		if required := profileBackendCapability(profile, bridge.WorkspaceBackendRemote); required != "" && !hasWorkspaceCapability(workspaceInfo.Capabilities, required) {
			reason := fmt.Sprintf("requires connected-computer capability %q", required)
			return bots.Bot{}, acpprofile.Profile{}, acpprofile.AgentSetup{}, "", bridge.WorkspaceInfo{}, feedback.New(
				feedback.CodeRemoteAdapterMissing,
				reason,
				http.StatusBadRequest,
				"chat.acp.remoteAdapterMissing",
				fmt.Sprintf("%s %s", profile.DisplayName, reason),
				map[string]string{"agent_id": agentID, "workspace_backend": bridge.WorkspaceBackendRemote},
			)
		}
	}
	if !isRemote && mode != client.SetupModeSelf {
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
		BotID:               h.botID,
		ChatID:              h.botID,
		RuntimeID:           h.id,
		RuntimeToken:        h.toolToken,
		SessionType:         sessionmode.ACPAgent,
		WorkspaceTargetID:   h.workspaceTargetID,
		WorkspaceTargetKind: h.workspaceTargetKind,
		WorkspaceTargetName: h.workspaceTargetName,
		WorkdirPath:         h.projectPath,
	}
}

// toolContext resolves the trusted MCP tool context for the runtime: stable
// identity plus, while a prompt is running, the per-prompt fields (stream,
// token, chat, reply target...).
func (h *runtimeHandle) toolContext() mcp.ToolSessionContext {
	h.state.Lock()
	defer h.state.Unlock()
	ctx := mcp.ToolSessionContext{
		BotID:               h.botID,
		ChatID:              h.botID,
		RuntimeID:           h.id,
		SessionID:           h.boundSession,
		SessionType:         sessionmode.ACPAgent,
		WorkspaceTargetID:   h.workspaceTargetID,
		WorkspaceTargetKind: h.workspaceTargetKind,
		WorkspaceTargetName: h.workspaceTargetName,
		WorkdirPath:         h.projectPath,
		CanListUserInput:    true,
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
	overlay(&ctx.ReasoningStoredEffort, h.active.ReasoningStoredEffort)
	overlay(&ctx.ReasoningRequestedEffort, h.active.ReasoningRequestedEffort)
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
		WorkspaceTargetID:   h.workspaceTargetID,
		WorkspaceTargetKind: h.workspaceTargetKind,
		WorkspaceTargetName: h.workspaceTargetName,
		WorkdirPath:         h.projectPath,
		// PromptInput.ReasoningEffort is the current turn's explicit selection.
		// The bot-stored fallback is loaded by SpawnProvider when this ACP tool
		// context does not already carry one.
		ReasoningRequestedEffort: strings.TrimSpace(input.ReasoningEffort),
		CanRequestUserInput:      input.CanRequestUserInput,
		IsSubagent:               false,
		SupportsImageInput:       input.SupportsImageInput,
		RuntimeFence:             fence,
		RunContext:               ctx,
		RuntimeGuard:             input.RuntimeGuard,
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
	raw := strings.TrimSpace(inputURL)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	allowedScheme := parsed != nil &&
		(parsed.Scheme == "https" || (parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())))
	if err != nil || !allowedScheme || strings.TrimSpace(parsed.Host) == "" || parsed.User != nil {
		return "", errors.New("remote ACP Memoh tools URL must be an absolute HTTPS URL without embedded credentials (plain HTTP is allowed only for loopback development)")
	}
	return parsed.String(), nil
}

// isLoopbackHost reports whether the URL host is the local machine. A
// loopback tools URL only ever works when the connected computer is the
// server host itself — the local development flow — so plain HTTP is
// acceptable there and rejected everywhere else.
func isLoopbackHost(hostname string) bool {
	host := strings.Trim(strings.TrimSpace(hostname), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
		mcp.ServeToolMCPHTTP(w, req, p.logger, p.tools, p.contexts, h.toolContext())
	})
}

func (p *SessionPool) reconcileManagedACPConfig(ctx context.Context, botID string, profile acpprofile.Profile, setup acpprofile.AgentSetup, mode client.SetupMode, resolved client.ResolvedSessionContext, guard client.RuntimeSyncGuard) error {
	if resolved.Backend == client.WorkspaceBackendRemote || mode == client.SetupModeSelf {
		return nil
	}
	runner, hasWorkspaceClient := p.runner.(workspaceClientRunner)
	if !hasWorkspaceClient {
		return nil
	}
	write := func(writeCtx context.Context) error {
		return client.WriteManagedACPConfig(writeCtx, client.ManagedACPConfigRequest{
			Profile:  profile,
			Setup:    setup,
			Mode:     mode,
			Resolved: resolved,
		}, func() (*bridge.Client, error) {
			return runner.MCPClient(writeCtx, botID)
		})
	}
	if guard == nil {
		return write(ctx)
	}
	return guard(ctx, write)
}

func (p *SessionPool) runtimeSyncGuard(botID string, expectedBotEpoch int64) client.RuntimeSyncGuard {
	if p == nil || p.stateStore == nil {
		return nil
	}
	return func(ctx context.Context, fn func(context.Context) error) error {
		err := p.stateStore.GuardRuntimeSync(ctx, botID, expectedBotEpoch, fn)
		if errors.Is(err, ErrRuntimeConfigStale) {
			return errors.Join(client.ErrRuntimeSyncGuardRejected, client.ErrRuntimeSyncGenerationStale, err)
		}
		if errors.Is(err, ErrRuntimeConfigResetInProgress) {
			return errors.Join(client.ErrRuntimeSyncGuardRejected, client.ErrRuntimeSyncResetInProgress, err)
		}
		return err
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

// RecordTerminalDecision updates the prompt's final snapshot without
// forwarding a late frame to the cancelled live stream. EventAbort will carry
// this corrected transcript as the one authoritative terminal event.
func (s *promptToolEventSink) RecordTerminalDecision(ev event.StreamEvent) {
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

func profileBackendCapability(profile acpprofile.Profile, backend string) string {
	for configuredBackend, capability := range profile.BackendCapabilities {
		if strings.EqualFold(strings.TrimSpace(configuredBackend), strings.TrimSpace(backend)) {
			return strings.ToLower(strings.TrimSpace(capability))
		}
	}
	return ""
}

func hasWorkspaceCapability(capabilities []string, required string) bool {
	required = strings.ToLower(strings.TrimSpace(required))
	if required == "" {
		return true
	}
	for _, capability := range capabilities {
		if strings.ToLower(strings.TrimSpace(capability)) == required {
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
