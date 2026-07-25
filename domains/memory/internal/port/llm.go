package port

import (
	"context"
	"time"
)

type LLM interface {
	Extract(ctx context.Context, req ExtractRequest) (ExtractResponse, error)
	Decide(ctx context.Context, req DecideRequest) (DecideResponse, error)
	Compact(ctx context.Context, req CompactRequest) (CompactResponse, error)
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ExtractRequest struct {
	BotID            string
	Messages         []Message
	Filters          map[string]any
	Metadata         map[string]any
	TimezoneLocation *time.Location
}

type ExtractResponse struct {
	Facts []string `json:"facts"`
}

type CandidateMemory struct {
	ID        string
	Memory    string
	CreatedAt string
	Metadata  map[string]any
}

type DecideRequest struct {
	BotID      string
	Facts      []string
	Candidates []CandidateMemory
	Filters    map[string]any
	Metadata   map[string]any
}

type DecisionAction struct {
	Event     string
	ID        string
	Text      string
	OldMemory string
}

type DecideResponse struct {
	Actions []DecisionAction
}

type CompactRequest struct {
	BotID       string
	Memories    []CandidateMemory
	TargetCount int
	DecayDays   int
}

type CompactResponse struct {
	Facts []string
}
