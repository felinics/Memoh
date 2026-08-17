package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/agent/event"
	acpagent "github.com/memohai/memoh/internal/agent/runtime/acp"
	acpclient "github.com/memohai/memoh/internal/agent/runtime/acp/client"
	"github.com/memohai/memoh/internal/agent/runtime/native"
	"github.com/memohai/memoh/internal/agent/sessionmode"
	"github.com/memohai/memoh/internal/heartbeat"
	"github.com/memohai/memoh/internal/schedule"
)

// attachCurrentTurnPrompt routes a trigger's rich prompt through Query so the
// context view classifies it as the live current request rather than history.
func attachCurrentTurnPrompt(cfg native.RunConfig, prompt string) native.RunConfig {
	cfg.Query = prompt
	return cfg
}

// TriggerSchedule executes a scheduled command via the internal agent.
func (s *Service) TriggerSchedule(ctx context.Context, botID string, payload schedule.TriggerPayload, token string) (triggerResult schedule.TriggerResult, err error) {
	if strings.TrimSpace(botID) == "" {
		return schedule.TriggerResult{}, errors.New("bot id is required")
	}
	if strings.TrimSpace(payload.Command) == "" {
		return schedule.TriggerResult{}, errors.New("schedule command is required")
	}

	submission, err := json.Marshal(scheduleSubmission{
		Kind:       "schedule",
		ScheduleID: strings.TrimSpace(payload.ID),
		Command:    payload.Command,
	})
	if err != nil {
		return schedule.TriggerResult{}, err
	}
	runCtx, admission, finish, err := s.admitTriggeredRun(ctx, botID, payload.SessionID, scheduleInvocationID(payload), submission)
	if err != nil {
		// Including a busy answer: a fire that cannot take the thread's slot has
		// no value once the next one is due, so it is reported and dropped rather
		// than retried here.
		return schedule.TriggerResult{}, err
	}
	defer func() { finish(triggeredRunTerminal{cause: err}) }()
	ctx = runCtx

	// Sessions with an ACP runtime execute through the session pool; the
	// schedule's model/effort overrides ride the per-prompt input.
	acpInfo, err := s.ACPSessionExecutionInfo(ctx, payload.SessionID)
	if err != nil {
		return schedule.TriggerResult{}, err
	}
	if acpInfo.IsACP {
		return s.triggerScheduleACP(ctx, botID, payload, token, admission.RunID, acpInfo)
	}

	req := ChatRequest{
		BotID:           botID,
		ChatID:          botID,
		ThreadID:        payload.SessionID,
		RunID:           admission.RunID,
		Query:           payload.Command,
		UserID:          payload.OwnerUserID,
		Token:           token,
		Model:           payload.ModelID,
		ReasoningEffort: payload.ReasoningEffort,
		SessionType:     sessionmode.Schedule,
	}
	rc, req, err := s.resolve(ctx, req)
	if err != nil {
		return schedule.TriggerResult{}, err
	}
	req.RunID = rc.runConfig.RunID

	cfg := rc.runConfig
	cfg.SessionType = sessionmode.Schedule
	cfg.Identity.ChannelIdentityID = strings.TrimSpace(payload.OwnerUserID)
	cfg.ContextScope.ChannelIdentityID = strings.TrimSpace(payload.OwnerUserID)

	schedulePrompt := native.GenerateSchedulePrompt(native.Schedule{
		ID:          payload.ID,
		Name:        payload.Name,
		Description: payload.Description,
		Pattern:     payload.Pattern,
		MaxCalls:    payload.MaxCalls,
		Command:     payload.Command,
	})
	cfg = attachCurrentTurnPrompt(cfg, schedulePrompt)
	cfg = s.prepareRunConfig(ctx, cfg)
	terminal := s.contextLifecycleTerminal(ctx, cfg)
	var lifecycleCause error
	defer func() { terminal(lifecycleCause) }()

	result, err := s.agent.Generate(ctx, cfg)
	lifecycleCause = err
	if err != nil {
		return schedule.TriggerResult{}, err
	}

	outputMessages := sdkMessagesToModelMessages(result.Messages)
	roundMessages := prependUserMessage(req.Query, outputMessages)
	storeErr := s.storeRoundWithOptions(ctx, req, roundMessages, rc.model.ID, storeRoundOptions{
		ContextLifecycle: cfg.ContextLifecycle,
	})
	if storeErr != nil {
		lifecycleCause = storeErr
	}

	totalUsageJSON, _ := json.Marshal(result.Usage)
	return schedule.TriggerResult{
		Status:     "ok",
		Text:       strings.TrimSpace(result.Text),
		UsageBytes: totalUsageJSON,
		ModelID:    rc.model.ID,
	}, storeErr
}

// triggerScheduleACP runs one schedule fire through the ACP session pool.
// The run is non-interactive: no user input requests, no streaming consumer
// — events are dropped and only the final result is reported back to the
// schedule log. The round persists into the session history exactly like an
// interactive ACP turn so the produced conversation can be opened and
// continued.
func (s *Service) triggerScheduleACP(ctx context.Context, botID string, payload schedule.TriggerPayload, token, runID string, info ACPSessionExecutionInfo) (schedule.TriggerResult, error) {
	if s.acpPool == nil {
		return schedule.TriggerResult{}, errors.New("ACP session pool is not configured")
	}
	runtimeOwner := strings.TrimSpace(info.RuntimeOwnerAccountID)
	if runtimeOwner == "" {
		return schedule.TriggerResult{}, errors.New("ACP runtime owner is missing; recreate the schedule or its session")
	}
	if err := s.requireACPRuntimeOwnerWorkspaceExec(ctx, botID, runtimeOwner); err != nil {
		return schedule.TriggerResult{}, err
	}

	req := ChatRequest{
		BotID:           botID,
		ChatID:          botID,
		ThreadID:        payload.SessionID,
		RunID:           runID,
		Query:           payload.Command,
		RawQuery:        payload.Command,
		UserID:          payload.OwnerUserID,
		Token:           token,
		Model:           payload.ACPModelID,
		ReasoningEffort: payload.ReasoningEffort,
		SessionType:     sessionmode.Schedule,
	}

	schedulePrompt := native.GenerateSchedulePrompt(native.Schedule{
		ID:          payload.ID,
		Name:        payload.Name,
		Description: payload.Description,
		Pattern:     payload.Pattern,
		MaxCalls:    payload.MaxCalls,
		Command:     payload.Command,
	})
	contextMarkdown := s.buildACPContextMarkdown(ctx, req, info.AgentID, info.ProjectPath)
	contextLifecycle := contextfrag.NewLifecycleHolder()
	contextLifecycle.SetManifest(contextfrag.BuildManifest(nil))
	terminal := s.contextLifecycleTerminal(ctx, native.RunConfig{
		RunID: runID,
		Identity: native.SessionContext{
			BotID:     botID,
			SessionID: payload.SessionID,
		},
		ContextLifecycle: contextLifecycle,
	})
	var lifecycleCause error
	defer func() { terminal(lifecycleCause) }()

	req, _ = s.persistACPLeadingUserMessage(context.WithoutCancel(ctx), req)

	result, promptErr := s.acpPool.Prompt(ctx, acpagent.PromptInput{
		BotID:             botID,
		ChatID:            botID,
		SessionID:         payload.SessionID,
		RunID:             runID,
		SessionType:       sessionmode.Schedule,
		AgentID:           info.AgentID,
		ProjectPath:       info.ProjectPath,
		ModelID:           strings.TrimSpace(payload.ACPModelID),
		ReasoningEffort:   strings.TrimSpace(payload.ReasoningEffort),
		Prompt:            schedulePrompt,
		ChannelIdentityID: strings.TrimSpace(payload.OwnerUserID),
		SessionToken:      token,
		// Nobody is on the other end of a scheduled run.
		CanRequestUserInput:   false,
		SupportsImageInput:    false,
		ToolOutputLimit:       s.toolOutputLimit(),
		ContextURI:            acpContextURI,
		ContextMarkdown:       contextMarkdown,
		RuntimeOwnerAccountID: runtimeOwner,
		Sink:                  acpclient.EventSinkFunc(func(event.StreamEvent) {}),
	})
	lifecycleCause = promptErr
	if promptErr != nil {
		s.cancelPendingACPApprovals(context.WithoutCancel(ctx), req, "tool approval cancelled: the scheduled run ended before a decision arrived")
		failedResult, _ := acpFailureResult(ensureACPPromptOutput(result), promptErr)
		if err := s.persistACPRound(context.WithoutCancel(ctx), req, info.AgentID, info.ProjectPath, failedResult, promptErr, contextLifecycle); err != nil {
			lifecycleCause = runtimeHistoryError(err)
			s.logger.Error("ACP schedule failure persist failed", slog.Any("error", err), slog.String("session_id", payload.SessionID))
		}
		return schedule.TriggerResult{}, promptErr
	}

	result = ensureACPPromptOutput(result)
	if err := s.persistACPRound(context.WithoutCancel(ctx), req, info.AgentID, info.ProjectPath, result, nil, contextLifecycle); err != nil {
		lifecycleCause = runtimeHistoryError(err)
		s.logger.Error("ACP schedule persist failed", slog.Any("error", err), slog.String("session_id", payload.SessionID))
		return schedule.TriggerResult{}, err
	}

	var usageJSON []byte
	if result.Usage != nil {
		usageJSON, _ = json.Marshal(result.Usage)
	}
	return schedule.TriggerResult{
		Status:     "ok",
		Text:       strings.TrimSpace(result.Text),
		UsageBytes: usageJSON,
	}, nil
}

// TriggerHeartbeat executes a heartbeat check via the internal agent.
func (s *Service) TriggerHeartbeat(ctx context.Context, botID string, payload heartbeat.TriggerPayload, token string) (triggerResult heartbeat.TriggerResult, err error) {
	if strings.TrimSpace(botID) == "" {
		return heartbeat.TriggerResult{}, errors.New("bot id is required")
	}

	submission, err := json.Marshal(heartbeatSubmission{
		Kind:     "heartbeat",
		BotID:    strings.TrimSpace(botID),
		Interval: payload.Interval,
	})
	if err != nil {
		return heartbeat.TriggerResult{}, err
	}
	runCtx, admission, finish, err := s.admitTriggeredRun(ctx, botID, payload.SessionID, heartbeatInvocationID(payload), submission)
	if err != nil {
		// A tick that cannot take the thread's slot is dropped: the next tick is
		// already scheduled and a stale check has nothing to report.
		return heartbeat.TriggerResult{}, err
	}
	defer func() { finish(triggeredRunTerminal{cause: err}) }()
	ctx = runCtx

	var heartbeatModel string
	if botSettings, err := s.loadBotSettings(ctx, botID); err == nil {
		heartbeatModel = strings.TrimSpace(botSettings.HeartbeatModelID)
	}

	req := ChatRequest{
		BotID:       botID,
		ChatID:      botID,
		ThreadID:    payload.SessionID,
		RunID:       admission.RunID,
		Query:       "heartbeat",
		UserID:      payload.OwnerUserID,
		Token:       token,
		Model:       heartbeatModel,
		SessionType: sessionmode.Heartbeat,
	}
	rc, req, err := s.resolve(ctx, req)
	if err != nil {
		return heartbeat.TriggerResult{}, err
	}
	req.RunID = rc.runConfig.RunID

	cfg := rc.runConfig
	cfg.SessionType = sessionmode.Heartbeat
	cfg.Identity.ChannelIdentityID = strings.TrimSpace(payload.OwnerUserID)
	cfg.ContextScope.ChannelIdentityID = strings.TrimSpace(payload.OwnerUserID)

	var checklist string
	if s.agent != nil {
		nowFn := time.Now
		if cfg.Identity.TimezoneLocation != nil {
			nowFn = func() time.Time { return time.Now().In(cfg.Identity.TimezoneLocation) }
		}
		fs := native.NewFSClient(s.agent.BridgeProvider(), botID, nowFn)
		checklist = fs.ReadTextSafe(ctx, "/data/HEARTBEAT.md")
	}
	now := time.Now().UTC()
	if cfg.Identity.TimezoneLocation != nil {
		now = now.In(cfg.Identity.TimezoneLocation)
	}
	heartbeatPrompt := native.GenerateHeartbeatPrompt(payload.Interval, checklist, now, payload.LastHeartbeatAt)
	cfg = attachCurrentTurnPrompt(cfg, heartbeatPrompt)
	cfg = s.prepareRunConfig(ctx, cfg)
	terminal := s.contextLifecycleTerminal(ctx, cfg)
	var lifecycleCause error
	defer func() { terminal(lifecycleCause) }()

	result, err := s.agent.Generate(ctx, cfg)
	lifecycleCause = err
	if err != nil {
		return heartbeat.TriggerResult{}, err
	}

	status := "alert"
	text := strings.TrimSpace(result.Text)
	if isHeartbeatOK(text) {
		status = "ok"
	}

	outputMessages := sdkMessagesToModelMessages(result.Messages)
	roundMessages := prependUserMessage(heartbeatPrompt, outputMessages)
	if storeErr := s.storeRoundWithOptions(ctx, req, roundMessages, rc.model.ID, storeRoundOptions{
		ContextLifecycle: cfg.ContextLifecycle,
	}); storeErr != nil {
		lifecycleCause = storeErr
	}

	totalUsageJSON, _ := json.Marshal(result.Usage)
	return heartbeat.TriggerResult{
		Status:     status,
		Text:       text,
		Usage:      totalUsageJSON,
		UsageBytes: totalUsageJSON,
		ModelID:    rc.model.ID,
		SessionID:  payload.SessionID,
	}, nil
}

// scheduleSubmission and heartbeatSubmission are the canonical fingerprint
// inputs for a triggered turn. They carry what the trigger asked for and nothing
// about when it ran, so re-running one tick's work is recognized as the same
// submission rather than a new one.
type scheduleSubmission struct {
	Kind       string `json:"kind"`
	ScheduleID string `json:"schedule_id"`
	Command    string `json:"command"`
}

type heartbeatSubmission struct {
	Kind     string `json:"kind"`
	BotID    string `json:"bot_id"`
	Interval int    `json:"interval_minutes"`
}

// scheduleInvocationID and heartbeatInvocationID name one fire.
//
// Each fire runs in a thread of its own, and invocation uniqueness is already
// scoped per thread, so the thread id is what distinguishes consecutive fires.
// Naming it explicitly also keeps these ids correct if a schedule ever reuses one
// thread across fires, which would otherwise make every fire after the first look
// like a replay of the first.
func scheduleInvocationID(payload schedule.TriggerPayload) string {
	return "schedule:" + strings.TrimSpace(payload.ID) + ":" + strings.TrimSpace(payload.SessionID)
}

func heartbeatInvocationID(payload heartbeat.TriggerPayload) string {
	return "heartbeat:" + strings.TrimSpace(payload.SessionID)
}

func isHeartbeatOK(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "HEARTBEAT_OK") || strings.HasSuffix(t, "HEARTBEAT_OK") || t == "HEARTBEAT_OK"
}
