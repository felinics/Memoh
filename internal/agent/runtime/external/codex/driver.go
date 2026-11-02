package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
	"github.com/felinics/memoh/internal/agent/runtime/external/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external/toolmount"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/mcp"
)

// ApprovalService is the decision flow the driver routes approvals through.
type ApprovalService interface {
	approval.FlowService
	RegisterWaiter(approvalID string) func()
}

// Driver runs codex turns over the app-server protocol.
type Driver struct {
	workspaces  external.Workspaces
	agents      *botagents.Service
	credentials *agentcredential.Service
	approval    ApprovalService
	userInput   UserInputService
	toolGateway toolmount.Gateway
	logger      *slog.Logger
	// launchers picks the codex CLI copy to run per bot. Nil falls back to
	// the toolkit path.
	launchers external.LauncherResolver

	// procs owns the app-server lifecycle: one per Bot Agent, shared by its
	// conversations.
	procs *agentproc.Supervisor
}

// NewDriver constructs the codex runtime driver.
func NewDriver(
	workspaces external.Workspaces,
	agents *botagents.Service,
	credentials *agentcredential.Service,
	approvalSvc ApprovalService,
	userInput UserInputService,
	toolGateway toolmount.Gateway,
	launchers external.LauncherResolver,
	procs *agentproc.Supervisor,
	logger *slog.Logger,
) *Driver {
	return &Driver{
		workspaces:  workspaces,
		agents:      agents,
		credentials: credentials,
		approval:    approvalSvc,
		userInput:   userInput,
		toolGateway: toolGateway,
		launchers:   launchers,
		procs:       procs,
		logger:      logger.With(slog.String("runtime", RuntimeType)),
	}
}

// RuntimeType implements external.Driver.
func (*Driver) RuntimeType() string { return RuntimeType }

// Capabilities is what Codex declares (external.Caps).
var Capabilities = external.Caps{
	Dependency:       dependencyID,
	Command:          "codex",
	ForkNeedsRuntime: true,
}

// Caps implements external.Driver.
func (*Driver) Caps() external.Caps { return Capabilities }

func serverKey(botID, botAgentID string) agentproc.Key {
	return agentproc.Key{Runtime: RuntimeType, BotID: botID, BotAgentID: botAgentID}
}

// peekServer returns the Agent's running app-server, if any, without
// starting one; callers may only read what it already holds.
func (d *Driver) peekServer(botID, botAgentID string) *appServer {
	if d.procs == nil {
		return nil
	}
	srv, _ := d.procs.Peek(serverKey(botID, botAgentID)).(*appServer)
	return srv
}

// markBroken takes a wedged app-server out of service: new work starts a
// fresh one, and it closes once the turns holding it are done.
func (d *Driver) markBroken(botID, botAgentID string, srv *appServer) {
	d.procs.MarkBroken(serverKey(botID, botAgentID), srv)
}

func (d *Driver) resolveAgentConfig(ctx context.Context, botID, botAgentID string, credentialRequired bool) (Config, error) {
	agent, err := d.agents.Get(ctx, botID, botAgentID)
	if err != nil {
		return Config{}, err
	}
	cfg, err := ParseAgentConfig(agent.Metadata)
	if err != nil {
		return Config{}, external.Fail(external.FailureAuthRequired, err)
	}
	// A ChatGPT login is Codex's own: it signs in, stores, and refreshes it
	// in CODEX_HOME, and Memoh keeps no copy.
	if cfg.Auth != AuthAPIKey {
		return cfg, nil
	}
	credential, err := d.credentials.ResolveForBotAgent(ctx, botID, botAgentID)
	if errors.Is(err, agentcredential.ErrNotFound) && !credentialRequired {
		return cfg, nil
	}
	if err != nil {
		return Config{}, external.CredentialError(err)
	}
	if credential.AuthKind != agentcredential.AuthKindOpenAIAPIKey {
		return Config{}, external.CredentialError(agentcredential.ErrIncompatible)
	}
	cfg.APIKey = credential.Secret["api_key"]
	return cfg, nil
}

// ModelCatalog returns the models available to this Agent. API-key Agents with
// an explicit Base URL use that provider's OpenAI-compatible /models endpoint;
// ChatGPT and default-OpenAI Agents retain Codex's richer native catalog.
func (d *Driver) ModelCatalog(ctx context.Context, request external.ModelCatalogRequest) (external.ModelCatalog, error) {
	botID, botAgentID := request.BotID, request.BotAgentID
	cfg, err := d.resolveAgentConfig(ctx, botID, botAgentID, true)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	if cfg.Auth == AuthAPIKey && strings.TrimSpace(cfg.BaseURL) != "" {
		catalog, err := customBaseURLModelCatalog(ctx, cfg, nil)
		if err != nil {
			return external.ModelCatalog{}, err
		}
		// Reasoning levels only enrich the endpoint's own list, which must stay
		// usable while the workspace or app-server is not.
		native, err := d.nativeModels(ctx, botID, botAgentID, cfg, true)
		if err != nil {
			if ctx.Err() != nil {
				return external.ModelCatalog{}, err
			}
			d.logger.WarnContext(ctx, "codex reasoning levels unavailable for custom Base URL models",
				slog.String("bot_id", botID),
				slog.Any("error", err))
			return catalog, nil
		}
		applyNativeReasoning(catalog.Models, native)
		return catalog, nil
	}
	models, err := d.nativeModels(ctx, botID, botAgentID, cfg, false)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	return external.ModelCatalog{
		Models:                    models,
		ConfiguredModelID:         cfg.Model,
		ConfiguredReasoningEffort: cfg.ReasoningEffort,
	}, nil
}

func (d *Driver) nativeModels(ctx context.Context, botID, botAgentID string, cfg Config, includeHidden bool) ([]external.ModelOption, error) {
	srv, releaseServer, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return nil, err
	}
	defer releaseServer()
	if err := srv.ensureAuth(ctx, cfg); err != nil {
		if errors.Is(err, ErrAuthRequired) {
			return nil, external.Fail(external.FailureAuthRequired, err)
		}
		return nil, err
	}

	const pageSize = uint64(100)
	limit := pageSize
	var cursor *string
	models := make([]external.ModelOption, 0, 16)
	seenCursors := map[string]struct{}{}
	for {
		var response protocol.ModelListResponse
		if err := srv.conn.Call(ctx, protocol.MethodModelList, protocol.ModelListParams{
			Cursor:        cursor,
			IncludeHidden: &includeHidden,
			Limit:         &limit,
		}, &response); err != nil {
			return nil, fmt.Errorf("codex model/list: %w", err)
		}
		for _, model := range response.Data {
			if model.Hidden && !includeHidden {
				continue
			}
			// turn/start accepts the catalog's concrete `model` value. `id` is
			// the app-server row identity and is only a fallback for older
			// catalog responses where both happened to be the same string.
			modelID := external.FirstNonEmpty(model.Model, model.ID)
			if modelID == "" {
				continue
			}
			efforts := make([]external.ReasoningEffortOption, 0, len(model.SupportedReasoningEfforts))
			for _, effort := range model.SupportedReasoningEfforts {
				id := strings.TrimSpace(effort.ReasoningEffort)
				if id == "" {
					continue
				}
				efforts = append(efforts, external.ReasoningEffortOption{
					ID:          id,
					Name:        id,
					Description: effort.Description,
				})
			}
			models = append(models, external.ModelOption{
				ID:                     modelID,
				Name:                   external.FirstNonEmpty(model.DisplayName, modelID),
				Description:            model.Description,
				Default:                model.IsDefault,
				DefaultReasoningEffort: strings.TrimSpace(model.DefaultReasoningEffort),
				ReasoningEfforts:       efforts,
			})
		}
		if response.NextCursor == nil || strings.TrimSpace(*response.NextCursor) == "" {
			break
		}
		next := strings.TrimSpace(*response.NextCursor)
		if _, exists := seenCursors[next]; exists {
			return nil, errs.NewDependency("codex model/list returned a repeated cursor")
		}
		seenCursors[next] = struct{}{}
		cursor = &next
	}
	return models, nil
}

// Prompt implements external.Driver: it runs one turn on the bot's
// app-server, streaming events through the sink.
func (d *Driver) Prompt(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
	if input.Command == "goal" && !goalExecutionAllowed(input) {
		return external.PromptResult{}, external.Fail(external.FailureGoalRequiresDefaultMode, nil)
	}
	cfg, err := d.resolveAgentConfig(ctx, input.BotID, input.BotAgentID, true)
	if err != nil {
		// A credential failure is already a Failure; reading the Agent's
		// configuration is Memoh's own and stays private.
		return external.PromptResult{}, err
	}

	if input.Command != "" {
		commands, _ := d.Commands(ctx, input)
		command, ok := external.FindCommand(commands, input.Command)
		if !ok || command.Kind != external.CommandTurn {
			return external.PromptResult{}, external.ErrCommandUnavailable
		}
	}
	preset, err := permissions(cfg, input)
	if err != nil {
		return external.PromptResult{}, err
	}

	srv, releaseServer, err := d.acquireServer(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.PromptResult{}, err
	}
	defer releaseServer()
	if err := srv.ensureAuth(ctx, cfg); err != nil {
		if errors.Is(err, ErrAuthRequired) {
			return external.PromptResult{}, external.Fail(external.FailureAuthRequired, err)
		}
		return external.PromptResult{}, external.Upstream(err)
	}

	continueGoal, err := srv.prepareGoal(ctx, input)
	if err != nil {
		return external.PromptResult{}, err
	}
	threadID, err := d.ensureThread(ctx, srv, cfg, input)
	if err != nil {
		return external.PromptResult{}, err
	}
	emitThreadNotices(srv, threadID, input.Sink)

	turn := newTurnState(ctx, input, threadID, d.approval, d.approval.RegisterWaiter, d.userInput, srv.toolLookup, d.logger)
	defer turn.close()
	if continueGoal {
		turn.followsGoal = true
		turn.goalStarting = true
	}
	srv.registerTurn(threadID, turn)
	defer srv.unregisterTurn(threadID, turn)
	defer d.pauseGoalOnExit(ctx, srv, turn)
	unregisterToolEvents := toolmount.RegisterTurnSink(d.toolGateway.Contexts, input.BotID, input.ThreadID, input.RunID, turn.emit)
	defer unregisterToolEvents()

	turnParams := protocol.TurnStartParams{
		ThreadID:          threadID,
		Input:             buildTurnInput(input),
		ApprovalPolicy:    &preset.approval,
		ApprovalsReviewer: &preset.reviewer,
		SandboxPolicy:     &preset.policy,
	}
	if model := external.FirstNonEmpty(input.ModelID, cfg.Model); model != "" {
		turnParams.Model = &model
	}
	if effort := external.FirstNonEmpty(input.ReasoningEffort, cfg.ReasoningEffort); effort != "" {
		turnParams.Effort = &effort
	}
	if err := applyCollaborationMode(&turnParams, input, srv.settingsForThread(threadID)); err != nil {
		return external.PromptResult{}, err
	}

	var turnResp protocol.TurnStartResponse
	if err := startRuntimeTurn(ctx, srv, input, turnParams, &turnResp); err != nil {
		if ctx.Err() != nil || input.Command == "goal" {
			// The server may have accepted the turn even though the response
			// never reached us; interrupt by the id from turn/started so no
			// orphan keeps running unsupervised. When even that id has not
			// arrived there is nothing to address the interrupt to — recycle
			// the server: siblings holding references finish on the draining
			// process (which dies at their last release), new work builds a
			// fresh one, and the unidentified turn is confined to a process
			// with a bounded life instead of sharing one with future turns.
			if turnID := turn.currentTurnID(); turnID != "" {
				d.interruptTurn(srv, turn, turnID)
			} else {
				d.logger.WarnContext(ctx, "codex turn/start cancelled before a turn id arrived; draining app-server",
					slog.String("thread_id", threadID))
				d.markBroken(input.BotID, input.BotAgentID, srv)
			}
		}
		return srv.turnResultAfterError(turn, err)
	}
	turn.setTurnID(turnResp.Turn.ID)
	if continueGoal {
		if err := srv.activateGoal(ctx, threadID); err != nil {
			d.interruptTurn(srv, turn, turn.currentTurnID())
			return srv.turnResultAfterError(turn, err)
		}
	}
	settings := srv.settingsForThread(threadID)
	if turnParams.Model != nil {
		settings.Model = *turnParams.Model
	}
	if turnParams.Effort != nil || turnParams.CollaborationMode != nil {
		settings.ReasoningEffort = turnParams.Effort
	}
	srv.rememberThreadSettings(threadID, settings.Model, settings.ReasoningEffort)
	stopSteering := startSteering(ctx, srv.conn, turn)
	defer stopSteering()

	select {
	case <-turn.done:
	case <-ctx.Done():
		d.pauseGoalOnExit(ctx, srv, turn)
		d.interruptTurn(srv, turn, external.FirstNonEmpty(turn.currentTurnID(), turnResp.Turn.ID))
		select {
		case <-turn.done:
		case <-time.After(interruptSettleTimeout):
			// A turn codex never settled would keep executing — and mutating
			// the workspace — or wedge the server busy. Recycle: siblings
			// finish on the draining process, new work cold-starts a fresh
			// one, and the wedged turn dies with the displaced process once
			// its last user releases it.
			d.logger.WarnContext(ctx, "codex turn did not settle after interrupt; draining app-server",
				slog.String("thread_id", threadID))
			d.markBroken(input.BotID, input.BotAgentID, srv)
		case <-srv.proc.Done():
		}
	case <-srv.proc.Done():
		stopSteering()
		result, _ := srv.turnResult(turn)
		return result, srv.conn.exitError("mid-turn")
	}

	stopSteering()
	result, resultErr := srv.turnResult(turn)
	if ctx.Err() != nil && resultErr == nil {
		// The application layer distinguishes stop from failure by context
		// state; an interrupted turn is not an error.
		return result, nil
	}
	if resultErr == nil && !result.TurnCompleted {
		// Codex ended the turn itself. The application records it as an
		// abort with no cause, so this is the only trace of what happened.
		d.logger.WarnContext(ctx, "codex turn ended without completing and without a stop",
			slog.String("thread_id", threadID), slog.String("turn_id", result.AgentTurnID),
			slog.String("status", result.StopReason))
	}
	return result, resultErr
}

// turnResult keeps the native session anchor together on every return path,
// including a rejected start, a failed goal activation, and a process exit.
func (s *appServer) turnResult(turn *turnState) (external.PromptResult, error) {
	result, err := turn.result()
	recordThreadMetadata(&result.RuntimeMetadata, turn.input.RuntimeMetadata, turn.threadID, s.threadPath(turn.threadID))
	return result, err
}

func (s *appServer) turnResultAfterError(turn *turnState, err error) (external.PromptResult, error) {
	result, _ := s.turnResult(turn)
	var rpcErr *protocol.RPCError
	if errors.As(err, &rpcErr) {
		return result, external.Reported(external.FailureReported, rpcErr.Message)
	}
	return result, err
}

// ensureThread starts or resumes the session's codex thread.
func (d *Driver) ensureThread(ctx context.Context, srv *appServer, cfg Config, input external.PromptInput) (threadID string, err error) {
	threadID = strings.TrimSpace(external.MetadataString(input.RuntimeMetadata, metadataThreadIDKey))
	if input.ForceFreshRuntime {
		// Discuss turns re-inject the full composed context every round;
		// resuming the stored thread would duplicate it on top of codex's
		// own saved history.
		threadID = ""
	}
	cwd := strings.TrimSpace(external.MetadataString(input.RuntimeMetadata, "project_path"))
	if cwd == "" {
		cwd = defaultProjectPath
	}
	preset, err := permissions(cfg, input)
	if err != nil {
		return "", err
	}

	if threadID == "" {
		return d.startThread(ctx, srv, cfg, input, cwd, preset)
	}

	if srv.threadLoaded(threadID) {
		return threadID, nil
	}
	toolsConfig, bindTools := d.prepareThreadTools(ctx, srv, input)
	// Memoh keeps its own history; loading a thread never reads Codex's turns.
	excludeTurns := true
	params := protocol.ThreadResumeParams{
		ExcludeTurns:      &excludeTurns,
		ThreadID:          threadID,
		Cwd:               &cwd,
		ApprovalPolicy:    &preset.approval,
		ApprovalsReviewer: &preset.reviewer,
		Sandbox:           &preset.sandbox,
		Config:            toolsConfig,
	}
	// The rollout path outlives codex's own index of it; the id alone still
	// works while that index is intact.
	if rolloutPath := external.MetadataString(input.RuntimeMetadata, metadataRolloutPathKey); rolloutPath != "" {
		params.Path = &rolloutPath
	}
	resp, err := srv.resumeThread(ctx, params)
	if err == nil && resp.Thread.ID == threadID {
		bindTools(threadID)
		srv.markThreadLoaded(threadID)
		srv.rememberThreadPath(threadID, resp.Thread.Path)
		srv.rememberThreadSettings(threadID, resp.Model, resp.ReasoningEffort)
		return threadID, nil
	}
	bindTools("")
	var refused *protocol.RPCError
	if err != nil && !errors.As(err, &refused) {
		// A transport failure says nothing about the thread; a retry may resume it.
		return "", external.Upstream(fmt.Errorf("codex thread/resume: %w", err))
	}
	if input.Command == "compact" {
		return "", external.ErrThreadUnavailable
	}
	// Codex itself refused the thread, or resumed a different one, and will do
	// so again on every later turn: its memory of this conversation is already
	// gone. A session that can never run again is worse than one that lost its
	// runtime-side context, so start over and say so.
	d.logger.WarnContext(ctx, "codex thread cannot be resumed; starting a fresh thread",
		slog.String("session_id", input.ThreadID), slog.String("thread_id", threadID),
		slog.String("resumed_thread_id", resp.Thread.ID), slog.Any("error", err))
	if input.Sink != nil {
		input.Sink.EmitStreamEvent(event.StreamEvent{
			Type:       event.RuntimeNotice,
			NoticeKind: event.NoticeNativeHistoryLost,
			Delta:      "Codex could not resume this conversation's session and started a new one. It does not remember the earlier messages shown here.",
		})
	}
	return d.startThread(ctx, srv, cfg, input, cwd, preset)
}

func (d *Driver) startThread(ctx context.Context, srv *appServer, cfg Config, input external.PromptInput, cwd string, preset permissionPreset) (string, error) {
	toolsConfig, bindTools := d.prepareThreadTools(ctx, srv, input)
	params := protocol.ThreadStartParams{
		Cwd:               &cwd,
		ApprovalPolicy:    &preset.approval,
		ApprovalsReviewer: &preset.reviewer,
		Sandbox:           &preset.sandbox,
		Config:            toolsConfig,
	}
	if cfg.Model != "" {
		params.Model = &cfg.Model
	}
	var resp protocol.ThreadStartResponse
	if err := srv.conn.Call(ctx, protocol.MethodThreadStart, params, &resp); err != nil {
		bindTools("")
		return "", fmt.Errorf("codex thread/start: %w", err)
	}
	if resp.Thread.ID == "" {
		bindTools("")
		return "", errs.NewDependency("codex thread/start returned no thread id")
	}
	bindTools(resp.Thread.ID)
	srv.markThreadLoaded(resp.Thread.ID)
	srv.rememberThreadPath(resp.Thread.ID, resp.Thread.Path)
	srv.rememberThreadSettings(resp.Thread.ID, resp.Model, resp.ReasoningEffort)
	return resp.Thread.ID, nil
}

// interruptTurn asks the app-server to stop the running turn and terminates
// the processes the turn's commands started; it runs on a background context
// because the turn context is already cancelled.
//
// turn/interrupt only cancels the turn: codex keeps unified-exec processes in
// a thread-scoped store, so a command that was still running keeps executing —
// and mutating the workspace — after the turn reports interrupted. Only this
// turn's processes are terminated; background terminals earlier turns left
// running (a dev server, a watcher) are not this stop's to end, which is why
// thread/backgroundTerminals/clean is not used.
//
//nolint:contextcheck // the turn context is cancelled; the interrupt must still go out
func (d *Driver) interruptTurn(srv *appServer, turn *turnState, turnID string) {
	if turnID == "" {
		return
	}
	interruptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := srv.conn.Call(interruptCtx, protocol.MethodTurnInterrupt, protocol.TurnInterruptParams{
		ThreadID: turn.threadID,
		TurnID:   turnID,
	}, nil)
	if err != nil {
		d.logger.Warn("codex turn/interrupt failed", slog.String("thread_id", turn.threadID), slog.Any("error", err))
	}
	for _, processID := range turn.startedProcesses() {
		// A process that already exited reports terminated=false; that is
		// the expected outcome for most commands, not a failure.
		err := srv.conn.Call(interruptCtx, protocol.MethodThreadBackgroundTerminalsTerminate, protocol.ThreadBackgroundTerminalsTerminateParams{
			ThreadID:  turn.threadID,
			ProcessID: processID,
		}, nil)
		if err != nil {
			d.logger.Warn("codex backgroundTerminals/terminate failed", slog.String("thread_id", turn.threadID),
				slog.String("process_id", processID), slog.Any("error", err))
		}
	}
}

// botGatewayToolLookup reports whether a tool name is served by the Memoh
// tool gateway for this bot. It uses the same base session as a thread with
// no active turn, so the consent-time lookup matches the registry the
// execution will see; misses and errors report false (fail closed).
func (d *Driver) botGatewayToolLookup(botID string) func(context.Context, string) bool {
	return func(ctx context.Context, toolName string) bool {
		_, ok, err := d.toolGateway.Tools.LookupTool(ctx, mcp.ToolSessionContext{
			BotID:            botID,
			ChatID:           botID,
			SessionType:      sessionmode.Chat,
			CanListUserInput: true,
		}, toolName)
		if err != nil {
			d.logger.Warn("codex: gateway tool lookup failed", slog.String("tool", toolName), slog.Any("error", err))
			return false
		}
		return ok
	}
}

// fingerprint names what an app-server is started from: a change to any of
// it needs a new process. Model, effort, and permission mode travel with each
// turn and are not part of it. The API key is hashed, never kept.
func fingerprint(cfg Config, launcher external.Launcher) string {
	key := sha256.Sum256([]byte(cfg.APIKey))
	return strings.Join([]string{
		string(cfg.Auth), strings.TrimSpace(cfg.BaseURL), hex.EncodeToString(key[:]),
		launcher.Path, string(launcher.Source), launcher.Version,
	}, "\x00")
}

// acquireServer leases the Agent's app-server, starting one from the current
// configuration when there is none or it was started from another. The
// caller must call the returned release when done.
func (d *Driver) acquireServer(ctx context.Context, botID, botAgentID string) (*appServer, func(), error) {
	info, err := external.ContainerWorkspace(ctx, d.workspaces, botID, RuntimeType)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := d.resolveAgentConfig(ctx, botID, botAgentID, false)
	if err != nil {
		return nil, nil, err
	}
	launcher, err := agentproc.ResolveLauncher(ctx, d.launchers, botID, dependencyID, defaultLauncherPath)
	if err != nil {
		return nil, nil, err
	}
	start := func(ctx context.Context) (agentproc.Process, string, error) {
		client, err := d.workspaces.MCPClient(ctx, botID)
		if err != nil {
			return nil, "", fmt.Errorf("workspace bridge for bot %s: %w", botID, err)
		}
		srv, err := startAppServerSession(context.WithoutCancel(ctx), botID, botAgentID, client, cfg, launcher, d.logger)
		if err != nil {
			// What the app-server said on its way down is the user's to read.
			return nil, "", external.Upstream(err)
		}
		agentproc.ObserveVersion(ctx, d.launchers, botID, dependencyID, srv.codexVersion)
		srv.workspaceInfo = info
		srv.toolLookup = d.botGatewayToolLookup(botID)
		started := launcher
		started.Version = external.FirstNonEmpty(srv.codexVersion, launcher.Version)
		return srv, fingerprint(cfg, started), nil
	}
	proc, release, err := d.procs.Lease(ctx, serverKey(botID, botAgentID), fingerprint(cfg, launcher), start)
	if err != nil {
		return nil, nil, err
	}
	return proc.(*appServer), release, nil
}

// emitThreadNotices surfaces runtime-side degradations at the start of a turn.
//
// Thread config is fixed at start, so a thread that began without a tool
// gateway stays toolless for the app-server's life; re-notice every turn,
// since silent capability loss is exactly what this channel exists to
// prevent.
func emitThreadNotices(srv *appServer, threadID string, sink external.EventSink) {
	if srv.threadToolless(threadID) {
		toolmount.EmitUnavailableNotice(sink, "this conversation started without Memoh tools; start a new session to restore them")
	}
}

// ForkThread implements external.ThreadForker: it derives a new codex thread
// sharing history up to lastTurnID (inclusive; empty forks at the head) and
// returns the runtime-metadata delta naming the forked thread. The session's
// runtime metadata supplies the source thread id and working directory.
func (d *Driver) ForkThread(ctx context.Context, botID, botAgentID string, runtimeMetadata map[string]any, lastTurnID string) (map[string]any, error) {
	threadID := strings.TrimSpace(external.MetadataString(runtimeMetadata, metadataThreadIDKey))
	if threadID == "" {
		return nil, external.ErrThreadUnavailable
	}
	srv, releaseServer, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return nil, err
	}
	defer releaseServer()
	toolsConfig, bindTools := d.prepareThreadTools(ctx, srv, external.PromptInput{BotID: botID, BotAgentID: botAgentID})
	cwd := strings.TrimSpace(external.MetadataString(runtimeMetadata, "project_path"))
	if cwd == "" {
		cwd = defaultProjectPath
	}
	approvalPolicy := protocol.AskForApproval{Unit: protocol.AskForApprovalUnitOnRequest}
	deferGoal := true
	excludeTurns := true
	params := protocol.ThreadForkParams{
		DeferGoalContinuation: &deferGoal,
		ExcludeTurns:          &excludeTurns,
		ThreadID:              threadID,
		Cwd:                   &cwd,
		ApprovalPolicy:        &approvalPolicy,
		Config:                toolsConfig,
	}
	if rolloutPath := external.MetadataString(runtimeMetadata, metadataRolloutPathKey); rolloutPath != "" {
		params.Path = &rolloutPath
	}
	if trimmed := strings.TrimSpace(lastTurnID); trimmed != "" {
		params.LastTurnID = &trimmed
	}
	var resp protocol.ThreadForkResponse
	if err := srv.conn.Call(ctx, protocol.MethodThreadFork, params, &resp); err != nil {
		bindTools("")
		return nil, fmt.Errorf("codex thread/fork: %w", err)
	}
	if resp.Thread.ID == "" {
		bindTools("")
		return nil, errs.NewDependency("codex thread/fork returned no thread id")
	}
	// Fork loads the thread immediately, so its tool config must be supplied
	// above; resuming an already loaded thread would ignore new overrides.
	bindTools(resp.Thread.ID)
	srv.markThreadLoaded(resp.Thread.ID)
	srv.rememberThreadPath(resp.Thread.ID, resp.Thread.Path)
	srv.rememberThreadSettings(resp.Thread.ID, resp.Model, resp.ReasoningEffort)
	// The fork overlays the source's metadata; its rollout path must not
	// survive into the branch, or the branch would resume the source's file.
	var delta map[string]any
	recordThreadMetadata(&delta, runtimeMetadata, resp.Thread.ID, srv.threadPath(resp.Thread.ID))
	return delta, nil
}

var _ external.DeviceLoginProvider = (*Driver)(nil)

// StartDeviceLogin begins a ChatGPT device-code login on the bot's codex,
// returning the code the user must enter. Completion lands in CODEX_HOME and
// is observable via PollDeviceLogin.
func (d *Driver) StartDeviceLogin(ctx context.Context, botID, botAgentID string) (external.DeviceLoginStart, error) {
	cfg, err := d.resolveAgentConfig(ctx, botID, botAgentID, false)
	if err != nil {
		return external.DeviceLoginStart{}, err
	}
	if cfg.Auth != AuthChatGPT {
		return external.DeviceLoginStart{}, external.CredentialError(agentcredential.ErrIncompatible)
	}
	srv, releaseServer, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return external.DeviceLoginStart{}, err
	}
	defer releaseServer()
	var resp protocol.LoginAccountResponse
	err = srv.conn.Call(ctx, protocol.MethodAccountLoginStart, protocol.LoginAccountParams{
		ChatgptDeviceCode: &protocol.ChatgptDeviceCodeLoginAccountParams{},
	}, &resp)
	if err != nil {
		return external.DeviceLoginStart{}, err
	}
	device := resp.ChatgptDeviceCode
	if device == nil || device.LoginID == "" || device.UserCode == "" || device.VerificationURL == "" {
		return external.DeviceLoginStart{}, errs.NewDependency("codex device login response is incomplete")
	}
	srv.trackLogin(device.LoginID)
	return external.DeviceLoginStart{
		LoginID:         device.LoginID,
		UserCode:        device.UserCode,
		VerificationURL: device.VerificationURL,
	}, nil
}

// PollDeviceLogin reports a tracked login's state. A login that succeeded is
// already in CODEX_HOME, written there by Codex itself.
func (d *Driver) PollDeviceLogin(_ context.Context, _, botID, botAgentID, loginID string) (external.DeviceLoginStatus, error) {
	srv := d.peekServer(botID, botAgentID)
	if srv == nil {
		return external.DeviceLoginStatus{Status: external.DeviceLoginUnknown}, nil
	}
	outcome, ok := srv.loginStatus(loginID)
	switch {
	case !ok:
		return external.DeviceLoginStatus{Status: external.DeviceLoginUnknown}, nil
	case !outcome.Done:
		return external.DeviceLoginStatus{Status: external.DeviceLoginPending}, nil
	case outcome.Success:
		srv.forgetLogin(loginID)
		return external.DeviceLoginStatus{Status: external.DeviceLoginSuccess}, nil
	default:
		srv.forgetLogin(loginID)
		message := outcome.Error
		if message == "" {
			message = "codex login failed"
		}
		return external.DeviceLoginStatus{Status: external.DeviceLoginError, Error: message}, nil
	}
}

// PurgeBotAgentAuth signs the Agent's Codex out by deleting its login file.
func (d *Driver) PurgeBotAgentAuth(ctx context.Context, botID, botAgentID string) error {
	return d.procs.SignOut(ctx, d.workspaces, RuntimeType, botID, botAgentID, path.Join(codexHome(botAgentID), "auth.json"), nil)
}

// CancelDeviceLogin aborts a pending device-code login.
func (d *Driver) CancelDeviceLogin(ctx context.Context, botID, botAgentID, loginID string) error {
	srv := d.peekServer(botID, botAgentID)
	if srv == nil {
		return nil
	}
	// The cancel RPC needs the connection live for its duration; peek holds
	// no reference, so pin one for the call.
	pinned, releaseServer, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil || pinned != srv {
		if err == nil {
			releaseServer()
		}
		return nil
	}
	defer releaseServer()
	srv.forgetLogin(loginID)
	var resp protocol.CancelLoginAccountResponse
	return srv.conn.Call(ctx, protocol.MethodAccountLoginCancel, protocol.CancelLoginAccountParams{LoginID: loginID}, &resp)
}
