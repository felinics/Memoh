package adapters

import "strings"

const (
	sourceRefSeparator = "/"

	// MaxSourceRefsPerMemory bounds durable provenance growth for one memory.
	// The PostgreSQL upsert enforces the same limit atomically.
	MaxSourceRefsPerMemory = 64

	// MaxSourceRefsPerToolResult bounds source locators exposed for one memory
	// hit after validation and authorization.
	MaxSourceRefsPerToolResult = 8
)

// EncodeSourceRef builds a "<sessionID>/<messageID>" source ref. The session
// part is optional so bare message IDs stay valid refs.
func EncodeSourceRef(sessionID, messageID string) string {
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return ""
	}
	if sessionID == "" {
		return messageID
	}
	return sessionID + sourceRefSeparator + messageID
}

// ParseSourceRef splits a source ref into its session and message parts. A ref
// without a separator is treated as a bare message ID.
func ParseSourceRef(ref string) (sessionID, messageID string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ""
	}
	session, message, found := strings.Cut(ref, sourceRefSeparator)
	if !found {
		return "", ref
	}
	return strings.TrimSpace(session), strings.TrimSpace(message)
}

// ParseScopedSourceRef accepts only the durable ref shape emitted by the chat
// persistence path. Bare legacy message IDs cannot be authorized across
// sessions and malformed multi-segment refs are rejected.
func ParseScopedSourceRef(ref string) (sessionID, messageID string, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Count(ref, sourceRefSeparator) != 1 {
		return "", "", false
	}
	sessionID, messageID = ParseSourceRef(ref)
	if sessionID == "" || messageID == "" {
		return "", "", false
	}
	return sessionID, messageID, true
}

// NormalizeSourceRefs validates, de-duplicates, and retains the newest
// associations up to the durable per-memory limit. Input order is association
// order; retained output keeps that order.
func NormalizeSourceRefs(refs []string) []string {
	return RetainSourceRefs(refs, MaxSourceRefsPerMemory)
}

// MergeSourceRefs appends new associations to existing provenance and applies
// the same canonical validation and bound used at storage boundaries.
func MergeSourceRefs(existing, extra []string) []string {
	combined := make([]string, 0, len(existing)+len(extra))
	combined = append(combined, existing...)
	combined = append(combined, extra...)
	return NormalizeSourceRefs(combined)
}

// RetainSourceRefs validates before applying the limit, so malformed tail
// entries cannot crowd out older valid provenance.
func RetainSourceRefs(refs []string, limit int) []string {
	if limit <= 0 || len(refs) == 0 {
		return nil
	}
	valid := make([]string, 0, min(len(refs), limit))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		sessionID, messageID, ok := ParseScopedSourceRef(ref)
		if !ok {
			continue
		}
		canonical := EncodeSourceRef(sessionID, messageID)
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		valid = append(valid, canonical)
	}
	if len(valid) > limit {
		valid = valid[len(valid)-limit:]
	}
	return append([]string(nil), valid...)
}
