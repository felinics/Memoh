package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	sdk "github.com/felinics/twilight/sdk"
	readability "github.com/go-shiori/go-readability"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/fetchproviders"
	"github.com/felinics/memoh/internal/redact"
	"github.com/felinics/memoh/internal/settings"
)

const (
	webFetchMaxTextContent = 10000
	webFetchTimeout        = 30 * time.Second
	webFetchUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"
)

type WebFetchProvider struct {
	logger         *slog.Logger
	client         *http.Client
	settings       *settings.Service
	fetchProviders *fetchproviders.Service
}

func NewWebFetchProvider(log *slog.Logger, settingsSvc *settings.Service, fetchSvc *fetchproviders.Service) *WebFetchProvider {
	if log == nil {
		log = slog.Default()
	}
	return &WebFetchProvider{
		logger:         log.With(slog.String("tool", "webfetch")),
		client:         &http.Client{Timeout: webFetchTimeout},
		settings:       settingsSvc,
		fetchProviders: fetchSvc,
	}
}

func (p *WebFetchProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	sess := session
	return []sdk.Tool{
		{
			Name:        ToolWebFetch().String(),
			Description: "Fetch a URL and convert the response to readable content. Supports HTML (converts to Markdown), JSON, XML, and plain text formats.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL to fetch",
					},
					"format": map[string]any{
						"type":        "string",
						"enum":        []string{"auto", "markdown", "json", "xml", "text"},
						"description": "Output format (default: auto - detects from content type)",
					},
				},
				"required": []string{"url"},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execWebFetch(ctx.Context, sess, inputAsMap(input))
			},
		},
	}, nil
}

func (p *WebFetchProvider) execWebFetch(ctx context.Context, session SessionContext, args map[string]any) (any, error) {
	rawURL := strings.TrimSpace(StringArg(args, "url"))
	if rawURL == "" {
		return nil, errors.New("url is required")
	}
	format := strings.TrimSpace(StringArg(args, "format"))
	if format == "" {
		format = "auto"
	}

	provider := sqlc.FetchProvider{Provider: string(fetchproviders.ProviderNative), Enable: true}
	if p.settings != nil && p.fetchProviders != nil && strings.TrimSpace(session.BotID) != "" {
		botSettings, err := p.settings.GetBot(ctx, session.BotID)
		if err != nil {
			return nil, err
		}
		if providerID := strings.TrimSpace(botSettings.FetchProviderID); providerID != "" {
			if configured, err := p.fetchProviders.GetRawByID(ctx, providerID); err == nil {
				provider = configured
			} else {
				p.logger.Warn("configured fetch provider not found, falling back to native",
					slog.String("bot_id", session.BotID),
					slog.String("provider_id", providerID),
					slog.Any("error", err),
				)
			}
		}
	}
	if provider.Provider == "" {
		provider.Provider = string(fetchproviders.ProviderNative)
	}
	if provider.Provider != string(fetchproviders.ProviderNative) && !provider.Enable {
		return nil, errors.New("fetch provider is disabled")
	}
	registerFetchProviderSecrets(provider)
	result, err := p.callFetchProvider(ctx, provider.Provider, provider.Config, rawURL, format)
	if err != nil {
		return nil, err
	}
	if body, ok := result.(map[string]any); ok {
		if _, exists := body["providerName"]; !exists {
			body["providerName"] = fetchProviderDisplayName(provider)
		}
	}
	return result, nil
}

func (p *WebFetchProvider) callFetchProvider(ctx context.Context, providerName string, configJSON []byte, rawURL, format string) (any, error) {
	switch strings.TrimSpace(providerName) {
	case "", string(fetchproviders.ProviderNative):
		return p.callNativeWebFetch(ctx, rawURL, format)
	case string(fetchproviders.ProviderJina):
		return p.callJinaReaderFetch(ctx, configJSON, rawURL)
	case string(fetchproviders.ProviderCloudflareMarkdown):
		return p.callCloudflareMarkdownFetch(ctx, configJSON, rawURL)
	default:
		return nil, errors.New("unsupported fetch provider")
	}
}

func (p *WebFetchProvider) callNativeWebFetch(ctx context.Context, rawURL, format string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	req.Header.Set("User-Agent", webFetchUserAgent)

	resp, err := p.client.Do(req) //nolint:gosec // intentionally fetches user-specified URLs
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http error: %d %s", resp.StatusCode, resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")
	detected := format
	if format == "auto" {
		detected = detectWebFetchFormat(contentType)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	switch detected {
	case "json":
		return p.processJSON(rawURL, contentType, body)
	case "xml":
		return p.processXML(rawURL, contentType, body)
	case "markdown":
		return p.processHTML(rawURL, contentType, body)
	default:
		return p.processText(rawURL, contentType, body)
	}
}

func (*WebFetchProvider) callJinaReaderFetch(ctx context.Context, configJSON []byte, rawURL string) (any, error) {
	cfg := parseFetchConfig(configJSON)
	endpoint := strings.TrimRight(firstNonEmpty(stringValue(cfg["base_url"]), "https://r.jina.ai/"), "/") + "/" + url.PathEscape(rawURL)
	timeout := parseFetchTimeout(configJSON, webFetchTimeout)
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	req.Header.Set("Accept", "text/markdown")
	if apiKey := stringValue(cfg["api_key"]); apiKey != "" {
		req.Header.Set("Authorization", bearerValue(apiKey))
	}
	resp, err := client.Do(req) //nolint:gosec // intentionally fetches user-specified URLs through configured provider
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildFetchHTTPError(resp.StatusCode, body)
	}
	content := strings.TrimSpace(string(body))
	return map[string]any{
		"success": true, "url": rawURL, "provider": string(fetchproviders.ProviderJina),
		"format": "markdown", "contentType": resp.Header.Get("Content-Type"),
		"content": content, "length": len(content),
	}, nil
}

func (*WebFetchProvider) callCloudflareMarkdownFetch(ctx context.Context, configJSON []byte, rawURL string) (any, error) {
	cfg := parseFetchConfig(configJSON)
	accountID := stringValue(cfg["account_id"])
	apiToken := stringValue(cfg["api_token"])
	if accountID == "" || apiToken == "" {
		return nil, errors.New("cloudflare markdown requires account_id and api_token")
	}
	baseURL := strings.TrimRight(firstNonEmpty(stringValue(cfg["base_url"]), "https://api.cloudflare.com/client/v4"), "/")
	endpoint := fmt.Sprintf("%s/accounts/%s/browser-rendering/markdown", baseURL, url.PathEscape(accountID))
	payload, _ := json.Marshal(map[string]any{"url": rawURL})
	timeout := parseFetchTimeout(configJSON, webFetchTimeout)
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", bearerValue(apiToken))
	resp, err := client.Do(req) //nolint:gosec // intentionally fetches user-specified URLs through configured provider
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildFetchHTTPError(resp.StatusCode, body)
	}
	var raw struct {
		Success bool   `json:"success"`
		Result  string `json:"result"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("invalid fetch response")
	}
	if !raw.Success {
		msg := "cloudflare markdown request failed"
		if len(raw.Errors) > 0 && strings.TrimSpace(raw.Errors[0].Message) != "" {
			msg = raw.Errors[0].Message
		}
		return nil, errors.New(msg)
	}
	content := strings.TrimSpace(raw.Result)
	return map[string]any{
		"success": true, "url": rawURL, "provider": string(fetchproviders.ProviderCloudflareMarkdown),
		"format": "markdown", "contentType": resp.Header.Get("Content-Type"),
		"content": content, "length": len(content),
	}, nil
}

func detectWebFetchFormat(contentType string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "application/json"):
		return "json"
	case strings.Contains(ct, "application/xml"), strings.Contains(ct, "text/xml"):
		return "xml"
	case strings.Contains(ct, "text/html"):
		return "markdown"
	default:
		return "text"
	}
}

func (*WebFetchProvider) processJSON(fetchedURL, contentType string, body []byte) (any, error) {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, errors.New("failed to parse json")
	}
	return map[string]any{"success": true, "url": fetchedURL, "provider": string(fetchproviders.ProviderNative), "format": "json", "contentType": contentType, "data": data}, nil
}

func (*WebFetchProvider) processXML(fetchedURL, contentType string, body []byte) (any, error) {
	content := string(body)
	if len(content) > webFetchMaxTextContent {
		content = content[:webFetchMaxTextContent]
	}
	return map[string]any{"success": true, "url": fetchedURL, "provider": string(fetchproviders.ProviderNative), "format": "xml", "contentType": contentType, "content": content}, nil
}

func (p *WebFetchProvider) processHTML(fetchedURL, contentType string, body []byte) (any, error) {
	parsed, err := url.Parse(fetchedURL)
	if err != nil {
		parsed = &url.URL{}
	}
	article, err := readability.FromReader(strings.NewReader(string(body)), parsed)
	if err != nil {
		return nil, fmt.Errorf("failed to extract readable content from html: %w", err)
	}
	if strings.TrimSpace(article.Content) == "" {
		return nil, errors.New("failed to extract readable content from html")
	}
	markdown, err := htmltomarkdown.ConvertString(article.Content)
	if err != nil {
		p.logger.Warn("html-to-markdown conversion failed, falling back to text", slog.Any("error", err))
		markdown = article.TextContent
	}
	textPreview := article.TextContent
	if len(textPreview) > 500 {
		textPreview = textPreview[:500]
	}
	return map[string]any{
		"success": true, "url": fetchedURL, "provider": string(fetchproviders.ProviderNative), "format": "markdown", "contentType": contentType,
		"title": article.Title, "byline": article.Byline, "excerpt": article.Excerpt,
		"content": markdown, "textContent": textPreview, "length": article.Length,
	}, nil
}

func (*WebFetchProvider) processText(fetchedURL, contentType string, body []byte) (any, error) {
	content := string(body)
	length := len(content)
	if length > webFetchMaxTextContent {
		content = content[:webFetchMaxTextContent]
	}
	return map[string]any{"success": true, "url": fetchedURL, "provider": string(fetchproviders.ProviderNative), "format": "text", "contentType": contentType, "content": content, "length": length}, nil
}

func buildFetchHTTPError(statusCode int, body []byte) error {
	detail := extractJSONErrorMessage(body)
	if detail == "" {
		detail = strings.TrimSpace(string(body))
	}
	if len(detail) > 200 {
		detail = detail[:200] + "..."
	}
	if detail != "" {
		return fmt.Errorf("fetch request failed (HTTP %d): %s", statusCode, detail)
	}
	return fmt.Errorf("fetch request failed (HTTP %d)", statusCode)
}

func parseFetchTimeout(configJSON []byte, fallback time.Duration) time.Duration {
	cfg := parseFetchConfig(configJSON)
	raw, ok := cfg["timeout_seconds"]
	if !ok {
		return fallback
	}
	switch value := raw.(type) {
	case float64:
		if value > 0 {
			return time.Duration(value * float64(time.Second))
		}
	case int:
		if value > 0 {
			return time.Duration(value) * time.Second
		}
	}
	return fallback
}

func parseFetchConfig(configJSON []byte) map[string]any {
	if len(configJSON) == 0 {
		return map[string]any{}
	}
	var cfg map[string]any
	if err := json.Unmarshal(configJSON, &cfg); err != nil || cfg == nil {
		return map[string]any{}
	}
	return cfg
}

func bearerValue(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return token
	}
	return "Bearer " + token
}

var fetchProviderSecretFields = []string{"api_key", "api_token"}

func registerFetchProviderSecrets(provider sqlc.FetchProvider) {
	cfg := parseFetchConfig(provider.Config)
	var secrets []string
	for _, key := range fetchProviderSecretFields {
		if v := stringValue(cfg[key]); v != "" {
			secrets = append(secrets, v)
		}
	}
	if len(secrets) > 0 {
		redact.SetSecrets("fetch:"+provider.ID.String(), secrets...)
	}
}

func fetchProviderDisplayName(provider sqlc.FetchProvider) string {
	if name := strings.TrimSpace(provider.Name); name != "" {
		return name
	}
	switch provider.Provider {
	case string(fetchproviders.ProviderJina):
		return "Jina Reader"
	case string(fetchproviders.ProviderCloudflareMarkdown):
		return "Cloudflare Markdown"
	default:
		return "Native"
	}
}
