package inbound

import (
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/i18n"
)

// runFailureReply decides the failure reply of a turn from its run_terminal
// event. For a handle that sends one, stream error events are held until the
// run ends: the terminal event then names the run's code and replaces them,
// and a recovered error before a clean end is not shown. Without the event,
// the held events are released as they arrived.
type runFailureReply struct {
	reportsTerminal bool
	terminal        *turn.RunTerminal
	held            []channel.StreamEvent
}

// observeTerminal records event when it is run_terminal and reports whether it
// was.
func (r *runFailureReply) observeTerminal(event turn.Event) bool {
	terminal, ok := turn.RunTerminalFrom(event)
	if !ok {
		return false
	}
	if r.reportsTerminal {
		r.terminal = &terminal
	}
	return true
}

// hold keeps a stream error event back and reports whether it did.
func (r *runFailureReply) hold(event channel.StreamEvent) bool {
	if !r.reportsTerminal || event.Type != channel.StreamEventError {
		return false
	}
	r.held = append(r.held, event)
	return true
}

// release returns the held events when no run_terminal arrived, and nothing
// when one did.
func (r *runFailureReply) release() []channel.StreamEvent {
	held := r.held
	r.held = nil
	if r.terminal != nil {
		return nil
	}
	return held
}

// reply is the failure reply the run_terminal event calls for, rendered from
// the run's code. The args come from the turn error when it carries the same
// code. A run that did not fail but still reported an error is answered by that
// error's code. It reports false without a run_terminal event, or for a run
// that ended without failing.
func (r *runFailureReply) reply(t *i18n.Localizer, turnErr error) (channel.StreamEvent, bool) {
	if r.terminal == nil {
		return channel.StreamEvent{}, false
	}
	code := apperror.Code(r.terminal.ErrorCode)
	if !r.terminal.Failed() {
		if turnErr == nil {
			return channel.StreamEvent{}, false
		}
		code = apperror.CodeOf(turnErr)
	}
	var args map[string]string
	if turnErr != nil && apperror.CodeOf(turnErr) == code {
		args = apperror.ArgsOf(turnErr)
	}
	return channel.CodeEvent(t, code, args), true
}
