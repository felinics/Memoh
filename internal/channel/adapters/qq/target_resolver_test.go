package qq

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	identitypkg "github.com/felinics/memoh/internal/channel/identities"
	routepkg "github.com/felinics/memoh/internal/channel/route"
)

const testQQOpenID = "00112233445566778899AABBCCDDEEFF"

func TestQQResolveTargetMapsRouteID(t *testing.T) {
	t.Parallel()

	adapter := NewQQAdapter(nil)
	adapter.SetRouteResolver(&fakeQQRouteResolver{
		byID: map[string]routepkg.Route{
			"3fe2bad9-3eae-4f23-872c-b7a63662aa00": {
				ID:          "3fe2bad9-3eae-4f23-872c-b7a63662aa00",
				Platform:    "qq",
				ReplyTarget: "c2c:" + testQQOpenID,
			},
		},
	})

	got, err := adapter.resolveTarget(context.Background(), "3fe2bad9-3eae-4f23-872c-b7a63662aa00")
	if err != nil {
		t.Fatalf("resolveTarget returned error: %v", err)
	}
	if got != "c2c:"+testQQOpenID {
		t.Fatalf("unexpected mapped target: %q", got)
	}
}

func TestQQResolveTargetMapsIdentityID(t *testing.T) {
	t.Parallel()

	adapter := NewQQAdapter(nil)
	adapter.SetChannelIdentityResolver(&fakeQQIdentityResolver{
		canonical: map[string][]identitypkg.ChannelIdentity{
			"3fe2bad9-3eae-4f23-872c-b7a63662aa00": {
				{ID: "qq-identity-1", Channel: "qq", ChannelSubjectID: testQQOpenID},
			},
		},
	})

	got, err := adapter.resolveTarget(context.Background(), "3fe2bad9-3eae-4f23-872c-b7a63662aa00")
	if err != nil {
		t.Fatalf("resolveTarget returned error: %v", err)
	}
	if got != "c2c:"+testQQOpenID {
		t.Fatalf("unexpected mapped target: %q", got)
	}
}

func TestQQResolveTargetSkipsNonOpenIDQQIdentity(t *testing.T) {
	t.Parallel()

	adapter := NewQQAdapter(nil)
	adapter.SetChannelIdentityResolver(&fakeQQIdentityResolver{
		canonical: map[string][]identitypkg.ChannelIdentity{
			"3fe2bad9-3eae-4f23-872c-b7a63662aa00": {
				{ID: "qq-guild-identity-1", Channel: "qq", ChannelSubjectID: "guild-user-id"},
			},
		},
	})

	got, err := adapter.resolveTarget(context.Background(), "3fe2bad9-3eae-4f23-872c-b7a63662aa00")
	if err != nil {
		t.Fatalf("resolveTarget returned error: %v", err)
	}
	if got != "c2c:3fe2bad9-3eae-4f23-872c-b7a63662aa00" {
		t.Fatalf("unexpected mapped target: %q", got)
	}
}

func TestQQResolveTargetReturnsRouteResolverErrors(t *testing.T) {
	t.Parallel()

	adapter := NewQQAdapter(nil)
	adapter.SetRouteResolver(&fakeQQRouteResolver{err: errors.New("route store unavailable")})

	_, err := adapter.resolveTarget(context.Background(), "3fe2bad9-3eae-4f23-872c-b7a63662aa00")
	if err == nil {
		t.Fatal("expected route resolver error")
	}
	if !strings.Contains(err.Error(), "route store unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQQResolveTargetReturnsIdentityResolverErrors(t *testing.T) {
	t.Parallel()

	adapter := NewQQAdapter(nil)
	adapter.SetChannelIdentityResolver(&fakeQQIdentityResolver{canonicalErr: errors.New("identity store unavailable")})

	_, err := adapter.resolveTarget(context.Background(), "3fe2bad9-3eae-4f23-872c-b7a63662aa00")
	if err == nil {
		t.Fatal("expected identity resolver error")
	}
	if !strings.Contains(err.Error(), "identity store unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseTargetRejectsUUIDForC2C(t *testing.T) {
	t.Parallel()

	_, err := parseTarget("3fe2bad9-3eae-4f23-872c-b7a63662aa00")
	if err == nil {
		t.Fatal("expected c2c uuid target error")
	}
	if !strings.Contains(err.Error(), "user_openid") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type fakeQQIdentityResolver struct {
	byID         map[string]identitypkg.ChannelIdentity
	canonical    map[string][]identitypkg.ChannelIdentity
	byIDErr      error
	canonicalErr error
}

func (f *fakeQQIdentityResolver) GetByID(_ context.Context, channelIdentityID string) (identitypkg.ChannelIdentity, error) {
	if f.byIDErr != nil {
		return identitypkg.ChannelIdentity{}, f.byIDErr
	}
	item, ok := f.byID[channelIdentityID]
	if !ok {
		return identitypkg.ChannelIdentity{}, identitypkg.ErrChannelIdentityNotFound
	}
	return item, nil
}

func (f *fakeQQIdentityResolver) ListCanonicalChannelIdentities(_ context.Context, channelIdentityID string) ([]identitypkg.ChannelIdentity, error) {
	if f.canonicalErr != nil {
		return nil, f.canonicalErr
	}
	items, ok := f.canonical[channelIdentityID]
	if !ok {
		return nil, identitypkg.ErrChannelIdentityNotFound
	}
	return items, nil
}

type fakeQQRouteResolver struct {
	byID map[string]routepkg.Route
	err  error
}

func (f *fakeQQRouteResolver) GetByID(_ context.Context, routeID string) (routepkg.Route, error) {
	if f.err != nil {
		return routepkg.Route{}, f.err
	}
	item, ok := f.byID[routeID]
	if !ok {
		return routepkg.Route{}, pgx.ErrNoRows
	}
	return item, nil
}
