package acp

import (
	"strings"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/fsevent"
)

// fsChangeEventSink observes the ACP prompt event stream and reports
// successful fs-mutating tool completions (already normalized to the native
// tool vocabulary by the ACP event mapper) before forwarding every event
// unchanged.
type fsChangeEventSink struct {
	next   client.EventSink
	notify func(paths []string)
}

func newFSChangeEventSink(next client.EventSink, notify func(paths []string)) *fsChangeEventSink {
	return &fsChangeEventSink{next: next, notify: notify}
}

func (s *fsChangeEventSink) EmitStreamEvent(ev event.StreamEvent) {
	if s.notify != nil && ev.Type == event.ToolCallEnd && strings.TrimSpace(ev.Error) == "" {
		if paths, mutating := fsevent.ToolChange(ev.ToolName, ev.Input); mutating {
			s.notify(paths)
		}
	}
	if s.next != nil {
		s.next.EmitStreamEvent(ev)
	}
}
