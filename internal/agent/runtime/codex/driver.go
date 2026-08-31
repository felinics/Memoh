package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/toolmount"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// BridgeSource resolves the workspace bridge client for a bot; the workspace
// manager implements it.
type BridgeSource interface {
	MCPClient(ctx context.Context, botID string) (*bridge.Client, error)
	WorkspaceInfo(ctx context.Context, botID string) (bridge.WorkspaceInfo, error)
}

// ApprovalService is the decision flow the driver routes approvals through.
type ApprovalService interface {
	approval.FlowService
	RegisterWaiter(approvalID string) func()
}

// Driver runs codex turns over the app-server protocol.
type Driver struct {
	bridges     BridgeSource
	agents      *botagents.Service
	credentials *agentcredential.Service
	approval    ApprovalService
	userInput   UserInputService
	toolGateway toolmount.Gateway
	logger      *slog.Logger

	// servers owns the shared per-Agent app-server lifecycle: reference
	// counting for concurrent users, drain-on-recycle instead of kill-by-bot.
	servers *serverTable
}

// NewDriver constructs the codex runtime driver.
func NewDriver(
	bridges BridgeSource,
	agents *botagents.Service,
	credentials *agentcredential.Service,
	approvalSvc ApprovalService,
	userInput UserInputService,
	toolGateway toolmount.Gateway,
	logger *slog.Logger,
) *Driver {
	d := &Driver{
		bridges:     bridges,
		agents:      agents,
		credentials: credentials,
		approval:    approvalSvc,
		userInput:   userInput,
		toolGateway: toolGateway,
		logger:      logger.With(slog.String("runtime", RuntimeType)),
	}
	d.servers = newServerTable(d.startServer, d.logger)
	return d
}

// RuntimeType implements external.Driver.
func (*Driver) RuntimeType() string { return RuntimeType }

func (d *Driver) ResetBot(botID string) { d.CloseBot(botID) }

func (d *Driver) ResetBotAgent(botID, botAgentID string) {
	d.servers.recycle(serverKey(botID, botAgentID))
}

func serverKey(botID, botAgentID string) string {
	return botID + "\x00" + botAgentID
}

func splitServerKey(key string) (string, string) {
	botID, botAgentID, _ := strings.Cut(key, "\x00")
	return botID, botAgentID
}

func (d *Driver) resolveAgentConfig(ctx context.Context, botID, botAgentID string, credentialRequired bool) (Config, agentcredential.ResolvedCredential, error) {
	agent, err := d.agents.Get(ctx, botID, botAgentID)
	if err != nil {
		return Config{}, agentcredential.ResolvedCredential{}, err
	}
	cfg, err := ParseAgentConfig(agent.Metadata)
	if err != nil {
		return Config{}, agentcredential.ResolvedCredential{}, err
	}
	credential, err := d.credentials.ResolveForBotAgent(ctx, botID, botAgentID)
	if errors.Is(err, agentcredential.ErrNotFound) && !credentialRequired {
		return cfg, agentcredential.ResolvedCredential{}, nil
	}
	if err != nil {
		return Config{}, agentcredential.ResolvedCredential{}, external.CredentialError(err)
	}
	switch cfg.Auth {
	case AuthAPIKey:
		if credential.AuthKind != agentcredential.AuthKindOpenAIAPIKey {
			return Config{}, agentcredential.ResolvedCredential{}, external.CredentialError(agentcredential.ErrIncompatible)
		}
		cfg.APIKey = credential.Secret["api_key"]
	case AuthChatGPT:
		if credential.AuthKind != agentcredential.AuthKindOpenAICodexOAuth {
			return Config{}, agentcredential.ResolvedCredential{}, external.CredentialError(agentcredential.ErrIncompatible)
		}
	}
	return cfg, credential, nil
}

// ModelCatalog returns the live model vocabulary advertised by the bot's
// pinned codex app-server. Authentication is checked first because ChatGPT
// plans and API-key accounts may expose different catalogs.
func (d *Driver) ModelCatalog(ctx context.Context, botID, botAgentID string) (external.ModelCatalog, error) {
	cfg, _, err := d.resolveAgentConfig(ctx, botID, botAgentID, true)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	srv, releaseServer, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return external.ModelCatalog{}, err
	}
	defer releaseServer()
	if err := srv.ensureAuth(ctx, cfg); err != nil {
		if errors.Is(err, ErrAuthRequired) {
			return external.ModelCatalog{}, apperror.Wrap(apperror.CodeExternalRuntimeAuthRequired, err, nil)
		}
		return external.ModelCatalog{}, err
	}

	const pageSize = uint64(100)
	includeHidden := false
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
			return external.ModelCatalog{}, fmt.Errorf("codex model/list: %w", err)
		}
		for _, model := range response.Data {
			if model.Hidden {
				continue
			}
			// turn/start accepts the catalog's concrete `model` value. `id` is
			// the app-server row identity and is only a fallback for older
			// catalog responses where both happened to be the same string.
			modelID := firstNonEmpty(model.Model, model.ID)
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
				Name:                   firstNonEmpty(model.DisplayName, modelID),
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
			return external.ModelCatalog{}, errors.New("codex model/list returned a repeated cursor")
		}
		seenCursors[next] = struct{}{}
		cursor = &next
	}

	return external.ModelCatalog{
		Models:                    models,
		ConfiguredModelID:         cfg.Model,
		ConfiguredReasoningEffort: cfg.ReasoningEffort,
	}, nil
}

// Prompt implements external.Driver: it runs one turn on the bot's
// app-server, streaming events through the sink.
func (d *Driver) Prompt(ctx context.Context, input external.PromptInput) (external.PromptResult, error) {
	cfg, credential, err := d.resolveAgentConfig(ctx, input.BotID, input.BotAgentID, true)
	if err != nil {
		if apperror.CodeOf(err) != "" {
			return external.PromptResult{}, err
		}
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}

	srv, releaseServer, err := d.acquireServer(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	defer releaseServer()
	if err := srv.ensureAuth(ctx, cfg); err != nil {
		if errors.Is(err, ErrAuthRequired) {
			return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeAuthRequired, err, nil)
		}
		return external.PromptResult{}, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}

	threadID, isNewThread, err := d.ensureThread(ctx, srv, cfg, input)
	if err != nil {
		return external.PromptResult{}, err
	}
	// Thread config is fixed at start, so a thread that began without a tool
	// gateway stays toolless for the app-server's life. Re-notice every turn:
	// silent capability loss is exactly what this channel exists to prevent.
	if srv.threadToolless(threadID) {
		toolmount.EmitUnavailableNotice(input.Sink, "this conversation started without Memoh tools; start a new session to restore them")
	}

	turn := newTurnState(ctx, input, threadID, d.approval, d.approval.RegisterWaiter, d.userInput, srv.toolLookup, d.logger)
	defer turn.close()
	srv.registerTurn(threadID, turn)
	defer srv.unregisterTurn(threadID, turn)
	unregisterToolEvents := toolmount.RegisterTurnSink(d.toolGateway.Contexts, input.BotID, input.ThreadID, input.RunID, turn.emit)
	defer unregisterToolEvents()

	turnParams := protocol.TurnStartParams{
		ThreadID: threadID,
		Input:    buildTurnInput(input),
	}
	if model := firstNonEmpty(input.ModelID, cfg.Model); model != "" {
		turnParams.Model = &model
	}
	if effort := firstNonEmpty(input.ReasoningEffort, cfg.ReasoningEffort); effort != "" {
		turnParams.Effort = &effort
	}

	var turnResp protocol.TurnStartResponse
	if err := srv.conn.Call(ctx, protocol.MethodTurnStart, turnParams, &turnResp); err != nil {
		if ctx.Err() != nil {
			// The server may have accepted the turn even though the response
			// never reached us; interrupt by the id from turn/started so no
			// orphan keeps running unsupervised. When even that id has not
			// arrived there is nothing to address the interrupt to — recycle
			// the server: siblings holding references finish on the draining
			// process (which dies at their last release), new work builds a
			// fresh one, and the unidentified turn is confined to a process
			// with a bounded life instead of sharing one with future turns.
			if turnID := turn.currentTurnID(); turnID != "" {
				d.interruptTurn(srv, threadID, turnID)
			} else {
				d.logger.Warn("codex turn/start cancelled before a turn id arrived; draining app-server",
					slog.String("thread_id", threadID))
				d.ResetBotAgent(input.BotID, input.BotAgentID)
			}
		}
		return d.turnResultAfterError(turn, isNewThread, threadID, err)
	}
	turn.setTurnID(turnResp.Turn.ID)

	select {
	case <-turn.done:
	case <-ctx.Done():
		d.interruptTurn(srv, threadID, firstNonEmpty(turnResp.Turn.ID, turn.currentTurnID()))
		select {
		case <-turn.done:
		case <-time.After(interruptSettleTimeout):
			// A turn codex never settled would keep executing — and mutating
			// the workspace — or wedge the server busy. Recycle: siblings
			// finish on the draining process, new work cold-starts a fresh
			// one, and the wedged turn dies with the displaced process once
			// its last user releases it.
			d.logger.Warn("codex turn did not settle after interrupt; draining app-server",
				slog.String("thread_id", threadID))
			d.ResetBotAgent(input.BotID, input.BotAgentID)
		case <-srv.proc.Done():
		}
	case <-srv.proc.Done():
		result, _ := turn.result(newThreadMetadata(isNewThread, threadID))
		return result, fmt.Errorf("codex app-server exited mid-turn: %s", srv.proc.StderrTail())
	}

	result, resultErr := turn.result(newThreadMetadata(isNewThread, threadID))
	if cfg.Auth == AuthChatGPT {
		d.persistChatGPTCredential(ctx, srv.client, input, credential)
	}
	if ctx.Err() != nil && resultErr == nil {
		// The application layer distinguishes stop from failure by context
		// state; an interrupted turn is not an error.
		return result, nil
	}
	return result, resultErr
}

// newThreadMetadata reports the thread id to persist when it was just created.
func newThreadMetadata(isNew bool, threadID string) string {
	if isNew {
		return threadID
	}
	return ""
}

func (*Driver) turnResultAfterError(turn *turnState, isNewThread bool, threadID string, err error) (external.PromptResult, error) {
	result, _ := turn.result(newThreadMetadata(isNewThread, threadID))
	var rpcErr *protocol.RPCError
	if errors.As(err, &rpcErr) {
		return result, fmt.Errorf("codex turn/start rejected: %s", rpcErr.Message)
	}
	return result, err
}

// ensureThread starts or resumes the session's codex thread.
func (d *Driver) ensureThread(ctx context.Context, srv *appServer, cfg Config, input external.PromptInput) (threadID string, isNew bool, err error) {
	threadID = strings.TrimSpace(metadataString(input.RuntimeMetadata, metadataThreadIDKey))
	if input.ForceFreshRuntime {
		// Discuss turns re-inject the full composed context every round;
		// resuming the stored thread would duplicate it on top of codex's
		// own saved history.
		threadID = ""
	}
	cwd := strings.TrimSpace(metadataString(input.RuntimeMetadata, "project_path"))
	if cwd == "" {
		cwd = defaultProjectPath
	}
	approvalPolicy := protocol.AskForApproval{Unit: protocol.AskForApprovalUnitOnRequest}

	if threadID == "" {
		toolsConfig, bindTools, err := d.prepareThreadTools(srv, input)
		if err != nil {
			return "", false, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
		}
		params := protocol.ThreadStartParams{
			Cwd:            &cwd,
			ApprovalPolicy: &approvalPolicy,
			Config:         toolsConfig,
		}
		if cfg.Model != "" {
			params.Model = &cfg.Model
		}
		var resp protocol.ThreadStartResponse
		if err := srv.conn.Call(ctx, protocol.MethodThreadStart, params, &resp); err != nil {
			bindTools("")
			return "", false, fmt.Errorf("codex thread/start: %w", err)
		}
		threadID = resp.Thread.ID
		if threadID == "" {
			bindTools("")
			return "", false, errors.New("codex thread/start returned no thread id")
		}
		bindTools(threadID)
		srv.markThreadLoaded(threadID)
		srv.setThreadToolless(threadID, toolsConfig == nil)
		return threadID, true, nil
	}

	if srv.threadLoaded(threadID) {
		return threadID, false, nil
	}
	toolsConfig, bindTools, err := d.prepareThreadTools(srv, input)
	if err != nil {
		return "", false, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	params := protocol.ThreadResumeParams{
		ThreadID:       threadID,
		Cwd:            &cwd,
		ApprovalPolicy: &approvalPolicy,
		Config:         toolsConfig,
	}
	var resp protocol.ThreadResumeResponse
	err = srv.conn.Call(ctx, protocol.MethodThreadResume, params, &resp)
	if err == nil {
		bindTools(threadID)
		srv.markThreadLoaded(threadID)
		srv.setThreadToolless(threadID, toolsConfig == nil)
		return threadID, false, nil
	}
	bindTools("")
	if ctx.Err() != nil {
		return "", false, fmt.Errorf("codex thread/resume: %w", err)
	}
	// The stored thread no longer exists on the codex side (wiped state,
	// version change). A session that can never run again is worse than one
	// that lost its runtime-side context: fall back to a fresh thread and
	// persist the new id.
	d.logger.Warn("codex thread/resume failed; starting a fresh thread",
		slog.String("thread_id", threadID), slog.Any("error", err))
	freshConfig, bindFresh, err2 := d.prepareThreadTools(srv, input)
	if err2 != nil {
		return "", false, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err2, map[string]string{"runtime": RuntimeType})
	}
	startParams := protocol.ThreadStartParams{
		Cwd:            &cwd,
		ApprovalPolicy: &approvalPolicy,
		Config:         freshConfig,
	}
	if cfg.Model != "" {
		startParams.Model = &cfg.Model
	}
	var startResp protocol.ThreadStartResponse
	if startErr := srv.conn.Call(ctx, protocol.MethodThreadStart, startParams, &startResp); startErr != nil {
		bindFresh("")
		return "", false, fmt.Errorf("codex thread/start after failed resume: %w", errors.Join(startErr, err))
	}
	if startResp.Thread.ID == "" {
		bindFresh("")
		return "", false, errors.New("codex thread/start returned no thread id")
	}
	bindFresh(startResp.Thread.ID)
	srv.markThreadLoaded(startResp.Thread.ID)
	srv.setThreadToolless(startResp.Thread.ID, freshConfig == nil)
	return startResp.Thread.ID, true, nil
}

// interruptTurn asks the app-server to stop the running turn; it runs on a
// background context because the turn context is already cancelled.
//
//nolint:contextcheck // the turn context is cancelled; the interrupt must still go out
func (d *Driver) interruptTurn(srv *appServer, threadID, turnID string) {
	if turnID == "" {
		return
	}
	interruptCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := srv.conn.Call(interruptCtx, protocol.MethodTurnInterrupt, protocol.TurnInterruptParams{
		ThreadID: threadID,
		TurnID:   turnID,
	}, nil)
	if err != nil {
		d.logger.Warn("codex turn/interrupt failed", slog.String("thread_id", threadID), slog.Any("error", err))
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

// startServer is the server table's start hook.
func (d *Driver) startServer(ctx context.Context, key string) (recyclable, error) {
	botID, botAgentID := splitServerKey(key)
	cfg, credential, err := d.resolveAgentConfig(ctx, botID, botAgentID, false)
	if err != nil {
		return nil, err
	}
	client, err := d.bridges.MCPClient(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("workspace bridge for bot %s: %w", botID, err)
	}
	info, err := d.bridges.WorkspaceInfo(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("workspace info for bot %s: %w", botID, err)
	}
	if cfg.Auth == AuthChatGPT && credential.ID != "" {
		if err := materializeChatGPTCredential(ctx, client, botAgentID, credential); err != nil {
			return nil, external.CredentialError(err)
		}
	}
	srv, err := startAppServerSession(context.WithoutCancel(ctx), botID, botAgentID, client, cfg, d.logger)
	if err != nil {
		return nil, err
	}
	srv.workspaceInfo = info
	srv.toolLookup = d.botGatewayToolLookup(botID)
	return srv, nil
}

// acquireServer returns the bot's live app-server plus a release the caller
// must defer; the reference keeps the server alive across a concurrent
// recycle (which drains instead of killing).
func (d *Driver) acquireServer(ctx context.Context, botID, botAgentID string) (*appServer, func(), error) {
	resource, release, err := d.servers.acquire(ctx, serverKey(botID, botAgentID))
	if err != nil {
		return nil, nil, err
	}
	return resource.(*appServer), release, nil
}

// CloseAll tears down every app-server (server shutdown).
func (d *Driver) CloseAll() {
	d.servers.closeAll()
}

// CloseBot recycles the bot's app-server: new work builds a fresh server on
// current configuration, while in-flight turns finish on the displaced one,
// which dies at their last release.
func (d *Driver) CloseBot(botID string) {
	prefix := botID + "\x00"
	d.servers.recycleWhere(func(key string) bool { return strings.HasPrefix(key, prefix) })
}

// ForkThread implements external.ThreadForker: it derives a new codex thread
// sharing history up to lastTurnID (inclusive; empty forks at the head) and
// returns the runtime-metadata delta naming the forked thread. The session's
// runtime metadata supplies the source thread id and working directory.
func (d *Driver) ForkThread(ctx context.Context, botID, botAgentID string, runtimeMetadata map[string]any, lastTurnID string) (map[string]any, error) {
	threadID := strings.TrimSpace(metadataString(runtimeMetadata, metadataThreadIDKey))
	if threadID == "" {
		return nil, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, errors.New("session has no codex thread to fork"), map[string]string{"runtime": RuntimeType})
	}
	srv, releaseServer, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeExternalRuntimeUnavailable, err, map[string]string{"runtime": RuntimeType})
	}
	defer releaseServer()
	cwd := strings.TrimSpace(metadataString(runtimeMetadata, "project_path"))
	if cwd == "" {
		cwd = defaultProjectPath
	}
	approvalPolicy := protocol.AskForApproval{Unit: protocol.AskForApprovalUnitOnRequest}
	params := protocol.ThreadForkParams{
		ThreadID:       threadID,
		Cwd:            &cwd,
		ApprovalPolicy: &approvalPolicy,
	}
	if trimmed := strings.TrimSpace(lastTurnID); trimmed != "" {
		params.LastTurnID = &trimmed
	}
	var resp protocol.ThreadForkResponse
	if err := srv.conn.Call(ctx, protocol.MethodThreadFork, params, &resp); err != nil {
		return nil, fmt.Errorf("codex thread/fork: %w", err)
	}
	if resp.Thread.ID == "" {
		return nil, errors.New("codex thread/fork returned no thread id")
	}
	// Deliberately not marked loaded: the forked session's first prompt goes
	// through thread/resume, which applies the per-thread tool-gateway config.
	return map[string]any{metadataThreadIDKey: resp.Thread.ID}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// DeviceLoginStart describes a pending ChatGPT device-code login.
type DeviceLoginStart struct {
	LoginID         string
	UserCode        string
	VerificationURL string
}

// DeviceLoginStatus is the poll answer for a pending device-code login.
type DeviceLoginStatus struct {
	// Status is pending, success, error, or unknown (login not tracked, e.g.
	// the app-server restarted).
	Status string
	Error  string
}

// StartChatGPTDeviceLogin begins a device-code login on the bot's codex,
// returning the code the user must enter. Completion lands in CODEX_HOME and
// is observable via PollDeviceLogin.
func (d *Driver) StartChatGPTDeviceLogin(ctx context.Context, botID, botAgentID string) (DeviceLoginStart, error) {
	cfg, _, err := d.resolveAgentConfig(ctx, botID, botAgentID, false)
	if err != nil {
		return DeviceLoginStart{}, err
	}
	if cfg.Auth != AuthChatGPT {
		return DeviceLoginStart{}, external.CredentialError(agentcredential.ErrIncompatible)
	}
	srv, releaseServer, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return DeviceLoginStart{}, err
	}
	defer releaseServer()
	var resp protocol.LoginAccountResponse
	err = srv.conn.Call(ctx, protocol.MethodAccountLoginStart, protocol.LoginAccountParams{
		ChatgptDeviceCode: &protocol.ChatgptDeviceCodeLoginAccountParams{},
	}, &resp)
	if err != nil {
		return DeviceLoginStart{}, err
	}
	device := resp.ChatgptDeviceCode
	if device == nil || device.LoginID == "" || device.UserCode == "" || device.VerificationURL == "" {
		return DeviceLoginStart{}, errors.New("codex device login response is incomplete")
	}
	srv.trackLogin(device.LoginID)
	return DeviceLoginStart{
		LoginID:         device.LoginID,
		UserCode:        device.UserCode,
		VerificationURL: device.VerificationURL,
	}, nil
}

// PollDeviceLogin reports a tracked login's state.
func (d *Driver) PollDeviceLogin(botID, botAgentID, loginID string) DeviceLoginStatus {
	resource := d.servers.peek(serverKey(botID, botAgentID))
	if resource == nil {
		return DeviceLoginStatus{Status: "unknown"}
	}
	srv := resource.(*appServer)
	outcome, ok := srv.loginStatus(loginID)
	switch {
	case !ok:
		return DeviceLoginStatus{Status: "unknown"}
	case !outcome.Done:
		return DeviceLoginStatus{Status: "pending"}
	case outcome.Success:
		return DeviceLoginStatus{Status: "success"}
	default:
		srv.forgetLogin(loginID)
		message := outcome.Error
		if message == "" {
			message = "codex login failed"
		}
		return DeviceLoginStatus{Status: "error", Error: message}
	}
}

func (d *Driver) CompleteChatGPTDeviceLogin(ctx context.Context, ownerUserID, botID, botAgentID, loginID string) error {
	srv, releaseServer, err := d.acquireServer(ctx, botID, botAgentID)
	if err != nil {
		return err
	}
	defer releaseServer()
	outcome, ok := srv.loginStatus(loginID)
	if !ok || !outcome.Done || !outcome.Success {
		return errors.New("codex device login is not complete")
	}
	credential, err := readChatGPTCredential(ctx, srv.client, botAgentID)
	if err != nil {
		return err
	}
	_, err = d.credentials.AttachToBotAgent(ctx, ownerUserID, botID, botAgentID, agentcredential.CreateRequest{
		Provider: agentcredential.ProviderOpenAI,
		AuthKind: agentcredential.AuthKindOpenAICodexOAuth,
		Secret: map[string]string{
			"access_token":  credential.accessToken,
			"id_token":      credential.idToken,
			"refresh_token": credential.refreshToken,
			"account_id":    credential.accountID,
		},
		AccountMetadata: map[string]any{
			"account_id":   credential.accountID,
			"last_refresh": credential.lastRefresh.UTC().Format(time.RFC3339Nano),
		},
	})
	if err != nil {
		return external.CredentialError(err)
	}
	srv.forgetLogin(loginID)
	return nil
}

func (d *Driver) PurgeBotAgentAuth(ctx context.Context, botID, botAgentID string) error {
	if resource := d.servers.peek(serverKey(botID, botAgentID)); resource != nil && resource.(*appServer).hasActiveTurns() {
		return apperror.New(apperror.CodeAgentCredentialRuntimeBusy, nil)
	}
	d.ResetBotAgent(botID, botAgentID)
	client, err := d.bridges.MCPClient(ctx, botID)
	if err != nil {
		return err
	}
	err = client.DeleteFile(ctx, path.Join(codexHome(botAgentID), "auth.json"), false)
	if errors.Is(err, bridge.ErrNotFound) {
		return nil
	}
	return err
}

// CancelDeviceLogin aborts a pending device-code login.
func (d *Driver) CancelDeviceLogin(ctx context.Context, botID, botAgentID, loginID string) error {
	resource := d.servers.peek(serverKey(botID, botAgentID))
	if resource == nil {
		return nil
	}
	srv := resource.(*appServer)
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
