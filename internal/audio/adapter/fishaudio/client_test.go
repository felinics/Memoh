package fishaudio_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/audio/adapter/fishaudio"
)

func TestConstructorsAndCatalogs(t *testing.T) {
	for _, base := range []string{"ftp://host", "https://user:password@host", "https://host?key=secret", "https://host?", "https://host#fragment", "/relative", "https://", "http://%zz"} {
		if _, err := fishaudio.NewSpeech("key", base); err == nil {
			t.Errorf("speech accepted %q", base)
		}
		if _, err := fishaudio.NewTranscription("key", base); err == nil {
			t.Errorf("transcription accepted %q", base)
		}
	}
	if _, err := fishaudio.NewSpeech(" ", ""); err == nil {
		t.Error("empty speech key accepted")
	}
	if _, err := fishaudio.NewTranscription("", ""); err == nil {
		t.Error("empty ASR key accepted")
	}
	speech, err := fishaudio.NewSpeech("key", "")
	if err != nil {
		t.Fatal(err)
	}
	asr, err := fishaudio.NewTranscription("key", "")
	if err != nil {
		t.Fatal(err)
	}
	models, err := speech.ListModels(t.Context())
	if err != nil || len(models) != 3 {
		t.Fatalf("speech catalog = %#v, %v", models, err)
	}
	for i, id := range []string{"s2.1-pro", "s2.1-pro-free", "drama-3"} {
		if models[i].ID != id || models[i].Provider != speech {
			t.Errorf("speech model = %#v", models[i])
		}
	}
	transcriptions, err := asr.ListModels(t.Context())
	if err != nil || len(transcriptions) != 2 {
		t.Fatalf("ASR catalog = %#v, %v", transcriptions, err)
	}
	for i, id := range []string{"transcribe-1-pro", "transcribe-1"} {
		if transcriptions[i].ID != id || transcriptions[i].Provider != asr {
			t.Errorf("ASR model = %#v", transcriptions[i])
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := speech.ListModels(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("catalog cancellation = %v", err)
	}
	if _, err := asr.ListModels(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ASR catalog cancellation = %v", err)
	}
	if _, err := speech.DoSynthesize(ctx, sdk.SpeechParams{Text: "hello"}); !errors.Is(err, context.Canceled) {
		t.Errorf("speech cancellation = %v", err)
	}
	if _, err := speech.DoStream(ctx, sdk.SpeechParams{Text: "hello"}); !errors.Is(err, context.Canceled) {
		t.Errorf("stream cancellation = %v", err)
	}
	if _, err := asr.DoTranscribe(ctx, sdk.TranscriptionParams{Audio: []byte("audio")}); !errors.Is(err, context.Canceled) {
		t.Errorf("ASR cancellation = %v", err)
	}
}

func TestHTTPFailurePrivacyAndRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); _, _ = io.WriteString(w, "audio") }))
	defer target.Close()
	for _, status := range []int{302, 307, 400, 401, 402, 429, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "credential-leak submitted-text audio-data")
		}))
		speech, err := fishaudio.NewSpeech("credential-leak", srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		asr, err := fishaudio.NewTranscription("credential-leak", srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, synthErr := speech.DoSynthesize(t.Context(), sdk.SpeechParams{Text: "submitted-text"})
		_, streamErr := speech.DoStream(t.Context(), sdk.SpeechParams{Text: "submitted-text"})
		_, asrErr := asr.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio-data")})
		for _, err := range []error{synthErr, streamErr, asrErr} {
			if err == nil {
				t.Error("HTTP failure accepted")
				continue
			}
			var apiErr *sdk.APIError
			isAPIError := errors.As(err, &apiErr)
			if status == http.StatusPaymentRequired {
				if !isAPIError || apiErr.StatusCode != status || apiErr.Kind != sdk.KindQuotaExhausted {
					t.Errorf("quota error = %v, want typed quota exhaustion", err)
				} else if apiErr.Message != "" || len(apiErr.Body) != 0 || len(apiErr.Header) != 0 {
					t.Error("quota error retained untrusted upstream content")
				}
			} else if isAPIError {
				t.Errorf("unrelated HTTP %d changed to an APIError", status)
			}
			for _, secret := range []string{"credential-leak", "submitted-text", "audio-data"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("private content in error = %v", err)
				}
			}
		}
		srv.Close()
	}
	if redirected.Load() != 0 {
		t.Fatalf("followed %d redirects", redirected.Load())
	}
}

func TestResponseSizeLimits(t *testing.T) {
	const mib = 1024 * 1024
	for _, tc := range []struct {
		name               string
		size               int
		transcribe, stream bool
	}{
		{"speech", 64*mib + 1, false, false},
		{"stream", 64*mib + 1, false, true},
		{"transcription", 32*mib + 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				chunk := make([]byte, 32*1024)
				for left := tc.size; left > 0; {
					n := min(left, len(chunk))
					if _, err := w.Write(chunk[:n]); err != nil {
						return
					}
					left -= n
				}
			}))
			defer srv.Close()
			var err error
			if tc.transcribe {
				p, newErr := fishaudio.NewTranscription("key", srv.URL)
				if newErr != nil {
					t.Fatal(newErr)
				}
				_, err = p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio")})
			} else {
				p, newErr := fishaudio.NewSpeech("key", srv.URL)
				if newErr != nil {
					t.Fatal(newErr)
				}
				if tc.stream {
					var result *sdk.SpeechStreamResult
					result, err = p.DoStream(t.Context(), sdk.SpeechParams{Text: "hello"})
					if err != nil {
						t.Fatal(err)
					}
					for range result.Stream {
					}
					err = result.Err()
				} else {
					_, err = p.DoSynthesize(t.Context(), sdk.SpeechParams{Text: "hello"})
				}
			}
			if err == nil || !strings.Contains(err.Error(), "size limit") {
				t.Fatalf("size error = %v", err)
			}
		})
	}
}

func TestNonStreamingReadFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = io.WriteString(w, "truncated")
	}))
	defer srv.Close()
	speech, err := fishaudio.NewSpeech("key", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	asr, err := fishaudio.NewTranscription("key", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, synthErr := speech.DoSynthesize(t.Context(), sdk.SpeechParams{Text: "hello"})
	_, asrErr := asr.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio")})
	if !errors.Is(synthErr, io.ErrUnexpectedEOF) || !errors.Is(asrErr, io.ErrUnexpectedEOF) {
		t.Fatalf("read failures = %v, %v", synthErr, asrErr)
	}
}
