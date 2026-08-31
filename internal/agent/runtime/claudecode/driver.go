package claudecode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
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
	stateStore  agentstate.SessionStateStore
	toolGateway toolmount.Gateway
	logger      *slog.Logger
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
func (d *Driver) ModelCatalog(ctx context.Context, botID, botAgentID string) (external.ModelCatalog, error) {
	cfg, err := d.resolveAgentConfig(ctx, botID, botAgentID)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	client, err := d.bridges.MCPClient(ctx, botID)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	proc, err := startCLI(ctx, client, defaultProjectPath, cliArgs(cfg, external.PromptInput{}, "", ""), cliEnv(cfg))
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
	defer turn.close()
	go turn.readLoop() //nolint:contextcheck // turnRunner owns the control-channel lifetime
	responseCh, _, err := turn.sendControl("initialize", nil)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	var raw json.RawMessage
	select {
	case raw = <-responseCh:
	case <-proc.Done():
		return external.ModelCatalog{}, fmt.Errorf("claude initialize exited: %s", proc.StderrTail())
	case <-ctx.Done():
		return external.ModelCatalog{}, ctx.Err()
	}
	proc.CloseStdin()
	var response initializeResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return external.ModelCatalog{}, fmt.Errorf("decode claude initialize response: %w", err)
	}
	return modelCatalogFromInitialize(cfg.Model, response), nil
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
	models := make([]external.ModelOption, 0, len(response.Models))
	configuredFound := configuredModel == ""
	defaultAssigned := false
	for _, model := range response.Models {
		id := strings.TrimSpace(model.Value)
		if id == "" || id == "default" {
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
		isDefault := configuredModel == "" && !defaultAssigned && defaultResolved != "" && resolved == defaultResolved
		defaultAssigned = defaultAssigned || isDefault
		models = append(models, external.ModelOption{
			ID:               id,
			Name:             firstNonEmpty(model.DisplayName, id),
			Description:      strings.TrimSpace(model.Description),
			Default:          isDefault,
			ReasoningEfforts: efforts,
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
	proc, err := startCLI(context.WithoutCancel(ctx), client, workDir, args, env)
	if err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	defer func() { _ = proc.Close() }()

	turn := newTurnRunner(ctx, input, proc, d.approval, d.approval.RegisterWaiter, d.logger)
	defer turn.close()
	go turn.readLoop() //nolint:contextcheck // turnRunner owns the control-channel lifetime
	unregisterToolEvents := toolmount.RegisterTurnSink(d.toolGateway.Contexts, input.BotID, input.ThreadID, input.RunID, turn.emit)
	defer unregisterToolEvents()

	// The SDK opens with a control-channel initialize; mirror it so the CLI
	// treats this client as control-capable before the first permission
	// callback. The response lands in the pending map and needs no reader.
	if _, _, err := turn.sendControl("initialize", nil); err != nil {
		d.logger.Warn("claude initialize send failed", slog.Any("error", err))
	}

	content := buildUserContent(input)
	line, err := userMessageLine(storedSessionID, content)
	if err != nil {
		return external.PromptResult{}, err
	}
	if err := turn.writeLine(line); err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}

	select {
	case <-turn.done:
	case <-ctx.Done():
		turn.interrupt()
		// Grace window for the CLI to flush its result after the interrupt;
		// a wedged process must not pin this turn (and the session's single
		// active slot) forever, so terminate it when the window closes.
		grace := time.NewTimer(interruptGraceTimeout)
		select {
		case <-turn.done:
		case <-proc.Done():
		case <-grace.C:
			_ = proc.Close()
		}
		grace.Stop()
	}
	// End the input stream so the process exits cleanly.
	proc.CloseStdin()

	result, resultErr := turn.buildResult(storedSessionID)
	if resultErr == nil && result.TurnCompleted {
		// A completed turn checkpoints regardless of a racing stop: the round
		// commits (as succeeded or aborted-after-completion) either way, and
		// its publication head must have a staged snapshot to point at.
		result.Checkpoint = d.stageTurnCheckpoint(ctx, client, input, result)
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
