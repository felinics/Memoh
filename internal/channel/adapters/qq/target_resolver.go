package qq

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	identitypkg "github.com/felinics/memoh/internal/channel/identities"
)

var qqOpenIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{32}$`)

func (a *QQAdapter) resolveTarget(ctx context.Context, raw string) (string, error) {
	target := normalizeTarget(raw)
	if !strings.HasPrefix(target, "c2c:") {
		return target, nil
	}
	id := strings.TrimSpace(strings.TrimPrefix(target, "c2c:"))
	if !qqUUIDTargetPattern.MatchString(id) {
		return target, nil
	}
	if mapped, found, err := a.resolveRouteTarget(ctx, id); err != nil {
		return "", err
	} else if found {
		return normalizeTarget(mapped), nil
	}
	if mapped, found, err := a.resolveIdentityTarget(ctx, id); err != nil {
		return "", err
	} else if found {
		return normalizeTarget(mapped), nil
	}
	return target, nil
}

func (a *QQAdapter) resolveRouteTarget(ctx context.Context, routeID string) (string, bool, error) {
	resolver := a.getRouteResolver()
	if resolver == nil {
		return "", false, nil
	}
	item, err := resolver.GetByID(ctx, routeID)
	if err != nil {
		if isQQLookupMiss(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !strings.EqualFold(strings.TrimSpace(item.Platform), string(Type)) {
		return "", false, nil
	}
	target := strings.TrimSpace(item.ReplyTarget)
	if target == "" {
		return "", false, nil
	}
	return target, true, nil
}

func (a *QQAdapter) resolveIdentityTarget(ctx context.Context, id string) (string, bool, error) {
	resolver := a.getIdentityResolver()
	if resolver == nil {
		return "", false, nil
	}
	if mapped, found, err := lookupQQIdentityTarget(ctx, resolver.ListCanonicalChannelIdentities, id); err != nil {
		return "", false, err
	} else if found {
		return mapped, true, nil
	}
	item, err := resolver.GetByID(ctx, id)
	if err != nil {
		if isQQLookupMiss(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if mapped := qqIdentityTarget(item); mapped != "" {
		return mapped, true, nil
	}
	return "", false, nil
}

func lookupQQIdentityTarget(ctx context.Context, lookup func(context.Context, string) ([]identitypkg.ChannelIdentity, error), id string) (string, bool, error) {
	items, err := lookup(ctx, id)
	if err != nil {
		if isQQLookupMiss(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if mapped := firstQQIdentityTarget(items); mapped != "" {
		return mapped, true, nil
	}
	return "", false, nil
}

func firstQQIdentityTarget(items []identitypkg.ChannelIdentity) string {
	for _, item := range items {
		if target := qqIdentityTarget(item); target != "" {
			return target
		}
	}
	return ""
}

func qqIdentityTarget(item identitypkg.ChannelIdentity) string {
	if !strings.EqualFold(strings.TrimSpace(item.Channel), string(Type)) {
		return ""
	}
	subjectID := strings.TrimSpace(item.ChannelSubjectID)
	if !qqOpenIDPattern.MatchString(subjectID) {
		return ""
	}
	return "c2c:" + subjectID
}

func isQQLookupMiss(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, identitypkg.ErrChannelIdentityNotFound)
}

func (a *QQAdapter) getRouteResolver() routeResolver {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.routes
}

func (a *QQAdapter) getIdentityResolver() channelIdentityResolver {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.identity
}
