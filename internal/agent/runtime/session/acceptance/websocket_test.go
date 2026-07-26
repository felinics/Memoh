//go:build integration

package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type wsEvent struct {
	Type      string         `json:"type"`
	StreamID  string         `json:"stream_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	Message   string         `json:"message,omitempty"`
	Feedback  map[string]any `json:"feedback,omitempty"`
}

func dialChatWebSocket(baseURL, token, botID string) (*websocket.Conn, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, err
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return nil, fmt.Errorf("unsupported Server URL scheme %q", parsed.Scheme)
	}
	parsed.Path = "/bots/" + url.PathEscape(botID) + "/web/ws"
	query := parsed.Query()
	query.Set("token", token)
	parsed.RawQuery = query.Encode()

	connection, response, err := websocket.DefaultDialer.Dial(parsed.String(), nil)
	if err != nil {
		if response != nil {
			statusCode := response.StatusCode
			_ = response.Body.Close()
			return nil, fmt.Errorf("dial %s: HTTP %d: %w", parsed.Redacted(), statusCode, err)
		}
		return nil, fmt.Errorf("dial %s: %w", parsed.Redacted(), err)
	}
	return connection, nil
}

func closeWebSocket(connection *websocket.Conn) {
	if connection != nil {
		_ = connection.Close()
	}
}

func sendChat(connection *websocket.Conn, sessionID, streamID, invocationID, text string) error {
	return connection.WriteJSON(map[string]any{
		"type":          "message",
		"stream_id":     streamID,
		"invocation_id": invocationID,
		"session_id":    sessionID,
		"text":          text,
	})
}

func subscribeRuntime(connection *websocket.Conn, sessionID string) error {
	return connection.WriteJSON(map[string]any{
		"type":       "runtime_subscribe",
		"session_id": sessionID,
	})
}

func sendAbort(connection *websocket.Conn, sessionID, streamID string) error {
	return connection.WriteJSON(map[string]any{
		"type":       "abort",
		"session_id": sessionID,
		"stream_id":  streamID,
	})
}

func readEvent(connection *websocket.Conn, timeout time.Duration) (wsEvent, error) {
	if err := connection.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return wsEvent{}, err
	}
	_, encoded, err := connection.ReadMessage()
	if err != nil {
		return wsEvent{}, err
	}
	var event wsEvent
	if err := json.Unmarshal(encoded, &event); err != nil {
		return wsEvent{}, fmt.Errorf("decode WebSocket event %q: %w", encoded, err)
	}
	return event, nil
}

func readUntil(connection *websocket.Conn, timeout time.Duration, predicate func(wsEvent) bool) ([]wsEvent, error) {
	deadline := time.Now().Add(timeout)
	var events []wsEvent
	for time.Now().Before(deadline) {
		event, err := readEvent(connection, time.Until(deadline))
		if err != nil {
			return events, err
		}
		events = append(events, event)
		if predicate(event) {
			return events, nil
		}
	}
	return events, fmt.Errorf("event predicate not satisfied within %s", timeout)
}

func isTerminal(event wsEvent) bool {
	return event.Type == "end" || event.Type == "error"
}

func isPartialText(event wsEvent) bool {
	return event.Type == "message" &&
		stringValue(event.Data["type"]) == "text" &&
		stringValue(event.Data["content"]) != ""
}

func hasEvent(events []wsEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
