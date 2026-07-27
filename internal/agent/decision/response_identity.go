package decision

import (
	"context"
	"strings"
)

// ResponseIdentity is the client retry identity attached to one durable
// decision transition.
type ResponseIdentity struct {
	ControlID   string
	PayloadHash string
}

type responseIdentityContextKey struct{}

func WithResponseIdentity(ctx context.Context, identity ResponseIdentity) context.Context {
	identity.ControlID = strings.TrimSpace(identity.ControlID)
	identity.PayloadHash = strings.TrimSpace(identity.PayloadHash)
	if ctx == nil || identity.ControlID == "" || identity.PayloadHash == "" {
		return ctx
	}
	return context.WithValue(ctx, responseIdentityContextKey{}, identity)
}

func ResponseIdentityFromContext(ctx context.Context) (ResponseIdentity, bool) {
	if ctx == nil {
		return ResponseIdentity{}, false
	}
	identity, ok := ctx.Value(responseIdentityContextKey{}).(ResponseIdentity)
	identity.ControlID = strings.TrimSpace(identity.ControlID)
	identity.PayloadHash = strings.TrimSpace(identity.PayloadHash)
	return identity, ok && identity.ControlID != "" && identity.PayloadHash != ""
}
