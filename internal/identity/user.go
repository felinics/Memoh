package identity

import (
	"fmt"
	"strings"

	ctr "github.com/felinics/memoh/internal/container"
)

// ValidateChannelIdentityID enforces a conservative ID charset for isolation.
func ValidateChannelIdentityID(channelIdentityID string) error {
	if channelIdentityID == "" {
		return fmt.Errorf("%w: channel identity id required", ctr.ErrInvalidArgument)
	}
	const allowedRunes = "-_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for _, r := range channelIdentityID {
		if !strings.ContainsRune(allowedRunes, r) {
			return fmt.Errorf("%w: invalid channel identity id", ctr.ErrInvalidArgument)
		}
	}
	return nil
}
