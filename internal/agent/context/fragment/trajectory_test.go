package contextfrag

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/context/trajectory"
)

type failingTrajectorySink struct{}

func (failingTrajectorySink) Append(context.Context, trajectory.Event, []trajectory.Content) error {
	return errors.New("unavailable")
}

func TestLifecycleReportsMissingFinalTrajectoryCapture(t *testing.T) {
	holder := NewLifecycleHolder()
	recorder := trajectory.NewRecorder(failingTrajectorySink{})
	recorder.Bind("run", "session")
	holder.SetTrajectoryRecorder(recorder)
	recorder.Record(context.Background(), "provider_request", nil, trajectory.Block{Content: "final"})
	snapshot, ok := holder.Snapshot()
	if !ok || snapshot.Trajectory == nil || snapshot.Trajectory.Events != 1 || snapshot.Trajectory.Errors != 1 {
		t.Fatalf("terminal capture failure is invisible: %#v", snapshot)
	}
	if snapshot.Trajectory.CaptureID == "" {
		t.Fatal("capture status has no segment identity")
	}
	holder.SetManifest(BuildManifest(nil))
	rebuilt, _ := holder.Snapshot()
	if rebuilt.Trajectory == nil || rebuilt.Trajectory.Errors != 1 {
		t.Fatal("manifest replacement lost the capture status")
	}
	if rebuilt.RowCopy().Trajectory != nil {
		t.Fatal("per-message lifecycle copied the run capture status")
	}
}
