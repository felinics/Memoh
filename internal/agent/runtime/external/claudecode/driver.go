package claudecode

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/agent/runtime/external/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/runtimekind"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	// RuntimeType is the thread runtime type this driver serves.
	RuntimeType = string(runtimekind.ClaudeCode)

	// metadataSessionIDKey stores the Claude Code session id in session
	// runtime metadata; `--resume` carries it into the next turn's process.
	metadataSessionIDKey = "claude_session_id"

	// interruptGraceTimeout bounds how long an interrupted turn waits for the
	// CLI to flush its result before the process is terminated.
	interruptGraceTimeout = 10 * time.Second
)

// ApprovalService is the decision flow the driver routes approvals through.
type ApprovalService interface {
	approval.FlowService
	RegisterWaiter(approvalID string) func()
}

// Driver runs Claude Code turns over the stream-json wire protocol.
type Driver struct {
	workspaces  external.Workspaces
	agents      *botagents.Service
	credentials *agentcredential.Service
	approval    ApprovalService
	userInput   userinput.FlowService
	toolGateway toolmount.Gateway
	procs       *agentproc.Supervisor
	logger      *slog.Logger

	// launchers picks the CLI copy a process executes. Nil
	// means the toolkit launcher, unconditionally.
	launchers external.LauncherResolver

	commands *commandCatalog

	signInsMu sync.Mutex
	signIns   map[string]*signIn
}

// NewDriver constructs the Claude Code runtime driver.
func NewDriver(
	workspaces external.Workspaces,
	agents *botagents.Service,
	credentials *agentcredential.Service,
	approvalSvc ApprovalService,
	userInput userinput.FlowService,
	toolGateway toolmount.Gateway,
	launchers external.LauncherResolver,
	procs *agentproc.Supervisor,
	logger *slog.Logger,
) *Driver {
	d := &Driver{
		workspaces:  workspaces,
		agents:      agents,
		credentials: credentials,
		approval:    approvalSvc,
		userInput:   userInput,
		toolGateway: toolGateway,
		launchers:   launchers,
		procs:       procs,
		logger:      logger.With(slog.String("runtime", RuntimeType)),
		signIns:     map[string]*signIn{},
	}
	d.commands = newCommandCatalog(d.discoverCommands, d.logger)
	return d
}

// RuntimeType implements external.Driver.
func (*Driver) RuntimeType() string { return RuntimeType }

// Capabilities is what Claude Code declares (external.Caps).
var Capabilities = external.Caps{
	Dependency:       dependencyID,
	Command:          "claude",
	ForkNeedsRuntime: true,
}

// Caps implements external.Driver.
func (*Driver) Caps() external.Caps { return Capabilities }

func (d *Driver) resolveAgentConfig(ctx context.Context, botID, botAgentID string) (claudecfg.Config, error) {
	agent, err := d.agents.Get(ctx, botID, botAgentID)
	if err != nil {
		return claudecfg.Config{}, err
	}
	cfg, err := claudecfg.ParseAgentConfig(agent.Metadata)
	if err != nil {
		return claudecfg.Config{}, err
	}
	if cfg.Auth == claudecfg.AuthClaudeAccount {
		return cfg, nil
	}
	credential, err := d.credentials.ResolveForBotAgent(ctx, botID, botAgentID)
	if err != nil {
		return claudecfg.Config{}, external.CredentialError(err)
	}
	if credential.AuthKind != agentcredential.AuthKindAnthropicAPIKey {
		return claudecfg.Config{}, external.CredentialError(agentcredential.ErrIncompatible)
	}
	cfg.APIKey = credential.Secret["api_key"]
	return cfg, nil
}

// ModelCatalog returns the model vocabulary advertised by Claude Code's
// control-channel initialize response under this Agent's actual configuration.
func (d *Driver) ModelCatalog(ctx context.Context, request external.ModelCatalogRequest) (external.ModelCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cfg, err := d.resolveAgentConfig(ctx, request.BotID, request.BotAgentID)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	turn, stop, err := d.controlTurn(ctx, cfg, request.BotID, request.BotAgentID, request.ProjectPath)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	defer stop()
	response, err := turn.initialize(ctx)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	catalog := modelCatalogFromInitialize(cfg.Model, response)
	if request.ResolveDefaults {
		// The CLI's applied settings resolve workspace/account defaults. They
		// enrich the picker, but must not invalidate a usable native catalog.
		settings, err := turn.getSettings(ctx)
		if err != nil {
			d.logger.WarnContext(ctx, "claude model defaults unavailable", slog.Any("error", err))
		} else {
			catalog = modelCatalogFromInitialize(external.FirstNonEmpty(cfg.Model, settings.Applied.Model), response)
			if err := turn.resolveModelDefaults(ctx, &catalog, request.ModelID, settings); err != nil {
				d.logger.WarnContext(ctx, "claude selected model defaults unavailable", slog.Any("error", err))
			}
		}
	}
	return catalog, nil
}

// controlCLI is a CLI that only answers control requests. It is never given
// a prompt, so it reaches no model and writes no session. Like every process
// of the Agent it is held under the Supervisor, in a scope of its own, so a
// sign-out closes it with the rest and holds new ones off.
type controlCLI struct {
	sess  *cliSession
	input external.PromptInput
	d     *Driver
	// close retires the process and gives up the lease: it exits once idle.
	close func()
}

func (d *Driver) startControlCLI(ctx context.Context, cfg claudecfg.Config, botID, botAgentID, projectPath string) (*controlCLI, error) {
	if _, err := external.ContainerWorkspace(ctx, d.workspaces, botID, RuntimeType); err != nil {
		return nil, err
	}
	// A missing dependency returns as external.DependencyMissingError; it
	// must not be re-wrapped into a generic runtime error.
	launcher, err := agentproc.ResolveLauncher(ctx, d.launchers, botID, dependencyID, defaultLauncherPath)
	if err != nil {
		return nil, err
	}
	return d.leaseControlCLI(ctx, botID, botAgentID, func(ctx context.Context) (cliProcess, error) {
		client, err := d.workspaces.MCPClient(ctx, botID)
		if err != nil {
			return nil, err
		}
		return startCLI(context.WithoutCancel(ctx), client, botAgentID, projectPath, controlCLIArgs(cfg), cliEnv(cfg, botAgentID), launcher.Path)
	})
}

func (d *Driver) leaseControlCLI(ctx context.Context, botID, botAgentID string, start func(context.Context) (cliProcess, error)) (*controlCLI, error) {
	key := agentproc.Key{Runtime: RuntimeType, BotID: botID, BotAgentID: botAgentID, Scope: "control:" + uuid.NewString()}
	proc, release, err := d.procs.Lease(ctx, key, "", func(ctx context.Context) (agentproc.Process, string, error) {
		proc, err := start(ctx)
		if err != nil {
			return nil, "", err
		}
		sess := newCLISession(proc, d.logger)
		go sess.readLoop() //nolint:contextcheck // the session owns its read loop
		return sess, "", nil
	})
	if err != nil {
		return nil, err
	}
	return &controlCLI{
		sess:  proc.(*cliSession),
		input: external.PromptInput{BotID: botID, BotAgentID: botAgentID, Sink: external.EventSinkFunc(func(event.StreamEvent) {})},
		d:     d,
		close: func() {
			d.procs.Retire(func(k agentproc.Key) bool { return k == key })
			release()
		},
	}, nil
}

// turn attaches one exchange of control requests. The process counts as at
// work until the turn closes; between turns a drain closes it.
func (c *controlCLI) turn(ctx context.Context) *turnRunner {
	return c.sess.beginTurn(ctx, c.input, c.d.approval, c.d.approval.RegisterWaiter)
}

// controlTurn starts a control CLI for a single exchange; stop ends both.
func (d *Driver) controlTurn(ctx context.Context, cfg claudecfg.Config, botID, botAgentID, projectPath string) (*turnRunner, func(), error) {
	cli, err := d.startControlCLI(ctx, cfg, botID, botAgentID, projectPath)
	if err != nil {
		return nil, nil, err
	}
	turn := cli.turn(ctx)
	return turn, func() {
		turn.close()
		cli.close()
	}, nil
}

// Resolve only the selected model. Other models already carry initialize's
// capabilities; switching through them adds no validation information.
func (t *turnRunner) resolveModelDefaults(ctx context.Context, catalog *external.ModelCatalog, selected string, settings settingsResponse) error {
	selected = external.FirstNonEmpty(selected, catalog.ConfiguredModelID)
	if selected == "" {
		return nil
	}
	for i := range catalog.Models {
		model := &catalog.Models[i]
		if model.ID != selected && model.ResolvedModelID != selected {
			continue
		}
		if model.ID != settings.Applied.Model && model.ResolvedModelID != settings.Applied.Model {
			if _, err := t.callControl(ctx, "set_model", map[string]any{"model": model.ID}); err != nil {
				return err
			}
			var err error
			settings, err = t.getSettings(ctx)
			if err != nil {
				return err
			}
		}
		model.ResolvedModelID = external.FirstNonEmpty(settings.Applied.Model, model.ResolvedModelID)
		for _, effort := range model.ReasoningEfforts {
			if effort.ID == settings.Applied.Effort {
				model.DefaultReasoningEffort = effort.ID
			}
		}
		break
	}
	return nil
}

func modelCatalogFromInitialize(configuredModel string, response initializeResponse) external.ModelCatalog {
	configuredModel = strings.TrimSpace(configuredModel)
	defaultResolved := ""
	for _, model := range response.Models {
		if strings.TrimSpace(model.Value) == "default" {
			defaultResolved = strings.TrimSpace(model.ResolvedModel)
			break
		}
	}
	defaultModelID := "default"
	if defaultResolved != "" {
		for _, model := range response.Models {
			id := strings.TrimSpace(model.Value)
			if id != "" && id != "default" && strings.TrimSpace(model.ResolvedModel) == defaultResolved {
				defaultModelID = id
				break
			}
		}
	}
	models := make([]external.ModelOption, 0, len(response.Models))
	configuredFound := configuredModel == ""
	defaultAssigned := false
	for _, model := range response.Models {
		id := strings.TrimSpace(model.Value)
		// Prefer a concrete advertised alias when the CLI resolves its default.
		// Otherwise retain the CLI's own `default` model ID so a user can return
		// to it after a persisted pick; an omitted model reuses that old pick.
		if id == "" || (id == "default" && (configuredModel != "" || defaultModelID != "default")) {
			continue
		}
		resolved := strings.TrimSpace(model.ResolvedModel)
		if id == configuredModel || resolved == configuredModel {
			configuredFound = true
		}
		efforts := make([]external.ReasoningEffortOption, 0, len(model.SupportedEffortLevels))
		if model.SupportsEffort {
			for _, value := range model.SupportedEffortLevels {
				value = strings.TrimSpace(value)
				if value != "" {
					efforts = append(efforts, external.ReasoningEffortOption{ID: value, Name: value})
				}
			}
		}
		isDefault := configuredModel == "" && !defaultAssigned && id == defaultModelID
		defaultAssigned = defaultAssigned || isDefault
		models = append(models, external.ModelOption{
			ID:                         id,
			ResolvedModelID:            resolved,
			Name:                       external.FirstNonEmpty(model.DisplayName, id),
			Description:                strings.TrimSpace(model.Description),
			Default:                    isDefault,
			ReasoningEfforts:           efforts,
			UnavailablePermissionModes: unavailablePermissionModes(model),
		})
	}
	if !configuredFound {
		models = append([]external.ModelOption{{ID: configuredModel, Name: configuredModel}}, models...)
	}
	return external.ModelCatalog{Models: models, ConfiguredModelID: configuredModel}
}

// Prompt implements external.Driver. The conversation's CLI process stays
// between turns and serves this one; a process holding another native
// session, or none to resume, is replaced first.
func (d *Driver) Prompt(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
	cfg, err := d.resolveAgentConfig(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		// A credential failure is already a Failure; reading the Agent's
		// configuration is Memoh's own and stays private.
		return external.PromptResult{}, err
	}
	storedSessionID := strings.TrimSpace(external.MetadataString(input.RuntimeMetadata, metadataSessionIDKey))
	if input.ForceFreshRuntime {
		// Discuss turns re-inject the full composed context every round;
		// resuming the stored session would duplicate it on top of the
		// CLI's own saved history.
		storedSessionID = ""
	}
	sess, release, err := d.acquireSession(ctx, cfg, input, storedSessionID)
	if err != nil {
		return external.PromptResult{}, err
	}
	defer release()
	key := sessionKey(input)
	if input.ForceFreshRuntime {
		// The next round must not find this one's history in the process.
		defer d.procs.MarkBroken(key, sess)
	}

	turn := sess.beginTurn(ctx, input, d.approval, d.approval.RegisterWaiter)
	turn.userInput = d.userInput
	defer turn.close()
	unregisterToolEvents := toolmount.RegisterTurnSink(d.toolGateway.Contexts, input.BotID, input.ThreadID, input.RunID, turn.emit)
	defer unregisterToolEvents()
	if sess.mount == nil {
		toolmount.EmitUnavailableNotice(input.Sink, "the tool gateway could not be mounted")
	}

	capabilities := sess.capabilities
	turn.mu.Lock()
	turn.runtimeMetadata["claude_commands"] = capabilities.Commands
	turn.mu.Unlock()
	if input.Command != "" {
		available := claudeTurnCommands(capabilities.Commands, nil)
		if input.Command == "compact" {
			available = capabilities.Commands
		} else if !slices.ContainsFunc(available, func(command initializeCommand) bool { return command.Name == input.Command }) {
			skills, err := turn.reloadSkillNames(ctx)
			if err != nil {
				return external.PromptResult{}, external.Upstream(err)
			}
			available = claudeTurnCommands(capabilities.Commands, skills)
		}
		if !slices.ContainsFunc(available, func(command initializeCommand) bool { return command.Name == input.Command }) {
			return external.PromptResult{}, external.ErrCommandUnavailable
		}
		input.Prompt = "/" + input.Command
		if input.CommandArgs != "" {
			input.Prompt += " " + input.CommandArgs
		}
		input.ContextMarkdown = ""
	}
	if err := turn.applySettings(ctx, external.FirstNonEmpty(input.ModelID, cfg.Model), strings.TrimSpace(input.ReasoningEffort)); err != nil {
		return external.PromptResult{}, external.Upstream(err)
	}
	if err := turn.configureModes(ctx, cfg, capabilities.Models); err != nil {
		if external.IsFailureKind(err, external.FailureModeUnavailable) {
			return external.PromptResult{}, external.Fail(external.FailureModeUnavailable, err)
		}
		return external.PromptResult{}, external.Upstream(err)
	}

	line, err := userMessageLine(external.FirstNonEmpty(sess.heldSession(), storedSessionID), buildUserContent(input))
	if err != nil {
		return external.PromptResult{}, err
	}
	if err := turn.writeLine(line); err != nil {
		return external.PromptResult{}, external.Upstream(err)
	}
	stopSteering := turn.startSteering(ctx)
	defer stopSteering()

	select {
	case <-turn.done:
	case <-ctx.Done():
		turn.interrupt()
		// Grace window for the CLI to settle the interrupted turn; a wedged
		// process must not pin this turn (and the session's single active
		// slot) forever, so it is ended when the window closes. The process
		// is this conversation's alone, and its end finishes the turn.
		grace := time.NewTimer(interruptGraceTimeout)
		select {
		case <-turn.done:
		case <-grace.C:
			sess.end()
			<-turn.done
		}
		grace.Stop()
	}
	stopSteering()
	turn.close()

	result, resultErr := turn.buildResult(storedSessionID)
	if input.Command == "reload-skills" && resultErr == nil && ctx.Err() == nil {
		d.commands.forget(input.BotID)
	}
	if ctx.Err() != nil {
		// The application layer distinguishes stop from failure by context
		// state; an interrupted turn is not an error, and its partial output
		// persists without a failure marker.
		return result, nil
	}
	return result, resultErr
}

func sessionKey(input external.PromptInput) agentproc.Key {
	return agentproc.Key{Runtime: RuntimeType, BotID: input.BotID, BotAgentID: input.BotAgentID, Scope: input.ThreadID}
}

// fingerprint names what a CLI process is started from: a change to any of
// it needs a new process. Model, effort and permission mode change through
// the control channel and are not part of it. Secrets are hashed, never kept.
func fingerprint(cfg claudecfg.Config, launcher external.Launcher, workDir string, allowBypass bool) string {
	secret := sha256.Sum256([]byte(cfg.APIKey))
	return strings.Join([]string{
		string(cfg.Auth), strings.TrimSpace(cfg.BaseURL), hex.EncodeToString(secret[:]),
		launcher.Path, string(launcher.Source), launcher.Version,
		workDir, strconv.FormatBool(allowBypass),
	}, "\x00")
}

// acquireSession leases the conversation's CLI process, starting one when
// there is none, it was started from another configuration, or it holds
// another native session than the stored one (history cleared, a fresh
// discuss round). The caller must call the returned release when done.
func (d *Driver) acquireSession(ctx context.Context, cfg claudecfg.Config, input external.PromptInput, storedSessionID string) (*cliSession, func(), error) {
	info, err := external.ContainerWorkspace(ctx, d.workspaces, input.BotID, RuntimeType)
	if err != nil {
		return nil, nil, err
	}
	// Resolve the CLI copy before any session or tool work: a missing
	// dependency ends the turn here with external.DependencyMissingError,
	// which the application translates.
	launcher, err := agentproc.ResolveLauncher(ctx, d.launchers, input.BotID, dependencyID, defaultLauncherPath)
	if err != nil {
		return nil, nil, err
	}
	workDir := strings.TrimSpace(external.MetadataString(input.RuntimeMetadata, "project_path"))
	allowBypass := external.FirstNonEmpty(external.MetadataString(input.RuntimeMetadata, "permission_mode"), cfg.PermissionMode) == "bypassPermissions"
	key := sessionKey(input)
	start := func(ctx context.Context) (agentproc.Process, string, error) {
		client, err := d.workspaces.MCPClient(ctx, input.BotID)
		if err != nil {
			return nil, "", fmt.Errorf("workspace bridge for bot %s: %w", input.BotID, err)
		}
		resume := d.ensureResumableSession(ctx, client, input, storedSessionID)
		sess, err := d.startSession(ctx, client, info, cfg, launcher, input, resume, workDir, allowBypass)
		if err != nil {
			return nil, "", err
		}
		started := launcher
		started.Version = external.FirstNonEmpty(sess.version, launcher.Version)
		return sess, fingerprint(cfg, started, workDir, allowBypass), nil
	}
	for range 2 {
		proc, release, err := d.procs.Lease(ctx, key, fingerprint(cfg, launcher, workDir, allowBypass), start)
		if err != nil {
			// A start failure is already shown in the CLI's words.
			return nil, nil, err
		}
		sess := proc.(*cliSession)
		// A process holding no session yet starts one: the stored one was
		// gone when it started, or there is none.
		if held := sess.heldSession(); held == "" || held == storedSessionID {
			return sess, release, nil
		}
		release()
		d.procs.MarkBroken(key, sess)
	}
	return nil, nil, errs.New("claude process could not be replaced")
}

// startSession starts a CLI process for the conversation, resuming resume
// when set, with the Memoh tool route it keeps for its whole life.
func (d *Driver) startSession(ctx context.Context, client *bridge.Client, info bridge.WorkspaceInfo, cfg claudecfg.Config, launcher external.Launcher, input external.PromptInput, resume, workDir string, allowBypass bool) (*cliSession, error) {
	var sess *cliSession
	mcpConfig, mount := d.mountSessionTools(ctx, client, info, input, func() *turnRunner { return sess.currentTurn() })
	model, effort := external.FirstNonEmpty(input.ModelID, cfg.Model), strings.TrimSpace(input.ReasoningEffort)
	args := cliArgs(model, effort, resume, mcpConfig, allowBypass)
	// The process outlives the turn that started it.
	proc, err := startCLI(context.WithoutCancel(ctx), client, input.BotAgentID, workDir, args, cliEnv(cfg, input.BotAgentID), launcher.Path)
	if err != nil {
		if mount != nil {
			mount.Stop()
		}
		return nil, external.Upstream(err)
	}
	sess = newCLISession(proc, d.logger)
	sess.mount, sess.nativeID, sess.model, sess.effort = mount, resume, model, effort
	go sess.readLoop() //nolint:contextcheck // the session owns its read loop
	// Do not send input until the CLI has accepted the host control contract.
	raw, err := sess.callControl(ctx, "initialize", nil)
	if err == nil {
		err = json.Unmarshal(raw, &sess.capabilities)
	}
	if err != nil {
		_ = sess.Close()
		return nil, external.Upstream(err)
	}
	// The version decides whether a later turn may keep this process.
	if raw, err := sess.callControl(ctx, "get_binary_version", nil); err == nil {
		var answer struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &answer) == nil {
			sess.version = strings.TrimSpace(answer.Version)
			agentproc.ObserveVersion(ctx, d.launchers, input.BotID, dependencyID, sess.version)
		}
	}
	if sess.version != PinnedCLIVersion {
		// Unknown fields and messages are tolerated, so a drifted CLI usually
		// still works; warn rather than refuse.
		d.logger.WarnContext(ctx, "claude CLI version differs from the pinned wire contract",
			slog.String("bot_id", input.BotID),
			slog.String("cli_version", sess.version),
			slog.String("pinned", PinnedCLIVersion),
			slog.String("launcher", launcher.Path),
			slog.String("launcher_source", string(launcher.Source)),
		)
	}
	return sess, nil
}

// ensureResumableSession verifies the stored session's transcript still
// exists in the workspace before handing it to --resume. The transcript is
// the CLI's own durable record on the bot volume; when it is gone the turn
// starts a fresh session (the new id lands in the result's runtime metadata)
// and says so, instead of failing on a resume the CLI cannot honor.
func (d *Driver) ensureResumableSession(ctx context.Context, client transcriptFS, input external.PromptInput, storedSessionID string) string {
	if storedSessionID == "" {
		return ""
	}
	_, found, err := locateSessionTranscript(ctx, client, claudeHome(input.BotAgentID), storedSessionID)
	if err != nil {
		d.logger.WarnContext(ctx, "claude transcript lookup failed; attempting resume anyway",
			slog.String("bot_id", input.BotID), slog.String("session_id", input.ThreadID), slog.Any("error", err))
		return storedSessionID
	}
	if found {
		return storedSessionID
	}
	d.logger.WarnContext(ctx, "claude session transcript is gone; starting a fresh session",
		slog.String("bot_id", input.BotID), slog.String("session_id", input.ThreadID))
	if input.Sink != nil {
		input.Sink.EmitStreamEvent(event.StreamEvent{
			Type:       event.RuntimeNotice,
			NoticeKind: event.NoticeNativeHistoryLost,
			Delta:      "Claude Code could not resume this conversation's session and started a new one. It does not remember the earlier messages shown here.",
		})
	}
	return ""
}

// cliArgs builds the pinned stream-json invocation. model and effort are where
// the process starts; later turns change them through the control channel.
func cliArgs(model, effort, sessionID, mcpConfig string, allowBypass bool) []string {
	args := []string{
		"--output-format", "stream-json",
		"--verbose",
		"--input-format", "stream-json",
		"--include-partial-messages",
		"--permission-prompt-tool", "stdio",
		// Without it the CLI leaves the display to the API, and current models
		// then stream thinking blocks with empty text.
		"--thinking-display", "summarized",
	}
	if allowBypass {
		args = append(args, "--allow-dangerously-skip-permissions")
	}
	if model != "" {
		args = append(args, "--model", agentproc.ShellQuote(model))
	}
	if effort != "" {
		args = append(args, "--effort", agentproc.ShellQuote(effort))
	}
	if sessionID != "" {
		args = append(args, "--resume", agentproc.ShellQuote(sessionID))
	}
	if mcpConfig != "" {
		args = append(args, "--mcp-config", agentproc.ShellQuote(mcpConfig))
	}
	return args
}

// Startup alone runs the workspace's hooks and starts its MCP servers. They
// belong to sessions; a catalog read must not run the user's scripts.
func controlCLIArgs(cfg claudecfg.Config) []string {
	return append(cliArgs(cfg.Model, "", "", "", cfg.PermissionMode == "bypassPermissions"),
		"--settings", agentproc.ShellQuote(`{"disableAllHooks":true}`), "--strict-mcp-config")
}

// cliEnv builds the process environment: the Agent's own config directory on
// the bot volume plus the configured credential. A Claude account login is
// in that directory.
func cliEnv(cfg claudecfg.Config, botAgentID string) []string {
	env := agentproc.BaseEnv(
		"CLAUDE_CONFIG_DIR="+claudeHome(botAgentID),
		"CLAUDE_CODE_ENTRYPOINT=sdk-go",
		// Both callers require a container workspace. Declare that existing
		// isolation so the CLI accepts bypass mode when the container runs as root.
		"IS_SANDBOX=1",
	)
	if cfg.Auth != claudecfg.AuthAPIKey {
		return env
	}
	if cfg.BaseURL == "" {
		env = append(env, "ANTHROPIC_API_KEY="+cfg.APIKey)
	} else {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+cfg.APIKey, "ANTHROPIC_BASE_URL="+cfg.BaseURL)
	}
	return env
}

// buildUserContent assembles the user message blocks: the Memoh context
// document, the user's message, and inline images.
func buildUserContent(input external.PromptInput) []map[string]any {
	content := make([]map[string]any, 0, 2+len(input.Images))
	if text := strings.TrimSpace(input.ContextMarkdown); text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	content = append(content, map[string]any{"type": "text", "text": input.Prompt})
	for _, img := range input.Images {
		if len(img.Data) == 0 {
			continue
		}
		mime := img.MimeType
		if mime == "" {
			mime = "image/png"
		}
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": mime,
				"data":       base64Encode(img.Data),
			},
		})
	}
	return content
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
