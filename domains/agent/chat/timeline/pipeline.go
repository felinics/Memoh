package timeline

import (
	"sync"
)

// Pipeline manages per-thread IC/RC state. It is goroutine-safe.
type Pipeline struct {
	mu           sync.RWMutex
	renderParams RenderParams
	sessions     map[string]IntermediateContext
	rendered     map[string]RenderedContext
}

// NewPipeline creates a Pipeline with the given default render params.
func NewPipeline(params RenderParams) *Pipeline {
	return &Pipeline{
		renderParams: params,
		sessions:     make(map[string]IntermediateContext),
		rendered:     make(map[string]RenderedContext),
	}
}

// PushEvent processes a single canonical event through the pipeline:
// reduce IC → render RC. Returns the new RenderedContext.
func (p *Pipeline) PushEvent(sessionID string, event CanonicalEvent) RenderedContext {
	p.mu.Lock()
	defer p.mu.Unlock()

	ic, ok := p.sessions[sessionID]
	if !ok {
		ic = NewEmptyIC(sessionID)
	}

	newIC := Reduce(ic, event)
	p.sessions[sessionID] = newIC

	rc := Render(newIC, p.renderParams)
	p.rendered[sessionID] = rc
	return rc
}

// ReplaySession rebuilds IC from persisted events, then renders RC.
// Used for cold-start recovery.
func (p *Pipeline) ReplaySession(sessionID string, events []CanonicalEvent) RenderedContext {
	p.mu.Lock()
	defer p.mu.Unlock()

	ic := NewEmptyIC(sessionID)
	for _, event := range events {
		ic = Reduce(ic, event)
	}
	p.sessions[sessionID] = ic

	rc := Render(ic, p.renderParams)
	p.rendered[sessionID] = rc
	return rc
}

// GetRC returns the current RenderedContext for a session, or nil if not loaded.
func (p *Pipeline) GetRC(sessionID string) RenderedContext {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.rendered[sessionID]
}

// GetIC returns the current IntermediateContext for a session.
func (p *Pipeline) GetIC(sessionID string) (IntermediateContext, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ic, ok := p.sessions[sessionID]
	return ic, ok
}

// SessionIDs returns all loaded session IDs.
func (p *Pipeline) SessionIDs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ids := make([]string, 0, len(p.rendered))
	for id := range p.rendered {
		ids = append(ids, id)
	}
	return ids
}

// DropSession removes a session's state from the pipeline.
func (p *Pipeline) DropSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, sessionID)
	delete(p.rendered, sessionID)
}

// UpdateRenderParams replaces the default render params and re-renders all
// loaded sessions.
func (p *Pipeline) UpdateRenderParams(params RenderParams) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.renderParams = params
	for sessionID, ic := range p.sessions {
		rc := Render(ic, p.renderParams)
		p.rendered[sessionID] = rc
	}
}
