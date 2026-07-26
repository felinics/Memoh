package link

import (
	"context"
	"testing"

	linkpersistence "github.com/memohai/memoh/domains/api/identity/link/persistence"
)

func TestListUserBindingsBatchEnrichesCurrentIdentityDetails(t *testing.T) {
	const (
		userID    = "00000000-0000-0000-0000-000000000010"
		identity1 = "00000000-0000-0000-0000-000000000020"
		identity2 = "00000000-0000-0000-0000-000000000021"
	)
	store := &fakeStore{userBindings: map[string][]linkpersistence.Binding{
		userID: {
			{ID: "binding-1", UserID: userID, ChannelIdentityID: identity1},
			{ID: "binding-2", UserID: userID, ChannelIdentityID: identity1},
			{ID: "binding-3", UserID: userID, ChannelIdentityID: identity2},
		},
	}}
	reader := &fakeChannelIdentityReader{items: []linkpersistence.ChannelIdentity{{
		ID: identity1, Channel: "telegram", ChannelSubjectID: "subject", DisplayName: "Alice", AvatarURL: "avatar",
	}}}

	items, err := NewService(nil, store, reader).ListUserBindings(t.Context(), userID)
	if err != nil {
		t.Fatalf("ListUserBindings() error = %v", err)
	}
	if reader.calls != 1 || len(reader.ids) != 2 {
		t.Fatalf("identity reader calls = %d, ids = %v", reader.calls, reader.ids)
	}
	for _, item := range items[:2] {
		if item.ChannelType != "telegram" || item.ChannelSubjectID != "subject" || item.ChannelIdentityDisplayName != "Alice" || item.ChannelIdentityAvatarURL != "avatar" {
			t.Fatalf("enriched binding = %#v", item)
		}
	}
	if items[2].ChannelType != "" || items[2].ChannelIdentityDisplayName != "" {
		t.Fatalf("missing identity binding = %#v", items[2])
	}
}

func TestListUserBindingsEmptySkipsIdentityReader(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000010"
	reader := &fakeChannelIdentityReader{}
	store := &fakeStore{userBindings: map[string][]linkpersistence.Binding{userID: {}}}

	items, err := NewService(nil, store, reader).ListUserBindings(t.Context(), userID)
	if err != nil {
		t.Fatalf("ListUserBindings() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("ListUserBindings() = %#v", items)
	}
	if reader.calls != 0 {
		t.Fatalf("identity reader calls = %d", reader.calls)
	}
}

type fakeStore struct {
	userBindings map[string][]linkpersistence.Binding
}

func (*fakeStore) CreateLinkCode(context.Context, linkpersistence.CreateLinkCodeCommand) (linkpersistence.LinkCode, error) {
	return linkpersistence.LinkCode{}, nil
}

func (*fakeStore) FindLinkCode(context.Context, string) (linkpersistence.LinkCodeState, bool, error) {
	return linkpersistence.LinkCodeState{}, false, nil
}

func (*fakeStore) RedeemLinkCode(context.Context, string, string) (linkpersistence.Binding, bool, error) {
	return linkpersistence.Binding{}, false, nil
}

func (f *fakeStore) ListBindingsForUser(_ context.Context, userID string) ([]linkpersistence.Binding, error) {
	return f.userBindings[userID], nil
}

func (*fakeStore) ListBindingsForBot(context.Context, string) ([]linkpersistence.Binding, error) {
	return nil, nil
}

func (*fakeStore) DeleteBinding(context.Context, string, string) error { return nil }

func (*fakeStore) ListUserIDsByChannelIdentity(context.Context, string) ([]string, error) {
	return nil, nil
}

type fakeChannelIdentityReader struct {
	items []linkpersistence.ChannelIdentity
	ids   []string
	calls int
}

func (f *fakeChannelIdentityReader) ListChannelIdentities(_ context.Context, ids []string) ([]linkpersistence.ChannelIdentity, error) {
	f.calls++
	f.ids = append([]string(nil), ids...)
	return f.items, nil
}
