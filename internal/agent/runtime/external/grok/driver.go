package grok

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/agent/runtime/external/grok/grokcfg"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/botagents"
)

// Driver runs Grok Build turns on the ACP core, as its Extension.
type Driver struct {
	host        *acp.Host
	workspaces  external.Workspaces
	agents      *botagents.Service
	credentials *agentcredential.Service
	logger      *slog.Logger
	// launchers picks the grok CLI copy to run per bot. Nil falls back to the
	// toolkit path.
	launchers external.LauncherResolver
	procs     *agentproc.Supervisor

	// signIns are the account sign-ins under way, by login id; signInGates
	// serialize starting, polling, and cancelling them per Agent.
	signInsMu   sync.Mutex
	signIns     map[string]*signIn
	signInGates map[agentproc.Key]chan struct{}
}

var _ acp.Extension = (*Driver)(nil)

func NewDriver(
	workspaces external.Workspaces,
	agents *botagents.Service,
	credentials *agentcredential.Service,
	approval acpclient.ToolApprovalService,
	userInput acpclient.UserInputService,
	toolGateway toolmount.Gateway,
	launchers external.LauncherResolver,
	procs *agentproc.Supervisor,
	logger *slog.Logger,
) *Driver {
	logger = logger.With(slog.String("runtime", RuntimeType))
	d := &Driver{
		workspaces:  workspaces,
		agents:      agents,
		credentials: credentials,
		launchers:   launchers,
		procs:       procs,
		logger:      logger,
		signIns:     map[string]*signIn{},
		signInGates: map[agentproc.Key]chan struct{}{},
	}
	d.host = acp.NewHost(d, acp.HostDeps{
		Logger:    logger,
		Procs:     procs,
		Gateway:   toolGateway,
		Approval:  approval,
		UserInput: userInput,
	})
	return d
}

// RuntimeType implements external.Driver.
func (*Driver) RuntimeType() string { return RuntimeType }

// Capabilities is what Grok Build declares (external.Caps).
var Capabilities = external.Caps{
	Dependency:              dependencyID,
	Command:                 "grok",
	ForkNeedsRuntime:        true,
	ForkAnchorKey:           metadataLastPromptIDKey,
	ReadCommandsRequireIdle: true,
}

// Caps implements external.Driver.
func (*Driver) Caps() external.Caps { return Capabilities }

// serverKey names the Agent's Grok process.
func serverKey(botID, botAgentID string) agentproc.Key {
	return agentproc.Key{Runtime: RuntimeType, BotID: botID, BotAgentID: botAgentID}
}

// agentProcesses matches every process of the Agent.
func agentProcesses(botID, botAgentID string) func(agentproc.Key) bool {
	return func(k agentproc.Key) bool {
		return k.Runtime == RuntimeType && k.BotID == botID && k.BotAgentID == botAgentID
	}
}

// agentConfig reads the Agent's configuration without its credential, which
// only a starting process needs.
func (d *Driver) agentConfig(ctx context.Context, botID, botAgentID string) (Config, error) {
	agent, err := d.agents.Get(ctx, botID, botAgentID)
	if err != nil {
		return Config{}, err
	}
	return grokcfg.ParseAgentConfig(agent.Metadata)
}

// fingerprint names what a Grok process is started from: a change to any of
// it needs a new process. Model, effort, and mode travel with each session or
// turn and are not part of it. The API key is hashed, never kept.
func fingerprint(cfg Config, launcher external.Launcher) string {
	key := sha256.Sum256([]byte(cfg.APIKey))
	return strings.Join([]string{
		string(cfg.Auth), hex.EncodeToString(key[:]), launcher.Path, string(launcher.Source), launcher.Version,
	}, "\x00")
}

// grokState is Grok's own state of a process the core started.
func grokState(proc *acp.Process) *grokProcess {
	state, _ := proc.State().(*grokProcess)
	return state
}

// Launch implements acp.Extension. One process serves every conversation of
// the Agent and its sign-in, account and catalog.
func (d *Driver) Launch(ctx context.Context, input acp.PromptInput) (acp.Launch, error) {
	botID, botAgentID := input.BotID, input.BotAgentID
	info, err := external.ContainerWorkspace(ctx, d.workspaces, botID, RuntimeType)
	if err != nil {
		return acp.Launch{}, err
	}
	cfg, err := d.agentConfig(ctx, botID, botAgentID)
	if err != nil {
		return acp.Launch{}, err
	}
	if cfg, err = d.apiKeyFor(ctx, botID, botAgentID, cfg); err != nil {
		return acp.Launch{}, err
	}
	launcher, err := agentproc.ResolveLauncher(ctx, d.launchers, botID, dependencyID, defaultLauncherPath)
	if err != nil {
		return acp.Launch{}, err
	}
	return acp.Launch{
		Key:         serverKey(botID, botAgentID),
		Fingerprint: fingerprint(cfg, launcher),
		Info:        info,
		Start: func(ctx context.Context) (acp.Started, error) {
			client, err := d.workspaces.MCPClient(ctx, botID)
			if err != nil {
				return acp.Started{}, fmt.Errorf("workspace bridge for bot %s: %w", botID, err)
			}
			if err := prepareHome(ctx, client, botAgentID, cfg); err != nil {
				return acp.Started{}, err
			}
			// Grok can also serve through a shared leader process that
			// outlives its stdio client; sessions and the login file must die
			// with this process. Memoh composes Grok's whole environment: its
			// state lives in GROK_HOME.
			proc, conn, err := acpclient.Launch(ctx, client, launcher.Path, []string{"agent", "--no-leader", "stdio"}, defaultProjectPath,
				acpclient.ProcessOptions{Env: processEnv(grokHome(botAgentID), cfg.APIKey), CleanEnv: true, Logger: d.logger}, acpclient.ClientCapabilities{})
			if err != nil {
				// What Grok said on its way down is the user's to read.
				return acp.Started{}, external.Upstream(err)
			}
			version, _ := conn.AgentMeta()["agentVersion"].(string)
			if version != pinnedVersion {
				// Grok's extensions are read as its pinned release defines
				// them. A drifted binary usually still answers them, so serve
				// and say so.
				d.logger.WarnContext(ctx, "grok CLI version differs from the release the driver was checked against",
					slog.String("bot_id", botID), slog.String("running", version), slog.String("pinned", pinnedVersion))
			}
			agentproc.ObserveVersion(ctx, d.launchers, botID, dependencyID, version)
			started := launcher
			started.Version = external.FirstNonEmpty(version, launcher.Version)
			return acp.Started{
				Proc:        proc,
				Conn:        conn,
				Bridge:      client,
				State:       newGrokProcess(conn),
				Fingerprint: fingerprint(cfg, started),
			}, nil
		},
	}, nil
}

// Session implements acp.Extension. Grok takes the permission mode only when
// a session is created or resumed, so a change to it opens the session
// again. The stored session id is the authority, not the session the process
// happens to hold: clearing a conversation's history clears the id.
func (*Driver) Session(_ context.Context, proc *acp.Process, input acp.PromptInput) (acp.SessionSpec, error) {
	stored := strings.TrimSpace(external.MetadataString(input.RuntimeMetadata, metadataSessionIDKey))
	mode := input.PermissionMode
	return acp.SessionSpec{
		ProjectPath: strings.TrimSpace(input.ProjectPath),
		Variant:     mode,
		Stored:      acp.StoredSession{ID: stored, Owned: stored != "", Known: true},
		Config: func(cfg *acpclient.SessionConfig) {
			// The session opened again is reported afresh.
			grokState(proc).forgetToolStatus(stored)
			cfg.Meta = sessionMeta(mode)
			cfg.MCPToolCall = mcpToolCall
			cfg.RuntimeOwnsApproval = true
			cfg.UserQuestions = userQuestions
			cfg.SessionGone = sessionGone
		},
	}, nil
}

// Opened implements acp.Extension; a Grok session needs nothing more.
func (*Driver) Opened(context.Context, *acp.Binding, acp.PromptInput) error { return nil }

// Configure implements acp.Extension: says whether Grok reached Memoh's tools,
// then moves the session onto the turn's model and effort.
func (d *Driver) Configure(ctx context.Context, b *acp.Binding, input acp.PromptInput) error {
	state := grokState(b.Process())
	// Grok reports a gateway it could not reach within a moment of the
	// session opening; a report that never comes does not hold the turn.
	if b.ToolsMounted() && input.Sink != nil {
		if status, ok := state.gatewayStatus(ctx, b.Session().ID(), gatewayStatusTimeout); ok && status.Status == mcpStatusUnavailable {
			toolmount.EmitUnavailableNotice(input.Sink, "Grok could not connect to Memoh tools: "+external.FirstNonEmpty(status.Reason, status.Detail, status.Status))
		}
	}
	cfg, err := d.agentConfig(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return err
	}
	// A catalog Grok did not report leaves the session's own defaults.
	catalog, _ := catalogFromState(state.modelState(ctx))
	return applyTurnConfig(ctx, b.Session(), catalog, external.FirstNonEmpty(input.ModelID, cfg.Model), strings.TrimSpace(input.ReasoningEffort), cfg.ReasoningEffort)
}

// turnInput is the ACP turn of a Grok turn.
func (d *Driver) turnInput(ctx context.Context, input external.PromptInput) (acp.PromptInput, error) {
	cfg, err := d.agentConfig(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return acp.PromptInput{}, err
	}
	turn := acp.TurnInput(input)
	turn.PermissionMode = permissionMode(input.RuntimeMetadata, cfg)
	return turn, nil
}

// Prompt implements external.Driver: one turn on the conversation's session.
func (d *Driver) Prompt(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
	turn, err := d.turnInput(ctx, input)
	if err != nil {
		return external.PromptResult{}, err
	}
	result, err := d.host.Prompt(ctx, turn)
	out := external.PromptResult{
		Output:     result.Output,
		Text:       result.Text,
		StopReason: result.StopReason,
		Usage:      usageFromMeta(result.Meta),
	}
	if out.Usage == nil {
		// A Grok that reports usage only where ACP defines it.
		out.Usage = result.Usage
	}
	// Discuss turns run on a throwaway session no later turn picks up.
	if result.SessionID != "" && !input.ForceFreshRuntime {
		out.RuntimeMetadata = map[string]any{metadataSessionIDKey: result.SessionID}
		// The prompt id is the turn's anchor: a fork is offered from the
		// latest.
		if promptID, _ := result.Meta["promptId"].(string); promptID != "" {
			out.AgentTurnID = promptID
			out.RuntimeMetadata[metadataLastPromptIDKey] = promptID
		}
	}
	switch {
	case err == nil:
		out.TurnCompleted = acp.TurnCompleted(result)
		return out, nil
	case ctx.Err() != nil:
		// A stop is not a failure: the partial transcript persists.
		return out, nil
	case errors.Is(err, acpclient.ErrImagePromptUnsupported), errors.Is(err, acpclient.ErrInvalidPromptImage), errors.Is(err, acpclient.ErrPromptRequired):
		// The application names these input errors itself.
		return out, err
	}
	return out, turnFailure(err)
}

// applyTurnConfig moves the session onto the turn's model and reasoning
// effort. The model goes first: its efforts are what the effort is checked
// against.
//
// The turn's own effort was picked for its model, so one the model lacks is
// an error. The Agent-wide effort was configured without a model in mind and
// applies only where the model offers it.
//
// Grok opens a session on the model and effort the process last switched to,
// in whichever conversation, so what the turn leaves unnamed is set to the
// default of the catalog Grok reported at start.
func applyTurnConfig(ctx context.Context, session *acpclient.Session, catalog []external.ModelOption, modelID, effort, agentEffort string) error {
	for _, model := range catalog {
		if modelID == "" && model.Default {
			modelID = model.ID
		}
	}
	// A session that exposes no selectable models keeps the one it has.
	if state := session.ModelState(); modelID != "" && state.Supported && state.CurrentModelID != modelID {
		if _, err := session.SetModel(ctx, modelID); err != nil {
			return err
		}
	}
	offered := func(id string) bool {
		return id != "" && slices.ContainsFunc(session.ReasoningState().Available, func(option acpclient.ReasoningEffortInfo) bool {
			return option.ID == id
		})
	}
	if effort == "" && offered(agentEffort) {
		effort = agentEffort
	}
	for _, model := range catalog {
		if effort == "" && model.ID == session.ModelState().CurrentModelID && offered(model.DefaultReasoningEffort) {
			effort = model.DefaultReasoningEffort
		}
	}
	if effort != "" && session.ReasoningState().CurrentEffort != effort {
		if _, err := session.SetReasoningEffort(ctx, effort); err != nil {
			return err
		}
	}
	return nil
}
