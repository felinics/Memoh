package contextfrag

import "github.com/felinics/memoh/internal/agent/context/trajectory"

func (h *LifecycleHolder) SetTrajectoryRecorder(recorder *trajectory.Recorder) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.trajectory = recorder
	h.mu.Unlock()
}

func (h *LifecycleHolder) TrajectoryRecorder() *trajectory.Recorder {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.trajectory
}
