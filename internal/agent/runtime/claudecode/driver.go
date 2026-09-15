package claudecode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/agentstate"
	"github.com/felinics/memoh/internal/agent/runtime/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
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

// BridgeSource resolves the workspace bridge client for a bot.
type BridgeSource interface {
	MCPClient(ctx context.Context, botID string) (*bridge.Client, error)
	WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error)
}

// ApprovalService is the decision flow the driver routes approvals through.
type ApprovalService interface {
	approval.FlowService
	RegisterWaiter(approvalID string) func()
}

// Driver runs Claude Code turns over the stream-json wire protocol.
type Driver struct {
	bridges     BridgeSource
	agents      *botagents.Service
	credentials *agentcredential.Service
	approval    ApprovalService
	userInput   userinput.FlowService
	stateStore  agentstate.SessionStateStore
	toolGateway toolmount.Gateway
	logger      *slog.Logger

	// launchers picks the CLI copy each turn executes. Nil
	// means the toolkit launcher, unconditionally.
	launchers external.LauncherResolver
}

// NewDriver constructs the Claude Code runtime driver.
func NewDriver(
	bridges BridgeSource,
	agents *botagents.Service,
	credentials *agentcredential.Service,
	approvalSvc ApprovalService,
	stateStore agentstate.SessionStateStore,
	toolGateway toolmount.Gateway,
	logger *slog.Logger,
) *Driver {
	return &Driver{
		bridges:     bridges,
		agents:      agents,
		credentials: credentials,
		approval:    approvalSvc,
		stateStore:  stateStore,
		toolGateway: toolGateway,
		logger:      logger.With(slog.String("runtime", RuntimeType)),
	}
}

// RuntimeType implements external.Driver.
func (*Driver) RuntimeType() string { return RuntimeType }

func (d *Driver) SetUserInputService(service userinput.FlowService) { d.userInput = service }

func (d *Driver) resolveAgentConfig(ctx context.Context, botID, botAgentID string) (claudecfg.Config, error) {
	agent, err := d.agents.Get(ctx, botID, botAgentID)
	if err != nil {
		return claudecfg.Config{}, err
	}
	cfg, err := claudecfg.ParseAgentConfig(agent.Metadata)
	if err != nil {
		return claudecfg.Config{}, err
	}
	if cfg.Auth == claudecfg.AuthWorkspace {
		return cfg, nil
	}
	credential, err := d.credentials.ResolveForBotAgent(ctx, botID, botAgentID)
	if err != nil {
		return claudecfg.Config{}, external.CredentialError(err)
	}
	switch cfg.Auth {
	case claudecfg.AuthAPIKey:
		if credential.AuthKind != agentcredential.AuthKindAnthropicAPIKey {
			return claudecfg.Config{}, external.CredentialError(agentcredential.ErrIncompatible)
		}
		cfg.APIKey = credential.Secret["api_key"]
	case claudecfg.AuthOAuthToken:
		if credential.AuthKind != agentcredential.AuthKindClaudeCodeOAuth {
			return claudecfg.Config{}, external.CredentialError(agentcredential.ErrIncompatible)
		}
		cfg.OAuthToken = credential.Secret["oauth_token"]
	}
	return cfg, nil
}

// ModelCatalog returns the model vocabulary advertised by Claude Code's
// control-channel initialize response under this Agent's actual configuration.
func (d *Driver) ModelCatalog(ctx context.Context, request external.ModelCatalogRequest) (external.ModelCatalog, error) {
	botID, botAgentID, projectPath := request.BotID, request.BotAgentID, request.ProjectPath
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cfg, err := d.resolveAgentConfig(ctx, botID, botAgentID)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	client, err := d.bridges.MCPClient(ctx, botID)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	workspaceInfo, err := d.bridges.WorkspaceInfo(ctx, botID)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	if err := external.RequireContainerWorkspace(workspaceInfo, RuntimeType); err != nil {
		return external.ModelCatalog{}, err
	}
	// A missing dependency returns as agent_dependency_missing feedback; it
	// must not be re-wrapped into a generic runtime error.
	launcher, lease, err := d.acquireLauncher(ctx, botID)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	defer lease.Release()
	proc, err := startCLI(ctx, client, projectPath, cliArgs(cfg, external.PromptInput{}, "", ""), cliEnv(cfg), launcher)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	defer func() { _ = proc.Close() }()

	input := external.PromptInput{
		BotID:      botID,
		BotAgentID: botAgentID,
		Sink:       external.EventSinkFunc(func(event.StreamEvent) {}),
	}
	turn := newTurnRunner(ctx, input, proc, d.approval, d.approval.RegisterWaiter, d.logger)
	turn.onCLIVersion = d.versionObserver(botID)
	defer turn.close()
	go turn.readLoop() //nolint:contextcheck // turnRunner owns the control-channel lifetime
	raw, err := turn.callControl(ctx, "initialize", nil)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	lease.Release() // The initialized CLI now owns the inherited payload lock.
	var response initializeResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return external.ModelCatalog{}, fmt.Errorf("decode claude initialize response: %w", err)
	}
	catalog := modelCatalogFromInitialize(cfg.Model, response)
	if request.ResolveDefaults {
		// The CLI's applied settings resolve workspace/account defaults. They
		// enrich the picker, but must not invalidate a usable native catalog.
		settings, err := turn.getSettings(ctx)
		if err != nil {
			d.logger.Warn("claude model defaults unavailable", slog.Any("error", err))
		} else {
			catalog = modelCatalogFromInitialize(firstNonEmpty(cfg.Model, settings.Applied.Model), response)
			if err := turn.resolveModelDefaults(ctx, &catalog, request.ModelID, settings); err != nil {
				d.logger.Warn("claude selected model defaults unavailable", slog.Any("error", err))
			}
		}
	}
	proc.CloseStdin()
	return catalog, nil
}

// Resolve only the selected model. Other models already carry initialize's
// capabilities; switching through them adds no validation information.
func (t *turnRunner) resolveModelDefaults(ctx context.Context, catalog *external.ModelCatalog, selected string, settings settingsResponse) error {
	selected = firstNonEmpty(selected, catalog.ConfiguredModelID)
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
		model.ResolvedModelID = firstNonEmpty(settings.Applied.Model, model.ResolvedModelID)
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
			Name:                       firstNonEmpty(model.DisplayName, id),
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

// Prompt implements external.Driver: one CLI process serves one turn, resumed
// from the session id in runtime metadata.
func (d *Driver) Prompt(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
	cfg, err := d.resolveAgentConfig(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		if apperror.CodeOf(err) != "" {
			return external.PromptResult{}, err
		}
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	client, err := d.bridges.MCPClient(ctx, input.BotID)
	if err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	workspaceInfo, err := d.bridges.WorkspaceInfo(ctx, input.BotID)
	if err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	if err := external.RequireContainerWorkspace(workspaceInfo, RuntimeType); err != nil {
		return external.PromptResult{}, err
	}
	if err := external.PrepareDependency(ctx, d.launchers, input.BotID, dependencyID); err != nil {
		var missing *external.DependencyMissingError
		if errors.As(err, &missing) {
			return external.PromptResult{}, dependencyMissingFeedback(missing)
		}
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	// Resolve the CLI copy before any session or tool work: a missing
	// dependency ends the turn here with agent_dependency_missing feedback,
	// already in its final user-facing shape.
	launcher, lease, err := d.acquireLauncher(ctx, input.BotID)
	if err != nil {
		return external.PromptResult{}, err
	}

	defer lease.Release()

	storedSessionID := strings.TrimSpace(metadataString(input.RuntimeMetadata, metadataSessionIDKey))
	if input.ForceFreshRuntime {
		// Discuss turns re-inject the full composed context every round;
		// resuming the stored session would duplicate it on top of the
		// CLI's own saved history.
		storedSessionID = ""
	}
	workDir := strings.TrimSpace(metadataString(input.RuntimeMetadata, "project_path"))
	storedSessionID, err = d.ensureResumableSession(ctx, client, input, storedSessionID)
	if err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeSessionHistoryInconsistent, err, nil)
	}
	// The mount must survive a caller disconnect exactly as long as the CLI
	// process does (the interrupt handshake still runs tools).
	mountCtx, cancelMount := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelMount()
	mcpConfig, toolsMount, err := d.mountTurnTools(mountCtx, client, workspaceInfo, input)
	if err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	defer toolsMount.Stop()
	args := cliArgs(cfg, input, storedSessionID, mcpConfig)
	env := cliEnv(cfg)

	// The process must survive a caller disconnect long enough for the
	// interrupt handshake below.
	proc, err := startCLI(context.WithoutCancel(ctx), client, workDir, args, env, launcher)
	if err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	defer func() { _ = proc.Close() }()

	turn := newTurnRunner(ctx, input, proc, d.approval, d.approval.RegisterWaiter, d.logger)
	turn.onCLIVersion = d.versionObserver(input.BotID)
	turn.userInput = d.userInput
	defer turn.close()
	go turn.readLoop() //nolint:contextcheck // turnRunner owns the control-channel lifetime
	unregisterToolEvents := toolmount.RegisterTurnSink(d.toolGateway.Contexts, input.BotID, input.ThreadID, input.RunID, turn.emit)
	defer unregisterToolEvents()

	// Do not send input until the CLI has accepted the host control contract.
	initialized, err := turn.callControl(ctx, "initialize", nil)
	if err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	lease.Release() // Startup is fenced; the process lock survives turn draining.
	var capabilities initializeResponse
	if err := json.Unmarshal(initialized, &capabilities); err != nil {
		return external.PromptResult{}, err
	}
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
				return external.PromptResult{}, apperror.Wrap(apperror.CodeRuntimeControlFailed, err, nil)
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
	if err := turn.configureModes(ctx, cfg, capabilities.Models); err != nil {
		if errors.Is(err, external.ErrModeUnavailable) {
			return external.PromptResult{}, apperror.Wrap(apperror.CodeRuntimeControlModeUnavailable, err, nil)
		}
		return external.PromptResult{}, apperror.Wrap(apperror.CodeRuntimeControlFailed, err, nil)
	}

	content := buildUserContent(input)
	line, err := userMessageLine(storedSessionID, content)
	if err != nil {
		return external.PromptResult{}, err
	}
	if err := turn.writeLine(line); err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	stopSteering := turn.startSteering(ctx)
	defer stopSteering()

	drainTimeout := interruptGraceTimeout
	select {
	case <-turn.done:
	case <-ctx.Done():
		deadline := time.Now().Add(interruptGraceTimeout)
		turn.interrupt()
		// Grace window for the CLI to flush its result after the interrupt;
		// a wedged process must not pin this turn (and the session's single
		// active slot) forever, so terminate it when the window closes.
		grace := time.NewTimer(time.Until(deadline))
		select {
		case <-turn.done:
		case <-proc.Done():
		case <-grace.C:
			_ = proc.Close()
		}
		grace.Stop()
		drainTimeout = time.Until(deadline)
	}
	// End the input stream so the process exits cleanly. The CLI's durable
	// writes may trail its result, so the teardown outcome is observed
	// separately from the protocol outcome the CLI already reported.
	stopSteering()
	turn.observeExit(drainCLI(proc, drainTimeout))
	// Process completion closes stdout; finish reading its buffered result and
	// transport outcome before taking the immutable turn snapshot.
	<-turn.readDone
	turn.close()

	result, resultErr := turn.buildResult(storedSessionID)
	if resultErr == nil && result.TurnCompleted {
		// A completed turn checkpoints regardless of a racing stop: the round
		// commits (as succeeded or aborted-after-completion) either way, and
		// its publication head must have a staged snapshot to point at.
		// A teardown that timed out, lost transport, or exited non-zero
		// leaves the transcript's completeness unknown; the round still
		// commits, but its head publishes a reset instead of a snapshot.
		if turn.exitFailed() {
			d.logger.Warn("claude checkpoint skipped after an unclean exit; the round publishes a reset head",
				slog.String("bot_id", input.BotID), slog.String("session_id", input.ThreadID))
			result.Checkpoint = external.CheckpointDeclined
		} else {
			result.Checkpoint = d.stageTurnCheckpoint(ctx, client, input, result)
		}
	}
	if ctx.Err() != nil {
		// The application layer distinguishes stop from failure by context
		// state; an interrupted turn is not an error, and its partial output
		// persists without a failure marker.
		return result, nil
	}
	return result, resultErr
}

// stageTurnCheckpoint stages the finished turn's transcript into the runtime
// session store and reports the outcome for the round's publication head.
func (d *Driver) stageTurnCheckpoint(ctx context.Context, fs checkpointFS, input external.PromptInput, result external.PromptResult) external.CheckpointOutcome {
	// The turn may have minted a new session id; staging must see it. The
	// caller's context may already be canceled by a stop — staging still runs
	// under the persistence fence the turn context carries.
	stageMeta := make(map[string]any, len(input.RuntimeMetadata)+len(result.RuntimeMetadata))
	for key, value := range input.RuntimeMetadata {
		stageMeta[key] = value
	}
	for key, value := range result.RuntimeMetadata {
		stageMeta[key] = value
	}
	staged, err := d.stageWithFS(context.WithoutCancel(ctx), fs, checkpointRequest{
		BotID:           input.BotID,
		ThreadID:        input.ThreadID,
		RunID:           input.RunID,
		RuntimeMetadata: stageMeta,
	})
	if err != nil {
		d.logger.Warn("claude checkpoint staging failed; the round publishes a reset head",
			slog.String("bot_id", input.BotID), slog.String("session_id", input.ThreadID), slog.Any("error", err))
		return external.CheckpointDeclined
	}
	if staged {
		return external.CheckpointStaged
	}
	return external.CheckpointDeclined
}

// ensureResumableSession verifies the stored session's transcript still
// exists in the workspace before handing it to --resume. A missing transcript
// is restored from the database checkpoint when one matches; otherwise the
// turn starts a fresh session (the new id lands in the result's runtime
// metadata) instead of failing on a resume the CLI cannot honor.
func (d *Driver) ensureResumableSession(ctx context.Context, client checkpointFS, input external.PromptInput, storedSessionID string) (string, error) {
	if storedSessionID == "" {
		return "", nil
	}
	_, found, err := locateSessionTranscript(ctx, client, storedSessionID)
	if err != nil {
		d.logger.Warn("claude transcript lookup failed; attempting resume anyway",
			slog.String("bot_id", input.BotID), slog.String("session_id", input.ThreadID), slog.Any("error", err))
		return storedSessionID, nil
	}
	if found {
		return storedSessionID, nil
	}
	// The workspace transcript is gone; the database checkpoint decides which
	// session resumes. Its id wins over the stored metadata id — metadata is a
	// separate store that can lag behind the published checkpoint, and
	// preferring the stale id here used to discard a perfectly good
	// checkpoint and silently start the conversation over.
	restoredID, err := d.restoreSessionCheckpoint(ctx, client, input.BotID, input.ThreadID)
	if err != nil {
		return "", fmt.Errorf("restore claude checkpoint: %w", err)
	}
	if restoredID != "" {
		if restoredID != storedSessionID {
			d.logger.Warn("claude checkpoint names a different session than runtime metadata; resuming the checkpointed session",
				slog.String("bot_id", input.BotID), slog.String("session_id", input.ThreadID),
				slog.String("stored", storedSessionID), slog.String("checkpoint", restoredID))
		} else {
			d.logger.Info("claude transcript restored from database checkpoint",
				slog.String("bot_id", input.BotID), slog.String("session_id", input.ThreadID))
		}
		return restoredID, nil
	}
	d.logger.Warn("claude session transcript is gone and no checkpoint exists; starting a fresh session",
		slog.String("bot_id", input.BotID), slog.String("session_id", input.ThreadID))
	return "", nil
}

// cliArgs builds the pinned stream-json invocation.
func cliArgs(cfg claudecfg.Config, input external.PromptInput, sessionID, mcpConfig string) []string {
	args := []string{
		"--output-format", "stream-json",
		"--verbose",
		"--input-format", "stream-json",
		"--include-partial-messages",
		"--permission-prompt-tool", "stdio",
	}
	if firstNonEmpty(metadataString(input.RuntimeMetadata, "permission_mode"), cfg.PermissionMode) == "bypassPermissions" {
		args = append(args, "--allow-dangerously-skip-permissions")
	}
	if model := firstNonEmpty(input.ModelID, cfg.Model); model != "" {
		args = append(args, "--model", shellQuote(model))
	}
	if effort := strings.TrimSpace(input.ReasoningEffort); effort != "" {
		args = append(args, "--effort", shellQuote(effort))
	}
	if sessionID != "" {
		args = append(args, "--resume", shellQuote(sessionID))
	}
	if mcpConfig != "" {
		args = append(args, "--mcp-config", shellQuote(mcpConfig))
	}
	return args
}

// cliEnv builds the process environment: durable state under the bot volume
// plus the configured credential.
func cliEnv(cfg claudecfg.Config) []string {
	env := []string{
		"PATH=" + containerPath,
		"HOME=" + defaultProjectPath,
		"CLAUDE_CONFIG_DIR=" + configDir,
		"CLAUDE_CODE_ENTRYPOINT=sdk-go",
		// Both callers require a container workspace. Declare that existing
		// isolation so the CLI accepts bypass mode when the container runs as root.
		"IS_SANDBOX=1",
	}
	switch cfg.Auth {
	case claudecfg.AuthAPIKey:
		if cfg.BaseURL == "" {
			env = append(env, "ANTHROPIC_API_KEY="+cfg.APIKey)
		} else {
			env = append(env, "ANTHROPIC_AUTH_TOKEN="+cfg.APIKey)
		}
	case claudecfg.AuthOAuthToken:
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+cfg.OAuthToken)
	case claudecfg.AuthWorkspace:
		// Credentials come from CLAUDE_CONFIG_DIR on the bot volume.
	}
	if cfg.Auth != claudecfg.AuthWorkspace && cfg.BaseURL != "" {
		env = append(env, "ANTHROPIC_BASE_URL="+cfg.BaseURL)
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

func metadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// shellQuote wraps one argument for the bridge's shell command line.
func shellQuote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
