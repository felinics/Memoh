package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/runtime/native"
	"github.com/memohai/memoh/internal/agent/sessionmode"
	"github.com/memohai/memoh/internal/heartbeat"
	"github.com/memohai/memoh/internal/schedule"
)

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
	defer func() { finish(err) }()
	ctx = runCtx

	req := ChatRequest{
		BotID:       botID,
		ChatID:      botID,
		ThreadID:    payload.SessionID,
		RunID:       admission.RunID,
		Query:       payload.Command,
		UserID:      payload.OwnerUserID,
		Token:       token,
		SessionType: sessionmode.Schedule,
	}
	rc, req, err := s.resolve(ctx, req)
	if err != nil {
		return schedule.TriggerResult{}, err
	}

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
	cfg.Messages = append(cfg.Messages, sdk.UserMessage(schedulePrompt))
	cfg = s.prepareRunConfig(ctx, cfg)

	result, err := s.agent.Generate(ctx, cfg)
	if err != nil {
		return schedule.TriggerResult{}, err
	}

	outputMessages := sdkMessagesToModelMessages(result.Messages)
	roundMessages := prependUserMessage(req.Query, outputMessages)
	storeErr := s.storeRound(ctx, req, roundMessages, rc.model.ID)

	totalUsageJSON, _ := json.Marshal(result.Usage)
	return schedule.TriggerResult{
		Status:     "ok",
		Text:       strings.TrimSpace(result.Text),
		UsageBytes: totalUsageJSON,
		ModelID:    rc.model.ID,
	}, storeErr
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
	defer func() { finish(err) }()
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
	cfg.Messages = append(cfg.Messages, sdk.UserMessage(heartbeatPrompt))
	cfg = s.prepareRunConfig(ctx, cfg)

	result, err := s.agent.Generate(ctx, cfg)
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
	_ = s.storeRound(ctx, req, roundMessages, rc.model.ID)

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
