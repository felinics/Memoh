package fishaudio_test

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/audio/adapter/fishaudio"
)

func TestTranscribeRequestAndAnnotatedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/asr" || r.Header.Get("model") != "transcribe-1-pro" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		want := map[string][]string{"language": {"zh"}, "ignore_timestamps": {"false"}, "tag_audio_events": {"true"}, "diarize": {"auto"}, "num_speakers": {"2"}}
		if !reflect.DeepEqual(r.MultipartForm.Value, want) {
			t.Errorf("form = %#v", r.MultipartForm.Value)
		}
		file, header, err := r.FormFile("audio")
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = file.Close() }()
		data, _ := io.ReadAll(file)
		if header.Filename != "conversation.webm" || string(data) != "original audio" || len(r.MultipartForm.File) != 1 {
			t.Errorf("audio upload = %q, %q", header.Filename, data)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":" <|speaker:0|> 你好。 [laughter] ","duration":6.4,"language_code":"zh","language":"Chinese","segments":[{"text":"你","start":0.32,"end":0.56}],"request_id":"req-1","speaker_turns":[{"speaker":"speaker:0","text":"你好。 [laughter]","start":0.32,"end":0.88}]}`)
	}))
	defer srv.Close()
	p, err := fishaudio.NewTranscription("secret", srv.URL+"/v1")
	if err != nil {
		t.Fatal(err)
	}
	var _ sdk.TranscriptionProvider = p
	out, err := p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("original audio"), Filename: "conversation.webm", Config: map[string]any{"language": "zh", "ignore_timestamps": false, "tag_audio_events": true, "diarize": "auto", "num_speakers": 2.0, "api_key": "do-not-forward", "base_url": "do-not-forward"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != " <|speaker:0|> 你好。 [laughter] " || out.Language != "zh" || out.DurationSeconds != 6.4 || !reflect.DeepEqual(out.Words, []sdk.TranscriptionWord{{Text: "你", Start: 0.32, End: 0.56}}) {
		t.Fatalf("result = %#v", out)
	}
	metadata := partmeta.Unfold(out.ProviderMetadata)
	wantTurns := []any{map[string]any{"speaker": "speaker:0", "text": "你好。 [laughter]", "start": 0.32, "end": 0.88}}
	if metadata["request_id"] != "req-1" || !reflect.DeepEqual(metadata["speaker_turns"], wantTurns) {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestTranscriptionResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body, text, language string
		valid                      bool
	}{
		{"silence", `{"text":"","duration":0,"segments":[]}`, "", "", true},
		{"language-fallback", `{"text":"hello","language":"English"}`, "hello", "English", true},
		{"missing", `{"duration":3}`, "", "", false},
		{"null", `{"text":null}`, "", "", false},
		{"wrong-type", `{"text":123}`, "", "", false},
		{"malformed", `{"text":"credential-leak"`, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer srv.Close()
			p, err := fishaudio.NewTranscription("key", srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			out, err := p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio")})
			if tc.valid {
				if err != nil || out.Text != tc.text || out.Language != tc.language {
					t.Fatalf("result = %#v, %v", out, err)
				}
			} else if err == nil || strings.Contains(err.Error(), "credential-leak") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTranscriptionStrictValidation(t *testing.T) {
	p, err := fishaudio.NewTranscription("key", "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		config map[string]any
	}{
		{"language-type", map[string]any{"language": false}},
		{"language-shape", map[string]any{"language": "en-US"}},
		{"timestamps", map[string]any{"ignore_timestamps": 1}},
		{"events", map[string]any{"tag_audio_events": "yes"}},
		{"diarize", map[string]any{"diarize": "AUTO"}},
		{"diarize-type", map[string]any{"diarize": 1}},
		{"count-fractional", map[string]any{"num_speakers": 1.5}},
		{"count-zero", map[string]any{"min_speakers": 0}},
		{"count-nan", map[string]any{"max_speakers": math.NaN()}},
		{"count-type", map[string]any{"num_speakers": "2"}},
		{"counts-disabled", map[string]any{"diarize": false, "num_speakers": 2}},
		{"counts-combined", map[string]any{"num_speakers": 2, "min_speakers": 1}},
		{"count-bounds", map[string]any{"min_speakers": 3, "max_speakers": 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio"), Config: tc.config})
			if err == nil || strings.Contains(err.Error(), "request failed") {
				t.Fatalf("expected local validation error, got %v", err)
			}
		})
	}
	for _, params := range []sdk.TranscriptionParams{
		{},
		{Audio: []byte("audio"), Model: &sdk.TranscriptionModel{ID: "Transcribe-1-Pro"}},
		{Audio: []byte("audio"), Model: &sdk.TranscriptionModel{ID: "unknown"}},
	} {
		_, err := p.DoTranscribe(t.Context(), params)
		if err == nil || strings.Contains(err.Error(), "request failed") {
			t.Fatalf("expected local validation error, got %v", err)
		}
	}
}

func TestTranscriptionModelsAndBooleanEnums(t *testing.T) {
	for _, model := range []string{"transcribe-1-pro", "transcribe-1"} {
		t.Run(model, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("model") != model {
					t.Errorf("model = %q", r.Header.Get("model"))
				}
				if err := r.ParseMultipartForm(1024); err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = r.MultipartForm.RemoveAll() }()
				if r.FormValue("ignore_timestamps") != "false" {
					t.Error("timestamps missing")
				}
				if model == "transcribe-1-pro" && (r.FormValue("tag_audio_events") != "false" || r.FormValue("diarize") != "true" || r.FormValue("min_speakers") != "1" || r.FormValue("max_speakers") != "3") {
					t.Errorf("unexpected form %#v", r.MultipartForm.Value)
				}
				_, _ = io.WriteString(w, `{"text":"hello"}`)
			}))
			defer srv.Close()
			p, err := fishaudio.NewTranscription("key", srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			config := map[string]any{"ignore_timestamps": "false"}
			if model == "transcribe-1-pro" {
				config["tag_audio_events"] = "false"
				config["diarize"] = true
				config["min_speakers"] = 1
				config["max_speakers"] = 3
			}
			_, err = p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio"), Model: &sdk.TranscriptionModel{ID: model}, Config: config})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	p, err := fishaudio.NewTranscription("key", "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"tag_audio_events": false, "diarize": "auto", "num_speakers": 2, "min_speakers": 1, "max_speakers": 3} {
		t.Run("base-model-rejects-"+key, func(t *testing.T) {
			_, err := p.DoTranscribe(t.Context(), sdk.TranscriptionParams{Audio: []byte("audio"), Model: &sdk.TranscriptionModel{ID: "transcribe-1"}, Config: map[string]any{key: value}})
			if err == nil || strings.Contains(err.Error(), "request failed") {
				t.Fatalf("expected local validation error, got %v", err)
			}
		})
	}
}

func TestTranscriptionInFlightCancellation(t *testing.T) {
	started := make(chan struct{})
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(closed)
	}))
	defer srv.Close()
	p, err := fishaudio.NewTranscription("key", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := p.DoTranscribe(ctx, sdk.TranscriptionParams{Audio: []byte("audio")})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transcription did not cancel")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("ASR request not closed")
	}
}
