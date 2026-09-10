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
)

const (
	DefaultBaseURL    = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	DefaultModel      = "qwen3-asr-flash"
	maxAudioDataBytes = 10_000_000
	maxResponseBytes  = 8 * 1024 * 1024
)

type asrRequest struct {
	Model    string       `json:"model"`
	Messages []asrMessage `json:"messages"`
	Stream   bool         `json:"stream"`
	Options  asrOptions   `json:"asr_options"`
}

type asrMessage struct {
	Role string `json:"role"`
	// Qwen expects a string for context and an audio-content array for the user.
	Content any `json:"content"`
}

type asrAudioContent struct {
	Type       string        `json:"type"`
	InputAudio asrInputAudio `json:"input_audio"`
}

type asrInputAudio struct {
	Data string `json:"data"`
}

type asrOptions struct {
	Language  string `json:"language,omitempty"`
	EnableITN *bool  `json:"enable_itn,omitempty"`
}

type Provider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

var _ sdk.TranscriptionProvider = (*Provider)(nil)

// Keep transport diagnostics private even when a legacy handler uses Error().
type requestError struct{ cause error }

func (*requestError) Error() string   { return "alibaba cloud ASR request failed" }
func (e *requestError) Unwrap() error { return e.cause }

func New(apiKey, baseURL string) (*Provider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("alibaba cloud ASR requires an API key")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("alibaba cloud ASR base URL must be an HTTP(S) API base URL")
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
		return nil, errors.New("alibaba cloud ASR requires a non-empty audio file")
	}
	// Keep the entire data URL within DashScope's 10 MB encoded input limit.
	audioPrefix := "data:" + audioContentType(params) + ";base64,"
	maxAudioBytes := (maxAudioDataBytes - len(audioPrefix)) / 4 * 3
	if len(params.Audio) > maxAudioBytes {
		return nil, errors.New("audio file must fit within 10 MB after Base64 encoding")
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
	prompt, err := optionalString(params.Config, "prompt")
	if err != nil {
		return nil, err
	}
	// The Agent tool's per-call prompt takes precedence over saved Qwen context.
	if prompt != "" {
		contextText = prompt
	}
	options := asrOptions{Language: language}
	if v, ok := params.Config["enable_itn"]; ok && v != nil {
		flag, valid := v.(bool)
		if !valid {
			return nil, errors.New("enable_itn must be a boolean")
		}
		options.EnableITN = &flag
	}
	messages := make([]asrMessage, 0, 2)
	if contextText != "" {
		messages = append(messages, asrMessage{Role: "system", Content: contextText})
	}
	messages = append(messages, asrMessage{Role: "user", Content: []asrAudioContent{{
		Type:       "input_audio",
		InputAudio: asrInputAudio{Data: audioPrefix + base64.StdEncoding.EncodeToString(params.Audio)},
	}}})
	body, err := json.Marshal(asrRequest{Model: modelID, Messages: messages, Options: options})
	if err != nil {
		return nil, &requestError{cause: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, &requestError{cause: err}
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	// #nosec G704 -- The endpoint is administrator-configured; redirects are disabled.
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &requestError{cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Upstream error bodies can echo submitted audio or credentials. Do not expose them.
		return nil, fmt.Errorf("alibaba cloud ASR returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &requestError{cause: err}
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("alibaba cloud ASR response exceeds size limit")
	}
	var result struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content     *string `json:"content"`
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
	if message.Content == nil {
		return nil, errors.New("alibaba cloud ASR response has no transcription content")
	}
	out := &sdk.TranscriptionResult{
		Text: *message.Content, DurationSeconds: result.Usage.Seconds,
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
		return "", fmt.Errorf("%s must be a string", key)
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
