package application

import (
	"testing"

	"github.com/google/uuid"
)

func TestRunIDForChatRequestPreservesAdmittedIdentity(t *testing.T) {
	const admittedRunID = "2a9737fa-af25-4a69-b305-c416aca891e1"

	if got := runIDForChatRequest(admittedRunID); got != admittedRunID {
		t.Fatalf("runIDForChatRequest() = %q, want admitted run ID %q", got, admittedRunID)
	}
}

func TestRunIDForChatRequestMintsUUIDWhenNoAdmissionExists(t *testing.T) {
	first := runIDForChatRequest("")
	second := runIDForChatRequest(" \t")

	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("runIDForChatRequest() = %q, want UUID: %v", first, err)
	}
	if _, err := uuid.Parse(second); err != nil {
		t.Fatalf("runIDForChatRequest() = %q, want UUID: %v", second, err)
	}
	if first == second {
		t.Fatalf("runIDForChatRequest() minted the same ID twice: %q", first)
	}
}
