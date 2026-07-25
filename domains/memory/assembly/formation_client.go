package assembly

import (
	"context"
	"time"

	"github.com/memohai/memoh/domains/memory/internal/formation"
	memport "github.com/memohai/memoh/domains/memory/internal/port"
	"github.com/memohai/memoh/domains/memory/registry"
)

// FormationClientConfig holds model resolution details for memory formation.
type FormationClientConfig struct {
	ModelID        string
	BaseURL        string
	APIKey         string `json:"-"`
	ClientType     string
	Timeout        time.Duration
	PromptCacheTTL string
}

// NewFormationClient constructs the Memory-owned formation LLM client.
func NewFormationClient(cfg FormationClientConfig) registry.LLM {
	return formationLLMAdapter{inner: formation.New(formation.Config{
		ModelID:        cfg.ModelID,
		BaseURL:        cfg.BaseURL,
		APIKey:         cfg.APIKey,
		ClientType:     cfg.ClientType,
		Timeout:        cfg.Timeout,
		PromptCacheTTL: cfg.PromptCacheTTL,
	})}
}

type formationLLMAdapter struct {
	inner memport.LLM
}

func (a formationLLMAdapter) Extract(ctx context.Context, req registry.ExtractRequest) (registry.ExtractResponse, error) {
	out, err := a.inner.Extract(ctx, memport.ExtractRequest{
		BotID:            req.BotID,
		Messages:         toPortMessages(req.Messages),
		Filters:          req.Filters,
		Metadata:         req.Metadata,
		TimezoneLocation: req.TimezoneLocation,
	})
	if err != nil {
		return registry.ExtractResponse{}, err
	}
	return registry.ExtractResponse{Facts: out.Facts}, nil
}

func (a formationLLMAdapter) Decide(ctx context.Context, req registry.DecideRequest) (registry.DecideResponse, error) {
	cands := make([]memport.CandidateMemory, 0, len(req.Candidates))
	for _, c := range req.Candidates {
		cands = append(cands, memport.CandidateMemory{
			ID: c.ID, Memory: c.Memory, CreatedAt: c.CreatedAt, Metadata: c.Metadata,
		})
	}
	out, err := a.inner.Decide(ctx, memport.DecideRequest{
		BotID: req.BotID, Facts: req.Facts, Candidates: cands, Filters: req.Filters, Metadata: req.Metadata,
	})
	if err != nil {
		return registry.DecideResponse{}, err
	}
	actions := make([]registry.DecisionAction, 0, len(out.Actions))
	for _, act := range out.Actions {
		actions = append(actions, registry.DecisionAction{
			Event: act.Event, ID: act.ID, Text: act.Text, OldMemory: act.OldMemory,
		})
	}
	return registry.DecideResponse{Actions: actions}, nil
}

func (a formationLLMAdapter) Compact(ctx context.Context, req registry.CompactRequest) (registry.CompactResponse, error) {
	mems := make([]memport.CandidateMemory, 0, len(req.Memories))
	for _, m := range req.Memories {
		mems = append(mems, memport.CandidateMemory{
			ID: m.ID, Memory: m.Memory, CreatedAt: m.CreatedAt, Metadata: m.Metadata,
		})
	}
	out, err := a.inner.Compact(ctx, memport.CompactRequest{
		BotID: req.BotID, Memories: mems, TargetCount: req.TargetCount, DecayDays: req.DecayDays,
	})
	if err != nil {
		return registry.CompactResponse{}, err
	}
	return registry.CompactResponse{Facts: out.Facts}, nil
}

func toPortMessages(in []registry.Message) []memport.Message {
	out := make([]memport.Message, 0, len(in))
	for _, m := range in {
		out = append(out, memport.Message{Role: m.Role, Content: m.Content})
	}
	return out
}
