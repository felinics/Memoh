package apps

// Event types emitted while an App operation runs.
const (
	EventStarted  = "started"
	EventStep     = "step"
	EventLog      = "log"
	EventStepDone = "step_done"
	EventDone     = "done"
	EventError    = "error"
)

// Step kinds: the component a step works on.
const (
	KindDependency = "dependency"
	KindSkills     = "skills"
	KindConnector  = "connector"
	KindApp        = "app"
)

// Step outcomes.
const (
	StepInstalled    = "installed"
	StepUpdated      = "updated"
	StepLinked       = "linked"
	StepNeedsAuth    = "needs_auth"
	StepFailed       = "failed"
	StepSkipped      = "skipped"
	StepRemoved      = "removed"
	StepKept         = "kept"
	StepDisconnected = "disconnected"
)

// Event is one progress frame of an App operation. Type selects which
// fields are meaningful: step and step_done carry Kind and ID, log carries
// Stream and Data, step_done and done carry Status.
type Event struct {
	Type    string
	Kind    string
	ID      string
	Stream  string
	Data    string
	Status  string
	Version string
	Message string
}

// EventSink receives operation events.
type EventSink interface {
	Send(Event)
}

// EventFunc adapts a function to EventSink.
type EventFunc func(Event)

func (f EventFunc) Send(event Event) {
	if f != nil {
		f(event)
	}
}

// StepResult summarizes one step of a completed operation.
type StepResult struct {
	Kind    string
	ID      string
	Status  string
	Version string
	Error   string
}

// OperationResult is the receipt of an App operation.
type OperationResult struct {
	Installation Installation
	Steps        []StepResult
}

func nonNilSink(sink EventSink) EventSink {
	if sink == nil {
		return EventFunc(func(Event) {})
	}
	return sink
}
