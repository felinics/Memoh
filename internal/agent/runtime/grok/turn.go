package grok

import (
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/event"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

// turnSink is a turn's one event stream. Grok's own events, the decisions
// that settle after a stop, and the events of Memoh tools served through the
// gateway arrive on different paths; they queue here in arrival order, and
// one goroutine records and forwards them, so the live stream and the
// persisted transcript agree. Emitting never waits: Grok's events are
// emitted on the loop that every session of the process shares.
type turnSink struct {
	next     external.EventSink
	recorder *external.TranscriptRecorder
	limit    contextlimit.ToolOutputLimit

	mu     sync.Mutex
	wake   *sync.Cond
	queue  []turnEvent
	closed bool
	done   chan struct{}
}

type turnEvent struct {
	ev event.StreamEvent
	// recordOnly keeps a decision that settled after the turn was stopped in
	// the transcript without sending another live frame.
	recordOnly bool
}

var _ acpclient.TerminalDecisionSink = (*turnSink)(nil)

func newTurnSink(next external.EventSink, limit contextlimit.ToolOutputLimit) *turnSink {
	s := &turnSink{next: next, recorder: external.NewTranscriptRecorder(), limit: limit, done: make(chan struct{})}
	s.wake = sync.NewCond(&s.mu)
	go s.pump()
	return s
}

func (s *turnSink) EmitStreamEvent(ev event.StreamEvent) {
	s.enqueue(turnEvent{ev: external.LimitStreamEvent(ev, s.limit)})
}

func (s *turnSink) RecordTerminalDecision(ev event.StreamEvent) {
	s.enqueue(turnEvent{ev: ev, recordOnly: true})
}

func (s *turnSink) enqueue(item turnEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.queue = append(s.queue, item)
		s.wake.Signal()
	}
}

func (s *turnSink) pump() {
	defer close(s.done)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.wake.Wait()
		}
		batch, closed := s.queue, s.closed
		s.queue = nil
		s.mu.Unlock()
		for _, item := range batch {
			s.recorder.Add(item.ev)
			if !item.recordOnly {
				s.next.EmitStreamEvent(item.ev)
			}
		}
		if closed && len(batch) == 0 {
			return
		}
	}
}

// finish delivers every event queued so far and stops taking more.
func (s *turnSink) finish() {
	s.mu.Lock()
	s.closed = true
	s.wake.Signal()
	s.mu.Unlock()
	<-s.done
}

// transcript is the turn as persisted, once every event has been delivered.
func (s *turnSink) transcript(finalText string) []sdk.Message {
	s.finish()
	return s.recorder.Messages(finalText)
}

// usageFromMeta reads the turn's token usage. Grok reports it in the prompt
// response's _meta.usage, summed over the turn's model calls, with cache
// reads already counted inside inputTokens.
func usageFromMeta(meta map[string]any) *sdk.Usage {
	usage, ok := meta["usage"].(map[string]any)
	if !ok {
		return nil
	}
	count := func(key string) int {
		value, _ := usage[key].(float64)
		return int(value)
	}
	input, read, write, reasoning := count("inputTokens"), count("cachedReadTokens"), count("cacheCreationTokens"), count("reasoningTokens")
	return &sdk.Usage{
		InputTokens:        input,
		OutputTokens:       count("outputTokens"),
		TotalTokens:        count("totalTokens"),
		ReasoningTokens:    reasoning,
		CachedInputTokens:  read,
		InputTokenDetails:  sdk.InputTokenDetail{NoCacheTokens: input - read - write, CacheReadTokens: read, CacheWriteTokens: write},
		OutputTokenDetails: sdk.OutputTokenDetail{ReasoningTokens: reasoning},
	}
}
