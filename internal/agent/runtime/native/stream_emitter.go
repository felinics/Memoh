package native

import (
	"context"
	"sync"

	tools "github.com/felinics/memoh/internal/agent/tool"
)

// streamEmitterGate bounds callbacks that may outlive the synchronous stream
// producer. The stream owner closes the channel; this gate only controls
// secondary producers before that close happens.
type streamEmitterGate struct {
	mu       sync.Mutex
	closing  bool
	inFlight sync.WaitGroup
	ctx      context.Context
	ch       chan<- StreamEvent
}

func newStreamEmitterGate(ctx context.Context, ch chan<- StreamEvent) *streamEmitterGate {
	return &streamEmitterGate{ctx: ctx, ch: ch}
}

func (g *streamEmitterGate) emit(evt tools.ToolStreamEvent) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closing {
		g.mu.Unlock()
		return
	}
	g.inFlight.Add(1)
	g.mu.Unlock()
	defer g.inFlight.Done()

	sendEvent(g.ctx, g.ch, toolStreamEventToAgentEvent(evt))
}

func (g *streamEmitterGate) emitAgentEvent(evt StreamEvent) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closing {
		g.mu.Unlock()
		return
	}
	g.inFlight.Add(1)
	g.mu.Unlock()
	defer g.inFlight.Done()

	sendEvent(g.ctx, g.ch, evt)
}

// close stops new callbacks and waits for callbacks already admitted. The
// caller must cancel g.ctx first when an admitted send may be blocked.
func (g *streamEmitterGate) close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closing {
		g.mu.Unlock()
		return
	}
	g.closing = true
	g.mu.Unlock()
	g.inFlight.Wait()
}
