package provider

import (
	"context"
	"time"
)

type LLM interface {
	Extract(ctx context.Context, req ExtractRequest) (ExtractResponse, error)
	Decide(ctx context.Context, req DecideRequest) (DecideResponse, error)
	Compact(ctx context.Context, req CompactRequest) (CompactResponse, error)
}

type ExtractRequest struct {
	BotID            string         `json:"bot_id,omitempty"`
	Messages         []Message      `json:"messages"`
	Filters          map[string]any `json:"filters,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	TimezoneLocation *time.Location `json:"-"`
}

type ExtractResponse struct {
	Facts []string `json:"facts"`
}

type CandidateMemory struct {
	ID        string         `json:"id"`
	Memory    string         `json:"memory"`
	CreatedAt string         `json:"created_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type DecideRequest struct {
	BotID      string            `json:"bot_id,omitempty"`
	Facts      []string          `json:"facts"`
	Candidates []CandidateMemory `json:"candidates"`
	Filters    map[string]any    `json:"filters,omitempty"`
	Metadata   map[string]any    `json:"metadata,omitempty"`
}

type DecisionAction struct {
	Event     string `json:"event"`
	ID        string `json:"id,omitempty"`
	Text      string `json:"text"`
	OldMemory string `json:"old_memory,omitempty"`
}

type DecideResponse struct {
	Actions []DecisionAction `json:"actions"`
}

type CompactRequest struct {
	BotID       string            `json:"bot_id,omitempty"`
	Memories    []CandidateMemory `json:"memories"`
	TargetCount int               `json:"target_count"`
	DecayDays   int               `json:"decay_days,omitempty"`
}

type CompactResponse struct {
	Facts []string `json:"facts"`
}
