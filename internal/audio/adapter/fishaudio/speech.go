package fishaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
)

type speech struct{ client *client }

func NewSpeech(apiKey, baseURL string) (sdk.SpeechProvider, error) {
	c, err := newClient(apiKey, baseURL)
	if err != nil {
		return nil, err
	}
	return &speech{client: c}, nil
}

func (p *speech) ListModels(ctx context.Context) ([]*sdk.SpeechModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []*sdk.SpeechModel{{ID: DefaultSpeechModel, Provider: p}, {ID: "s2.1-pro-free", Provider: p}, {ID: "drama-3", Provider: p}}, nil
}

func (p *speech) DoSynthesize(ctx context.Context, params sdk.SpeechParams) (*sdk.SpeechResult, error) {
	body, model, contentType, err := speechRequest(ctx, params)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.post(ctx, "/tts", model, "application/json", bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	audio, err := readBounded(resp.Body, maxSpeechBytes)
	if err != nil {
		return nil, err
	}
	return &sdk.SpeechResult{Audio: audio, ContentType: contentType}, nil
}

func (p *speech) DoStream(ctx context.Context, params sdk.SpeechParams) (*sdk.SpeechStreamResult, error) {
	body, model, contentType, err := speechRequest(ctx, params)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.post(ctx, "/tts", model, "application/json", bytes.NewReader(body), int64(len(body))) //nolint:bodyclose // Ownership passes to the goroutine, which closes on every exit.
	if err != nil {
		return nil, err
	}
	chunks := make(chan []byte)
	errCh := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errCh)
		defer func() { _ = resp.Body.Close() }()
		buf := make([]byte, 32*1024)
		var total int
		for {
			n, readErr := resp.Body.Read(buf)
			if err := ctx.Err(); err != nil {
				errCh <- &requestError{cause: err}
				return
			}
			total += n
			if total > maxSpeechBytes {
				errCh <- errors.New("fish audio response exceeds size limit")
				return
			}
			if n > 0 {
				chunk := bytes.Clone(buf[:n])
				select {
				case chunks <- chunk:
				case <-ctx.Done():
					errCh <- &requestError{cause: ctx.Err()}
					return
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					errCh <- &requestError{cause: readErr}
				}
				return
			}
		}
	}()
	return sdk.NewSpeechStreamResult(chunks, contentType, errCh), nil
}

func speechRequest(ctx context.Context, params sdk.SpeechParams) ([]byte, string, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}
	if strings.TrimSpace(params.Text) == "" {
		return nil, "", "", errors.New("fish audio requires non-empty text")
	}
	model := DefaultSpeechModel
	if params.Model != nil && params.Model.ID != "" {
		model = params.Model.ID
	}
	switch model {
	case DefaultSpeechModel, "s2.1-pro-free", "drama-3":
	default:
		return nil, "", "", errors.New("unsupported fish audio speech model")
	}
	format, err := configString(params.Config, "format")
	if err != nil {
		return nil, "", "", err
	}
	if format == "" {
		format = "mp3"
	}
	var contentType string
	switch format {
	case "mp3":
		contentType = "audio/mpeg"
	case "wav":
		contentType = "audio/wav"
	case "pcm":
		contentType = "audio/pcm"
	case "opus":
		contentType = "audio/ogg"
	default:
		return nil, "", "", errors.New("unsupported fish audio format")
	}
	body := map[string]any{"text": params.Text, "format": format}
	for _, key := range []string{"reference_id", "language"} {
		v, err := configString(params.Config, key)
		if err != nil {
			return nil, "", "", err
		}
		if key == "language" {
			if len(v) > 35 {
				return nil, "", "", errors.New("language must contain at most 35 letters, digits, hyphens or underscores")
			}
			for _, c := range v {
				if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
					return nil, "", "", errors.New("language must contain at most 35 letters, digits, hyphens or underscores")
				}
			}
		}
		if v != "" {
			body[key] = v
		}
	}
	prosody := map[string]any{}
	for _, field := range []struct {
		key      string
		min, max float64
	}{{"speed", 0.5, 2}, {"volume", -20, 20}} {
		if _, ok := params.Config[field.key]; !ok {
			continue
		}
		n, err := configNumber(params.Config, field.key, field.min, field.max, false)
		if err != nil {
			return nil, "", "", err
		}
		prosody[field.key] = n
	}
	if len(prosody) > 0 {
		body["prosody"] = prosody
	}
	if _, ok := params.Config["sample_rate"]; ok {
		n, err := configNumber(params.Config, "sample_rate", 1, 384000, true)
		if err != nil {
			return nil, "", "", err
		}
		body["sample_rate"] = n
	}
	if value, ok := params.Config["normalize"]; ok {
		flag, err := configBool(value, "normalize")
		if err != nil {
			return nil, "", "", err
		}
		body["normalize"] = flag
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, "", "", &requestError{cause: err}
	}
	return data, model, contentType, nil
}
