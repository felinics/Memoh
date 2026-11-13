package fishaudio_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/audio/adapter/fishaudio"
)

func TestSynthesizeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tts" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("model") != "s2.1-pro" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		want := map[string]any{"text": " [happy] Hello ", "format": "mp3", "reference_id": "voice-id", "prosody": map[string]any{"speed": 1.2, "volume": -3.0}, "sample_rate": 44100.0, "language": "en-US", "normalize": false}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("body = %#v, want %#v", body, want)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "audio bytes")
	}))
	defer srv.Close()
	p, err := fishaudio.NewSpeech("secret", srv.URL+"/v1/")
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.DoSynthesize(t.Context(), sdk.SpeechParams{Text: " [happy] Hello ", Config: map[string]any{"reference_id": "voice-id", "speed": 1.2, "volume": -3, "sample_rate": 44100, "language": "en-US", "normalize": false, "api_key": "do-not-forward", "base_url": "do-not-forward"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Audio) != "audio bytes" || result.ContentType != "audio/mpeg" {
		t.Fatalf("result = %#v", result)
	}
}

func BenchmarkSynthesize(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{{"1KiB", 1024}, {"4MiB", 4 * 1024 * 1024}, {"64MiB", 64 * 1024 * 1024}} {
		b.Run(size.name, func(b *testing.B) {
			audio := make([]byte, size.bytes)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.Copy(w, bytes.NewReader(audio))
			}))
			defer srv.Close()
			p, err := fishaudio.NewSpeech("key", srv.URL)
			if err != nil {
				b.Fatal(err)
			}
			params := sdk.SpeechParams{Text: "hello"}
			if _, err := p.DoSynthesize(b.Context(), params); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(size.bytes))
			b.ResetTimer()
			for range b.N {
				result, err := p.DoSynthesize(b.Context(), params)
				if err != nil {
					b.Fatal(err)
				}
				if len(result.Audio) != size.bytes {
					b.Fatalf("audio length = %d, want %d", len(result.Audio), size.bytes)
				}
			}
		})
	}
}

func TestSpeechStreamsBeforeResponseCompletes(t *testing.T) {
	first := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		close(first)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "second")
	}))
	defer srv.Close()
	p, err := fishaudio.NewSpeech("key", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := p.DoStream(t.Context(), sdk.SpeechParams{Text: "hello", Model: &sdk.SpeechModel{ID: "drama-3"}, Config: map[string]any{"format": "opus"}})
	if err != nil {
		t.Fatal(err)
	}
	<-first
	select {
	case chunk := <-stream.Stream:
		if string(chunk) != "first" {
			t.Errorf("first chunk = %q", chunk)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("stream buffered until response completed")
	}
	close(release)
	data, err := stream.Bytes()
	if err != nil || string(data) != "second" || stream.ContentType != "audio/ogg" {
		t.Fatalf("remaining = %q, %v, %s", data, err, stream.ContentType)
	}
}

func TestSpeechStreamReadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "truncated")
	}))
	defer srv.Close()
	p, err := fishaudio.NewSpeech("key", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := p.DoStream(t.Context(), sdk.SpeechParams{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := stream.Bytes()
	if string(data) != "truncated" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("bytes = %q, err = %v", data, err)
	}
}

func TestSpeechCancellationClosesResponse(t *testing.T) {
	for _, mode := range []string{"synthesize", "stream"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			closed := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "first")
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
				close(closed)
			}))
			defer srv.Close()
			p, err := fishaudio.NewSpeech("key", srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				if mode == "synthesize" {
					_, err := p.DoSynthesize(ctx, sdk.SpeechParams{Text: "hello"})
					result <- err
					return
				}
				stream, err := p.DoStream(ctx, sdk.SpeechParams{Text: "hello"})
				if err == nil {
					_, err = stream.Bytes()
				}
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("speech request not started")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("speech request did not cancel")
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("speech response not closed")
			}
		})
	}
}

func TestSpeechModelAndFormatCatalog(t *testing.T) {
	for _, model := range []string{"s2.1-pro", "s2.1-pro-free", "drama-3"} {
		for format, mime := range map[string]string{"mp3": "audio/mpeg", "wav": "audio/wav", "pcm": "audio/pcm", "opus": "audio/ogg"} {
			t.Run(model+"/"+format, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("model") != model {
						t.Errorf("model header = %s", r.Header.Get("model"))
					}
					_, _ = io.WriteString(w, "audio")
				}))
				defer srv.Close()
				p, err := fishaudio.NewSpeech("key", srv.URL)
				if err != nil {
					t.Fatal(err)
				}
				out, err := p.DoSynthesize(t.Context(), sdk.SpeechParams{Model: &sdk.SpeechModel{ID: model}, Text: "hello", Config: map[string]any{"format": format}})
				if err != nil || out.ContentType != mime {
					t.Fatalf("output = %#v, %v", out, err)
				}
			})
		}
	}
}

func TestSpeechStrictValidation(t *testing.T) {
	p, err := fishaudio.NewSpeech("key", "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		params sdk.SpeechParams
	}{
		{"empty", sdk.SpeechParams{Text: " \n"}},
		{"unknown-model", sdk.SpeechParams{Text: "hello", Model: &sdk.SpeechModel{ID: "s2-pro"}}},
		{"model-case", sdk.SpeechParams{Text: "hello", Model: &sdk.SpeechModel{ID: "S2.1-Pro"}}},
		{"format", sdk.SpeechParams{Text: "hello", Config: map[string]any{"format": "flac"}}},
		{"voice", sdk.SpeechParams{Text: "hello", Config: map[string]any{"reference_id": map[string]any{"id": "wrong"}}}},
		{"speed", sdk.SpeechParams{Text: "hello", Config: map[string]any{"speed": "fast"}}},
		{"speed-range", sdk.SpeechParams{Text: "hello", Config: map[string]any{"speed": 2.1}}},
		{"volume", sdk.SpeechParams{Text: "hello", Config: map[string]any{"volume": 21}}},
		{"rate", sdk.SpeechParams{Text: "hello", Config: map[string]any{"sample_rate": 1.5}}},
		{"normalize", sdk.SpeechParams{Text: "hello", Config: map[string]any{"normalize": "yes"}}},
		{"language-type", sdk.SpeechParams{Text: "hello", Config: map[string]any{"language": false}}},
		{"language-shape", sdk.SpeechParams{Text: "hello", Config: map[string]any{"language": "en US"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.DoSynthesize(t.Context(), tc.params)
			if err == nil || strings.Contains(err.Error(), "request failed") {
				t.Fatalf("expected local validation error, got %v", err)
			}
		})
	}
}

func TestSpeechBooleanEnumConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["normalize"] != false {
			t.Errorf("normalize = %#v", body["normalize"])
		}
		_, _ = io.WriteString(w, "audio")
	}))
	defer srv.Close()
	p, err := fishaudio.NewSpeech("key", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.DoSynthesize(t.Context(), sdk.SpeechParams{Text: "hello", Config: map[string]any{"normalize": "false"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSpeechCancellationWithoutConsumingClosesStream(t *testing.T) {
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer srv.Close()
	p, err := fishaudio.NewSpeech("key", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := p.DoStream(ctx, sdk.SpeechParams{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	// Err must complete even when the caller has never received a chunk.
	result := make(chan error, 1)
	go func() { result <- stream.Err() }()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream blocked sending to an abandoned consumer")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("response not closed")
	}
}
