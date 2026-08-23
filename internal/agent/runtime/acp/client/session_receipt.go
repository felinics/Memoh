package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
)

const claudeSDKMessageMethod = "_claude/sdkMessage"

func sessionLifecycleMeta(locator acpprofile.RuntimeSessionLocator) map[string]any {
	if locator != acpprofile.RuntimeSessionLocatorClaudeProject {
		return nil
	}
	return map[string]any{
		"claudeCode": map[string]any{
			"options": map[string]any{
				"env": map[string]string{"CLAUDE_CODE_EAGER_FLUSH": "1"},
			},
			"emitRawSDKMessages": []map[string]string{
				{"type": "user"},
				{"type": "assistant"},
				{"type": "result"},
			},
		},
	}
}

// SessionStateReceipt is an agent-native acknowledgement for one successful
// Prompt. Its contents stay private: callers may carry the receipt to the
// snapshot validator, but must not interpret or persist adapter-specific IDs.
type SessionStateReceipt struct {
	locator                     acpprofile.RuntimeSessionLocator
	sessionID                   string
	claudeResultUUID            string
	claudeResultUserMessageUUID string
	claudeRecords               []claudeSessionRecordReceipt
}

type claudeSessionRecordReceipt struct {
	kind    string
	uuid    string
	aborted bool
}

type claudeMessageOrigin struct {
	Kind string `json:"kind"`
}

type sessionStateReceiptCollector struct {
	mu        sync.Mutex
	locator   acpprofile.RuntimeSessionLocator
	sessionID string
	records   map[string]claudeSessionRecordReceipt
	resultID  string
	resultFor string
	err       error
	finalized bool
}

func newSessionStateReceiptCollector(locator acpprofile.RuntimeSessionLocator, sessionID string) (*sessionStateReceiptCollector, error) {
	if locator != acpprofile.RuntimeSessionLocatorClaudeProject {
		return nil, nil
	}
	sessionID, err := validateSessionID(sessionID)
	if err != nil {
		return nil, fmt.Errorf("start Claude session-state receipt: %w", err)
	}
	return &sessionStateReceiptCollector{
		locator:   locator,
		sessionID: sessionID,
		records:   make(map[string]claudeSessionRecordReceipt),
	}, nil
}

func (c *clientCallbacks) beginSessionStateReceipt(locator acpprofile.RuntimeSessionLocator, sessionID string) (*sessionStateReceiptCollector, error) {
	if c == nil {
		if locator == acpprofile.RuntimeSessionLocatorClaudeProject {
			return nil, errors.New("claude session-state receipt callbacks are unavailable")
		}
		return nil, nil
	}
	collector, err := newSessionStateReceiptCollector(locator, sessionID)
	if err != nil || collector == nil {
		return collector, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stateReceipt != nil {
		return nil, errors.New("ACP session-state receipt collector is already active")
	}
	c.stateReceipt = collector
	return collector, nil
}

func (c *clientCallbacks) finishSessionStateReceipt(collector *sessionStateReceiptCollector, promptSucceeded bool) (*SessionStateReceipt, error) {
	if collector == nil {
		return nil, nil
	}
	if c != nil {
		c.mu.Lock()
		if c.stateReceipt == collector {
			c.stateReceipt = nil
		}
		c.mu.Unlock()
	}
	return collector.finish(promptSucceeded)
}

func (c *clientCallbacks) recordClaudeSDKMessage(params json.RawMessage) {
	if c == nil {
		return
	}
	c.mu.RLock()
	collector := c.stateReceipt
	c.mu.RUnlock()
	if collector != nil {
		collector.record(params)
	}
}

func (c *sessionStateReceiptCollector) record(params json.RawMessage) {
	if c == nil {
		return
	}
	var notification struct {
		SessionID string          `json:"sessionId"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(params, &notification); err != nil {
		c.fail(fmt.Errorf("decode Claude SDK notification: %w", err))
		return
	}
	if strings.TrimSpace(notification.SessionID) != c.sessionID {
		c.fail(fmt.Errorf("claude SDK notification belongs to session %q, want %q", notification.SessionID, c.sessionID))
		return
	}
	var message struct {
		Type            string               `json:"type"`
		Subtype         string               `json:"subtype"`
		UUID            string               `json:"uuid"`
		SessionID       string               `json:"session_id"`
		UserMessageUUID string               `json:"user_message_uuid"`
		Aborted         bool                 `json:"aborted"`
		IsError         bool                 `json:"is_error"`
		Origin          *claudeMessageOrigin `json:"origin"`
	}
	if err := json.Unmarshal(notification.Message, &message); err != nil {
		c.fail(fmt.Errorf("decode Claude SDK message: %w", err))
		return
	}
	if isClaudeAutonomousOrigin(message.Origin) {
		return
	}
	if strings.TrimSpace(message.SessionID) != "" && strings.TrimSpace(message.SessionID) != c.sessionID {
		c.fail(fmt.Errorf("claude SDK message belongs to session %q, want %q", message.SessionID, c.sessionID))
		return
	}

	switch message.Type {
	case "user", "assistant":
		messageID, err := validateClaudeMessageUUID(message.UUID)
		if err != nil {
			c.fail(fmt.Errorf("claude %s SDK message: %w", message.Type, err))
			return
		}
		record := claudeSessionRecordReceipt{kind: message.Type, uuid: messageID, aborted: message.Aborted}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.finalized {
			return
		}
		if previous, exists := c.records[messageID]; exists && previous != record {
			c.err = errors.Join(c.err, fmt.Errorf("claude SDK message UUID %q has conflicting receipts", messageID))
			return
		}
		c.records[messageID] = record
	case "result":
		if message.Subtype != "success" || message.IsError {
			return
		}
		resultID, err := validateClaudeMessageUUID(message.UUID)
		if err != nil {
			c.fail(fmt.Errorf("claude result SDK message: %w", err))
			return
		}
		userMessageID, err := validateClaudeMessageUUID(message.UserMessageUUID)
		if err != nil {
			c.fail(fmt.Errorf("claude successful result user_message_uuid: %w", err))
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.finalized {
			return
		}
		if c.resultID != "" && (c.resultID != resultID || c.resultFor != userMessageID) {
			c.err = errors.Join(c.err, errors.New("claude prompt emitted more than one distinct successful result receipt"))
			return
		}
		c.resultID = resultID
		c.resultFor = userMessageID
	}
}

func (c *sessionStateReceiptCollector) fail(err error) {
	if c == nil || err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finalized {
		c.err = errors.Join(c.err, err)
	}
}

func (c *sessionStateReceiptCollector) finish(promptSucceeded bool) (*SessionStateReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finalized = true
	if !promptSucceeded {
		return nil, nil
	}
	if c.err != nil {
		return nil, c.err
	}
	if c.resultID == "" || c.resultFor == "" {
		return nil, errors.New("successful Claude prompt did not emit a result with user_message_uuid")
	}
	records := make([]claudeSessionRecordReceipt, 0, len(c.records))
	for _, record := range c.records {
		records = append(records, record)
	}
	return &SessionStateReceipt{
		locator:                     c.locator,
		sessionID:                   c.sessionID,
		claudeResultUUID:            c.resultID,
		claudeResultUserMessageUUID: c.resultFor,
		claudeRecords:               records,
	}, nil
}

func validateClaudeMessageUUID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("UUID is missing")
	}
	if _, err := uuid.Parse(value); err != nil {
		return "", fmt.Errorf("invalid UUID %q: %w", value, err)
	}
	return value, nil
}

func isClaudeAutonomousOrigin(origin *claudeMessageOrigin) bool {
	if origin == nil {
		return false
	}
	switch origin.Kind {
	case "task-notification", "peer", "coordinator", "observer", "observer-activity":
		return true
	default:
		return false
	}
}
