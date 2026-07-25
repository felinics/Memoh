package email

import "context"

// Runtime is the live Email adapter surface hosted by the Channel process.
type Runtime interface {
	RefreshProvider(context.Context, string) error
	SendEmail(context.Context, string, string, OutboundEmail) (string, error)
}
