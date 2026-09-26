//nolint:gosec
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/attachment"
	audiopkg "github.com/felinics/memoh/internal/audio"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/settings"
)

type TranscriptionProvider struct {
	logger   *slog.Logger
	settings *settings.Service
	audio    *audiopkg.Service
	media    *media.Service
	http     *http.Client
}

func NewTranscriptionProvider(log *slog.Logger, settingsSvc *settings.Service, audioSvc *audiopkg.Service, mediaSvc *media.Service) *TranscriptionProvider {
	if log == nil {
		log = slog.Default()
	}
	return &TranscriptionProvider{
		logger:   log.With(slog.String("tool", "transcribe_audio")),
		settings: settingsSvc,
		audio:    audioSvc,
		media:    mediaSvc,
		http: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				if _, err := validateURL(req.Context(), req.URL.String()); err != nil {
					return fmt.Errorf("redirect to non-public address is not allowed: %w", err)
				}
				return nil
			},
		},
	}
}

func (p *TranscriptionProvider) Tools(ctx context.Context, session SessionContext) ([]toolexec.Tool, error) {
	if p.settings == nil || p.audio == nil || p.media == nil {
		return nil, nil
	}
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, nil
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil || strings.TrimSpace(botSettings.TranscriptionModelID) == "" {
		return nil, nil
	}
	return p.transcribeTools(session), nil
}

// transcribeArgs is the wire shape of transcribe_audio. The alias names
// (audio_path, file_path, audio_url, content_type) are not in the schema
// but have always been accepted, so decoding folds them in.
type transcribeArgs struct {
	Path        string `json:"path,omitempty" jsonschema:"Audio file path from the message context, usually under /data/.memoh/media/..."`
	URL         string `json:"url,omitempty" jsonschema:"Direct audio URL when a path is unavailable"`
	Language    string `json:"language,omitempty" jsonschema:"Optional language hint"`
	Prompt      string `json:"prompt,omitempty" jsonschema:"Optional transcription prompt"`
	ContentType string `json:"contentType,omitempty" jsonschema:"Optional MIME type override"`
}

func (a *transcribeArgs) UnmarshalJSON(data []byte) error {
	var wire struct {
		Path         string `json:"path"`
		AudioPath    string `json:"audio_path"`
		FilePath     string `json:"file_path"`
		URL          string `json:"url"`
		AudioURL     string `json:"audio_url"`
		Language     string `json:"language"`
		Prompt       string `json:"prompt"`
		ContentType  string `json:"contentType"`
		ContentType2 string `json:"content_type"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*a = transcribeArgs{
		Path:        firstNonEmpty(wire.Path, wire.AudioPath, wire.FilePath),
		URL:         firstNonEmpty(wire.URL, wire.AudioURL),
		Language:    strings.TrimSpace(wire.Language),
		Prompt:      strings.TrimSpace(wire.Prompt),
		ContentType: firstNonEmpty(wire.ContentType, wire.ContentType2),
	}
	return nil
}

func (p *TranscriptionProvider) transcribeTools(session SessionContext) []toolexec.Tool {
	sess := session
	return []toolexec.Tool{toolexec.Define(ToolTranscribeAudio().String(),
		"Transcribe an audio or voice message into text. Use this when the user sent a voice message and you need to understand its contents. Accepts a bot media path such as /data/.memoh/media/... or a direct URL.",
		func(execCtx *toolexec.ToolExecContext, args transcribeArgs) (sdk.ToolOutput, error) {
			return toolexec.OutputPair(p.execTranscribe(execCtx.Context, sess, args))
		},
	)}
}

func (p *TranscriptionProvider) execTranscribe(ctx context.Context, session SessionContext, args transcribeArgs) (any, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	botSettings, err := p.settings.GetBot(ctx, botID)
	if err != nil {
		return nil, errors.New("failed to load bot settings")
	}
	modelID := strings.TrimSpace(botSettings.TranscriptionModelID)
	if modelID == "" {
		return nil, errors.New("bot has no transcription model configured")
	}

	path := args.Path
	rawURL := args.URL
	if path == "" && rawURL == "" {
		return nil, errors.New("path or url is required")
	}

	audio, filename, contentType, err := p.loadAudio(ctx, botID, path, rawURL, args.ContentType)
	if err != nil {
		return nil, err
	}

	override := map[string]any{}
	if language := args.Language; language != "" {
		override["language"] = language
	}
	if prompt := args.Prompt; prompt != "" {
		override["prompt"] = prompt
	}
	result, err := p.audio.Transcribe(ctx, modelID, audio, filename, contentType, override)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":               true,
		"text":             result.Text,
		"language":         result.Language,
		"duration_seconds": result.DurationSeconds,
	}, nil
}

func (p *TranscriptionProvider) loadAudio(ctx context.Context, botID, pathValue, rawURL, contentTypeOverride string) ([]byte, string, string, error) {
	if pathValue != "" {
		return p.loadAudioFromPath(ctx, botID, pathValue, contentTypeOverride)
	}
	u, err := validateURL(ctx, rawURL)
	if err != nil {
		return nil, "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, "", "", fmt.Errorf("download audio: unexpected status %d", resp.StatusCode)
	}
	defer func(body io.ReadCloser) {
		if closeErr := body.Close(); closeErr != nil {
			p.logger.WarnContext(ctx, "failed to close audio response body", slog.Any("error", closeErr))
		}
	}(resp.Body)
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", "", err
	}
	contentType := strings.TrimSpace(contentTypeOverride)
	if contentType == "" {
		contentType = strings.TrimSpace(resp.Header.Get("Content-Type"))
	}
	return audio, filepath.Base(strings.TrimSpace(req.URL.Path)), contentType, nil
}

func (p *TranscriptionProvider) loadAudioFromPath(ctx context.Context, botID, pathValue, contentTypeOverride string) ([]byte, string, string, error) {
	storageKey := attachment.ExtractStorageKey(pathValue)
	if storageKey == "" {
		return nil, "", "", fmt.Errorf("unsupported media path: %s", pathValue)
	}
	asset, err := p.media.GetByStorageKey(ctx, botID, storageKey)
	if err != nil {
		return nil, "", "", err
	}
	reader, _, err := p.media.Open(ctx, botID, asset.ContentHash)
	if err != nil {
		return nil, "", "", err
	}
	defer func(reader io.ReadCloser) {
		if closeErr := reader.Close(); closeErr != nil {
			p.logger.WarnContext(ctx, "failed to close media reader", slog.Any("error", closeErr))
		}
	}(reader)
	audio, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", "", err
	}
	contentType := strings.TrimSpace(contentTypeOverride)
	if contentType == "" {
		contentType = strings.TrimSpace(asset.Mime)
	}
	return audio, filepath.Base(storageKey), contentType, nil
}

func validateURL(ctx context.Context, rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}

	hostname := u.Hostname()
	if hostname == "" {
		return nil, errors.New("missing hostname in url")
	}

	resolver := net.Resolver{}
	ips, err := resolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed for %s: %w", hostname, err)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no ip addresses found for %s", hostname)
	}

	for _, ip := range ips {
		if ip.IP.IsLoopback() || ip.IP.IsPrivate() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() {
			return nil, fmt.Errorf("url resolves to a non-public ip address: %s", ip.IP.String())
		}
	}

	return u, nil
}
