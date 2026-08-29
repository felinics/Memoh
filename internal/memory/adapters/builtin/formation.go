package builtin

import (
	"context"
	"log/slog"
	"strings"
	"time"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
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
func runFormation(ctx context.Context, logger *slog.Logger, llm adapters.LLM, runtime Runtime, req adapters.AfterChatRequest) formationResult {
	ctx, cancel := context.WithTimeout(ctx, formationTimeout)
	defer cancel()

	botID := strings.TrimSpace(req.BotID)
	result := formationResult{}

	extracted, err := llm.Extract(ctx, adapters.ExtractRequest{
		BotID:            botID,
		Messages:         req.Messages,
		TimezoneLocation: req.TimezoneLocation,
	})
	if err != nil {
		logger.Warn("memory formation: extract failed", slog.String("bot_id", botID), slog.Any("error", err))
		return result
	}
	facts, factSourceMessageIDs := normalizeExtractedFacts(extracted)
	if len(facts) == 0 {
		return result
	}
	result.ExtractedFacts = len(facts)

	candidates := gatherCandidates(ctx, logger, runtime, botID, facts)

	decided, err := llm.Decide(ctx, adapters.DecideRequest{
		BotID:      botID,
		Facts:      facts,
		Candidates: candidates,
	})
	if err != nil {
		logger.Warn("memory formation: decide failed", slog.String("bot_id", botID), slog.Any("error", err))
		return result
	}

	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}
	metadata := adapters.BuildProfileMetadata(req.UserID, req.ChannelIdentityID, req.DisplayName)

	applyActions(ctx, logger, runtime, botID, decided.Actions, facts, factSourceMessageIDs, filters, metadata, &result)
	return result
}

// gatherCandidates collects existing memories relevant to the extracted facts.
func gatherCandidates(ctx context.Context, logger *slog.Logger, runtime Runtime, botID string, facts []string) []adapters.CandidateMemory {
	seen := make(map[string]struct{})
	candidates := make([]adapters.CandidateMemory, 0, candidateSearchLimit)

	filters := map[string]any{
		"namespace": sharedMemoryNamespace,
		"scopeId":   botID,
		"bot_id":    botID,
	}

	for _, fact := range facts {
		if len(candidates) >= maxCandidatesPerDecide {
			break
		}
		resp, err := runtime.Search(ctx, adapters.SearchRequest{
			Query:   fact,
			BotID:   botID,
			Limit:   candidateSearchLimit / max(len(facts), 1),
			Filters: filters,
			NoStats: true,
		})
		if err != nil {
			logger.Debug("memory formation: search candidates failed", slog.String("bot_id", botID), slog.Any("error", err))
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
			candidates = append(candidates, adapters.CandidateMemory{
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
		resp, err := runtime.GetAll(ctx, adapters.GetAllRequest{
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
				candidates = append(candidates, adapters.CandidateMemory{
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
func applyActions(ctx context.Context, logger *slog.Logger, runtime Runtime, botID string, actions []adapters.DecisionAction, facts []string, factSourceMessageIDs [][]string, filters map[string]any, metadata map[string]any, result *formationResult) {
	deleted := make(map[string]struct{})
	updated := make(map[string]struct{})

	for _, action := range actions {
		event := strings.ToUpper(strings.TrimSpace(action.Event))
		sourceMessageIDs := sourceRefsForAction(action, facts, factSourceMessageIDs)
		switch event {
		case actionADD:
			text := strings.TrimSpace(action.Text)
			if text == "" {
				logger.Debug("memory formation: ADD skipped (empty text)", slog.String("bot_id", botID))
				result.Skipped++
				continue
			}
			if _, err := runtime.Add(ctx, adapters.AddRequest{
				Message:          text,
				BotID:            botID,
				Metadata:         metadata,
				Filters:          filters,
				SourceMessageIDs: sourceMessageIDs,
			}); err != nil {
				logger.Warn("memory formation: ADD failed", slog.String("bot_id", botID), slog.Any("error", err))
			} else {
				result.Added++
			}

		case actionUPDATE:
			id := strings.TrimSpace(action.ID)
			text := strings.TrimSpace(action.Text)
			if id == "" || text == "" {
				logger.Debug("memory formation: UPDATE skipped (missing id or text)", slog.String("bot_id", botID))
				result.Skipped++
				continue
			}
			if _, ok := updated[id]; ok {
				result.Skipped++
				continue
			}
			if _, err := runtime.Update(ctx, adapters.UpdateRequest{
				MemoryID:         id,
				Memory:           text,
				SourceMessageIDs: sourceMessageIDs,
			}); err != nil {
				logger.Warn("memory formation: UPDATE failed", slog.String("bot_id", botID), slog.String("memory_id", id), slog.Any("error", err))
			} else {
				updated[id] = struct{}{}
				result.Updated++
			}

		case actionDELETE:
			id := strings.TrimSpace(action.ID)
			if id == "" {
				logger.Debug("memory formation: DELETE skipped (missing id)", slog.String("bot_id", botID))
				result.Skipped++
				continue
			}
			if _, ok := deleted[id]; ok {
				result.Skipped++
				continue
			}
			if _, err := runtime.Delete(ctx, id); err != nil {
				logger.Warn("memory formation: DELETE failed", slog.String("bot_id", botID), slog.String("memory_id", id), slog.Any("error", err))
			} else {
				deleted[id] = struct{}{}
				result.Deleted++
			}

		case actionNOOP, "":
			result.Skipped++

		default:
			logger.Debug("memory formation: unknown action event", slog.String("bot_id", botID), slog.String("event", event))
			result.Skipped++
		}
	}
}

func normalizeExtractedFacts(resp adapters.ExtractResponse) ([]string, [][]string) {
	facts := make([]string, 0, len(resp.Facts))
	sources := make([][]string, 0, len(resp.Facts))
	for i, fact := range resp.Facts {
		fact = strings.TrimSpace(fact)
		if fact == "" {
			continue
		}
		var refs []string
		if i < len(resp.FactSourceMessageIDs) {
			refs = adapters.NormalizeSourceRefs(resp.FactSourceMessageIDs[i])
		}
		facts = append(facts, fact)
		sources = append(sources, refs)
	}
	return facts, sources
}

func sourceRefsForAction(action adapters.DecisionAction, facts []string, sources [][]string) []string {
	indices := action.SourceFactIndices
	if len(indices) == 0 {
		// Backward-compatible models sometimes omit the new index field. Only an
		// exact fact/action match is safe to infer; ambiguous rewrites stay
		// uncited instead of inheriting the whole round.
		text := normalizeActionFact(action.Text)
		for i, fact := range facts {
			if normalizeActionFact(fact) == text && text != "" {
				indices = append(indices, i)
			}
		}
	}
	refs := make([]string, 0)
	for _, index := range indices {
		if index < 0 || index >= len(sources) {
			continue
		}
		refs = append(refs, sources[index]...)
	}
	return adapters.NormalizeSourceRefs(refs)
}

func normalizeActionFact(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func sourceMessageIDsFromMessages(messages []adapters.Message) []string {
	refs := make([]string, 0, len(messages))
	for _, message := range messages {
		refs = append(refs, message.SourceMessageID)
	}
	return adapters.NormalizeSourceRefs(refs)
}
