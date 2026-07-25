package execution

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	alibabaimages "github.com/memohai/twilight-ai/provider/alibabacloud/images"
	openaiimages "github.com/memohai/twilight-ai/provider/openai/images"
	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
)

const (
	maxGeneratedImageBytes = 50 << 20
	maxImageErrorBodyBytes = 512
)

type imageModelConfig struct {
	modelID        string
	providerName   string
	clientType     string
	apiKey         string
	codexAccountID string
	baseURL        string
	promptCacheTTL string
}

// GeneratedImage is image content returned by a resolved image execution.
type GeneratedImage struct {
	Data      []byte
	MediaType string
}

// ImageTextResponseError indicates that a chat-fallback image model replied
// with text instead of an image. Consumers may relay Text as a normal result.
type ImageTextResponseError struct {
	text string
}

func (e *ImageTextResponseError) Error() string {
	return "no image generated; model response: " + e.text
}

func (e *ImageTextResponseError) Text() string { return e.text }

// Generate invokes the provider using the resolved, private credential set.
func (m ImageModel) Generate(ctx context.Context, prompt, size string) (GeneratedImage, error) {
	httpClient := NewProviderHTTPClient(DefaultProviderRequestTimeout)
	var (
		image GeneratedImage
		err   error
	)
	switch {
	case shouldUseDashScopeImageGeneration(m.config.clientType, m.config.baseURL):
		image, err = generateDashScopeImage(ctx, httpClient, m.config, prompt, size)
	case shouldUseOpenAIImagesGeneration(m.config.clientType, m.config.baseURL):
		image, err = generateOpenAIImagesImage(ctx, httpClient, m.config, prompt, size)
	default:
		image, err = generateChatImage(ctx, m.config, prompt, size)
	}
	return image, SanitizeError(err, m.secrets...)
}

func shouldUseDashScopeImageGeneration(clientType, baseURL string) bool {
	ct := modeldomain.ClientType(clientType)
	if ct != modeldomain.ClientTypeOpenAICompletions && ct != modeldomain.ClientTypeOpenAIResponses {
		return false
	}
	lowerBase := strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(lowerBase, "dashscope") || strings.Contains(lowerBase, "maas.aliyuncs.com")
}

func shouldUseOpenAIImagesGeneration(clientType, baseURL string) bool {
	ct := modeldomain.ClientType(clientType)
	if ct != modeldomain.ClientTypeOpenAICompletions && ct != modeldomain.ClientTypeOpenAIResponses {
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

func generateDashScopeImage(ctx context.Context, httpClient *http.Client, config imageModelConfig, prompt, size string) (GeneratedImage, error) {
	opts := []alibabaimages.Option{
		alibabaimages.WithAPIKey(config.apiKey),
		alibabaimages.WithHTTPClient(httpClient),
	}
	if strings.TrimSpace(config.baseURL) != "" {
		opts = append(opts, alibabaimages.WithBaseURL(strings.TrimSpace(config.baseURL)))
	}
	provider := alibabaimages.New(opts...)
	result, err := sdk.GenerateImage(ctx,
		sdk.WithImageGenerationModel(provider.GenerationModel(config.modelID)),
		sdk.WithImagePrompt(prompt),
		sdk.WithImageSize(size),
		sdk.WithImageN(1),
	)
	if err != nil {
		return GeneratedImage{}, err
	}
	return imageResultToGeneratedImage(ctx, httpClient, result)
}

func generateOpenAIImagesImage(ctx context.Context, httpClient *http.Client, config imageModelConfig, prompt, size string) (GeneratedImage, error) {
	opts := []openaiimages.Option{
		openaiimages.WithAPIKey(config.apiKey),
		openaiimages.WithHTTPClient(httpClient),
	}
	if strings.TrimSpace(config.baseURL) != "" {
		opts = append(opts, openaiimages.WithBaseURL(strings.TrimRight(strings.TrimSpace(config.baseURL), "/")))
	}
	provider := openaiimages.New(opts...)
	result, err := sdk.GenerateImage(ctx,
		sdk.WithImageGenerationModel(provider.GenerationModel(config.modelID)),
		sdk.WithImagePrompt(prompt),
		sdk.WithImageSize(size),
		sdk.WithImageN(1),
	)
	if err != nil {
		return GeneratedImage{}, err
	}
	return imageResultToGeneratedImage(ctx, httpClient, result)
}

func generateChatImage(ctx context.Context, config imageModelConfig, prompt, size string) (GeneratedImage, error) {
	if strings.TrimSpace(size) == "" {
		size = "1024x1024"
	}
	sdkModel := NewSDKChatModel(SDKModelConfig{
		ModelID:        config.modelID,
		ClientType:     config.clientType,
		APIKey:         config.apiKey,
		CodexAccountID: config.codexAccountID,
		BaseURL:        config.baseURL,
	})
	userMsg := fmt.Sprintf("Generate an image with the following description. Size: %s\n\n%s", size, prompt)
	system, messages, _ := ApplyPromptCache(
		sdkModel,
		config.promptCacheTTL,
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
		return GeneratedImage{}, err
	}
	if len(result.Files) == 0 {
		if result.Text != "" {
			return GeneratedImage{}, &ImageTextResponseError{text: result.Text}
		}
		return GeneratedImage{}, errors.New("no image was generated by the model")
	}
	file := result.Files[0]
	data, err := base64.StdEncoding.DecodeString(file.Data)
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("failed to decode generated image: %w", err)
	}
	return GeneratedImage{Data: data, MediaType: normalizeImageMediaType(file.MediaType, data)}, nil
}

func imageResultToGeneratedImage(ctx context.Context, httpClient *http.Client, result *sdk.ImageResult) (GeneratedImage, error) {
	if result == nil || len(result.Data) == 0 {
		return GeneratedImage{}, errors.New("no image was generated by the model")
	}
	first := result.Data[0]
	if strings.TrimSpace(first.B64JSON) != "" {
		data, err := base64.StdEncoding.DecodeString(first.B64JSON)
		if err != nil {
			return GeneratedImage{}, fmt.Errorf("failed to decode generated image: %w", err)
		}
		return GeneratedImage{Data: data, MediaType: normalizeImageMediaType("", data)}, nil
	}
	if strings.TrimSpace(first.URL) != "" {
		return fetchGeneratedImageURL(ctx, httpClient, first.URL)
	}
	return GeneratedImage{}, errors.New("image response did not include image data or URL")
}

func fetchGeneratedImageURL(ctx context.Context, httpClient *http.Client, rawURL string) (GeneratedImage, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("parse image URL: %w", err)
	}
	if err := validateImageDownloadURL(parsed); err != nil {
		return GeneratedImage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("create image download request: %w", err)
	}
	resp, err := imageDownloadClient(httpClient).Do(req) //nolint:gosec // every dialed IP is validated by ssrfSafeDialContext
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("download image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGeneratedImageBytes+1))
	if err != nil {
		return GeneratedImage{}, fmt.Errorf("read image response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return GeneratedImage{}, fmt.Errorf("download image failed with status %d: %s", resp.StatusCode, truncateForError(string(data), maxImageErrorBodyBytes))
	}
	if len(data) > maxGeneratedImageBytes {
		return GeneratedImage{}, fmt.Errorf("download image exceeded %d bytes", maxGeneratedImageBytes)
	}
	if len(data) == 0 {
		return GeneratedImage{}, errors.New("downloaded image response was empty")
	}
	mediaType, ok := detectImageMediaType(data)
	if !ok {
		return GeneratedImage{}, fmt.Errorf("downloaded content is not an image: %s", strings.TrimSpace(resp.Header.Get("Content-Type")))
	}
	return GeneratedImage{Data: data, MediaType: mediaType}, nil
}

func imageDownloadClient(base *http.Client) *http.Client {
	clone := &http.Client{Transport: imageDownloadTransport(), CheckRedirect: checkImageRedirect}
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
		for _, address := range addrs {
			ips = append(ips, address.IP)
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

var imageConnectDialer = (&net.Dialer{}).DialContext

func validateImageDownloadURL(parsed *url.URL) error {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.TrimSpace(parsed.Hostname()) == "" {
		return fmt.Errorf("unsupported image URL: %s", parsed)
	}
	return nil
}

func isRestrictedImageDownloadIP(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

func detectImageMediaType(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	detected := http.DetectContentType(data)
	return detected, strings.HasPrefix(detected, "image/")
}

func truncateForError(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...(truncated)"
}

func normalizeImageMediaType(_ string, data []byte) string {
	if detected, ok := detectImageMediaType(data); ok {
		return detected
	}
	return "image/png"
}
