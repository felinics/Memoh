package alibabacloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/stretchr/testify/require"

	"github.com/felinics/memoh/internal/audio/adapter"
)

func TestTranscribeWireContract(t *testing.T) {
	for _, tc := range []struct{ filename, contentType, wantMIME string }{
		{"sample.wav", "audio/wav; charset=binary", "audio/wav"},
		{"sample.M4A", "application/octet-stream", "audio/mp4"},
		{"sample.mp3", "", "audio/mpeg"},
	} {
		t.Run(tc.filename, func(t *testing.T) {
			var request struct {
				Model    string `json:"model"`
				Stream   bool   `json:"stream"`
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
				Options map[string]any `json:"asr_options"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/compatible-mode/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("unexpected request method, path or authorization")
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"id":"request-1","model":"qwen3-asr-flash","choices":[{"message":{"content":"明天上午9点开会。","annotations":[{"type":"audio_info","language":"zh"}]}}],"usage":{"seconds":5.39}}`)
			}))
			defer server.Close()
			provider, err := New("test-key", server.URL+"/compatible-mode/v1/")
			require.NoError(t, err)
			result, err := sdk.Transcribe(t.Context(), sdk.WithTranscriptionModel(&sdk.TranscriptionModel{ID: DefaultModel, Provider: provider}),
				sdk.WithAudio([]byte("fixture"), tc.filename, tc.contentType), sdk.WithTranscriptionConfig(map[string]any{"language": "zh", "enable_itn": true, "context": "会议", "api_key": "must-not-be-forwarded"}))
			require.NoError(t, err)
			require.Equal(t, "明天上午9点开会。", result.Text)
			require.Equal(t, "zh", result.Language)
			require.Equal(t, 5.39, result.DurationSeconds)
			require.Equal(t, "request-1", result.ProviderMetadata["id"])
			require.Equal(t, DefaultModel, request.Model)
			require.False(t, request.Stream)
			require.Equal(t, map[string]any{"language": "zh", "enable_itn": true}, request.Options)
			require.Len(t, request.Messages, 2)
			require.Equal(t, "system", request.Messages[0].Role)
			require.JSONEq(t, `"会议"`, string(request.Messages[0].Content))
			require.Equal(t, "user", request.Messages[1].Role)
			var content []struct {
				Type       string `json:"type"`
				InputAudio struct {
					Data string `json:"data"`
				} `json:"input_audio"`
			}
			require.NoError(t, json.Unmarshal(request.Messages[1].Content, &content))
			require.Len(t, content, 1)
			require.Equal(t, "input_audio", content[0].Type)
			require.Equal(t, "data:"+tc.wantMIME+";base64,"+base64.StdEncoding.EncodeToString([]byte("fixture")), content[0].InputAudio.Data)
		})
	}
}

func TestTranscribeFailureAndSilence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body, wantErr string
		wantKind      error
	}{
		{"bad request", 400, `private-audio`, "HTTP 400", adapter.ErrRequestRejected},
		{"unauthorized", 401, `{"message":"secret-key private-audio"}`, "HTTP 401", adapter.ErrRequestRejected},
		{"forbidden", 403, `private-audio`, "HTTP 403", adapter.ErrRequestRejected},
		{"not found", 404, `private-audio`, "HTTP 404", adapter.ErrRequestRejected},
		{"rate limited", 429, `busy`, "HTTP 429", adapter.ErrRateLimited},
		{"server error", 500, `private-audio`, "HTTP 500", adapter.ErrUnavailable},
		{"unavailable", 503, `private-audio`, "HTTP 503", adapter.ErrUnavailable},
		{"redirect", 307, `redirect`, "HTTP 307", adapter.ErrRequestRejected},
		{"invalid JSON", 200, `<html>private-audio</html>`, "response JSON", nil},
		{"no choices", 200, `{"choices":[]}`, "no transcription choice", nil},
		{"null content", 200, `{"choices":[{"message":{"content":null}}]}`, "no transcription content", nil},
		{"wrong content", 200, `{"choices":[{"message":{"content":42}}]}`, "must be text", nil},
		{"silence", 200, `{"choices":[{"message":{"content":""}}]}`, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status); _, _ = fmt.Fprint(w, tc.body) }))
			defer server.Close()
			p, err := New("secret-key", server.URL)
			require.NoError(t, err)
			result, err := p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("private-audio"), Filename: "test.wav"})
			if tc.wantErr == "" {
				require.NoError(t, err)
				require.Empty(t, result.Text)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
			if tc.wantKind != nil {
				require.ErrorIs(t, err, tc.wantKind)
			}
			require.NotContains(t, err.Error(), "secret-key")
			require.NotContains(t, err.Error(), "private-audio")
		})
	}
}

func TestTranscribeCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { close(started); <-release }))
	defer server.Close()
	defer close(release)
	p, err := New("test-key", server.URL)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.DoTranscribe(ctx, sdk.TranscriptionParams{Audio: []byte("audio")}); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not abort request")
	}
}

func TestValidation(t *testing.T) {
	_, err := New("", "")
	require.ErrorContains(t, err, "API key")
	for _, endpoint := range []string{"wss://dashscope.aliyuncs.com", "https://user:pass@example.com", "https://example.com?key=secret"} {
		_, err := New("test-key", endpoint)
		require.Error(t, err)
	}
	p, err := New("test-key", "")
	require.NoError(t, err)
	for _, audio := range [][]byte{nil, make([]byte, maxAudioBytes+1)} {
		_, err := p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: audio})
		require.ErrorContains(t, err, "audio file")
	}
}

func TestInvalidOptionsNeverSendAudio(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("invalid options must not send audio upstream")
	}))
	defer server.Close()
	p, err := New("test-key", server.URL)
	require.NoError(t, err)
	for _, key := range []string{"language", "context", "enable_itn"} {
		t.Run(key, func(t *testing.T) {
			_, err := p.DoTranscribe(t.Context(), sdk.TranscriptionParams{
				Audio: []byte("audio"), Config: map[string]any{key: 123},
			})
			require.ErrorIs(t, err, adapter.ErrInvalidInput)
		})
	}
}

func TestTranscriptionDefaultsAndExplicitFalse(t *testing.T) {
	t.Parallel()
	for _, config := range []map[string]any{nil, {"language": "  ", "context": nil, "enable_itn": false}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model    string         `json:"model"`
				Messages []asrMessage   `json:"messages"`
				Options  map[string]any `json:"asr_options"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			if body.Model != "qwen3-asr-flash" || len(body.Messages) != 1 || r.Header.Get("Content-Type") != "application/json" {
				t.Error("unexpected default request")
			}
			if config == nil && len(body.Options) != 0 {
				t.Error("missing options must stay omitted")
			}
			if config != nil && (len(body.Options) != 1 || body.Options["enable_itn"] != false) {
				t.Error("explicit false must be preserved without empty language")
			}
			_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"hello"}}]}`)
		}))
		p, err := New("test-key", server.URL)
		require.NoError(t, err)
		_, err = p.DoTranscribe(t.Context(), sdk.TranscriptionParams{
			Model: &sdk.TranscriptionModel{ID: " qwen3-asr-flash "}, Audio: []byte("audio"), Config: config,
		})
		server.Close()
		require.NoError(t, err)
	}
}

func TestResponseSizeLimit(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	defer server.Close()
	p, err := New("key", server.URL)
	require.NoError(t, err)
	_, err = p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio")})
	require.ErrorContains(t, err, "response exceeds size limit")
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not follow redirects with audio or credentials")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	p, err := New("key", source.URL)
	require.NoError(t, err)
	_, err = p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio")})
	require.ErrorIs(t, err, adapter.ErrRequestRejected)
}

func TestTranscriptionTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	p, err := New("key", server.URL)
	require.NoError(t, err)
	p.client.Timeout = 50 * time.Millisecond
	_, err = p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio")})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, adapter.ErrUnavailable)
}
