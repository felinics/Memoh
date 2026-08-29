package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	alibabaimages "github.com/felinics/twilight/provider/alibabacloud/images"
	openaiimages "github.com/felinics/twilight/provider/openai/images"
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	imageGenDir            = "/data/generated-images"
	maxGeneratedImageBytes = 50 << 20
	maxImageErrorBodyBytes = 512
)

type generatedImage struct {
	Data      []byte
	MediaType string
}

// imageModelTextResponseError signals that the chat-fallback image model
// replied with text instead of an image (e.g. a refusal or a request for
// clarification). This is not a tool failure — execGenerateImage relays the
// text as a normal tool result so the agent can act on it, rather than
// surfacing it as an execution error that counts against loop/retry guards.
type imageModelTextResponseError struct {
	text string
}

func (e *imageModelTextResponseError) Error() string {
	return "no image generated; model response: " + e.text
}

type ImageGenProvider struct {
	logger     *slog.Logger
	settings   *settings.Service
	models     *models.Service
	queries    dbstore.Queries
	containers bridge.Provider
	dataMount  string
}

func NewImageGenProvider(
	log *slog.Logger,
	settingsSvc *settings.Service,
	modelsSvc *models.Service,
	queries dbstore.Queries,
	containers bridge.Provider,
	dataMount string,
) *ImageGenProvider {
	if log == nil {
		log = slog.Default()
	}
	return &ImageGenProvider{
		logger:     log.With(slog.String("tool", "image_gen")),
		settings:   settingsSvc,
		models:     modelsSvc,
		queries:    queries,
		containers: containers,
		dataMount:  dataMount,
	}
}

func (p *ImageGenProvider) Tools(ctx context.Context, session SessionContext) ([]sdk.Tool, error) {
	if p.settings == nil || p.models == nil || p.queries == nil {
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
	if strings.TrimSpace(botSettings.ImageModelID) == "" {
		return nil, nil
	}
	description := "Generate an image from a text description using the configured image generation model. Returns the workspace file path of the generated image."
	if session.CanUseLocalMessagingShortcut() {
		description += " The generated image is automatically shown to the user in the current conversation."
	} else {
		description += " The image is not shown to the user automatically."
	}
	sess := session
	return []sdk.Tool{
		{
			Name:        ToolGenerateImage().String(),
			Description: description,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt": map[string]any{"type": "string", "description": "Detailed description of the image to generate"},
					"size":   map[string]any{"type": "string", "description": "Optional image size, e.g. 1024x1024, 1792x1024, 1024x1792. Leave empty to use the provider default."},
				},
				"required": []string{"prompt"},
			},
			Execute: func(execCtx *sdk.ToolExecContext, input any) (any, error) {
				return p.execGenerateImage(execCtx.Context, sess, execCtx.ToolCallID, inputAsMap(input))
			},
		},
	}, nil
}

func (*ImageGenProvider) Usage(_ context.Context, session SessionContext, available AvailableTools) string {
	genRef, ok := available.Ref(ToolGenerateImage())
	if !ok {
		return ""
	}
	sendRef, hasSend := available.Ref(ToolSend())
	var items []string
	if session.CanUseLocalMessagingShortcut() {
		items = append(items, genRef+" already shows the generated image to the user in the current conversation; do not send it there again.")
		if hasSend {
			items = append(items, "To share a generated image with another channel or person, call "+sendRef+" with the returned path in `attachments`.")
		}
	} else if hasSend {
		items = append(items, genRef+" only saves the image to the workspace; the user never sees it automatically. Share it by calling "+sendRef+" with the returned path in `attachments`.")
	}
	return usageSection("Image generation", items)
}

func (p *ImageGenProvider) execGenerateImage(ctx context.Context, session SessionContext, toolCallID string, args map[string]any) (any, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	prompt := strings.TrimSpace(StringArg(args, "prompt"))
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}
	size := strings.TrimSpace(StringArg(args, "size"))

	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, errors.New("failed to load bot settings")
	}
	imageModelID := strings.TrimSpace(botSettings.ImageModelID)
	if imageModelID == "" {
		return nil, errors.New("no image generation model configured")
	}

	modelResp, err := p.models.GetByID(ctx, imageModelID)
	if err != nil {
		return nil, fmt.Errorf("failed to load image model: %w", err)
	}
	if !modelResp.Enable {
		return nil, fmt.Errorf("image model %s is disabled", modelResp.ModelID)
	}
	if !modelResp.HasCompatibility(models.CompatImageOutput) {
		return nil, errors.New("configured model does not support image generation")
	}

	provider, err := models.FetchProviderByID(ctx, p.queries, modelResp.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("failed to load model provider: %w", err)
	}
	if !provider.Enable {
		return nil, fmt.Errorf("image model provider %s is disabled", provider.Name)
	}

	authResolver := providers.NewService(nil, p.queries, "")
	creds, err := authResolver.ResolveModelCredentials(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve provider credentials: %w", err)
	}

	image, err := p.generateImage(ctx, provider, creds.APIKey, modelResp.ModelID, prompt, size)
	if err != nil {
		var textResp *imageModelTextResponseError
		if errors.As(err, &textResp) {
			return map[string]any{
				"model_response": textResp.text,
				"note":           "the image model returned a text response instead of an image",
			}, nil
		}
		return nil, fmt.Errorf("image generation failed: %w", err)
	}
	imgBytes := image.Data

	ext := "png"
	switch {
	case strings.Contains(image.MediaType, "jpeg"), strings.Contains(image.MediaType, "jpg"):
		ext = "jpg"
	case strings.Contains(image.MediaType, "webp"):
		ext = "webp"
	}

	imageDir := strings.TrimRight(p.dataMount, "/") + strings.TrimPrefix(imageGenDir, "/data")
	if resolver, ok := p.containers.(bridge.WorkspaceInfoProvider); ok {
		if info, err := resolver.WorkspaceInfo(ctx, botID); err == nil &&
			info.Backend == bridge.WorkspaceBackendRemote &&
			strings.TrimSpace(info.DefaultWorkDir) != "" {
			imageDir = strings.TrimRight(info.DefaultWorkDir, "/") + "/generated-images"
		}
	}
	containerPath := fmt.Sprintf("%s/%d.%s", imageDir, time.Now().UnixMilli(), ext)

	if p.containers == nil {
		return p.unsavedImageResult(session, toolCallID, image, "Image generated, but the workspace is not reachable, so the file was not saved"), nil
	}

	client, clientErr := p.containers.MCPClient(ctx, botID)
	if clientErr != nil {
		return p.unsavedImageResult(session, toolCallID, image, "Image generated, but the workspace is not reachable, so the file was not saved"), nil
	}

	if writeErr := client.WriteFile(ctx, containerPath, imgBytes); writeErr != nil {
		return p.unsavedImageResult(session, toolCallID, image, fmt.Sprintf("Image generated (failed to save: %s)", writeErr.Error())), nil
	}

	result := map[string]any{
		"path":       containerPath,
		"media_type": image.MediaType,
		"size_bytes": len(imgBytes),
	}
	if p.deliverGeneratedImage(session, toolCallID, Attachment{
		Type: "image",
		Path: containerPath,
		Name: path.Base(containerPath),
		Mime: image.MediaType,
		Size: int64(len(imgBytes)),
	}) {
		result["delivered"] = "current_conversation"
	}
	return result, nil
}

// deliverGeneratedImage shows the generated image to the user in the current
// conversation via the live agent stream, mirroring how speak delivers
// same-conversation voice attachments. Non-interactive runs return false and
// rely on the model sharing the returned path through the send tool.
func (*ImageGenProvider) deliverGeneratedImage(session SessionContext, toolCallID string, att Attachment) bool {
	if !session.CanUseLocalMessagingShortcut() {
		return false
	}
	session.Emitter(ToolStreamEvent{
		Type:        StreamEventAttachment,
		ToolCallID:  toolCallID,
		Attachments: []Attachment{att},
	})
	return true
}

// unsavedImageResult handles images that could not be persisted to the
// workspace: deliver them to the user inline when the live stream allows it,
// otherwise fall back to returning the image content to the model.
func (p *ImageGenProvider) unsavedImageResult(session SessionContext, toolCallID string, image generatedImage, text string) map[string]any {
	if p.deliverGeneratedImage(session, toolCallID, Attachment{
		Type: "image",
		URL:  fmt.Sprintf("data:%s;base64,%s", image.MediaType, base64.StdEncoding.EncodeToString(image.Data)),
		Mime: image.MediaType,
		Size: int64(len(image.Data)),
	}) {
		return map[string]any{
			"delivered":  "current_conversation",
			"media_type": image.MediaType,
			"size_bytes": len(image.Data),
			"note":       text,
		}
	}
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
			{"type": "image", "data": base64.StdEncoding.EncodeToString(image.Data), "mimeType": image.MediaType},
		},
	}
}

func (*ImageGenProvider) generateImage(ctx context.Context, provider sqlc.Provider, apiKey, modelID, prompt, size string) (generatedImage, error) {
	baseURL := providers.ProviderConfigString(provider, "base_url")
	httpClient := models.NewProviderHTTPClient(models.DefaultProviderRequestTimeout)

	switch {
	case shouldUseDashScopeImageGeneration(provider.ClientType, baseURL, modelID):
		return generateDashScopeImage(ctx, httpClient, baseURL, apiKey, modelID, prompt, size)
	case shouldUseOpenAIImagesGeneration(provider.ClientType, baseURL, modelID):
		return generateOpenAIImagesImage(ctx, httpClient, baseURL, apiKey, modelID, prompt, size)
	default:
		return generateChatImage(ctx, provider, apiKey, modelID, prompt, size)
	}
}

// shouldUseDashScopeImageGeneration routes to the DashScope native images API
// only when the provider's base URL confirms DashScope. Routing by bare model
// name (e.g. "qwen-image", "wan*") would send the DashScope request format to
// any OpenAI-compatible aggregator that happens to host a same-named model,
// which such aggregators serve through chat completions instead — so the base
// URL is the authoritative signal and the chat fallback handles the rest.
func shouldUseDashScopeImageGeneration(clientType, baseURL, _ string) bool {
	ct := models.ClientType(clientType)
	if ct != models.ClientTypeOpenAICompletions && ct != models.ClientTypeOpenAIResponses {
		return false
	}
	lowerBase := strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(lowerBase, "dashscope") ||
		strings.Contains(lowerBase, "maas.aliyuncs.com")
}

// shouldUseOpenAIImagesGeneration routes to the OpenAI /images/generations API
// only for base URLs known to serve it. As with DashScope, routing by bare
// model name alone is unsafe on unknown bases, so those fall through to the
// chat fallback rather than being forced onto the images endpoint.
func shouldUseOpenAIImagesGeneration(clientType, baseURL, _ string) bool {
	ct := models.ClientType(clientType)
	if ct != models.ClientTypeOpenAICompletions && ct != models.ClientTypeOpenAIResponses {
		return false
	}
	lowerBase := strings.ToLower(strings.TrimSpace(baseURL))
	if strings.Contains(lowerBase, "openrouter.ai") {
		return false
	}
	return strings.Contains(lowerBase, "api.openai.com") ||
		strings.Contains(lowerBase, "volces.com") ||
		strings.Contains(lowerBase, "bytepluses.com") ||
		strings.Contains(lowerBase, "siliconflow")
}

func generateDashScopeImage(ctx context.Context, httpClient *http.Client, baseURL, apiKey, modelID, prompt, size string) (generatedImage, error) {
	opts := []alibabaimages.Option{
		alibabaimages.WithAPIKey(apiKey),
		alibabaimages.WithHTTPClient(httpClient),
	}
	if strings.TrimSpace(baseURL) != "" {
		opts = append(opts, alibabaimages.WithBaseURL(strings.TrimSpace(baseURL)))
	}
	provider := alibabaimages.New(opts...)
	result, err := sdk.GenerateImage(ctx,
		sdk.WithImageGenerationModel(provider.GenerationModel(modelID)),
		sdk.WithImagePrompt(prompt),
		sdk.WithImageSize(size),
		sdk.WithImageN(1),
	)
	if err != nil {
		return generatedImage{}, err
	}
	return imageResultToGeneratedImage(ctx, httpClient, result)
}

func generateOpenAIImagesImage(ctx context.Context, httpClient *http.Client, baseURL, apiKey, modelID, prompt, size string) (generatedImage, error) {
	opts := []openaiimages.Option{
		openaiimages.WithAPIKey(apiKey),
		openaiimages.WithHTTPClient(httpClient),
	}
	if strings.TrimSpace(baseURL) != "" {
		opts = append(opts, openaiimages.WithBaseURL(strings.TrimRight(strings.TrimSpace(baseURL), "/")))
	}
	provider := openaiimages.New(opts...)
	imageModel := provider.GenerationModel(modelID)
	result, err := sdk.GenerateImage(ctx,
		sdk.WithImageGenerationModel(imageModel),
		sdk.WithImagePrompt(prompt),
		sdk.WithImageSize(size),
		sdk.WithImageN(1),
	)
	if err != nil {
		return generatedImage{}, err
	}
	return imageResultToGeneratedImage(ctx, httpClient, result)
}

func generateChatImage(ctx context.Context, provider sqlc.Provider, apiKey, modelID, prompt, size string) (generatedImage, error) {
	if strings.TrimSpace(size) == "" {
		size = "1024x1024"
	}
	sdkModel := models.NewSDKChatModel(models.SDKModelConfig{
		ModelID:               modelID,
		ClientType:            provider.ClientType,
		APIKey:                apiKey,
		BaseURL:               providers.ProviderConfigString(provider, "base_url"),
		ChatCompletionsCompat: providers.ProviderConfigString(provider, models.ChatCompletionsCompatConfigKey),
	})

	userMsg := fmt.Sprintf("Generate an image with the following description. Size: %s\n\n%s", size, prompt)
	system, messages, _ := models.ApplyPromptCache(
		sdkModel,
		providers.ProviderConfigString(provider, "prompt_cache_ttl"),
		"",
		[]sdk.Message{sdk.UserMessage(userMsg)},
		nil,
	)
	result, err := sdk.GenerateTextResult(ctx,
		sdk.WithModel(sdkModel),
		sdk.WithSystem(system),
		sdk.WithMessages(messages),
	)
	if err != nil {
		return generatedImage{}, err
	}

	if len(result.Files) == 0 {
		if result.Text != "" {
			return generatedImage{}, &imageModelTextResponseError{text: result.Text}
		}
		return generatedImage{}, errors.New("no image was generated by the model")
	}

	file := result.Files[0]
	imgBytes, err := base64.StdEncoding.DecodeString(file.Data)
	if err != nil {
		return generatedImage{}, fmt.Errorf("failed to decode generated image: %w", err)
	}
	return generatedImage{
		Data:      imgBytes,
		MediaType: normalizeImageMediaType(file.MediaType, imgBytes),
	}, nil
}

func imageResultToGeneratedImage(ctx context.Context, httpClient *http.Client, result *sdk.ImageResult) (generatedImage, error) {
	if result == nil || len(result.Data) == 0 {
		return generatedImage{}, errors.New("no image was generated by the model")
	}
	first := result.Data[0]
	if strings.TrimSpace(first.B64JSON) != "" {
		data, err := base64.StdEncoding.DecodeString(first.B64JSON)
		if err != nil {
			return generatedImage{}, fmt.Errorf("failed to decode generated image: %w", err)
		}
		return generatedImage{Data: data, MediaType: normalizeImageMediaType("", data)}, nil
	}
	if strings.TrimSpace(first.URL) != "" {
		return fetchGeneratedImageURL(ctx, httpClient, first.URL)
	}
	return generatedImage{}, errors.New("image response did not include image data or URL")
}

func fetchGeneratedImageURL(ctx context.Context, httpClient *http.Client, rawURL string) (generatedImage, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return generatedImage{}, fmt.Errorf("parse image URL: %w", err)
	}
	if err := validateImageDownloadURL(ctx, parsed); err != nil {
		return generatedImage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return generatedImage{}, fmt.Errorf("create image download request: %w", err)
	}
	resp, err := imageDownloadClient(httpClient).Do(req) //nolint:gosec // G704: provider-returned image URL; the download transport validates every dialed IP against restricted ranges at connect time (ssrfSafeDialContext), closing the DNS-rebinding TOCTOU
	if err != nil {
		return generatedImage{}, fmt.Errorf("download image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGeneratedImageBytes+1))
	if err != nil {
		return generatedImage{}, fmt.Errorf("read image response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return generatedImage{}, fmt.Errorf("download image failed with status %d: %s", resp.StatusCode, truncateForError(string(data), maxImageErrorBodyBytes))
	}
	if len(data) > maxGeneratedImageBytes {
		return generatedImage{}, fmt.Errorf("download image exceeded %d bytes", maxGeneratedImageBytes)
	}
	if len(data) == 0 {
		return generatedImage{}, errors.New("downloaded image response was empty")
	}
	mediaType, ok := detectImageMediaType(resp.Header.Get("Content-Type"), data)
	if !ok {
		return generatedImage{}, fmt.Errorf("downloaded content is not an image: %s", strings.TrimSpace(resp.Header.Get("Content-Type")))
	}
	return generatedImage{
		Data:      data,
		MediaType: mediaType,
	}, nil
}

// imageDownloadClient wraps the provider HTTP client with an SSRF-safe
// transport. IP validation happens inside the dialer (ssrfSafeDialContext), so
// the address that is actually connected to is the one that was checked — this
// closes the DNS-rebinding TOCTOU that a validate-then-Do split leaves open,
// and covers every redirect hop because the transport dials each one.
func imageDownloadClient(base *http.Client) *http.Client {
	clone := &http.Client{
		Transport:     imageDownloadTransport(),
		CheckRedirect: checkImageRedirect,
	}
	if base != nil {
		clone.Timeout = base.Timeout
	}
	return clone
}

func checkImageRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("unsupported redirect scheme: %s", req.URL.Scheme)
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

func imageDownloadTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := base.Clone()
		clone.DialContext = ssrfSafeDialContext
		return clone
	}
	return &http.Transport{DialContext: ssrfSafeDialContext}
}

// ssrfSafeDialContext resolves the destination host, rejects any address in a
// restricted range, and dials the validated IP literal directly. Because the
// connection targets the exact IP that passed validation, a short-TTL rebinding
// record cannot swap in an internal address between check and connect. TLS
// verification still uses the original hostname (the transport sets ServerName
// from the request URL, not from the dialed address).
func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve image URL host %s: %w", host, err)
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve image URL host %s: no addresses", host)
	}
	var lastErr error
	for _, ip := range ips {
		if isRestrictedImageDownloadIP(ip) {
			lastErr = fmt.Errorf("blocked image URL host %s resolved to restricted address %s", host, ip.String())
			continue
		}
		conn, err := imageConnectDialer(ctx, network, net.JoinHostPort(ip.String(), port))
		if err != nil {
			lastErr = err
			continue
		}
		return conn, nil
	}
	return nil, lastErr
}

// imageConnectDialer performs the TCP connect after an address has passed SSRF
// validation. It is a package variable so tests can redirect the connect to a
// local server while still exercising the real IP-range validation above.
var imageConnectDialer = (&net.Dialer{}).DialContext

// validateImageDownloadURL is a cheap pre-flight check for a clear early error;
// the authoritative IP-range enforcement lives in ssrfSafeDialContext, which
// runs for the initial request and every redirect hop.
func validateImageDownloadURL(_ context.Context, parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.TrimSpace(parsed.Hostname()) == "" {
		return fmt.Errorf("unsupported image URL: %s", parsed)
	}
	return nil
}

func isRestrictedImageDownloadIP(ip net.IP) bool {
	return ip == nil ||
		ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}

func detectImageMediaType(_ string, data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	detected := http.DetectContentType(data)
	if strings.HasPrefix(detected, "image/") {
		return detected, true
	}
	return "", false
}

// truncateForError bounds an untrusted response body before it is embedded in
// an error message, so an expired-URL error page or junk payload cannot balloon
// the tool result, conversation stream, and logs.
func truncateForError(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…(truncated)"
}

func normalizeImageMediaType(mediaType string, data []byte) string {
	if detected, ok := detectImageMediaType(mediaType, data); ok {
		return detected
	}
	return "image/png"
}
