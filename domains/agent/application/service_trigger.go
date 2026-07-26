package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/domains/agent/automation/heartbeat"
	"github.com/memohai/memoh/domains/agent/automation/schedule"
	"github.com/memohai/memoh/domains/agent/chat/convert"
	"github.com/memohai/memoh/domains/agent/engine"
	sessionmode "github.com/memohai/memoh/domains/agent/chat/session/mode"
)

// TriggerSchedule executes a scheduled command via the internal agent.
func (s *Service) TriggerSchedule(ctx context.Context, botID string, payload schedule.TriggerPayload, token string) (schedule.TriggerResult, error) {
	if strings.TrimSpace(botID) == "" {
		return schedule.TriggerResult{}, errors.New("bot id is required")
	}
	if strings.TrimSpace(payload.Command) == "" {
		return schedule.TriggerResult{}, errors.New("schedule command is required")
	}

	req := ChatRequest{
		BotID:       botID,
		ChatID:      botID,
		ThreadID:    payload.SessionID,
		Query:       payload.Command,
		UserID:      payload.OwnerUserID,
		Token:       token,
		SessionType: sessionmode.Schedule,
	}
	rc, err := s.resolve(ctx, req)
	if err != nil {
		return schedule.TriggerResult{}, err
	}

	cfg := rc.runConfig
	cfg.SessionType = sessionmode.Schedule
	cfg.Identity.ChannelIdentityID = strings.TrimSpace(payload.OwnerUserID)
	cfg.ContextScope.ChannelIdentityID = strings.TrimSpace(payload.OwnerUserID)

	schedulePrompt := engine.GenerateSchedulePrompt(engine.Schedule{
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

	outputMessages := convert.SDKMessagesToModelMessages(result.Messages)
	roundMessages := convert.PrependUserMessage(req.Query, outputMessages)
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
func (s *Service) TriggerHeartbeat(ctx context.Context, botID string, payload heartbeat.TriggerPayload, token string) (heartbeat.TriggerResult, error) {
	if strings.TrimSpace(botID) == "" {
		return heartbeat.TriggerResult{}, errors.New("bot id is required")
	}

	var heartbeatModel string
	if botSettings, err := s.loadBotSettings(ctx, botID); err == nil {
		heartbeatModel = strings.TrimSpace(botSettings.HeartbeatModelID)
	}

	req := ChatRequest{
		BotID:       botID,
		ChatID:      botID,
		ThreadID:    payload.SessionID,
		Query:       "heartbeat",
		UserID:      payload.OwnerUserID,
		Token:       token,
		Model:       heartbeatModel,
		SessionType: sessionmode.Heartbeat,
	}
	rc, err := s.resolve(ctx, req)
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
		fs := engine.NewFSClient(s.agent.BridgeProvider(), botID, nowFn)
		checklist = fs.ReadTextSafe(ctx, "/data/HEARTBEAT.md")
	}
	now := time.Now().UTC()
	if cfg.Identity.TimezoneLocation != nil {
		now = now.In(cfg.Identity.TimezoneLocation)
	}
	heartbeatPrompt := engine.GenerateHeartbeatPrompt(payload.Interval, checklist, now, payload.LastHeartbeatAt)
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

	outputMessages := convert.SDKMessagesToModelMessages(result.Messages)
	roundMessages := convert.PrependUserMessage(heartbeatPrompt, outputMessages)
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

func isHeartbeatOK(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "HEARTBEAT_OK") || strings.HasSuffix(t, "HEARTBEAT_OK") || t == "HEARTBEAT_OK"
}
