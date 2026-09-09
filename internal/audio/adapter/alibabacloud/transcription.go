// Package alibabacloud implements DashScope's OpenAI-compatible Qwen ASR API.
package alibabacloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/audio/adapter"
)

const (
	DefaultBaseURL   = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	DefaultModel     = "qwen3-asr-flash"
	maxAudioBytes    = 10 * 1024 * 1024
	maxResponseBytes = 8 * 1024 * 1024
)

type Provider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

var _ sdk.TranscriptionProvider = (*Provider)(nil)

func New(apiKey, baseURL string) (*Provider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: Alibaba Cloud ASR requires an API key", adapter.ErrInvalidInput)
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: Alibaba Cloud ASR base URL must be an HTTP(S) API base URL", adapter.ErrInvalidInput)
	}
	return &Provider{apiKey: apiKey, baseURL: baseURL, client: &http.Client{
		Timeout:       90 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// ListModels returns the supported preset catalog; DashScope has no ASR-only model list.
func (p *Provider) ListModels(ctx context.Context) ([]*sdk.TranscriptionModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []*sdk.TranscriptionModel{{ID: DefaultModel, Provider: p}}, nil
}

func (p *Provider) DoTranscribe(ctx context.Context, params sdk.TranscriptionParams) (*sdk.TranscriptionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(params.Audio) == 0 {
		return nil, fmt.Errorf("%w: Alibaba Cloud ASR requires a non-empty audio file", adapter.ErrInvalidInput)
	}
	if len(params.Audio) > maxAudioBytes {
		return nil, fmt.Errorf("%w: audio file must be at most 10 MiB", adapter.ErrAudioTooLarge)
	}
	modelID := DefaultModel
	if params.Model != nil && strings.TrimSpace(params.Model.ID) != "" {
		modelID = strings.TrimSpace(params.Model.ID)
	}
	language, err := optionalString(params.Config, "language")
	if err != nil {
		return nil, err
	}
	contextText, err := optionalString(params.Config, "context")
	if err != nil {
		return nil, err
	}
	options := asrOptions{Language: language}
	if v, ok := params.Config["enable_itn"]; ok && v != nil {
		flag, valid := v.(bool)
		if !valid {
			return nil, fmt.Errorf("%w: enable_itn must be a boolean", adapter.ErrInvalidInput)
		}
		options.EnableITN = &flag
	}
	messages := make([]asrMessage, 0, 2)
	if contextText != "" {
		messages = append(messages, asrMessage{Role: "system", Content: contextText})
	}
	messages = append(messages, asrMessage{Role: "user", Content: []asrAudioContent{{
		Type:       "input_audio",
		InputAudio: asrInputAudio{Data: "data:" + audioContentType(params) + ";base64," + base64.StdEncoding.EncodeToString(params.Audio)},
	}}})
	body, err := json.Marshal(asrRequest{Model: modelID, Messages: messages, Options: options})
	if err != nil {
		return nil, fmt.Errorf("encode Alibaba Cloud ASR request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Alibaba Cloud ASR request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	// #nosec G704 -- The endpoint is administrator-configured; redirects are disabled.
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: Alibaba Cloud ASR request: %w", adapter.ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Upstream error bodies can echo submitted audio or credentials. Do not expose them.
		kind := adapter.ErrRequestRejected
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			kind = adapter.ErrRateLimited
		case resp.StatusCode >= http.StatusInternalServerError, resp.StatusCode == http.StatusRequestTimeout:
			kind = adapter.ErrUnavailable
		}
		return nil, fmt.Errorf("%w: Alibaba Cloud ASR returned HTTP %d", kind, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read Alibaba Cloud ASR response: %w", adapter.ErrUnavailable, err)
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("alibaba cloud ASR response exceeds size limit")
	}
	var result struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content     json.RawMessage `json:"content"`
				Annotations []struct {
					Type     string `json:"type"`
					Language string `json:"language"`
				} `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Seconds float64 `json:"seconds"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.New("invalid Alibaba Cloud ASR response JSON")
	}
	if len(result.Choices) == 0 {
		return nil, errors.New("alibaba cloud ASR response has no transcription choice")
	}
	message := result.Choices[0].Message
	var text string
	if len(message.Content) == 0 || bytes.Equal(message.Content, []byte("null")) {
		return nil, errors.New("alibaba cloud ASR response has no transcription content")
	}
	if err := json.Unmarshal(message.Content, &text); err != nil {
		return nil, errors.New("alibaba cloud ASR transcription content must be text")
	}
	out := &sdk.TranscriptionResult{
		Text: text, DurationSeconds: result.Usage.Seconds,
		ProviderMetadata: map[string]any{"id": result.ID, "model": result.Model},
	}
	for _, annotation := range message.Annotations {
		if annotation.Type == "audio_info" {
			out.Language = annotation.Language
			break
		}
	}
	return out, nil
}

func optionalString(config map[string]any, key string) (string, error) {
	value, ok := config[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%w: %s must be a string", adapter.ErrInvalidInput, key)
	}
	return strings.TrimSpace(text), nil
}

func audioContentType(params sdk.TranscriptionParams) string {
	contentType, _, _ := mime.ParseMediaType(params.ContentType)
	if strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "video/") {
		return contentType
	}
	// The PC test uploader can send application/octet-stream. Infer its media type.
	switch strings.ToLower(filepath.Ext(params.Filename)) {
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".webm":
		return "audio/webm"
	default:
		return http.DetectContentType(params.Audio)
	}
}
