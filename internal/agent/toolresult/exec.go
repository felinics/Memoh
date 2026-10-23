package toolresult

// Exec is the result of an exec call that ran a command to completion.
// Stdout and Stderr are always present; a runtime that only has the merged
// output reports it as Stdout. ExitCode is set by the native tool always and by
// an external runtime only when its protocol reported one; it is never filled
// with a default.
type Exec struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

func (r Exec) Map() map[string]any { return toMap(r) }

// ExecBackground is the result of an exec call that returned before the
// command finished: it was started in the background, or moved there after
// the foreground wait. OutputTail is set when output had already been
// collected.
type ExecBackground struct {
	Status     string `json:"status"`
	TaskID     string `json:"task_id"`
	OutputFile string `json:"output_file"`
	Message    string `json:"message"`
	OutputTail string `json:"output_tail,omitempty"`
}

func (r ExecBackground) Map() map[string]any { return toMap(r) }

// Exec statuses of ExecBackground.
const (
	ExecStatusBackgroundStarted = "background_started"
	ExecStatusAutoBackgrounded  = "auto_backgrounded"
)

// ExecTask is the exec-shaped result nested in a background task snapshot.
// ExitCode is absent while the task is still running.
type ExecTask struct {
	OutputFile string `json:"output_file"`
	OutputTail string `json:"output_tail"`
	ExitCode   *int   `json:"exit_code,omitempty"`
}

func (r ExecTask) Map() map[string]any { return toMap(r) }
