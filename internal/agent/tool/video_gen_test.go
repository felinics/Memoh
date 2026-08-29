package tools

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/settings"
)

func TestVideoGenStartsBackgroundTaskAndCompletes(t *testing.T) {
	mgr := background.New(nil)
	prov := &fakeVideoProvider{
		createJob: &sdk.VideoJob{
			ID:      "job-1",
			ModelID: "video-model",
			Status:  sdk.VideoJobSucceeded,
			Outputs: []sdk.VideoOutput{{
				URL:             "https://example.com/video.mp4",
				ContentType:     "video/mp4",
				DurationSeconds: 8,
			}},
		},
		downloadData: []byte("mp4"),
		downloadType: "video/mp4",
	}
	p := newTestVideoGenProvider(mgr, "model-row", prov)

	res, err := p.execGenerateVideo(context.Background(), SessionContext{BotID: "bot1", SessionID: "sess1"}, map[string]any{
		"prompt": "waves at sunrise",
	})
	if err != nil {
		t.Fatalf("execGenerateVideo returned error: %v", err)
	}
	started := res.(map[string]any)
	taskID, _ := started["task_id"].(string)
	if started["status"] != "background_started" || taskID == "" {
		t.Fatalf("unexpected start payload: %v", started)
	}

	snap := waitForVideoTask(t, mgr, taskID)
	if snap.Status != background.TaskCompleted {
		t.Fatalf("status = %s, want completed; snapshot=%+v", snap.Status, snap)
	}
	if snap.Result["job_id"] != "job-1" || snap.Result["output_url"] != "https://example.com/video.mp4" {
		t.Fatalf("video result missing job/output: %+v", snap.Result)
	}
	if snap.Result["warning"] == "" {
		t.Fatalf("nil workspace should leave save warning in result: %+v", snap.Result)
	}
}

func TestVideoGenProviderFailureCompletesFailedTask(t *testing.T) {
	mgr := background.New(nil)
	prov := &fakeVideoProvider{createErr: errors.New("provider down")}
	p := newTestVideoGenProvider(mgr, "model-row", prov)

	res, err := p.execGenerateVideo(context.Background(), SessionContext{BotID: "bot1", SessionID: "sess1"}, map[string]any{
		"prompt": "waves at sunrise",
	})
	if err != nil {
		t.Fatalf("execGenerateVideo returned error: %v", err)
	}
	taskID := res.(map[string]any)["task_id"].(string)
	snap := waitForVideoTask(t, mgr, taskID)
	if snap.Status != background.TaskFailed || snap.Error == "" {
		t.Fatalf("snapshot = %+v, want failed provider error", snap)
	}
}

func TestVideoGenKillCancelsProviderJob(t *testing.T) {
	mgr := background.New(nil)
	prov := &fakeVideoProvider{
		createJob: &sdk.VideoJob{ID: "job-1", ModelID: "video-model", Status: sdk.VideoJobRunning},
		cancelCh:  make(chan struct{}),
	}
	p := newTestVideoGenProvider(mgr, "model-row", prov)

	res, err := p.execGenerateVideo(context.Background(), SessionContext{BotID: "bot1", SessionID: "sess1"}, map[string]any{
		"prompt": "waves at sunrise",
	})
	if err != nil {
		t.Fatalf("execGenerateVideo returned error: %v", err)
	}
	taskID := res.(map[string]any)["task_id"].(string)
	if err := mgr.KillForSession("bot1", "sess1", taskID); err != nil {
		t.Fatalf("KillForSession returned error: %v", err)
	}

	select {
	case <-prov.cancelCh:
	case <-time.After(time.Second):
		t.Fatal("provider job was not canceled")
	}
	snap := mgr.Get(taskID).Snapshot()
	if snap.Status != background.TaskKilled {
		t.Fatalf("status = %s, want killed", snap.Status)
	}
}

func TestVideoGenToolsHiddenWithoutConfiguredModel(t *testing.T) {
	p := &VideoGenProvider{
		logger:    slog.New(slog.DiscardHandler),
		settings:  fakeVideoSettings{},
		video:     fakeVideoResolver{},
		bgManager: background.New(nil),
	}
	tools, err := p.Tools(context.Background(), SessionContext{BotID: "bot1"})
	if err != nil {
		t.Fatalf("Tools returned error: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("tools = %#v, want none", tools)
	}
}

func waitForVideoTask(t *testing.T, mgr *background.Manager, taskID string) background.TaskSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snap, _, err := mgr.WaitForSessionTask(ctx, "bot1", "sess1", taskID, 0)
	if err != nil {
		t.Fatalf("WaitForSessionTask returned error: %v", err)
	}
	return snap
}

func newTestVideoGenProvider(mgr *background.Manager, modelID string, prov *fakeVideoProvider) *VideoGenProvider {
	return &VideoGenProvider{
		logger:   slog.New(slog.DiscardHandler),
		settings: fakeVideoSettings{settings: settings.Settings{VideoModelID: modelID}},
		video: fakeVideoResolver{
			model: &sdk.VideoModel{ID: "video-model", Provider: prov},
		},
		bgManager: mgr,
	}
}

type fakeVideoSettings struct {
	settings settings.Settings
	err      error
}

func (f fakeVideoSettings) GetBot(context.Context, string) (settings.Settings, error) {
	return f.settings, f.err
}

type fakeVideoResolver struct {
	model *sdk.VideoModel
	cfg   map[string]any
	err   error
}

func (f fakeVideoResolver) ResolveVideoModel(context.Context, string) (*sdk.VideoModel, map[string]any, error) {
	return f.model, f.cfg, f.err
}

type fakeVideoProvider struct {
	mu           sync.Mutex
	createJob    *sdk.VideoJob
	createErr    error
	getJobs      []*sdk.VideoJob
	getErr       error
	downloadData []byte
	downloadType string
	downloadErr  error
	cancelCh     chan struct{}
}

func (*fakeVideoProvider) ListModels(context.Context) ([]*sdk.VideoModel, error) {
	return nil, nil
}

func (f *fakeVideoProvider) DoCreate(context.Context, sdk.VideoParams) (*sdk.VideoJob, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return cloneVideoJob(f.createJob), nil
}

func (f *fakeVideoProvider) DoGet(ctx context.Context, _ *sdk.VideoModel, _ string) (*sdk.VideoJob, error) {
	f.mu.Lock()
	if f.getErr != nil {
		err := f.getErr
		f.mu.Unlock()
		return nil, err
	}
	if len(f.getJobs) > 0 {
		job := cloneVideoJob(f.getJobs[0])
		f.getJobs = f.getJobs[1:]
		f.mu.Unlock()
		return job, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeVideoProvider) DoCancel(_ context.Context, _ *sdk.VideoModel, _ string) error {
	if f.cancelCh != nil {
		select {
		case <-f.cancelCh:
		default:
			close(f.cancelCh)
		}
	}
	return nil
}

func (f *fakeVideoProvider) DoDownload(_ context.Context, _ *sdk.VideoModel, _ sdk.VideoOutput) ([]byte, string, error) {
	if f.downloadErr != nil {
		return nil, "", f.downloadErr
	}
	return append([]byte(nil), f.downloadData...), f.downloadType, nil
}

func cloneVideoJob(job *sdk.VideoJob) *sdk.VideoJob {
	if job == nil {
		return nil
	}
	clone := *job
	clone.Outputs = append([]sdk.VideoOutput(nil), job.Outputs...)
	return &clone
}
