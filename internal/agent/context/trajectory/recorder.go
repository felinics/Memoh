package trajectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

const contentChunkBytes = 64 << 10

type Recorder struct {
	recordMu  sync.Mutex
	mu        sync.Mutex
	sink      Sink
	logger    *slog.Logger
	captureID string
	runID     string
	sessionID string
	stats     Stats
	jobs      []*captureJob
	working   bool
	last      <-chan struct{}
}

func NewRecorder(sink Sink) *Recorder {
	return &Recorder{sink: sink, captureID: uuid.NewString(), logger: slog.Default()}
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

func (r *Recorder) prepareCapture(ctx context.Context, stage string, stepIndex *int, blocks []Block) *captureJob {
	r.mu.Lock()
	if r.runID == "" || r.sessionID == "" {
		r.mu.Unlock()
		return nil
	}
	r.stats.Events++
	var encodingErrors int64
	for _, block := range blocks {
		if block.Kind == "capture_error" {
			encodingErrors++
		}
	}
	event := Event{
		CaptureID: r.captureID,
		RunID:     r.runID, SessionID: r.sessionID, Sequence: r.stats.Events,
		Stage: stage, RecordedAt: time.Now().UTC(), CaptureErrors: r.stats.Errors,
		Blocks: make([]BlockRef, 0, len(blocks)), Request: requestFromContext(ctx),
	}
	r.mu.Unlock()
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
	return &captureJob{ctx: context.WithoutCancel(ctx), event: event, contents: contents, encodingErrors: encodingErrors, done: make(chan struct{})}
}

func appendCapture(ctx context.Context, sink Sink, event Event, contents []Content) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("context trajectory sink panicked: %v", recovered)
		}
	}()
	return sink.Append(ctx, event, contents)
}

func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := r.stats
	stats.CaptureID = r.captureID
	return stats
}

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
