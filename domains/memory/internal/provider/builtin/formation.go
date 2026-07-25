package builtin

import (
	"context"
	"log/slog"
	"strings"
	"time"

	memport "github.com/memohai/memoh/domains/memory/internal/port"
	memreg "github.com/memohai/memoh/domains/memory/registry"
)

const (
	formationTimeout       = 60 * time.Second
	candidateSearchLimit   = 20
	candidateGetAllLimit   = 50
	maxCandidatesPerDecide = 30

	actionADD    = "ADD"
	actionUPDATE = "UPDATE"
	actionDELETE = "DELETE"
	actionNOOP   = "NOOP"
)

// formationResult holds the outcome of a memory formation cycle.
type formationResult struct {
	ExtractedFacts int
	Added          int
	Updated        int
	Deleted        int
	Skipped        int
}

// runFormation executes the Extract -> candidate retrieval -> Decide -> apply pipeline.
func runFormation(ctx context.Context, logger *slog.Logger, llm memreg.LLM, runtime Runtime, req memreg.AfterChatRequest) formationResult {
	ctx, cancel := context.WithTimeout(ctx, formationTimeout)
	defer cancel()

	botID := strings.TrimSpace(req.BotID)
	result := formationResult{}

	extracted, err := llm.Extract(ctx, memreg.ExtractRequest{
		BotID:            botID,
		Messages:         req.Messages,
		TimezoneLocation: req.TimezoneLocation,
	})
	if err != nil {
		logger.WarnContext(ctx, "memory formation: extract failed", slog.String("bot_id", botID), slog.Any("error", err))
		return result
	}
	facts := filterNonEmpty(extracted.Facts)
	if len(facts) == 0 {
		return result
	}
	result.ExtractedFacts = len(facts)

	candidates := gatherCandidates(ctx, logger, runtime, botID, facts)

	decided, err := llm.Decide(ctx, memreg.DecideRequest{
		BotID:      botID,
		Facts:      facts,
		Candidates: candidates,
	})
	if err != nil {
		logger.WarnContext(ctx, "memory formation: decide failed", slog.String("bot_id", botID), slog.Any("error", err))
		return result
	}

	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}
	metadata := memport.BuildProfileMetadata(req.UserID, req.ChannelIdentityID, req.DisplayName)

	applyActions(ctx, logger, runtime, botID, decided.Actions, filters, metadata, &result)
	return result
}

// gatherCandidates collects existing memories relevant to the extracted facts.
func gatherCandidates(ctx context.Context, logger *slog.Logger, runtime Runtime, botID string, facts []string) []memreg.CandidateMemory {
	seen := make(map[string]struct{})
	candidates := make([]memreg.CandidateMemory, 0, candidateSearchLimit)

	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}

	for _, fact := range facts {
		if len(candidates) >= maxCandidatesPerDecide {
			break
		}
		resp, err := runtime.Search(ctx, memreg.SearchRequest{
			Query:   fact,
			BotID:   botID,
			Limit:   candidateSearchLimit / max(len(facts), 1),
			Filters: filters,
			NoStats: true,
		})
		if err != nil {
			logger.DebugContext(ctx, "memory formation: search candidates failed", slog.String("bot_id", botID), slog.Any("error", err))
			continue
		}
		for _, item := range resp.Results {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			candidates = append(candidates, memreg.CandidateMemory{
				ID:        id,
				Memory:    item.Memory,
				CreatedAt: item.CreatedAt,
				Metadata:  item.Metadata,
			})
			if len(candidates) >= maxCandidatesPerDecide {
				break
			}
		}
	}

	if len(candidates) < maxCandidatesPerDecide {
		resp, err := runtime.GetAll(ctx, memreg.GetAllRequest{
			BotID:   botID,
			Limit:   candidateGetAllLimit,
			Filters: filters,
			NoStats: true,
		})
		if err == nil {
			for _, item := range resp.Results {
				id := strings.TrimSpace(item.ID)
				if id == "" {
					continue
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				candidates = append(candidates, memreg.CandidateMemory{
					ID:        id,
					Memory:    item.Memory,
					CreatedAt: item.CreatedAt,
					Metadata:  item.Metadata,
				})
				if len(candidates) >= maxCandidatesPerDecide {
					break
				}
			}
		}
	}

	return candidates
}

// applyActions executes the decided CRUD actions against the runtime.
func applyActions(ctx context.Context, logger *slog.Logger, runtime Runtime, botID string, actions []memreg.DecisionAction, filters map[string]any, metadata map[string]any, result *formationResult) {
	deleted := make(map[string]struct{})
	updated := make(map[string]struct{})

	for _, action := range actions {
		event := strings.ToUpper(strings.TrimSpace(action.Event))
		switch event {
		case actionADD:
			text := strings.TrimSpace(action.Text)
			if text == "" {
				logger.DebugContext(ctx, "memory formation: ADD skipped (empty text)", slog.String("bot_id", botID))
				result.Skipped++
				continue
			}
			if _, err := runtime.Add(ctx, memreg.AddRequest{
				Message:  text,
				BotID:    botID,
				Metadata: metadata,
				Filters:  filters,
			}); err != nil {
				logger.WarnContext(ctx, "memory formation: ADD failed", slog.String("bot_id", botID), slog.Any("error", err))
			} else {
				result.Added++
			}

		case actionUPDATE:
			id := strings.TrimSpace(action.ID)
			text := strings.TrimSpace(action.Text)
			if id == "" || text == "" {
				logger.DebugContext(ctx, "memory formation: UPDATE skipped (missing id or text)", slog.String("bot_id", botID))
				result.Skipped++
				continue
			}
			if _, ok := updated[id]; ok {
				result.Skipped++
				continue
			}
			if _, err := runtime.Update(ctx, memreg.UpdateRequest{
				MemoryID: id,
				Memory:   text,
			}); err != nil {
				logger.WarnContext(ctx, "memory formation: UPDATE failed", slog.String("bot_id", botID), slog.String("memory_id", id), slog.Any("error", err))
			} else {
				updated[id] = struct{}{}
				result.Updated++
			}

		case actionDELETE:
			id := strings.TrimSpace(action.ID)
			if id == "" {
				logger.DebugContext(ctx, "memory formation: DELETE skipped (missing id)", slog.String("bot_id", botID))
				result.Skipped++
				continue
			}
			if _, ok := deleted[id]; ok {
				result.Skipped++
				continue
			}
			if _, err := runtime.Delete(ctx, id); err != nil {
				logger.WarnContext(ctx, "memory formation: DELETE failed", slog.String("bot_id", botID), slog.String("memory_id", id), slog.Any("error", err))
			} else {
				deleted[id] = struct{}{}
				result.Deleted++
			}

		case actionNOOP, "":
			result.Skipped++

		default:
			logger.DebugContext(ctx, "memory formation: unknown action event", slog.String("bot_id", botID), slog.String("event", event))
			result.Skipped++
		}
	}
}

func filterNonEmpty(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
