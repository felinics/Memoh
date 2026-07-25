package identity

import (
	"context"
	"testing"
)

type identityStoreFake struct {
	upsertInput WriteInput
	listIDs     []string
	listResult  []ChannelIdentity
}

func (*identityStoreFake) Create(context.Context, WriteInput) (ChannelIdentity, error) {
	return ChannelIdentity{}, nil
}

func (*identityStoreFake) FindByID(context.Context, string) (ChannelIdentity, error) {
	return ChannelIdentity{}, nil
}

func (s *identityStoreFake) ListByIDs(_ context.Context, ids []string) ([]ChannelIdentity, error) {
	s.listIDs = append([]string(nil), ids...)
	return s.listResult, nil
}

func (s *identityStoreFake) Upsert(_ context.Context, input WriteInput) (ChannelIdentity, error) {
	s.upsertInput = input
	return ChannelIdentity{Channel: input.Channel, ChannelSubjectID: input.ChannelSubjectID}, nil
}

func (*identityStoreFake) ListUserIDs(context.Context, string) ([]string, error) {
	return nil, nil
}

func (*identityStoreFake) Search(context.Context, string, int) ([]ChannelIdentity, error) {
	return nil, nil
}

func TestNormalizeChannel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"feishu", "feishu"},
		{" FEISHU ", "feishu"},
		{"Web", "web"},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := normalizeChannel(tc.input)
			if result != tc.expected {
				t.Errorf("normalizeChannel(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestUpsertNormalizesIdentityInput(t *testing.T) {
	store := &identityStoreFake{}
	service := NewService(nil, store)

	_, err := service.UpsertChannelIdentity(t.Context(), " FEISHU ", " subject ", " display ", map[string]any{
		"avatar_url": " avatar ",
	})
	if err != nil {
		t.Fatalf("UpsertChannelIdentity() error = %v", err)
	}
	if store.upsertInput.Channel != "feishu" {
		t.Fatalf("Channel = %q, want feishu", store.upsertInput.Channel)
	}
	if store.upsertInput.ChannelSubjectID != "subject" {
		t.Fatalf("ChannelSubjectID = %q, want subject", store.upsertInput.ChannelSubjectID)
	}
	if store.upsertInput.DisplayName != "display" || store.upsertInput.AvatarURL != "avatar" {
		t.Fatalf("upsert input = %#v", store.upsertInput)
	}
}

func TestListIdentityProjectionsUsesBatchStore(t *testing.T) {
	store := &identityStoreFake{listResult: []ChannelIdentity{{
		ID: "identity", Channel: "slack", ChannelSubjectID: "U1", DisplayName: "Alice", AvatarURL: "avatar",
	}}}
	items, err := NewService(nil, store).ListIdentityProjections(t.Context(), []string{"identity", "missing"})
	if err != nil {
		t.Fatalf("ListIdentityProjections() error = %v", err)
	}
	if len(store.listIDs) != 2 || len(items) != 1 {
		t.Fatalf("IDs/items = (%#v, %#v)", store.listIDs, items)
	}
	if items[0].Channel != "slack" || items[0].ChannelSubjectID != "U1" ||
		items[0].DisplayName != "Alice" || items[0].AvatarURL != "avatar" {
		t.Fatalf("projection = %#v", items[0])
	}
}
