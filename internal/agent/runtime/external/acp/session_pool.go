// Package acp is Memoh's ACP core and its custom agents.
//
// The core (Host) runs ACP agent processes and the conversations' sessions
// on them. ACP lets one connection host several concurrent sessions, so an
// Agent keeps one agent process for all of its conversations, each on a
// session of its own. An Extension says how a kind of agent's process starts
// and what its sessions and turns carry beyond ACP.
//
// SessionPool is the Extension of user-supplied agents, configured by an
// agent profile and the bot's setup of it. An agent that cannot host
// concurrent sessions does not follow ACP and is not supported.
package acp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/external/acp/profile"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

var (
	// ErrAgentNotFound reports an agent id with no ACP profile.
	ErrAgentNotFound = errors.New("unknown ACP agent")
	// ErrAgentNotEnabled reports an agent the bot has not enabled.
	ErrAgentNotEnabled = errors.New("ACP agent is not enabled for this bot")
	// ErrAgentNotConfigured reports a bot setup the agent cannot run with:
	// an unsupported setup mode or workspace backend, or a missing managed
	// field.
	ErrAgentNotConfigured = errors.New("ACP agent is not configured for this bot")
	// ErrRuntimeOwnerMissing reports a runtime request without the account
	// that owns the runtime.
	ErrRuntimeOwnerMissing = errors.New("ACP runtime owner is missing")
)

const (
	stateIdle   = "idle"
	stateActive = "active"
)

// SessionPool runs the user-supplied ACP agents of bots.
type SessionPool struct {
	*Host

	logger     *slog.Logger
	workspaces external.Workspaces
	bots       botGetter
	instances  agentSetupResolver
	store      SessionDescriptorReader
	stateStore agentstate.RuntimeStateStore
}

var _ Extension = (*SessionPool)(nil)

type botGetter interface {
	Get(ctx context.Context, botID string) (bots.Bot, error)
}

// agentSetupResolver resolves the setup of the agent instance a session runs
// as. *botagents.Service satisfies it.
type agentSetupResolver interface {
	ResolveACPSetup(ctx context.Context, botID, botAgentID, provider string, botMetadata map[string]any) (acpprofile.AgentSetup, error)
}

// SessionDescriptor contains the minimal persisted session state required
// to launch an ACP runtime. The Chat domain supplies it through an adapter,
// with the session's identity (agent profile, project, owner) already
// resolved.
type SessionDescriptor struct {
	BotID                 string
	BotAgentID            string
	SessionType           string
	AgentID               string
	ProjectPath           string
	RuntimeOwnerAccountID string
	RuntimeMetadata       map[string]any
	IsACP                 bool
}

// SessionDescriptorReader resolves runtime metadata without exposing Chat
// domain types to the ACP runtime.
type SessionDescriptorReader interface {
	Get(ctx context.Context, sessionID string) (SessionDescriptor, error)
}

// SessionPreferenceWriter is implemented by the Chat adapter. ACP keeps its
// agent-owned IDs in runtime metadata, independently of native model UUIDs.
type SessionPreferenceWriter interface {
	SaveModelPreference(ctx context.Context, sessionID, modelID, effort string) error
}

// PromptInput carries one prompt (or runtime control call) for a chat
// session. Session metadata (agent, project path) is resolved from the
// session store when available.
type PromptInput struct {
	BotID                    string
	BotAgentID               string
	ChatID                   string
	SessionID                string
	RunID                    string
	SessionType              string
	RouteID                  string
	AgentID                  string
	ProjectPath              string
	ModelID                  string
	ReasoningEffort          string
	PermissionMode           string
	Prompt                   string
	Images                   []client.PromptImage
	AttachmentReferences     []string
	CanFallbackImagesToFiles bool
	ChannelIdentityID        string
	// SessionToken is consumed only by Prompt, where it flows into the
	// per-prompt tool context overlay. Ensure and SetModel ignore it.
	SessionToken           string //nolint:gosec // runtime session credential, not a hardcoded secret.
	CurrentPlatform        string
	ReplyTarget            string
	ConversationType       string
	CanRequestUserInput    bool
	SupportsImageInput     bool
	ToolOutputLimit        client.ToolOutputLimit
	ToolHTTPURL            string
	ContextURI             string
	ContextMarkdown        string
	RuntimeOwnerAccountID  string
	ForceFreshRuntime      bool
	ContextBudgetMaxTokens int
	// RuntimeMetadata is the conversation's runtime metadata as the turn
	// read it, for an extension that keeps its state there.
	RuntimeMetadata map[string]any
	Sink            client.EventSink
	RuntimeGuard    func(context.Context) error
	// RequiredCommand is the exact agent-command selector the admission layer
	// matched against a live runtime. The client Session re-validates it at
	// the dispatch boundary: a process replaced or updated between admission
	// and prompt must still advertise the command, or the turn fails with
	// ErrAgentCommandUnavailable instead of delivering stale slash text.
	RequiredCommand string
}

// ErrAgentCommandUnavailable reports that PromptInput.RequiredCommand is not
// advertised by the session that would actually receive the prompt.
var ErrAgentCommandUnavailable = client.ErrAgentCommandUnavailable

// RuntimeStatus describes the live state of an ACP agent process as exposed
// over the HTTP API.
type RuntimeStatus struct {
	RuntimeID             string                        `json:"runtime_id,omitempty"`
	SessionID             string                        `json:"session_id,omitempty"`
	AgentID               string                        `json:"agent_id,omitempty"`
	ProjectPath           string                        `json:"project_path,omitempty"`
	RuntimeOwnerAccountID string                        `json:"-"`
	State                 string                        `json:"state"`
	ACPSession            string                        `json:"acp_session_id,omitempty"`
	Models                *client.ModelState            `json:"models,omitempty"`
	Reasoning             *client.ReasoningState        `json:"reasoning,omitempty"`
	Modes                 *client.ModeState             `json:"modes,omitempty"`
	AvailableCommands     []client.AvailableCommandInfo `json:"available_commands,omitempty"`
	DefaultModelID        string                        `json:"default_model_id,omitempty"`
} // @name acpagent.RuntimeStatus

// Deps are the services an ACP turn reaches beyond its process. Optional
// ones (nil) leave the feature off: no runtime state store skips the
// configuration-generation guard, no session runtime skips the history-reset
// wait.
type Deps struct {
	Sessions       SessionDescriptorReader
	Setups         agentSetupResolver
	StateStore     agentstate.RuntimeStateStore
	SessionRuntime HistoryResetWaiter
	Tools          *mcp.ToolGatewayService
	ToolContexts   *mcp.ToolSessionContextStore
	Approval       client.ToolApprovalService
	UserInput      client.UserInputService
}

func NewSessionPool(log *slog.Logger, workspaces external.Workspaces, botService *bots.Service, procs *agentproc.Supervisor, deps Deps) *SessionPool {
	return newSessionPool(log, workspaces, botService, procs, deps)
}

func newSessionPool(log *slog.Logger, workspaces external.Workspaces, botService botGetter, procs *agentproc.Supervisor, deps Deps) *SessionPool {
	if log == nil {
		log = slog.Default()
	}
	log = log.With(slog.String("service", "acp_session_pool"), slog.String("runtime", RuntimeType))
	p := &SessionPool{
		logger:     log,
		workspaces: workspaces,
		bots:       botService,
		instances:  deps.Setups,
		store:      deps.Sessions,
		stateStore: deps.StateStore,
	}
	p.Host = NewHost(p, HostDeps{
		Logger:         log,
		Procs:          procs,
		Gateway:        toolmount.Gateway{Tools: deps.Tools, Contexts: deps.ToolContexts, Logger: log},
		Approval:       deps.Approval,
		UserInput:      deps.UserInput,
		SessionRuntime: deps.SessionRuntime,
	})
	return p
}

// storedSession reads the conversation's stored native session. The
// application records it after every turn, as it does a Codex thread, so an
// ID cleared from it means the conversation was reset.
func (p *SessionPool) storedSession(ctx context.Context, conversation string) (StoredSession, error) {
	if p.store == nil || conversation == "" {
		return StoredSession{}, nil
	}
	desc, err := p.store.Get(ctx, conversation)
	if err != nil {
		return StoredSession{}, fmt.Errorf("load ACP native session: %w", err)
	}
	id, owned := storedNativeSession(desc.RuntimeMetadata, conversation)
	return StoredSession{ID: id, Owned: owned, Known: true}, nil
}

// Launch implements Extension. The fingerprint covers every input of the
// start, so a changed setup, launch command or bot configuration generation
// replaces the process on the next lease.
func (p *SessionPool) Launch(ctx context.Context, input PromptInput) (Launch, error) {
	agentID := acpprofile.NormalizeAgentID(input.AgentID)
	if agentID == "" {
		return Launch{}, errs.New("ACP agent id is required")
	}
	setup, info, err := p.resolveAgentSetup(ctx, input.BotID, input.BotAgentID, agentID)
	if err != nil {
		return Launch{}, err
	}
	command, args, err := acpprofile.ResolveLaunch(setup)
	if err != nil {
		return Launch{}, fmt.Errorf("resolve ACP launch command: %w", err)
	}
	resolved, err := client.ResolveSessionContext(client.SessionContextInput{Backend: info.Backend})
	if err != nil {
		return Launch{}, fmt.Errorf("resolve ACP session context: %w", err)
	}
	epoch, err := p.loadRuntimeConfigEpoch(ctx, input.BotID, "")
	if err != nil {
		return Launch{}, err
	}
	managed := make([]string, 0, len(setup.Managed))
	for name, value := range setup.Managed {
		managed = append(managed, name+"="+value)
	}
	slices.Sort(managed)
	managedHash := sha256.Sum256([]byte(strings.Join(managed, "\x00")))
	fingerprint := strings.Join(append([]string{
		agentID, setup.Mode, hex.EncodeToString(managedHash[:]), info.Backend, strconv.FormatInt(epoch.Bot, 10), command,
	}, args...), "\x00")
	botID := input.BotID
	return Launch{
		Key:         agentproc.Key{Runtime: RuntimeType, BotID: botID, BotAgentID: strings.TrimSpace(input.BotAgentID)},
		Fingerprint: fingerprint,
		Info:        info,
		Start: func(ctx context.Context) (Started, error) {
			// A start may have waited out a reset: one resolved on an older
			// configuration generation must not run the agent at all.
			current := func(when string) error {
				final, err := p.loadRuntimeConfigEpoch(ctx, botID, "")
				if err != nil {
					return err
				}
				if final != epoch {
					return fmt.Errorf("%w: runtime configuration changed %s (expected=%+v, actual=%+v)",
						agentstate.ErrRuntimeConfigStale, when, epoch, final)
				}
				return nil
			}
			if err := current("before startup"); err != nil {
				return Started{}, err
			}
			bridgeClient, err := p.workspaces.MCPClient(ctx, botID)
			if err != nil {
				return Started{}, fmt.Errorf("connect workspace bridge: %w", err)
			}
			proc, conn, err := client.Launch(ctx, bridgeClient, command, args, resolved.ProjectPath, client.ProcessOptions{
				BotID:   botID,
				AgentID: agentID,
				Logger:  p.logger,
			}, client.ClientCapabilities{Workspace: true, Elicitation: p.userInput != nil})
			if err != nil {
				return Started{}, err
			}
			// The configuration may have changed while the process started:
			// revalidate its generation before the process serves anyone.
			if err := current("during startup"); err != nil {
				_ = proc.Close()
				return Started{}, err
			}
			return Started{
				Proc:   proc,
				Conn:   conn,
				Bridge: bridgeClient,
				// Custom adapters rarely advertise mcpCapabilities.http but
				// take an HTTP MCP server; offer Memoh's tools either way.
				ForceHTTPMCP: true,
			}, nil
		},
	}, nil
}

// Session implements Extension. A conversation's session opens in its
// project, under its owner and configuration generation; a change to any of
// them opens it again.
func (p *SessionPool) Session(ctx context.Context, _ *Process, input PromptInput) (SessionSpec, error) {
	spec := SessionSpec{ProjectPath: strings.TrimSpace(input.ProjectPath)}
	conversation := strings.TrimSpace(input.SessionID)
	if conversation == "" {
		return spec, nil
	}
	variant, err := p.sessionVariant(ctx, input)
	if err != nil {
		return SessionSpec{}, err
	}
	spec.Variant = variant
	if spec.Stored, err = p.storedSession(ctx, conversation); err != nil {
		return SessionSpec{}, err
	}
	return spec, nil
}

func (p *SessionPool) sessionVariant(ctx context.Context, input PromptInput) (string, error) {
	epoch, err := p.loadRuntimeConfigEpoch(ctx, input.BotID, strings.TrimSpace(input.SessionID))
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		external.FirstNonEmpty(input.RuntimeOwnerAccountID, input.ChannelIdentityID),
		strconv.FormatInt(epoch.Bot, 10), strconv.FormatInt(epoch.Session, 10),
	}, "\x00"), nil
}

// Opened implements Extension: a conversation's session takes its remembered
// selection.
func (p *SessionPool) Opened(ctx context.Context, b *Binding, input PromptInput) error {
	if b.conversation == "" || b.transient {
		return nil
	}
	// Opening took protocol round trips: a reset or configuration change
	// that landed meanwhile must not be served by this session.
	if final, err := p.sessionVariant(ctx, input); err != nil {
		return err
	} else if final != b.variant {
		return fmt.Errorf("%w: runtime configuration changed while opening the session", agentstate.ErrRuntimeConfigStale)
	}
	p.replayRememberedPair(ctx, b)
	return nil
}

// Configure implements Extension: the turn's mode, model and effort, then
// the conversation keeps the pair it now runs on.
func (p *SessionPool) Configure(ctx context.Context, b *Binding, input PromptInput) error {
	if err := applyPromptConfig(ctx, b.sess, input); err != nil {
		return err
	}
	p.persistModelPreference(ctx, b)
	return nil
}

// prepareInput validates required input fields and applies session metadata.
func (p *SessionPool) prepareInput(ctx context.Context, input PromptInput) (PromptInput, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return PromptInput{}, errs.New("session_id is required")
	}
	resolved, err := p.resolveSessionMetadata(ctx, input)
	if err != nil {
		return PromptInput{}, err
	}
	if strings.TrimSpace(resolved.BotID) == "" {
		return PromptInput{}, errs.New("bot_id is required")
	}
	if strings.TrimSpace(external.FirstNonEmpty(resolved.RuntimeOwnerAccountID, resolved.ChannelIdentityID)) == "" {
		return PromptInput{}, ErrRuntimeOwnerMissing
	}
	return resolved, nil
}

// Prompt sends a prompt to the conversation's session, opening one when the
// conversation has none.
func (p *SessionPool) Prompt(ctx context.Context, input PromptInput) (client.PromptResult, error) {
	input, err := p.prepareInput(ctx, input)
	if err != nil {
		return client.PromptResult{}, err
	}
	return p.Host.Prompt(ctx, input)
}

func (p *SessionPool) loadRuntimeConfigEpoch(ctx context.Context, botID, sessionID string) (agentstate.RuntimeConfigEpoch, error) {
	if p.stateStore == nil {
		return agentstate.RuntimeConfigEpoch{}, nil
	}
	epoch, err := p.stateStore.RuntimeConfigEpoch(ctx, botID, sessionID)
	if err != nil {
		return agentstate.RuntimeConfigEpoch{}, fmt.Errorf("load ACP runtime config epoch: %w", err)
	}
	return epoch, nil
}

// rememberedACPPair reads the session's persisted ACP (model, effort) pair
// from runtime_metadata. Missing or unreadable metadata is an empty pair,
// never an error: a new session keeps the agent's defaults.
func (p *SessionPool) rememberedACPPair(ctx context.Context, sessionID string) (string, string) {
	if p.store == nil || sessionID == "" {
		return "", ""
	}
	desc, err := p.store.Get(ctx, sessionID)
	if err != nil {
		p.logger.WarnContext(ctx, "load ACP remembered pair failed; using agent defaults",
			slog.String("session_id", sessionID), slog.Any("error", err))
		return "", ""
	}
	return external.MetadataString(desc.RuntimeMetadata, "acp_model_id"), external.MetadataString(desc.RuntimeMetadata, "acp_reasoning_effort")
}

// replayRememberedPair puts the conversation's remembered (model, effort)
// pair back over the agent's defaults; without it every new session would
// silently revert the picker choice. Model first: its answer can replace the
// reasoning options. A refused value keeps the agent's state.
func (p *SessionPool) replayRememberedPair(ctx context.Context, b *Binding) {
	model, effort := p.rememberedACPPair(ctx, b.conversation)
	if model != "" && strings.TrimSpace(b.sess.ModelState().CurrentModelID) != model {
		if _, err := b.sess.SetModel(ctx, model); err != nil {
			p.logger.WarnContext(ctx, "ACP remembered model rejected; keeping agent state",
				slog.String("session_id", b.conversation), slog.String("model_id", model), slog.Any("error", err))
		}
	}
	if effort != "" && strings.TrimSpace(b.sess.ReasoningState().CurrentEffort) != effort {
		if _, err := b.sess.SetReasoningEffort(ctx, effort); err != nil {
			p.logger.WarnContext(ctx, "ACP remembered reasoning effort rejected; keeping agent state",
				slog.String("session_id", b.conversation), slog.String("reasoning_effort", effort), slog.Any("error", err))
		}
	}
}

// applyPromptConfig applies the per-turn composer selection while the caller
// holds the operation lock. Model must be applied first because the
// authoritative response can replace the available reasoning options.
func applyPromptConfig(ctx context.Context, sess *client.Session, input PromptInput) error {
	if input.PermissionMode != "" && sess.ModeState().CurrentModeID != input.PermissionMode {
		if _, err := sess.SetMode(ctx, input.PermissionMode); err != nil {
			return err
		}
	}
	if model := strings.TrimSpace(input.ModelID); model != "" && strings.TrimSpace(sess.ModelState().CurrentModelID) != model {
		if _, err := sess.SetModel(ctx, model); err != nil {
			return err
		}
	}
	if effort := strings.TrimSpace(input.ReasoningEffort); effort != "" && strings.TrimSpace(sess.ReasoningState().CurrentEffort) != effort {
		if _, err := sess.SetReasoningEffort(ctx, effort); err != nil {
			return err
		}
	}
	return nil
}

// Commands reports the commands the conversation's session offers, opening
// it when it has none live and waiting a moment for a new session's first
// advertisement.
func (p *SessionPool) Commands(ctx context.Context, input PromptInput) ([]client.AvailableCommandInfo, error) {
	input, err := p.prepareInput(ctx, input)
	if err != nil {
		return nil, err
	}
	b, release, err := p.Acquire(ctx, input)
	if err != nil {
		return nil, err
	}
	defer release()
	return b.sess.AwaitCommands(ctx), nil
}

// Ensure opens (or reuses) the conversation's session without prompting it.
func (p *SessionPool) Ensure(ctx context.Context, input PromptInput) (RuntimeStatus, error) {
	return p.control(ctx, input, func(context.Context, *Binding) error { return nil })
}

// SetModel switches the conversation's model; an empty model resets it to
// the agent's default.
func (p *SessionPool) SetModel(ctx context.Context, input PromptInput, modelID string) (RuntimeStatus, error) {
	if strings.TrimSpace(modelID) == "" {
		return RuntimeStatus{}, client.ErrModelIDRequired
	}
	return p.control(ctx, input, func(ctx context.Context, b *Binding) error {
		model := strings.TrimSpace(modelID)
		return p.updateConfig(ctx, b,
			func(sess *client.Session) bool { return strings.TrimSpace(sess.ModelState().CurrentModelID) == model },
			func(ctx context.Context, sess *client.Session) error { _, err := sess.SetModel(ctx, model); return err })
	})
}

// SetReasoning switches the conversation's reasoning effort.
func (p *SessionPool) SetReasoning(ctx context.Context, input PromptInput, effort string) (RuntimeStatus, error) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return RuntimeStatus{}, client.ErrReasoningEffortRequired
	}
	return p.control(ctx, input, func(ctx context.Context, b *Binding) error {
		return p.updateConfig(ctx, b,
			func(sess *client.Session) bool {
				return strings.TrimSpace(sess.ReasoningState().CurrentEffort) == effort
			},
			func(ctx context.Context, sess *client.Session) error {
				_, err := sess.SetReasoningEffort(ctx, effort)
				return err
			})
	})
}

// SetMode switches the conversation's agent-declared mode. The mode stays
// with the session.
func (p *SessionPool) SetMode(ctx context.Context, input PromptInput, modeID string) (RuntimeStatus, error) {
	if strings.TrimSpace(modeID) == "" {
		return RuntimeStatus{}, client.ErrModeIDRequired
	}
	return p.control(ctx, input, func(ctx context.Context, b *Binding) error {
		return p.updateConfig(ctx, b,
			func(sess *client.Session) bool { return sess.ModeState().CurrentModeID == modeID },
			func(ctx context.Context, sess *client.Session) error { _, err := sess.SetMode(ctx, modeID); return err })
	})
}

// control runs fn on the conversation's session and reports its state
// afterwards.
func (p *SessionPool) control(ctx context.Context, input PromptInput, fn func(context.Context, *Binding) error) (RuntimeStatus, error) {
	input, err := p.prepareInput(ctx, input)
	if err != nil {
		return RuntimeStatus{}, err
	}
	b, release, err := p.Acquire(ctx, input)
	if err != nil {
		return RuntimeStatus{}, err
	}
	defer release()
	if err := fn(ctx, b); err != nil {
		return RuntimeStatus{}, err
	}
	return snapshot(ctx, b), nil
}

func (p *SessionPool) updateConfig(
	ctx context.Context,
	b *Binding,
	matches func(*client.Session) bool,
	update func(context.Context, *client.Session) error,
) error {
	defer b.lockOp()()
	if matches(b.sess) {
		p.persistModelPreference(ctx, b)
		return nil
	}
	err := update(ctx, b.sess)
	switch {
	case err == nil:
		p.persistModelPreference(ctx, b)
		return nil
	case isPromptConfigSelectionError(err):
		// Refused before anything reached the agent: keep the session.
		return err
	}
	// The agent may have taken the value while Memoh never received its new
	// configuration, a cancelled request included, so the cached state is no
	// longer trustworthy.
	b.discard()
	if configUpdateCanceled(ctx, err) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrRuntimeConfigUpdateFailed, err)
}

// AgentOptions reports what a new conversation with the Agent could choose:
// its models, reasoning efforts, modes and commands, from the Agent's own
// options session; what the user picks travels with the conversation's
// creation. A model's reasoning efforts are known only once it is selected,
// so the options of a named model come from a throwaway session that selects
// it: the options session every draft reads its defaults from never changes.
func (p *SessionPool) AgentOptions(ctx context.Context, input PromptInput) (RuntimeStatus, error) {
	if strings.TrimSpace(input.BotID) == "" {
		return RuntimeStatus{}, errs.New("bot_id is required")
	}
	r, release, err := p.Process(ctx, input)
	if err != nil {
		return RuntimeStatus{}, err
	}
	defer release()
	model := strings.TrimSpace(input.ModelID)
	var b *Binding
	if model == "" {
		b, err = r.optionsSession(ctx, input)
	} else {
		b, err = r.probeSession(ctx, input)
	}
	if err != nil {
		return RuntimeStatus{}, err
	}
	if model != "" {
		defer b.close()
	}
	if model != "" && strings.TrimSpace(b.sess.ModelState().CurrentModelID) != model {
		if _, err := b.sess.SetModel(ctx, model); err != nil {
			return RuntimeStatus{}, err
		}
	}
	out := snapshot(ctx, b)
	out.SessionID = ""
	return out, nil
}

// RuntimeStatus reports the conversation's live session, or an idle skeleton
// when it has none.
func (p *SessionPool) RuntimeStatus(input PromptInput) RuntimeStatus {
	if b := p.holder(input.SessionID); b != nil && b.rt.key.BotID == strings.TrimSpace(input.BotID) {
		return status(b)
	}
	return RuntimeStatus{
		SessionID:   strings.TrimSpace(input.SessionID),
		AgentID:     strings.TrimSpace(input.AgentID),
		ProjectPath: strings.TrimSpace(input.ProjectPath),
		State:       stateIdle,
	}
}

// snapshot is the session's state for a caller that shows it: a newly opened
// session announces its commands a moment after it opens.
func snapshot(ctx context.Context, b *Binding) RuntimeStatus {
	b.sess.AwaitCommands(ctx)
	return status(b)
}

func status(b *Binding) RuntimeStatus {
	state := stateIdle
	if b.busy.Load() {
		state = stateActive
	}
	b.mu.Lock()
	defaultModel := b.defaultModelID
	b.mu.Unlock()
	models, reasoning, modes, commands := b.sess.ConfigurationState()
	return RuntimeStatus{
		RuntimeID:             b.id,
		SessionID:             b.conversation,
		AgentID:               acpprofile.AgentACPID,
		ProjectPath:           b.project,
		RuntimeOwnerAccountID: b.owner,
		State:                 state,
		ACPSession:            b.sess.ID(),
		Models:                &models,
		Reasoning:             &reasoning,
		Modes:                 &modes,
		AvailableCommands:     commands,
		DefaultModelID:        defaultModel,
	}
}

func (p *SessionPool) resolveSessionMetadata(ctx context.Context, input PromptInput) (PromptInput, error) {
	if p.store == nil {
		return input, nil
	}
	sess, err := p.store.Get(ctx, input.SessionID)
	if err != nil {
		return input, fmt.Errorf("load ACP session metadata: %w", err)
	}
	if !sess.IsACP {
		return input, errs.New(fmt.Sprintf("session %s is not an ACP agent session", input.SessionID))
	}
	if input.BotID != "" && sess.BotID != "" && input.BotID != sess.BotID {
		return input, errs.New(fmt.Sprintf("session %s does not belong to bot %s", input.SessionID, input.BotID))
	}
	if input.BotID == "" {
		input.BotID = sess.BotID
	}
	input.SessionType = sess.SessionType
	input.BotAgentID = strings.TrimSpace(sess.BotAgentID)
	input.AgentID = strings.TrimSpace(sess.AgentID)
	if projectPath := strings.TrimSpace(sess.ProjectPath); projectPath != "" {
		input.ProjectPath = projectPath
	}
	if ownerID := strings.TrimSpace(sess.RuntimeOwnerAccountID); ownerID != "" {
		input.RuntimeOwnerAccountID = ownerID
	}
	return input, nil
}

func (p *SessionPool) resolveAgentSetup(ctx context.Context, botID, botAgentID, agentID string) (acpprofile.AgentSetup, bridge.WorkspaceInfo, error) {
	agentID = acpprofile.NormalizeAgentID(agentID)
	if _, ok := acpprofile.Lookup(agentID); !ok {
		return acpprofile.AgentSetup{}, bridge.WorkspaceInfo{}, fmt.Errorf("%w: %q", ErrAgentNotFound, agentID)
	}
	bot, err := p.bots.Get(ctx, botID)
	if err != nil {
		return acpprofile.AgentSetup{}, bridge.WorkspaceInfo{}, fmt.Errorf("load bot ACP setup: %w", err)
	}
	if strings.TrimSpace(bot.Status) == bots.BotStatusDeleting {
		return acpprofile.AgentSetup{}, bridge.WorkspaceInfo{}, errs.New(fmt.Sprintf("bot %s is not ready for ACP runtime (status %q)", botID, bot.Status))
	}
	setup := acpprofile.ParseAgentSetup(bot.Metadata, agentID)
	if p.instances != nil {
		setup, err = p.instances.ResolveACPSetup(ctx, botID, botAgentID, agentID, bot.Metadata)
		if err != nil {
			return acpprofile.AgentSetup{}, bridge.WorkspaceInfo{}, fmt.Errorf("load ACP agent setup: %w", err)
		}
	}
	if !setup.Enabled {
		return acpprofile.AgentSetup{}, bridge.WorkspaceInfo{}, fmt.Errorf("%w: %q", ErrAgentNotEnabled, agentID)
	}
	workspaceInfo, err := p.workspaces.WorkspaceInfo(ctx, botID)
	if err != nil {
		return acpprofile.AgentSetup{}, bridge.WorkspaceInfo{}, fmt.Errorf("resolve workspace: %w", err)
	}
	if err := acpprofile.CheckSetup(setup); err != nil {
		return acpprofile.AgentSetup{}, bridge.WorkspaceInfo{}, fmt.Errorf("%w: %w", ErrAgentNotConfigured, err)
	}
	if err := external.RequireContainerWorkspace(workspaceInfo, RuntimeType); err != nil {
		return acpprofile.AgentSetup{}, bridge.WorkspaceInfo{}, fmt.Errorf("%w: %w", ErrAgentNotConfigured, err)
	}
	return setup, workspaceInfo, nil
}

// persistModelPreference stores the conversation's live (model, effort)
// pair; the caller holds the operation lock through the change and this
// write.
func (p *SessionPool) persistModelPreference(ctx context.Context, b *Binding) {
	writer, ok := p.store.(SessionPreferenceWriter)
	if !ok || b.conversation == "" || b.transient {
		return
	}
	if err := writer.SaveModelPreference(ctx, b.conversation, strings.TrimSpace(b.sess.ModelState().CurrentModelID), strings.TrimSpace(b.sess.ReasoningState().CurrentEffort)); err != nil {
		p.logger.WarnContext(ctx, "persist ACP model preference", slog.String("session_id", b.conversation), slog.Any("error", err))
	}
}
