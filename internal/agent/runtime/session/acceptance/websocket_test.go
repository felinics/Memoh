//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type wsEvent struct {
	Type         string         `json:"type"`
	RunID        string         `json:"run_id,omitempty"`
	TurnID       string         `json:"turn_id,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	InvocationID string         `json:"invocation_id,omitempty"`
	State        string         `json:"state,omitempty"`
	Epoch        string         `json:"epoch,omitempty"`
	Seq          int64          `json:"seq,omitempty"`
	Duplicate    bool           `json:"duplicate,omitempty"`
	Code         string         `json:"code,omitempty"`
	Control      string         `json:"control,omitempty"`
	ControlID    string         `json:"control_id,omitempty"`
	Applied      bool           `json:"applied,omitempty"`
	Data         map[string]any `json:"data,omitempty"`
	Snapshot     map[string]any `json:"snapshot,omitempty"`
	Delta        map[string]any `json:"delta,omitempty"`
	Message      string         `json:"message,omitempty"`
	Feedback     map[string]any `json:"feedback,omitempty"`
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

func sendChat(connection *websocket.Conn, sessionID, invocationID, text string) error {
	return connection.WriteJSON(map[string]any{
		"type":          "message",
		"invocation_id": invocationID,
		"session_id":    sessionID,
		"text":          text,
	})
}

func sendEdit(connection *websocket.Conn, sessionID, invocationID, messageID, text string) error {
	return connection.WriteJSON(map[string]any{
		"type":          "edit_message",
		"invocation_id": invocationID,
		"session_id":    sessionID,
		"message_id":    messageID,
		"text":          text,
	})
}

func subscribeRuntime(connection *websocket.Conn, sessionID string, cursor ...map[string]any) error {
	message := map[string]any{
		"type":       "runtime_subscribe",
		"session_id": sessionID,
	}
	if len(cursor) > 0 && cursor[0] != nil {
		message["cursor"] = cursor[0]
	}
	return connection.WriteJSON(message)
}

func sendAbort(connection *websocket.Conn, sessionID, runID, controlID string) error {
	return connection.WriteJSON(map[string]any{
		"type":       "abort",
		"session_id": sessionID,
		"run_id":     runID,
		"control_id": controlID,
	})
}

func sendUserInputResponse(
	connection *websocket.Conn,
	sessionID string,
	runID string,
	decisionID string,
	controlID string,
	answers []map[string]any,
) error {
	return connection.WriteJSON(map[string]any{
		"type":        "user_input_response",
		"session_id":  sessionID,
		"run_id":      runID,
		"decision_id": decisionID,
		"control_id":  controlID,
		"answers":     answers,
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

// isTerminal reads the run's published state, which is the only place a turn
// ends now: the socket that sent the message is not told anything the session's
// other subscribers are not also told. An `error` frame still counts, since a
// failure the caller is waiting on is reported to it directly.
func isTerminal(event wsEvent) bool {
	if event.Type == "error" {
		return true
	}
	switch eventState(event) {
	case "completed", "aborted", "failed", "lost":
		return event.Type == "runtime_delta" || event.Type == "runtime_snapshot"
	default:
		return false
	}
}

func isPartialText(event wsEvent) bool {
	if event.Type != "runtime_delta" {
		return false
	}
	return nestedNonEmptyString(event.Delta, "content") ||
		nestedNonEmptyString(event.Data, "content")
}

func eventRunID(event wsEvent) string {
	if event.RunID != "" {
		return event.RunID
	}
	return firstNestedString("run_id", event.Data, event.Snapshot, event.Delta)
}

func eventTurnID(event wsEvent) string {
	if event.TurnID != "" {
		return event.TurnID
	}
	return firstNestedString("turn_id", event.Data, event.Snapshot, event.Delta)
}

func eventState(event wsEvent) string {
	if event.State != "" {
		return event.State
	}
	if state := firstNestedString("state", event.Data, event.Snapshot, event.Delta); state != "" {
		return state
	}
	return firstNestedString("status", event.Data, event.Snapshot, event.Delta)
}

func eventEpoch(event wsEvent) string {
	if event.Epoch != "" {
		return event.Epoch
	}
	return firstNestedString("epoch", event.Data, event.Snapshot, event.Delta)
}

func eventSeq(event wsEvent) int64 {
	if event.Seq != 0 {
		return event.Seq
	}
	for _, object := range []map[string]any{event.Data, event.Snapshot, event.Delta} {
		if value, ok := nestedNumber(object, "seq"); ok {
			return int64(value)
		}
	}
	return 0
}

func eventCode(event wsEvent) string {
	if event.Code != "" {
		return event.Code
	}
	return firstNestedString("code", event.Data, event.Feedback)
}

func readAccepted(connection *websocket.Conn, invocationID string, timeout time.Duration) ([]wsEvent, wsEvent, error) {
	events, err := readUntil(connection, timeout, func(event wsEvent) bool {
		return event.Type == "run_accepted" &&
			(event.InvocationID == "" || event.InvocationID == invocationID)
	})
	if err != nil {
		return events, wsEvent{}, err
	}
	return events, events[len(events)-1], nil
}

func readRejected(connection *websocket.Conn, invocationID, code string, timeout time.Duration) ([]wsEvent, wsEvent, error) {
	events, err := readUntil(connection, timeout, func(event wsEvent) bool {
		return event.Type == "run_rejected" &&
			(event.InvocationID == "" || event.InvocationID == invocationID) &&
			eventCode(event) == code
	})
	if err != nil {
		return events, wsEvent{}, err
	}
	return events, events[len(events)-1], nil
}

func firstNestedString(key string, objects ...map[string]any) string {
	for _, object := range objects {
		if value := nestedString(object, key); value != "" {
			return value
		}
	}
	return ""
}

func nestedString(object map[string]any, key string) string {
	if object == nil {
		return ""
	}
	if value, ok := object[key].(string); ok && value != "" {
		return value
	}
	for _, value := range object {
		switch typed := value.(type) {
		case map[string]any:
			if result := nestedString(typed, key); result != "" {
				return result
			}
		case []any:
			for _, item := range typed {
				if child, ok := item.(map[string]any); ok {
					if result := nestedString(child, key); result != "" {
						return result
					}
				}
			}
		}
	}
	return ""
}

func nestedNumber(object map[string]any, key string) (float64, bool) {
	if object == nil {
		return 0, false
	}
	if value, ok := object[key].(float64); ok {
		return value, true
	}
	for _, value := range object {
		switch typed := value.(type) {
		case map[string]any:
			if result, ok := nestedNumber(typed, key); ok {
				return result, true
			}
		case []any:
			for _, item := range typed {
				if child, ok := item.(map[string]any); ok {
					if result, ok := nestedNumber(child, key); ok {
						return result, true
					}
				}
			}
		}
	}
	return 0, false
}

func nestedNonEmptyString(object map[string]any, key string) bool {
	return nestedString(object, key) != ""
}
