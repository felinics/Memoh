package turn

import (
	"encoding/json"
	"strings"
)

// EventRunTerminal is the last event of a run whose durable record reached a
// terminal state. Its payload is RunTerminal, read from that record, so it
// names the same outcome and error code the run's history shows. A run that
// parks on a decision, or whose owner lost the run before recording its end,
// has no such event.
const EventRunTerminal = "run_terminal"

// Terminal run states. They are the durable record's values.
const (
	RunStateCompleted = "completed"
	RunStateAborted   = "aborted"
	RunStateFailed    = "failed"
	RunStateLost      = "lost"
)

// RunTerminal is the payload of EventRunTerminal. ErrorCode is the catalog code
// the run's record carries; it is empty for a completed or aborted run.
type RunTerminal struct {
	Type      string `json:"type"`
	State     string `json:"state"`
	ErrorCode string `json:"error_code,omitempty"`
}

// NewRunTerminalEvent builds the EventRunTerminal payload.
func NewRunTerminalEvent(state, errorCode string) (json.RawMessage, error) {
	return json.Marshal(RunTerminal{
		Type:      EventRunTerminal,
		State:     strings.TrimSpace(state),
		ErrorCode: strings.TrimSpace(errorCode),
	})
}

// RunTerminalFrom decodes event as an EventRunTerminal. It reports false for
// any other event.
func RunTerminalFrom(event Event) (RunTerminal, bool) {
	if event.Kind != EventRunTerminal {
		return RunTerminal{}, false
	}
	var terminal RunTerminal
	if err := json.Unmarshal(event.Payload, &terminal); err != nil {
		return RunTerminal{}, false
	}
	terminal.State = strings.TrimSpace(terminal.State)
	terminal.ErrorCode = strings.TrimSpace(terminal.ErrorCode)
	return terminal, terminal.State != ""
}

// Failed reports whether the run ended in failure: its owner reported one, or
// the runtime declared the run lost.
func (t RunTerminal) Failed() bool {
	return t.State == RunStateFailed || t.State == RunStateLost
}

// TerminalReporter is implemented by a RunHandle that ends every terminal run
// with EventRunTerminal. A consumer that holds back error events until the run
// ends checks it first; a handle from a runtime that does not send the event
// keeps delivering failures only as they happen.
type TerminalReporter interface {
	ReportsRunTerminal() bool
}

// ReportsRunTerminal reports whether handle sends EventRunTerminal.
func ReportsRunTerminal(handle RunHandle) bool {
	reporter, ok := handle.(TerminalReporter)
	return ok && reporter.ReportsRunTerminal()
}
