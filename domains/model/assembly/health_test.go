package assembly

import (
	"context"
	"errors"
	"testing"
)

func TestNewBotModelLookup(t *testing.T) {
	t.Parallel()

	lookup := NewBotModelLookup(func(_ context.Context, botID string) (string, string, error) {
		if botID != "bot-1" {
			t.Fatalf("botID = %q, want bot-1", botID)
		}
		return "user-1", "model-1", nil
	})

	got, err := lookup.GetBotModelIDs(t.Context(), "bot-1")
	if err != nil {
		t.Fatalf("GetBotModelIDs() error = %v", err)
	}
	if got.OwnerUserID != "user-1" || got.ChatModelID != "model-1" {
		t.Fatalf("GetBotModelIDs() = %#v", got)
	}
}

func TestNewBotModelLookupPropagatesError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("lookup failed")
	lookup := NewBotModelLookup(func(context.Context, string) (string, string, error) {
		return "", "", wantErr
	})

	_, err := lookup.GetBotModelIDs(t.Context(), "bot-1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetBotModelIDs() error = %v, want %v", err, wantErr)
	}
}
