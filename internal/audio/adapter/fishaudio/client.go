// Package fishaudio implements Fish Audio's HTTP speech and transcription APIs.
package fishaudio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/errs"
)

const (
	DefaultBaseURL            = "https://api.fish.audio/v1"
	DefaultSpeechModel        = "s2.1-pro"
	DefaultTranscriptionModel = "transcribe-1-pro"
	maxSpeechBytes            = 64 * 1024 * 1024
	maxResponseBytes          = 32 * 1024 * 1024
)

type client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// Keep transport diagnostics private even when a handler uses Error().
type requestError struct{ cause error }

func (*requestError) Error() string   { return "fish audio request failed" }
func (e *requestError) Unwrap() error { return e.cause }

func newClient(apiKey, baseURL string) (*client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("fish audio requires an API key")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("fish audio base URL must be an HTTP(S) API base URL")
	}
	return &client{apiKey: apiKey, baseURL: baseURL, http: &http.Client{
		Timeout:       15 * time.Minute,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *client) post(ctx context.Context, path, model, contentType string, body io.Reader, contentLength int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, body)
	if err != nil {
		return nil, &requestError{cause: err}
	}
	req.ContentLength = contentLength
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("model", model)
	req.Header.Set("Content-Type", contentType)
	// #nosec G704 -- The endpoint is administrator-configured; redirects are disabled.
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &requestError{cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		// Error bodies can echo credentials or submitted text/audio. Never expose them.
		if resp.StatusCode == http.StatusPaymentRequired {
			return nil, errs.WrapDependency(&sdk.APIError{
				Provider: "fishaudio", StatusCode: resp.StatusCode, Kind: sdk.KindQuotaExhausted,
			}, "fish audio request")
		}
		return nil, fmt.Errorf("fish audio returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func readBounded(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, &requestError{cause: err}
	}
	if int64(len(data)) > limit {
		return nil, errors.New("fish audio response exceeds size limit")
	}
	return data, nil
}

func configString(config map[string]any, key string) (string, error) {
	v, ok := config[key]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return strings.TrimSpace(s), nil
}

func configNumber(config map[string]any, key string, minimum, maximum float64, integer bool) (float64, error) {
	var n float64
	switch v := config[key].(type) {
	case float64:
		n = v
	case float32:
		n = float64(v)
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case int32:
		n = float64(v)
	default:
		return 0, fmt.Errorf("%s must be a number", key)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < minimum || n > maximum || (integer && math.Trunc(n) != n) {
		return 0, fmt.Errorf("%s is outside its supported range", key)
	}
	return n, nil
}

// Config enums arrive as strings from the settings UI; API callers may use booleans.
func configBool(value any, key string) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		if v == "true" {
			return true, nil
		}
		if v == "false" {
			return false, nil
		}
	}
	return false, fmt.Errorf("%s must be true or false", key)
}
