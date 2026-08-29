package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/background"
	"github.com/felinics/memoh/internal/settings"
	videopkg "github.com/felinics/memoh/internal/video"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	videoGenDir        = "/data/generated-videos"
	videoPollInterval  = 5 * time.Second
	videoCancelTimeout = 10 * time.Second
)

type videoSettingsGetter interface {
	GetBot(ctx context.Context, botID string) (settings.Settings, error)
}

type videoModelResolver interface {
	ResolveVideoModel(ctx context.Context, modelID string) (*sdk.VideoModel, map[string]any, error)
}

type VideoGenProvider struct {
	logger     *slog.Logger
	settings   videoSettingsGetter
	video      videoModelResolver
	bgManager  *background.Manager
	containers bridge.Provider
	dataMount  string
}

func NewVideoGenProvider(
	log *slog.Logger,
	settingsSvc *settings.Service,
	videoSvc *videopkg.Service,
	bgManager *background.Manager,
	containers bridge.Provider,
	dataMount string,
) *VideoGenProvider {
	if log == nil {
		log = slog.Default()
	}
	return &VideoGenProvider{
		logger:     log.With(slog.String("tool", "video_gen")),
		settings:   settingsSvc,
		video:      videoSvc,
		bgManager:  bgManager,
		containers: containers,
		dataMount:  dataMount,
	}
}

func (p *VideoGenProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	if p.settings == nil || p.video == nil || p.bgManager == nil {
		return nil, nil
	}
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, nil
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, nil
	}
	if strings.TrimSpace(botSettings.VideoModelID) == "" {
		return nil, nil
	}
	sess := session
	return []sdk.Tool{
		{
			Name:        ToolGenerateVideo().String(),
			Description: "Start a background video generation task using the configured video generation model. Returns a task_id immediately; use wait_until(task_id), then get_background_status(task_id) to inspect the result.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt":           map[string]any{"type": "string", "description": "Detailed description of the video to generate"},
					"duration_seconds": map[string]any{"type": "integer", "description": "Optional duration in seconds"},
					"resolution":       map[string]any{"type": "string", "description": "Optional resolution, e.g. 720p or 1080p"},
					"aspect_ratio":     map[string]any{"type": "string", "description": "Optional aspect ratio, e.g. 16:9, 9:16, or 1:1"},
					"size":             map[string]any{"type": "string", "description": "Optional provider-specific size, e.g. 1280x720"},
					"generate_audio":   map[string]any{"type": "boolean", "description": "Whether the provider should generate audio when supported"},
				},
				"required": []string{"prompt"},
			},
			Execute: func(execCtx *sdk.ToolExecContext, input any) (any, error) {
				return p.execGenerateVideo(execCtx.Context, sess, inputAsMap(input))
			},
		},
	}, nil
}

func (p *VideoGenProvider) execGenerateVideo(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	if p.bgManager == nil {
		return nil, errors.New("background task manager is not available")
	}
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	prompt := strings.TrimSpace(StringArg(args, "prompt"))
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}

	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, errors.New("failed to load bot settings")
	}
	videoModelID := strings.TrimSpace(botSettings.VideoModelID)
	if videoModelID == "" {
		return nil, errors.New("no video generation model configured")
	}

	model, cfg, err := p.video.ResolveVideoModel(ctx, videoModelID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve video model: %w", err)
	}

	opts, err := videoOptionsFromArgs(model, cfg, prompt, args)
	if err != nil {
		return nil, err
	}
	description := "generate video"
	if prompt != "" {
		description = "generate video: " + truncateStr(prompt, 80)
	}

	taskID, taskCtx, err := p.bgManager.StartVideoTask(ctx, botID, session.SessionID, description)
	if err != nil {
		return nil, err
	}
	go p.runVideoTask(taskCtx, taskID, botID, model, opts)

	return map[string]any{
		"status":      "background_started",
		"kind":        string(background.KindVideo),
		"task_id":     taskID,
		"description": description,
		"message":     fmt.Sprintf("Video generation started with task ID: %s. Use wait_until(task_id), then get_background_status(task_id) to inspect result.", taskID),
	}, nil
}

func videoOptionsFromArgs(model *sdk.VideoModel, cfg map[string]any, prompt string, args map[string]any) ([]sdk.VideoOption, error) {
	opts := []sdk.VideoOption{
		sdk.WithVideoModel(model),
		sdk.WithVideoPrompt(prompt),
		sdk.WithVideoConfig(cfg),
	}
	if v := strings.TrimSpace(StringArg(args, "size")); v != "" {
		opts = append(opts, sdk.WithVideoSize(v))
	}
	if v := strings.TrimSpace(StringArg(args, "resolution")); v != "" {
		opts = append(opts, sdk.WithVideoResolution(v))
	}
	if v := strings.TrimSpace(StringArg(args, "aspect_ratio")); v != "" {
		opts = append(opts, sdk.WithVideoAspectRatio(v))
	}
	if v, ok, err := IntArg(args, "duration_seconds"); err != nil {
		return nil, err
	} else if ok && v > 0 {
		opts = append(opts, sdk.WithVideoDuration(v))
	}
	if v, ok, err := BoolArg(args, "generate_audio"); err != nil {
		return nil, err
	} else if ok {
		opts = append(opts, sdk.WithVideoGenerateAudio(v))
	}
	return opts, nil
}

func (p *VideoGenProvider) runVideoTask(ctx context.Context, taskID, botID string, model *sdk.VideoModel, opts []sdk.VideoOption) {
	var job *sdk.VideoJob
	completeFailed := func(result map[string]any, err error) {
		msg := "video generation failed"
		if err != nil {
			msg = err.Error()
		}
		if result == nil {
			result = map[string]any{}
		}
		result["error"] = msg
		p.bgManager.CompleteVideoTask(taskID, background.TaskFailed, result, msg)
	}

	job, err := sdk.CreateVideo(ctx, opts...)
	if err != nil {
		completeFailed(map[string]any{"model_id": modelIDFromJob(model, nil)}, fmt.Errorf("video generation failed: %w", err))
		return
	}
	if job == nil || strings.TrimSpace(job.ID) == "" {
		completeFailed(map[string]any{"model_id": modelIDFromJob(model, job)}, errors.New("video provider returned empty job id"))
		return
	}
	lastStatus, lastProgress := "", ""
	recordIfChanged := func(job *sdk.VideoJob, force bool) {
		if job == nil {
			return
		}
		status := string(job.Status)
		progress := ""
		if job.Progress != nil {
			progress = fmt.Sprintf("%.6f", *job.Progress)
		}
		if !force && status == lastStatus && progress == lastProgress {
			return
		}
		lastStatus, lastProgress = status, progress
		p.recordVideoJobProgress(taskID, model, job)
	}
	recordIfChanged(job, true)

	ticker := time.NewTicker(videoPollInterval)
	defer ticker.Stop()
	for job != nil && !job.Status.Terminal() {
		select {
		case <-ctx.Done():
			p.cancelVideoJob(ctx, model, job.ID)
			completeFailed(videoJobResult(model, job), ctx.Err())
			return
		case <-ticker.C:
			job, err = sdk.GetVideo(ctx, model, job.ID)
			if err != nil {
				completeFailed(videoJobResult(model, job), err)
				return
			}
			recordIfChanged(job, false)
		}
	}
	if job == nil {
		completeFailed(map[string]any{"model_id": modelIDFromJob(model, nil)}, errors.New("video provider returned empty job"))
		return
	}
	result := videoJobResult(model, job)
	if job.Status != sdk.VideoJobSucceeded {
		completeFailed(result, videoJobError(job))
		return
	}
	if len(job.Outputs) == 0 {
		result["warning"] = "Video generated but provider returned no downloadable output."
		p.bgManager.CompleteVideoTask(taskID, background.TaskCompleted, result, "")
		return
	}

	output := job.Outputs[0]
	data, contentType, err := sdk.DownloadVideo(ctx, model, output)
	if err != nil {
		result["warning"] = fmt.Sprintf("Video generated but download failed: %s", err.Error())
		p.bgManager.CompleteVideoTask(taskID, background.TaskCompleted, result, "")
		return
	}
	if len(data) == 0 {
		result["warning"] = "Video generated but download returned empty data."
		p.bgManager.CompleteVideoTask(taskID, background.TaskCompleted, result, "")
		return
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = output.ContentType
	}
	saveResult, warning := p.saveGeneratedVideo(ctx, botID, taskID, contentType, data, &output)
	for k, v := range saveResult {
		result[k] = v
	}
	if warning != "" {
		result["warning"] = warning
	}
	p.bgManager.CompleteVideoTask(taskID, background.TaskCompleted, result, "")
}

func (p *VideoGenProvider) recordVideoJobProgress(taskID string, model *sdk.VideoModel, job *sdk.VideoJob) {
	if job == nil {
		return
	}
	result := videoJobResult(model, job)
	line := fmt.Sprintf("Video job %s is %s", job.ID, job.Status)
	if job.Progress != nil {
		line = fmt.Sprintf("%s (progress %.2f)", line, *job.Progress)
	}
	p.bgManager.RecordVideoTaskProgress(taskID, result, line)
}

func (p *VideoGenProvider) cancelVideoJob(ctx context.Context, model *sdk.VideoModel, jobID string) {
	if strings.TrimSpace(jobID) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), videoCancelTimeout)
	defer cancel()
	if err := sdk.CancelVideo(ctx, model, jobID); err != nil {
		p.logger.Debug("cancel video job failed",
			slog.String("job_id", jobID),
			slog.Any("error", err),
		)
	}
}

func (p *VideoGenProvider) saveGeneratedVideo(ctx context.Context, botID, taskID, contentType string, data []byte, output *sdk.VideoOutput) (map[string]any, string) {
	result := map[string]any{
		"media_type": contentType,
		"size_bytes": len(data),
		"output":     videoOutputMap(output),
	}
	if output != nil {
		result["duration_seconds"] = output.DurationSeconds
		if strings.TrimSpace(output.URL) != "" {
			result["output_url"] = output.URL
		}
	}

	if p.containers == nil {
		return result, "Video generated but workspace is not reachable, so it was not saved to disk."
	}
	videoDir := strings.TrimRight(p.dataMount, "/") + strings.TrimPrefix(videoGenDir, "/data")
	if resolver, ok := p.containers.(bridge.WorkspaceInfoProvider); ok {
		if info, err := resolver.WorkspaceInfo(ctx, botID); err == nil &&
			info.Backend == bridge.WorkspaceBackendRemote &&
			strings.TrimSpace(info.DefaultWorkDir) != "" {
			videoDir = strings.TrimRight(info.DefaultWorkDir, "/") + "/generated-videos"
		}
	}
	containerPath := fmt.Sprintf("%s/%s.%s", videoDir, taskID, videoExtension(contentType))

	client, clientErr := p.containers.MCPClient(ctx, botID)
	if clientErr != nil {
		return result, "Video generated but workspace is not reachable, so it was not saved to disk."
	}
	if writeErr := client.WriteFile(ctx, containerPath, data); writeErr != nil {
		return result, fmt.Sprintf("Video generated but failed to save: %s", writeErr.Error())
	}
	result["path"] = containerPath
	return result, ""
}

func videoJobResult(model *sdk.VideoModel, job *sdk.VideoJob) map[string]any {
	result := map[string]any{"model_id": modelIDFromJob(model, job)}
	if job == nil {
		return result
	}
	result["job_id"] = job.ID
	result["provider_status"] = string(job.Status)
	if job.Progress != nil {
		result["progress"] = *job.Progress
	}
	if len(job.Outputs) > 0 {
		result["outputs"] = videoOutputURLs(job.Outputs)
		result["output"] = videoOutputMap(&job.Outputs[0])
		if strings.TrimSpace(job.Outputs[0].URL) != "" {
			result["output_url"] = job.Outputs[0].URL
		}
	}
	if job.Error != nil {
		result["error"] = job.Error.Message
		if strings.TrimSpace(job.Error.Code) != "" {
			result["error_code"] = job.Error.Code
		}
	}
	if len(job.ProviderMetadata) > 0 {
		result["metadata"] = job.ProviderMetadata
	}
	return result
}

func modelIDFromJob(model *sdk.VideoModel, job *sdk.VideoJob) string {
	if job != nil && strings.TrimSpace(job.ModelID) != "" {
		return job.ModelID
	}
	if model != nil {
		return model.ID
	}
	return ""
}

func videoJobError(job *sdk.VideoJob) error {
	if job == nil {
		return errors.New("video generation failed")
	}
	if job.Error != nil && strings.TrimSpace(job.Error.Message) != "" {
		return errors.New(job.Error.Message)
	}
	return fmt.Errorf("video generation finished with status %s", job.Status)
}

func videoExtension(contentType string) string {
	switch {
	case strings.Contains(contentType, "webm"):
		return "webm"
	case strings.Contains(contentType, "quicktime"), strings.Contains(contentType, "mov"):
		return "mov"
	default:
		return "mp4"
	}
}

func videoOutputMap(output *sdk.VideoOutput) map[string]any {
	if output == nil {
		return nil
	}
	return map[string]any{
		"url":              output.URL,
		"content_type":     output.ContentType,
		"width":            output.Width,
		"height":           output.Height,
		"duration_seconds": output.DurationSeconds,
		"has_audio":        output.HasAudio,
	}
}

func videoOutputURLs(outputs []sdk.VideoOutput) []string {
	urls := make([]string, 0, len(outputs))
	for _, output := range outputs {
		if strings.TrimSpace(output.URL) != "" {
			urls = append(urls, output.URL)
		}
	}
	return urls
}
