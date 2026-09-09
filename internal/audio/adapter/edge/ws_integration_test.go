//go:build integration

package edge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/audio"
)

// Real Edge TTS integration tests. Not compiled by default (requires -tags=integration).
// Requires network access to speech.platform.bing.com.
//
// Run:
//
//	go test -tags=integration ./internal/audio/adapter/edge/... -run TestRealEdgeTTS -v

func TestRealEdgeTTS_Synthesize(t *testing.T) {
	client := NewEdgeWsClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	config := audio.AudioConfig{Voice: audio.VoiceConfig{ID: "en-US-JennyNeural", Lang: "en-US"}, Speed: 1.0}
	audio, err := client.Synthesize(ctx, "Hello, this is a real Edge TTS test.", config)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(audio) == 0 {
		t.Fatal("expected non-empty audio from real Edge TTS")
	}
	t.Logf("got %d bytes of audio", len(audio))
}

func TestRealEdgeTTS_Stream(t *testing.T) {
	client := NewEdgeWsClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	config := audio.AudioConfig{Voice: audio.VoiceConfig{ID: "zh-CN-XiaoxiaoNeural", Lang: "zh-CN"}}
	ch, errCh := client.Stream(ctx, "你好，这是流式测试。", config)
	var total int
	for b := range ch {
		total += len(b)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if total == 0 {
		t.Fatal("expected non-empty audio stream")
	}
	t.Logf("streamed %d bytes total", total)
}

// TestRealEdgeTTS_Formats tries every candidate format and reports which ones are supported.
//
//	go test -tags=integration ./internal/audio/adapter/edge/... -run TestRealEdgeTTS_Formats -v
func TestRealEdgeTTS_Formats(t *testing.T) {
	formats := []string{
		"audio-24khz-48kbitrate-mono-mp3",
		"audio-24khz-96kbitrate-mono-mp3",
		"webm-24khz-16bit-mono-opus",
	}

	for _, fmt := range formats {
		t.Run(fmt, func(t *testing.T) {
			client := NewEdgeWsClient()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			config := audio.AudioConfig{
				Voice:  audio.VoiceConfig{ID: "en-US-JennyNeural", Lang: "en-US"},
				Format: fmt,
				Speed:  1.0,
			}
			audio, err := client.Synthesize(ctx, "Hello, format test.", config)
			if err != nil {
				t.Errorf("UNSUPPORTED format %q: %v", fmt, err)
				return
			}
			t.Logf("OK format %q -> %d bytes", fmt, len(audio))
		})
	}
}

// TestRealEdgeTTS_SaveAudio synthesizes speech and writes the result to a file for manual inspection.
//
//	go test -tags=integration ./internal/audio/adapter/edge/... -run TestRealEdgeTTS_SaveAudio -v
func TestRealEdgeTTS_SaveAudio(t *testing.T) {
	client := NewEdgeWsClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cases := []struct {
		name  string
		text  string
		voice audio.VoiceConfig
		file  string
	}{
		{"en", "Hello, this is an Edge TTS audio save test.", audio.VoiceConfig{ID: "en-US-JennyNeural", Lang: "en-US"}, "test_en.mp3"},
		{"zh", "你好，这是一段中文语音合成测试。", audio.VoiceConfig{ID: "zh-CN-XiaoxiaoNeural", Lang: "zh-CN"}, "test_zh.mp3"},
	}

	outDir := filepath.Join(os.TempDir(), "edge_tts_test")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", outDir, err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := audio.AudioConfig{Voice: tc.voice, Speed: 1.0, Pitch: -10.0}
			audio, err := client.Synthesize(ctx, tc.text, config)
			if err != nil {
				t.Fatalf("Synthesize: %v", err)
			}
			if len(audio) == 0 {
				t.Fatal("expected non-empty audio")
			}

			outPath := filepath.Join(outDir, tc.file)
			if err := os.WriteFile(outPath, audio, 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}
			t.Logf("saved %d bytes -> %s", len(audio), outPath)
		})
	}
}
