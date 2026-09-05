package application

import (
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestCompactionClientOutlivesColdSummarizerTTFB(t *testing.T) {
	t.Parallel()

	svc := NewService(slog.New(slog.DiscardHandler), nil, nil, nil, nil, nil, nil, nil, 0)

	if svc.compactionHTTPClient == nil {
		t.Fatal("compaction HTTP client not wired")
	}
	transport, ok := svc.compactionHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("compaction client transport = %T, want *http.Transport", svc.compactionHTTPClient.Transport)
	}
	if transport.ResponseHeaderTimeout < 2*time.Minute {
		t.Fatalf("compaction ResponseHeaderTimeout = %v, want >= 2m (cold summarizer TTFB measured 60-93s)", transport.ResponseHeaderTimeout)
	}

	nonStreaming, ok := svc.nonStreamingHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("non-streaming transport = %T, want *http.Transport", svc.nonStreamingHTTPClient.Transport)
	}
	if nonStreaming.ResponseHeaderTimeout != 30*time.Second {
		t.Fatalf("non-streaming ResponseHeaderTimeout = %v, want unchanged 30s", nonStreaming.ResponseHeaderTimeout)
	}
	if svc.compactionHTTPClient == svc.nonStreamingHTTPClient {
		t.Fatal("compaction must not share the interactive non-streaming client")
	}
}
