// Agent-format knowledge: how each adapter's transcript identifies its
// session, which records prove freshness, and the Claude receipt audit.
//
// This file is deliberately the ONLY place that branches on
// acpprofile.RuntimeSessionLocator for transcript semantics (the receipt
// wiring in session_receipt.go and the pool's receipt gate are the two
// documented exceptions). Supporting a new resumable agent means extending
// the switches here plus declaring SessionRoots/SessionLocator in its
// profile - nothing elsewhere. Two formats do not justify an interface;
// co-located switches read straighter.
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	acpprofile "github.com/memohai/memoh/internal/agent/runtime/acp/profile"
)

type primaryStateValidator struct {
	locator       acpprofile.RuntimeSessionLocator
	sessionID     string
	logicalLines  int64
	terminalTurns int64
	claudeFoundID bool
	invalid       bool
}

func (v *primaryStateValidator) observe(raw json.RawMessage) {
	v.logicalLines++
	switch v.locator {
	case acpprofile.RuntimeSessionLocatorCodexRollout:
		if v.logicalLines == 1 {
			id, ok := codexSessionMetaID(raw)
			if !ok || id != v.sessionID {
				v.invalid = true
			}
		}
		var envelope struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			v.invalid = true
		} else if envelope.Type == "event_msg" && (envelope.Payload.Type == "task_complete" || envelope.Payload.Type == "turn_complete") {
			v.terminalTurns++
		}
	case acpprofile.RuntimeSessionLocatorClaudeProject:
		var record map[string]json.RawMessage
		if json.Unmarshal(raw, &record) != nil {
			v.invalid = true
			return
		}
		for _, key := range []string{"sessionId", "session_id"} {
			encoded, ok := record[key]
			if !ok {
				continue
			}
			var id string
			if json.Unmarshal(encoded, &id) != nil || id != v.sessionID {
				v.invalid = true
				continue
			}
			v.claudeFoundID = true
		}
	default:
		v.invalid = true
	}
}

func (v *primaryStateValidator) valid() bool {
	if v.invalid || v.logicalLines == 0 {
		return false
	}
	if v.locator == acpprofile.RuntimeSessionLocatorClaudeProject {
		return v.claudeFoundID
	}
	return true
}

func (v *primaryStateValidator) cursor() SessionStateCursor {
	return SessionStateCursor{
		locator:       v.locator,
		sessionID:     v.sessionID,
		primaryLines:  v.logicalLines,
		terminalTurns: v.terminalTurns,
	}
}

type claudeReceiptRecord struct {
	kind    string
	aborted bool
	found   bool
}

type claudeReceiptValidator struct {
	sessionID string
	required  map[string]claudeReceiptRecord
}

func newClaudeReceiptValidator(sessionID string, receipt *SessionStateReceipt) (*claudeReceiptValidator, error) {
	if receipt == nil {
		return nil, errors.New("claude prompt has no native session-state receipt")
	}
	if receipt.locator != acpprofile.RuntimeSessionLocatorClaudeProject || receipt.sessionID != sessionID {
		return nil, errors.New("claude prompt receipt belongs to a different native session")
	}
	resultUserID, err := validateClaudeMessageUUID(receipt.claudeResultUserMessageUUID)
	if err != nil {
		return nil, fmt.Errorf("claude result receipt user_message_uuid: %w", err)
	}
	if _, err := validateClaudeMessageUUID(receipt.claudeResultUUID); err != nil {
		return nil, fmt.Errorf("claude result receipt UUID: %w", err)
	}
	validator := &claudeReceiptValidator{sessionID: sessionID, required: map[string]claudeReceiptRecord{
		resultUserID: {kind: "user"},
	}}
	for _, expected := range receipt.claudeRecords {
		id, err := validateClaudeMessageUUID(expected.uuid)
		if err != nil {
			return nil, fmt.Errorf("claude %s SDK receipt: %w", expected.kind, err)
		}
		if expected.kind != "user" && expected.kind != "assistant" {
			return nil, fmt.Errorf("claude SDK receipt has unsupported record type %q", expected.kind)
		}
		if expected.kind == "assistant" && expected.aborted {
			return nil, fmt.Errorf("claude assistant SDK record %q is aborted", id)
		}
		if existing, ok := validator.required[id]; ok && existing.kind != expected.kind {
			return nil, fmt.Errorf("claude SDK receipt UUID %q has conflicting types", id)
		}
		validator.required[id] = claudeReceiptRecord{kind: expected.kind}
	}
	return validator, nil
}

func (v *claudeReceiptValidator) observe(raw json.RawMessage) error {
	var record struct {
		Type    string `json:"type"`
		UUID    string `json:"uuid"`
		Aborted bool   `json:"aborted"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	id := strings.TrimSpace(record.UUID)
	expected, ok := v.required[id]
	if !ok {
		return nil
	}
	if record.Type != expected.kind || (record.Type == "assistant" && record.Aborted) {
		return fmt.Errorf("claude transcript UUID %q has incompatible record", id)
	}
	if expected.found && expected.aborted != record.Aborted {
		return fmt.Errorf("claude transcript UUID %q has conflicting records", id)
	}
	expected.found = true
	expected.aborted = record.Aborted
	v.required[id] = expected
	return nil
}

func (v *claudeReceiptValidator) validate() error {
	for id, expected := range v.required {
		if !expected.found {
			return fmt.Errorf("claude %s record %q is absent from the stable snapshot", expected.kind, id)
		}
	}
	return nil
}

func sessionPrimaryPathMatches(locator acpprofile.RuntimeSessionLocator, filePath, sessionID string) bool {
	base := path.Base(filePath)
	switch locator {
	case acpprofile.RuntimeSessionLocatorCodexRollout:
		return strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, "-"+sessionID+".jsonl")
	case acpprofile.RuntimeSessionLocatorClaudeProject:
		return base == sessionID+".jsonl"
	default:
		return false
	}
}

func sessionFileBelongsToPrimary(locator acpprofile.RuntimeSessionLocator, filePath, primaryPath, sessionID string) bool {
	if filePath == primaryPath {
		return true
	}
	if locator != acpprofile.RuntimeSessionLocatorClaudeProject {
		return false
	}
	relatedRoot := path.Join(path.Dir(primaryPath), sessionID)
	return strings.HasPrefix(filePath, relatedRoot+"/")
}

func codexSessionMetaID(line json.RawMessage) (string, bool) {
	var record map[string]json.RawMessage
	if json.Unmarshal(line, &record) != nil {
		return "", false
	}
	var recordType string
	if json.Unmarshal(record["type"], &recordType) != nil || recordType != "session_meta" {
		return "", false
	}
	for _, containerKey := range []string{"payload", "meta"} {
		var container map[string]json.RawMessage
		if json.Unmarshal(record[containerKey], &container) != nil {
			continue
		}
		if id, ok := sessionIDFromObject(container); ok {
			return id, true
		}
		var nestedMeta map[string]json.RawMessage
		if json.Unmarshal(container["meta"], &nestedMeta) == nil {
			if id, ok := sessionIDFromObject(nestedMeta); ok {
				return id, true
			}
		}
	}
	return "", false
}

func sessionIDFromObject(object map[string]json.RawMessage) (string, bool) {
	for _, key := range []string{"id", "session_id"} {
		raw, ok := object[key]
		if !ok {
			continue
		}
		var id string
		if json.Unmarshal(raw, &id) == nil && id != "" {
			return id, true
		}
	}
	return "", false
}
