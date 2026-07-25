package assembly

import (
	"log/slog"

	channelhealth "github.com/memohai/memoh/domains/channel/internal/health"
	"github.com/memohai/memoh/internal/healthcheck"
)

// NewHealthChecker constructs the Channel connection health checker.
// cmd must call this constructor rather than importing owner-private health.
func NewHealthChecker(log *slog.Logger, observer channelhealth.ConnectionObserver) healthcheck.Checker {
	return channelhealth.NewChecker(log, observer)
}
