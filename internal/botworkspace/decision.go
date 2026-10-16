package botworkspace

import "time"

// Action is what the reconciler does with a claimed row.
type Action int

const (
	// ActionNone means observation already matches intent; the reconciler only
	// records that the generation is caught up.
	ActionNone Action = iota
	// ActionProvision brings the workspace to running.
	ActionProvision
	// ActionTeardown removes the workspace.
	ActionTeardown
	// ActionWait means the row is not due (backoff still running).
	ActionWait
)

func (a Action) String() string {
	switch a {
	case ActionProvision:
		return "provision"
	case ActionTeardown:
		return "teardown"
	case ActionWait:
		return "wait"
	default:
		return "none"
	}
}

// Decide compares intent with observation. It looks only at the present state,
// never at history, so an interrupted pass (lease expired mid-provisioning)
// simply continues from what the backend reports.
func Decide(w Workspace, now time.Time) Action {
	switch w.Desired {
	case DesiredAbsent:
		// "absent" is also the column default, so it only counts as an answer
		// once the observation has caught up with this intent. Until then the
		// backend is asked to tear down; the call is idempotent and a missing
		// workspace is success.
		if w.Observed == ObservedAbsent && w.ObservedGeneration >= w.DesiredGeneration {
			return ActionNone
		}
		if w.Observed == ObservedFailed && w.NextAttemptAt.After(now) {
			return ActionWait
		}
		return ActionTeardown
	default:
		switch w.Observed {
		case ObservedRunning, ObservedStopped:
			return ActionNone
		case ObservedFailed:
			if w.NextAttemptAt.After(now) {
				return ActionWait
			}
			return ActionProvision
		default:
			return ActionProvision
		}
	}
}

// Data safety: no automated step deletes workspace data. A teardown happens
// only for an explicit absent intent, honouring its preserve_data flag; a
// retry reuses whatever the previous attempt left behind, and when it must
// replace a container built from a different image it exports the data first
// (see Service.replaceStaleContainer).

// NextBackoff returns when the next attempt may run after attempt number
// `attempts` (1-based) failed. Exponential from base, capped.
func NextBackoff(now time.Time, attempts int32, base, capDuration time.Duration) time.Time {
	if attempts < 1 {
		attempts = 1
	}
	d := base
	for i := int32(1); i < attempts && d < capDuration; i++ {
		d *= 2
	}
	if d > capDuration {
		d = capDuration
	}
	return now.Add(d)
}

// DeriveBotStatus maps the workspace state onto bots.status. ok is false when
// the bot's status must not be touched (the workspace is absent on purpose or
// the bot is being deleted; those transitions belong to the bot lifecycle).
func DeriveBotStatus(w Workspace) (string, bool) {
	if w.Desired == DesiredAbsent {
		return "", false
	}
	switch w.Observed {
	case ObservedRunning, ObservedStopped:
		return BotStatusReady, true
	case ObservedFailed:
		if w.EverReady {
			return BotStatusReady, true
		}
		return BotStatusFailed, true
	default:
		if w.EverReady {
			return BotStatusReady, true
		}
		return BotStatusCreating, true
	}
}
