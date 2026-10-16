package adapters

import (
	"errors"
	"strings"
)

// ErrMemoryScope deliberately does not reveal whether a foreign ID exists.
var ErrMemoryScope = errors.New("memory does not belong to this bot")

// ValidateMemoryScope checks canonical builtin IDs against an independently
// authorized bot. An ID identifies an object; it never grants access to its bot.
// Callers must validate a whole batch before performing any mutations.
func ValidateMemoryScope(botID string, memoryIDs ...string) error {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		return ErrMemoryScope
	}
	for _, id := range memoryIDs {
		owner, localID, ok := strings.Cut(strings.TrimSpace(id), ":")
		if !ok || owner != botID || strings.TrimSpace(localID) == "" {
			return ErrMemoryScope
		}
	}
	return nil
}
