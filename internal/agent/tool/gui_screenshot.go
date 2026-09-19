package tools

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
)

type toolCallIDContextKey struct{}

// withToolCallID carries the SDK tool call id into the tool implementation
// so side-effect events (attachments) can be tied to the call.
func withToolCallID(ctx context.Context, id string) context.Context {
	if strings.TrimSpace(id) == "" {
		return ctx
	}
	return context.WithValue(ctx, toolCallIDContextKey{}, strings.TrimSpace(id))
}

func toolCallIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(toolCallIDContextKey{}).(string)
	return id
}

// screenshotResult saves a captured image to the workspace, shows it in the
// conversation when the session can render attachments, and returns the
// model-facing result: the path, pixel size, coordinate metadata carried in
// data, and whether the image itself was handed to the model (image_mode).
func (p *BrowserProvider) screenshotResult(ctx context.Context, session SessionContext, botID string, imgBytes []byte, mimeType string, data map[string]any, args map[string]any) any {
	if mimeType == "" {
		mimeType = "image/png"
	}
	ext := screenshotExtension(mimeType)
	containerPath := fmt.Sprintf("%s/%d%s", p.screenshotDir(), time.Now().UnixMilli(), ext)
	saveErr := p.saveBytes(ctx, botID, containerPath, imgBytes)
	text := fmt.Sprintf("Screenshot saved to %s", containerPath)
	if saveErr != nil {
		text = fmt.Sprintf("Screenshot captured (failed to save: %s)", saveErr.Error())
	}
	content := []map[string]any{{"type": "text", "text": text}}
	if annotations, ok := data["annotations"]; ok {
		content = append(content, map[string]any{"type": "text", "text": fmt.Sprintf("Annotations: %v", annotations)})
	}
	public := map[string]any{"content": content, "path": containerPath, "mimeType": mimeType, "captured_at": time.Now().UTC().Format(time.RFC3339Nano)}
	if saveErr != nil {
		public["save_error"] = saveErr.Error()
	}
	for k, v := range data {
		if k == "screenshot" {
			continue
		}
		if _, exists := public[k]; !exists {
			public[k] = v
		}
	}
	delivery := resolveScreenshotDelivery(session, imageModeArg(args))
	if delivery.AttachToChat && saveErr == nil {
		session.Emitter(ToolStreamEvent{
			Type:       StreamEventAttachment,
			ToolCallID: toolCallIDFrom(ctx),
			Attachments: []Attachment{{
				Type: "image",
				Path: containerPath,
				Name: path.Base(containerPath),
				Mime: mimeType,
				Size: int64(len(imgBytes)),
			}},
		})
		public["shown_in_conversation"] = true
	}
	return finishScreenshot(public, delivery, imgBytes, mimeType)
}
