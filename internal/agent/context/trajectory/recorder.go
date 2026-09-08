package trajectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"
)

const contentChunkBytes = 64 << 10

type Recorder struct {
	mu        sync.Mutex
	sink      Sink
	runID     string
	sessionID string
	stats     Stats
}

func NewRecorder(sink Sink) *Recorder {
	return &Recorder{sink: sink}
}

func (r *Recorder) Bind(runID, sessionID string) {
	if r == nil || runID == "" || sessionID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runID == "" {
		r.runID, r.sessionID = runID, sessionID
	}
}

func (r *Recorder) Record(ctx context.Context, stage string, stepIndex *int, blocks ...Block) int64 {
	if r == nil || r.sink == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runID == "" || r.sessionID == "" {
		return 0
	}
	r.stats.Events++
	event := Event{
		RunID: r.runID, SessionID: r.sessionID, Sequence: r.stats.Events,
		Stage: stage, RecordedAt: time.Now().UTC(), CaptureErrors: r.stats.Errors,
		Blocks: make([]BlockRef, 0, len(blocks)), Request: requestFromContext(ctx),
	}
	if stepIndex != nil {
		index := *stepIndex
		event.StepIndex = &index
	}
	var contents []Content
	seen := make(map[string]bool)
	for _, block := range blocks {
		ref := BlockRef{Kind: block.Kind, Label: block.Label, Format: block.Format, Hash: Hash([]byte(block.Content)), Bytes: len(block.Content), Chunks: []string{}}
		for offset := 0; offset < len(block.Content); offset += contentChunkBytes {
			data := []byte(block.Content[offset:min(offset+contentChunkBytes, len(block.Content))])
			hash := Hash(data)
			ref.Chunks = append(ref.Chunks, hash)
			if !seen[hash] {
				seen[hash] = true
				contents = append(contents, Content{Hash: hash, Data: data})
			}
		}
		event.Blocks = append(event.Blocks, ref)
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.sink.Append(writeCtx, event, contents); err != nil {
		r.stats.Errors++
		slog.Warn("context trajectory capture failed", "run_id", r.runID, "stage", stage, "sequence", event.Sequence, "error", err)
	}
	return event.Sequence
}

func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
