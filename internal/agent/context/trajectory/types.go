package trajectory

import (
	"context"
	"time"
)

type Block struct {
	Kind    string `json:"kind"`
	Label   string `json:"label,omitempty"`
	Format  string `json:"format,omitempty"`
	Content string `json:"content"`
}

type BlockRef struct {
	Kind   string   `json:"kind"`
	Label  string   `json:"label,omitempty"`
	Format string   `json:"format,omitempty"`
	Hash   string   `json:"hash"`
	Bytes  int      `json:"bytes"`
	Chunks []string `json:"chunks"`
}

type Content struct {
	Hash string
	Data []byte
}

type Event struct {
	CaptureID     string     `json:"capture_id"`
	RunID         string     `json:"run_id"`
	SessionID     string     `json:"session_id"`
	Sequence      int64      `json:"sequence"`
	Stage         string     `json:"stage"`
	StepIndex     *int       `json:"step_index,omitempty"`
	Request       int64      `json:"request,omitempty"`
	RecordedAt    time.Time  `json:"recorded_at"`
	CaptureErrors int64      `json:"capture_errors,omitempty"`
	Blocks        []BlockRef `json:"blocks"`
}

type Stats struct {
	CaptureID string `json:"capture_id"`
	Events    int64  `json:"events"`
	Errors    int64  `json:"errors"`
	Pending   int64  `json:"pending,omitempty"`
}

type Sink interface {
	Append(context.Context, Event, []Content) error
}
