package modelchecker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/healthcheck"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
)

const (
	checkTypeModelConnection = "model.connection"
	titleKeyModelConnection  = "bots.checks.titles.modelConnection"
	defaultTimeout           = models.DefaultProviderProbeTimeout
)

// BotModelLookup fetches model IDs configured for a bot.
type BotModelLookup interface {
	GetBotModelIDs(ctx context.Context, botID string) (BotModels, error)
}

// BotModels holds the model UUIDs associated with a bot.
type BotModels struct {
	OwnerUserID      string
	ChatModelID      string
	MemoryModelID    string
	EmbeddingModelID string
}

// ModelProber probes a model by its internal UUID.
type ModelProber interface {
	Test(ctx context.Context, id string) (models.TestResponse, error)
}

// Checker evaluates model connection health checks for a bot.
type Checker struct {
	logger  *slog.Logger
	lookup  BotModelLookup
	prober  ModelProber
	timeout time.Duration
}

// NewChecker creates a model health checker.
func NewChecker(log *slog.Logger, lookup BotModelLookup, prober ModelProber) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{
		logger:  log.With(slog.String("checker", "healthcheck_model")),
		lookup:  lookup,
		prober:  prober,
		timeout: defaultTimeout,
	}
}

type modelSlot struct {
	key   string // "chat", "memory", "embedding"
	id    string // model UUID
	label string // subtitle for display
}

// ListChecks evaluates model health for a bot.
func (c *Checker) ListChecks(ctx context.Context, botID string) []healthcheck.CheckResult {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return nil
	}
	if c.lookup == nil || c.prober == nil {
		c.logger.Warn("model healthcheck dependencies unavailable", slog.String("bot_id", botID))
		return []healthcheck.CheckResult{{
			ID:       checkTypeModelConnection + ".service",
			Type:     checkTypeModelConnection,
			TitleKey: titleKeyModelConnection,
			Status:   healthcheck.StatusWarn,
			Summary:  "Model checker service is not available.",
		}}
	}

	botModels, err := c.lookup.GetBotModelIDs(ctx, botID)
	if err != nil {
		c.logger.Warn("model healthcheck lookup failed", slog.String("bot_id", botID), slog.Any("error", err))
		return []healthcheck.CheckResult{{
			ID:       checkTypeModelConnection + ".lookup",
			Type:     checkTypeModelConnection,
			TitleKey: titleKeyModelConnection,
			Status:   healthcheck.StatusError,
			Summary:  "Failed to look up bot model configuration.",
			Detail:   err.Error(),
		}}
	}

	var slots []modelSlot
	if botModels.ChatModelID != "" {
		slots = append(slots, modelSlot{key: "chat", id: botModels.ChatModelID, label: "Chat Model"})
	}
	if botModels.MemoryModelID != "" {
		slots = append(slots, modelSlot{key: "memory", id: botModels.MemoryModelID, label: "Memory Model"})
	}
	if botModels.EmbeddingModelID != "" {
		slots = append(slots, modelSlot{key: "embedding", id: botModels.EmbeddingModelID, label: "Embedding Model"})
	}
	if len(slots) == 0 {
		return nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	results := make([]healthcheck.CheckResult, len(slots))
	var wg sync.WaitGroup
	for i, slot := range slots {
		wg.Add(1)
		go func(idx int, s modelSlot) {
			defer wg.Done()
			slotCtx := oauthctx.WithUserID(probeCtx, botModels.OwnerUserID)
			results[idx] = c.probeSlot(slotCtx, s)
		}(i, slot)
	}
	wg.Wait()

	return results
}

func (c *Checker) probeSlot(ctx context.Context, s modelSlot) healthcheck.CheckResult {
	checkID := checkTypeModelConnection + "." + s.key
	result := healthcheck.CheckResult{
		ID:       checkID,
		Type:     checkTypeModelConnection,
		TitleKey: titleKeyModelConnection,
		Subtitle: s.label,
		Metadata: map[string]any{
			"model_id": s.id,
			"role":     s.key,
		},
	}

	resp, err := c.prober.Test(ctx, s.id)
	if err != nil {
		result.Status = healthcheck.StatusError
		result.Summary = fmt.Sprintf("%s is not reachable.", s.label)
		result.Detail = err.Error()
		return result
	}

	switch resp.Status {
	case models.TestStatusOK:
		result.Status = healthcheck.StatusOK
		result.Summary = fmt.Sprintf("%s is healthy.", s.label)
	case models.TestStatusAuthError:
		result.Status = healthcheck.StatusError
		result.Summary = fmt.Sprintf("%s authentication failed.", s.label)
		result.Detail = resp.Message
	default:
		result.Status = healthcheck.StatusError
		result.Summary = fmt.Sprintf("%s probe failed.", s.label)
		result.Detail = resp.Message
	}

	if resp.LatencyMs > 0 {
		result.Metadata["latency_ms"] = resp.LatencyMs
	}

	return result
}
