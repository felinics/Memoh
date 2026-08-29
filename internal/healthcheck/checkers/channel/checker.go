package channelchecker

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/healthcheck"
)

const (
	checkTypeChannelConnection = "channel.connection"
	titleKeyChannelConnection  = "bots.checks.titles.channelConnection"
)

// ConnectionObserver reads runtime channel connection statuses.
type ConnectionObserver interface {
	ConnectionStatusesByBot(botID string) []channel.ConnectionStatus
}

// Checker evaluates channel connection health checks.
type Checker struct {
	logger   *slog.Logger
	observer ConnectionObserver
}

// NewChecker creates a channel health checker.
func NewChecker(log *slog.Logger, observer ConnectionObserver) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{
		logger:   log.With(slog.String("checker", "healthcheck_channel")),
		observer: observer,
	}
}

// ListChecks evaluates channel connection statuses for a bot.
func (c *Checker) ListChecks(ctx context.Context, botID string) []healthcheck.CheckResult {
	// Connection observer is context-free; best effort early cancellation guard.
	if err := ctx.Err(); err != nil {
		return []healthcheck.CheckResult{}
	}
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return []healthcheck.CheckResult{}
	}
	if c.observer == nil {
		if c.logger != nil {
			c.logger.Warn(
				"channel healthcheck dependency is unavailable",
				slog.String("bot_id", botID),
			)
		}
		return []healthcheck.CheckResult{
			{
				ID:       checkTypeChannelConnection + ".service",
				Type:     checkTypeChannelConnection,
				TitleKey: titleKeyChannelConnection,
				Status:   healthcheck.StatusWarn,
				Summary:  "Channel checker service is not available.",
				Detail:   "connection observer is nil",
			},
		}
	}

	statuses := c.observer.ConnectionStatusesByBot(botID)
	if len(statuses) == 0 {
		return []healthcheck.CheckResult{}
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].ChannelType == statuses[j].ChannelType {
			return statuses[i].ConfigID < statuses[j].ConfigID
		}
		return statuses[i].ChannelType < statuses[j].ChannelType
	})

	checks := make([]healthcheck.CheckResult, 0, len(statuses))
	for idx, status := range statuses {
		channelType := strings.TrimSpace(status.ChannelType.String())
		if channelType == "" {
			channelType = "unknown"
		}
		checkID := buildCheckID(status.ConfigID, idx)
		subtitle := buildSubtitle(channelType, status.ConfigID)
		item := healthcheck.CheckResult{
			ID:       checkID,
			Type:     checkTypeChannelConnection,
			TitleKey: titleKeyChannelConnection,
			Subtitle: subtitle,
			Status:   healthcheck.StatusError,
			Summary:  fmt.Sprintf("Channel %s connection is down.", channelType),
			Metadata: map[string]any{
				"config_id":    status.ConfigID,
				"channel_type": channelType,
				"running":      status.Running,
			},
		}
		if status.UpdatedAt.Unix() > 0 {
			item.Metadata["updated_at"] = status.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		if status.Running {
			item.Status = healthcheck.StatusOK
			item.Summary = fmt.Sprintf("Channel %s is connected.", channelType)
		} else if strings.TrimSpace(status.LastError) != "" {
			item.Summary = fmt.Sprintf("Channel %s connection failed.", channelType)
			item.Detail = strings.TrimSpace(status.LastError)
		}
		checks = append(checks, item)
	}
	return checks
}

func buildCheckID(configID string, idx int) string {
	configID = strings.TrimSpace(configID)
	if configID != "" {
		return checkTypeChannelConnection + "." + configID
	}
	return fmt.Sprintf("%s.unknown_%d", checkTypeChannelConnection, idx+1)
}

func buildSubtitle(channelType, configID string) string {
	configID = strings.TrimSpace(configID)
	if configID == "" {
		return channelType
	}
	if len(configID) > 8 {
		configID = configID[:8]
	}
	return channelType + " (" + configID + ")"
}
