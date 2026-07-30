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

var (
	acceptanceDirective = regexp.MustCompile(`\[acceptance:([a-zA-Z0-9_-]+)([^\]]*)\]`)
	directiveOption     = regexp.MustCompile(`([a-z_]+)=([a-zA-Z0-9_-]+)`)
)

type modelDirective struct {
	marker string
	chunks int
	delay  time.Duration
	mode   string
}

type fakeModel struct {
	listener net.Listener
	server   *http.Server

	mu           sync.Mutex
	requestCount map[string]int
	disconnected map[string]int
	release      map[string]chan struct{}
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
		release:      make(map[string]chan struct{}),
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
	m.release = make(map[string]chan struct{})
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

func (m *fakeModel) Release(marker string) {
	m.mu.Lock()
	release := m.release[marker]
	if release != nil {
		delete(m.release, marker)
	}
	m.mu.Unlock()
	if release != nil {
		close(release)
	}
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
	directive := parseDirective(userText)
	m.begin(directive.marker)
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
		m.markDisconnected(directive.marker)
		return
	}
	if directive.mode == "block" {
		if err := m.waitForRelease(request.Context(), directive.marker); err != nil {
			m.markDisconnected(directive.marker)
			return
		}
	}
	if directive.mode == "ask_user" && !hasToolResult(payload) {
		if err := writeAskUserCall(writer, flusher, requestID, directive.marker); err != nil {
			m.markDisconnected(directive.marker)
		}
		return
	}
	if directive.mode == "tool_approval" && !hasToolResult(payload) {
		if err := writeApprovalRequiredToolCall(writer, flusher, requestID, directive.marker); err != nil {
			m.markDisconnected(directive.marker)
		}
		return
	}
	for index := 0; index < directive.chunks; index++ {
		if err := waitForRequest(request.Context(), directive.delay); err != nil {
			m.markDisconnected(directive.marker)
			return
		}
		content := fmt.Sprintf("%s-chunk-%02d ", directive.marker, index)
		if err := writeSSE(writer, flusher, completionChunk(requestID, map[string]any{"content": content}, nil)); err != nil {
			m.markDisconnected(directive.marker)
			return
		}
	}
	if directive.mode == "partial_block" {
		if err := m.waitForRelease(request.Context(), directive.marker); err != nil {
			m.markDisconnected(directive.marker)
			return
		}
	}
	reason := "stop"
	terminal := completionChunk(requestID, map[string]any{}, &reason)
	terminal["usage"] = map[string]any{
		"prompt_tokens":     10,
		"completion_tokens": directive.chunks,
		"total_tokens":      10 + directive.chunks,
	}
	if err := writeSSE(writer, flusher, terminal); err != nil {
		m.markDisconnected(directive.marker)
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

func (m *fakeModel) waitForRelease(ctx context.Context, marker string) error {
	m.mu.Lock()
	release := m.release[marker]
	if release == nil {
		release = make(chan struct{})
		m.release[marker] = release
	}
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-release:
		return nil
	}
}

func parseDirective(text string) modelDirective {
	match := acceptanceDirective.FindStringSubmatch(text)
	if len(match) == 0 {
		return modelDirective{marker: "unmarked", chunks: 2, delay: 10 * time.Millisecond}
	}
	directive := modelDirective{
		marker: match[1],
		chunks: 2,
		delay:  10 * time.Millisecond,
	}
	for _, option := range directiveOption.FindAllStringSubmatch(match[2], -1) {
		switch option[1] {
		case "chunks":
			directive.chunks, _ = strconv.Atoi(option[2])
		case "delay_ms":
			delayMS, _ := strconv.Atoi(option[2])
			directive.delay = time.Duration(delayMS) * time.Millisecond
		case "mode":
			directive.mode = option[2]
		}
	}
	if directive.chunks < 1 {
		directive.chunks = 1
	}
	if directive.delay < 0 {
		directive.delay = 0
	}
	return directive
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

func hasToolResult(payload map[string]any) bool {
	messages, _ := payload["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] == "tool" {
			return true
		}
	}
	return false
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

func writeAskUserCall(writer http.ResponseWriter, flusher http.Flusher, requestID, marker string) error {
	arguments, err := json.Marshal(map[string]any{
		"questions": []map[string]any{{
			"text": "Continue the acceptance run?",
			"kind": "single_select",
			"options": []map[string]any{
				{"label": "Continue"},
				{"label": "Stop"},
			},
		}},
	})
	if err != nil {
		return err
	}
	if err := writeSSE(writer, flusher, completionChunk(requestID, map[string]any{
		"tool_calls": []map[string]any{{
			"index": 0,
			"id":    "ask-" + marker,
			"type":  "function",
			"function": map[string]any{
				"name":      "ask_user",
				"arguments": string(arguments),
			},
		}},
	}, nil)); err != nil {
		return err
	}
	reason := "tool_calls"
	if err := writeSSE(writer, flusher, completionChunk(requestID, map[string]any{}, &reason)); err != nil {
		return err
	}
	_, err = writer.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
	return err
}

func writeApprovalRequiredToolCall(writer http.ResponseWriter, flusher http.Flusher, requestID, marker string) error {
	arguments, err := json.Marshal(map[string]any{
		"command": "printf 'session-runtime-approval-" + marker + "'",
	})
	if err != nil {
		return err
	}
	if err := writeSSE(writer, flusher, completionChunk(requestID, map[string]any{
		"tool_calls": []map[string]any{{
			"index": 0,
			"id":    "approval-" + marker,
			"type":  "function",
			"function": map[string]any{
				"name":      "exec",
				"arguments": string(arguments),
			},
		}},
	}, nil)); err != nil {
		return err
	}
	reason := "tool_calls"
	if err := writeSSE(writer, flusher, completionChunk(requestID, map[string]any{}, &reason)); err != nil {
		return err
	}
	_, err = writer.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
	return err
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
