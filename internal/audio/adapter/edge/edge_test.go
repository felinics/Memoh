package edge

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/audio"
)

func TestEdgeAdapter_TypeAndMeta(t *testing.T) {
	t.Parallel()
	adapter := NewEdgeAdapter(slog.Default())
	if adapter.Type() != TtsTypeEdge {
		t.Errorf("Type() = %q, want %q", adapter.Type(), TtsTypeEdge)
	}
	meta := adapter.Meta()
	if meta.Provider != "Microsoft Edge" {
		t.Errorf("Meta().Provider = %q, want %q", meta.Provider, "Microsoft Edge")
	}
	if meta.Description != "Microsoft Edge TTS" {
		t.Errorf("Meta().Description = %q, want %q", meta.Description, "Microsoft Edge TTS")
	}
}

func TestEdgeAdapter_Synthesize_WithMockServer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(mockEdgeTTSHandler(t))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/edge/v1"
	client := NewEdgeWsClient()
	client.BaseURL = wsURL
	adapter := NewEdgeAdapterWithClient(slog.Default(), client)

	ctx := context.Background()
	config := audio.AudioConfig{Voice: audio.VoiceConfig{ID: "en-US-JennyNeural", Lang: "en-US"}}
	audio, err := adapter.Synthesize(ctx, "Hello", edgeModelReadAloud, config)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(audio) == 0 {
		t.Fatal("expected non-empty audio")
	}
	if !bytes.Equal(audio, []byte("fake-webm-audio-data")) {
		t.Errorf("audio = %q", string(audio))
	}
}

func TestEdgeAdapter_Stream_WithMockServer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(mockEdgeTTSHandler(t))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/edge/v1"
	client := NewEdgeWsClient()
	client.BaseURL = wsURL
	adapter := NewEdgeAdapterWithClient(slog.Default(), client)

	ctx := context.Background()
	config := audio.AudioConfig{Voice: audio.VoiceConfig{ID: "en-US-JennyNeural", Lang: "en-US"}}
	ch, errCh := adapter.Stream(ctx, "Hi", edgeModelReadAloud, config)
	var chunks [][]byte
	for b := range ch {
		chunks = append(chunks, b)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Stream err: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !bytes.Equal(chunks[0], []byte("fake-webm-audio-data")) {
		t.Errorf("chunk = %q", chunks[0])
	}
}

func TestEdgeAdapter_Synthesize_NotConnected(t *testing.T) {
	t.Parallel()
	// use client without BaseURL and not Connect, Synthesize will try to connect real Edge address, here use invalid URL to trigger quick failure
	client := NewEdgeWsClient()
	client.BaseURL = "ws://127.0.0.1:0/edge/v1" // no service
	adapter := NewEdgeAdapterWithClient(slog.Default(), client)

	ctx := context.Background()
	_, err := adapter.Synthesize(ctx, "x", edgeModelReadAloud, audio.AudioConfig{})
	if err == nil {
		t.Fatal("expected error when connection fails")
	}
}

func TestEdgeAdapter_ResolveModel(t *testing.T) {
	t.Parallel()
	adapter := NewEdgeAdapter(slog.Default())

	got, err := adapter.ResolveModel("")
	if err != nil {
		t.Fatalf("ResolveModel default: %v", err)
	}
	if got != edgeModelReadAloud {
		t.Fatalf("ResolveModel default got %q, want %q", got, edgeModelReadAloud)
	}

	got, err = adapter.ResolveModel("EDGE-READ-ALOUD")
	if err != nil {
		t.Fatalf("ResolveModel case-insensitive: %v", err)
	}
	if got != edgeModelReadAloud {
		t.Fatalf("ResolveModel normalized got %q, want %q", got, edgeModelReadAloud)
	}

	if _, err := adapter.ResolveModel("unsupported"); err == nil {
		t.Fatal("expected unsupported model error")
	}
}
