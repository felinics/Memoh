package adapter

import (
	"testing"

	"github.com/memohai/memoh/domains/channel/inbound"
)

func TestNewSessionCreatedByUserIDPrefersCreator(t *testing.T) {
	got := newSessionCreatedByUserID(inbound.NewSessionSpec{
		CreatedByUserID:       "creator-user",
		RuntimeOwnerAccountID: "runtime-owner",
	})
	if got != "creator-user" {
		t.Fatalf("created_by_user_id = %q, want creator-user", got)
	}

	got = newSessionCreatedByUserID(inbound.NewSessionSpec{
		RuntimeOwnerAccountID: "runtime-owner",
	})
	if got != "runtime-owner" {
		t.Fatalf("created_by_user_id fallback = %q, want runtime-owner", got)
	}
}
