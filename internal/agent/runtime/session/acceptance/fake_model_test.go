//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var acceptanceDirective = regexp.MustCompile(
	`\[acceptance:([a-zA-Z0-9_-]+)(?:\s+chunks=(\d+))?(?:\s+delay_ms=(\d+))?\]`,
)

type fakeModel struct {
	listener net.Listener
	server   *http.Server

	mu           sync.Mutex
	requestCount map[string]int
	disconnected map[string]int
	active       int
	maxActive    int
}

func startFakeModel() (*fakeModel, error) {
	listener, err := (&net.ListenConfig{}).Listen(
		context.Background(),
		"tcp4",
		"0.0.0.0:"+envOr(fakeModelPortEnv, "19090"),
	)
	if err != nil {
		return nil, err
	}
	model := &fakeModel{
		listener:     listener,
		requestCount: make(map[string]int),
		disconnected: make(map[string]int),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", model.handleHealth)
	mux.HandleFunc("/v1/models", model.handleModels)
	mux.HandleFunc("/v1/chat/completions", model.handleChatCompletions)
	model.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = model.server.Serve(listener)
	}()
	return model, nil
}

func (m *fakeModel) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = m.server.Shutdown(ctx)
}

func (m *fakeModel) ContainerBaseURL() string {
	_, port, _ := net.SplitHostPort(m.listener.Addr().String())
	return "http://host.docker.internal:" + port + "/v1"
}

func (m *fakeModel) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requestCount = make(map[string]int)
	m.disconnected = make(map[string]int)
	m.active = 0
	m.maxActive = 0
}

func (m *fakeModel) RequestCount(marker string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requestCount[marker]
}

func (m *fakeModel) MaxActive() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxActive
}

func (m *fakeModel) WaitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		active := m.active
		m.mu.Unlock()
		if active == 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (m *fakeModel) WaitRequestCount(marker string, minimum int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.RequestCount(marker) >= minimum {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (m *fakeModel) WaitDisconnected(marker string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		count := m.disconnected[marker]
		m.mu.Unlock()
		if count > 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (*fakeModel) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
}

func (*fakeModel) handleModels(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id":       "session-runtime-acceptance-model",
			"object":   "model",
			"created":  0,
			"owned_by": "session-runtime-acceptance",
		}},
	})
}

func (m *fakeModel) handleChatCompletions(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": err.Error()},
		})
		return
	}

	userText := latestUserText(payload)
	marker, chunks, delay := parseDirective(userText)
	m.begin(marker)
	defer m.finish()

	requestID := fmt.Sprintf("acceptance-%d", time.Now().UnixNano())
	if stream, _ := payload["stream"].(bool); !stream {
		writeJSON(writer, http.StatusOK, map[string]any{
			"id":      requestID,
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "session-runtime-acceptance-model",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "acceptance response",
				},
				"finish_reason": "stop",
			}},
		})
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.WriteHeader(http.StatusOK)
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	if err := writeSSE(writer, flusher, completionChunk(requestID, map[string]any{"role": "assistant"}, nil)); err != nil {
		m.markDisconnected(marker)
		return
	}
	for index := 0; index < chunks; index++ {
		if err := waitForRequest(request.Context(), delay); err != nil {
			m.markDisconnected(marker)
			return
		}
		content := fmt.Sprintf("%s-chunk-%02d ", marker, index)
		if err := writeSSE(writer, flusher, completionChunk(requestID, map[string]any{"content": content}, nil)); err != nil {
			m.markDisconnected(marker)
			return
		}
	}
	reason := "stop"
	terminal := completionChunk(requestID, map[string]any{}, &reason)
	terminal["usage"] = map[string]any{
		"prompt_tokens":     10,
		"completion_tokens": chunks,
		"total_tokens":      10 + chunks,
	}
	if err := writeSSE(writer, flusher, terminal); err != nil {
		m.markDisconnected(marker)
		return
	}
	_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

func (m *fakeModel) begin(marker string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requestCount[marker]++
	m.active++
	if m.active > m.maxActive {
		m.maxActive = m.active
	}
}

func (m *fakeModel) finish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
}

func (m *fakeModel) markDisconnected(marker string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnected[marker]++
}

func parseDirective(text string) (string, int, time.Duration) {
	match := acceptanceDirective.FindStringSubmatch(text)
	if len(match) == 0 {
		return "unmarked", 2, 10 * time.Millisecond
	}
	chunks := 2
	if match[2] != "" {
		chunks, _ = strconv.Atoi(match[2])
	}
	delayMS := 10
	if match[3] != "" {
		delayMS, _ = strconv.Atoi(match[3])
	}
	if chunks < 1 {
		chunks = 1
	}
	if delayMS < 0 {
		delayMS = 0
	}
	return match[1], chunks, time.Duration(delayMS) * time.Millisecond
}

func latestUserText(payload map[string]any) string {
	messages, _ := payload["messages"].([]any)
	for index := len(messages) - 1; index >= 0; index-- {
		message, _ := messages[index].(map[string]any)
		if message["role"] != "user" {
			continue
		}
		return contentText(message["content"])
	}
	return ""
}

func contentText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, raw := range value {
			switch item := raw.(type) {
			case string:
				parts = append(parts, item)
			case map[string]any:
				if text, _ := item["text"].(string); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func waitForRequest(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func completionChunk(requestID string, delta map[string]any, finishReason *string) map[string]any {
	var reason any
	if finishReason != nil {
		reason = *finishReason
	}
	return map[string]any{
		"id":      requestID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   "session-runtime-acceptance-model",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": reason,
		}},
	}
}

func writeSSE(writer http.ResponseWriter, flusher http.Flusher, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "data: %s\n\n", encoded); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(encoded)
}
