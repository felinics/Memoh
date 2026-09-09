package sessionruntime

import (
	"context"
	"testing"
	"time"
)

func TestMemoryHistoryResetLeaseLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := NewMemoryBackend()
	botScope := ResetScope{BotID: "bot-1"}
	sessionScope := ResetScope{BotID: "bot-1", SessionID: "session-1"}

	lease, applied, err := backend.AcquireHistoryReset(ctx, botScope, "token-bot", time.Minute)
	if err != nil || !applied {
		t.Fatalf("acquire bot lease = (%v, %v)", applied, err)
	}
	if lease.Token != "token-bot" || lease.ExpiresAt.Before(time.Now().Add(30*time.Second)) {
		t.Fatalf("bot lease = %#v", lease)
	}

	// The bot lease blocks both a competing bot lease and any session lease.
	if _, applied, err := backend.AcquireHistoryReset(ctx, botScope, "token-other", time.Minute); err != nil || applied {
		t.Fatalf("competing bot acquire = (%v, %v), want blocked", applied, err)
	}
	if _, applied, err := backend.AcquireHistoryReset(ctx, sessionScope, "token-session", time.Minute); err != nil || applied {
		t.Fatalf("session acquire under bot lease = (%v, %v), want blocked", applied, err)
	}
	for _, scope := range []ResetScope{botScope, sessionScope} {
		effective, blocked, err := backend.EffectiveHistoryReset(ctx, scope)
		if err != nil || !blocked || effective.Token != "token-bot" {
			t.Fatalf("effective(%#v) = (%#v, %v, %v)", scope, effective, blocked, err)
		}
	}

	// Renewal and release are token CAS operations.
	if _, ok, err := backend.RenewHistoryReset(ctx, ResetLease{Scope: botScope, Token: "token-wrong", ExpiresAt: lease.ExpiresAt}, time.Minute); err != nil || ok {
		t.Fatalf("renew with wrong token = (%v, %v), want miss", ok, err)
	}
	renewed, ok, err := backend.RenewHistoryReset(ctx, lease, 2*time.Minute)
	if err != nil || !ok || !renewed.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatalf("renew own lease = (%#v, %v, %v)", renewed, ok, err)
	}
	if ok, err := backend.ReleaseHistoryReset(ctx, ResetLease{Scope: botScope, Token: "token-wrong", ExpiresAt: renewed.ExpiresAt}); err != nil || ok {
		t.Fatalf("release with wrong token = (%v, %v), want miss", ok, err)
	}
	if ok, err := backend.ReleaseHistoryReset(ctx, renewed); err != nil || !ok {
		t.Fatalf("release own lease = (%v, %v)", ok, err)
	}
	if _, blocked, err := backend.EffectiveHistoryReset(ctx, botScope); err != nil || blocked {
		t.Fatalf("effective after release = (%v, %v), want free", blocked, err)
	}
}

func TestMemoryHistoryResetSessionLeaseBlocksBotScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := NewMemoryBackend()
	first := ResetScope{BotID: "bot-1", SessionID: "session-1"}
	second := ResetScope{BotID: "bot-1", SessionID: "session-2"}

	lease, applied, err := backend.AcquireHistoryReset(ctx, first, "token-1", time.Minute)
	if err != nil || !applied {
		t.Fatalf("acquire session lease = (%v, %v)", applied, err)
	}
	// A session lease blocks the bot scope but not a sibling session.
	if _, applied, err := backend.AcquireHistoryReset(ctx, ResetScope{BotID: "bot-1"}, "token-bot", time.Minute); err != nil || applied {
		t.Fatalf("bot acquire under session lease = (%v, %v), want blocked", applied, err)
	}
	if _, applied, err := backend.AcquireHistoryReset(ctx, second, "token-2", time.Minute); err != nil || !applied {
		t.Fatalf("sibling session acquire = (%v, %v), want applied", applied, err)
	}
	if effective, blocked, err := backend.EffectiveHistoryReset(ctx, ResetScope{BotID: "bot-1"}); err != nil || !blocked || effective.Token == "" {
		t.Fatalf("bot-scope effective under session leases = (%#v, %v, %v)", effective, blocked, err)
	}
	if effective, blocked, err := backend.EffectiveHistoryReset(ctx, first); err != nil || !blocked || effective.Token != "token-1" {
		t.Fatalf("session-scope effective = (%#v, %v, %v)", effective, blocked, err)
	}
	// A session lease cannot be renewed once the opposite scope holds… but the
	// opposite scope cannot exist here, so renewal succeeds while held.
	if _, ok, err := backend.RenewHistoryReset(ctx, lease, time.Minute); err != nil || !ok {
		t.Fatalf("renew held session lease = (%v, %v)", ok, err)
	}
}

func TestMemoryHistoryResetExpiryAllowsTakeover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := NewMemoryBackend()
	scope := ResetScope{BotID: "bot-1"}

	lease, applied, err := backend.AcquireHistoryReset(ctx, scope, "token-old", 20*time.Millisecond)
	if err != nil || !applied {
		t.Fatalf("acquire short lease = (%v, %v)", applied, err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, blocked, err := backend.EffectiveHistoryReset(ctx, scope); err != nil || blocked {
		t.Fatalf("expired lease still effective = (%v, %v)", blocked, err)
	}
	if _, applied, err := backend.AcquireHistoryReset(ctx, scope, "token-new", time.Minute); err != nil || !applied {
		t.Fatalf("takeover after expiry = (%v, %v)", applied, err)
	}
	// The expired owner's token no longer renews or releases the scope.
	if _, ok, err := backend.RenewHistoryReset(ctx, lease, time.Minute); err != nil || ok {
		t.Fatalf("expired owner renew = (%v, %v), want miss", ok, err)
	}
	if ok, err := backend.ReleaseHistoryReset(ctx, lease); err != nil || ok {
		t.Fatalf("expired owner release = (%v, %v), want miss", ok, err)
	}
}

func TestEffectiveResetLeasePrecedence(t *testing.T) {
	t.Parallel()
	bot := &ResetLease{Scope: ResetScope{BotID: "bot-1"}, Token: "bot-token", ExpiresAt: time.Now().Add(time.Minute)}
	sessionLease := ResetLease{
		Scope: ResetScope{BotID: "bot-1", SessionID: "session-1"}, Token: "session-token", ExpiresAt: time.Now().Add(time.Minute),
	}

	// The bot lease always wins.
	if got, ok := effectiveResetLease(ResetScope{BotID: "bot-1", SessionID: "session-1"}, bot, []ResetLease{sessionLease}); !ok || got.Token != "bot-token" {
		t.Fatalf("bot lease precedence = (%#v, %v)", got, ok)
	}
	// A bot-scope query is blocked by any session lease.
	if got, ok := effectiveResetLease(ResetScope{BotID: "bot-1"}, nil, []ResetLease{sessionLease}); !ok || got.Token != "session-token" {
		t.Fatalf("bot query vs session lease = (%#v, %v)", got, ok)
	}
	// A session-scope query is blocked only by its own session's lease.
	if _, ok := effectiveResetLease(ResetScope{BotID: "bot-1", SessionID: "session-2"}, nil, []ResetLease{sessionLease}); ok {
		t.Fatal("sibling session query should not be blocked")
	}
	if got, ok := effectiveResetLease(ResetScope{BotID: "bot-1", SessionID: "session-1"}, nil, []ResetLease{sessionLease}); !ok || got.Token != "session-token" {
		t.Fatalf("own session query = (%#v, %v)", got, ok)
	}
	if _, ok := effectiveResetLease(ResetScope{BotID: "bot-1"}, nil, nil); ok {
		t.Fatal("empty lease set should not block")
	}
}
