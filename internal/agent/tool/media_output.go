package tools

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/jpeg" // register JPEG for image.DecodeConfig
	_ "image/png"  // register PNG for image.DecodeConfig
	"strings"
)

// MediaToolOutput is the execution result of a tool whose output carries an
// image the model should see on its next step (a screenshot). Like
// ReadMediaToolOutput, the native runtime keeps Public as the visible tool
// result and injects the image separately, so the base64 payload never
// enters the model-facing result or rebuilt history.
type MediaToolOutput struct {
	Public         map[string]any
	ImageBase64    string
	ImageMediaType string
}

// imageDimensions reads the pixel size of an encoded PNG or JPEG without
// decoding the whole picture.
func imageDimensions(data []byte) (width, height int, ok bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// imageModeArg resolves the image_mode parameter: "auto" (default) injects
// the screenshot into the model's next input when the model accepts images,
// "path" only saves it to the workspace.
func imageModeArg(args map[string]any) string {
	mode := strings.ToLower(strings.TrimSpace(StringArg(args, "image_mode")))
	if mode == "" {
		return "auto"
	}
	return mode
}

// screenshotDelivery decides how a captured screenshot reaches the model and
// the conversation, and records that decision in the public result so the
// model never assumes it saw an image it did not receive.
type screenshotDelivery struct {
	Mode           string // auto or path
	Inject         bool   // image goes into the next model step
	AttachToChat   bool   // image is shown in the conversation as an attachment
	NotInjectedWhy string
}

func resolveScreenshotDelivery(session SessionContext, mode string) screenshotDelivery {
	d := screenshotDelivery{Mode: mode, AttachToChat: session.CanUseLocalMessagingShortcut()}
	switch mode {
	case "path":
		d.NotInjectedWhy = "image_mode is path"
	case "auto":
		if session.SupportsImageInput {
			d.Inject = true
		} else {
			d.NotInjectedWhy = "the current model does not accept image input; read the saved path with a vision-capable model or describe the page with snapshot"
		}
	}
	return d
}

// finishScreenshot applies the delivery decision: the public result always
// names the saved path, size, and coordinate space; when the image is
// injected the tool returns a MediaToolOutput so the runtime feeds the image
// to the model on the next step.
func finishScreenshot(public map[string]any, delivery screenshotDelivery, imgBytes []byte, mimeType string) any {
	if width, height, ok := imageDimensions(imgBytes); ok {
		public["image_width"] = width
		public["image_height"] = height
	}
	public["image_mode"] = delivery.Mode
	if delivery.Inject {
		public["image_delivery"] = "model_input"
		return MediaToolOutput{
			Public:         public,
			ImageBase64:    base64.StdEncoding.EncodeToString(imgBytes),
			ImageMediaType: mimeType,
		}
	}
	public["image_delivery"] = "path_only"
	public["image_not_injected_because"] = delivery.NotInjectedWhy
	return public
}
