package grpctransport

import (
	"errors"

	"github.com/felinics/memoh/internal/apperror"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
)

// threadError gives the External Agent thread sentinels their catalog codes
// before they cross the wire: errors.Is cannot survive serialization, and the
// channel process renders these by code. Any other error is returned
// unchanged.
func threadError(err error) error {
	if apperror.CodeOf(err) != "" {
		return err
	}
	var code apperror.Code
	switch {
	case errors.Is(err, sessionpkg.ErrACPAgentIDRequired), errors.Is(err, sessionpkg.ErrACPAgentNotConfigured):
		code = apperror.CodeACPAgentNotConfigured
	case errors.Is(err, sessionpkg.ErrACPUnknownAgent):
		code = apperror.CodeACPAgentNotFound
	case errors.Is(err, sessionpkg.ErrACPAgentNotEnabled):
		code = apperror.CodeACPAgentNotEnabled
	case errors.Is(err, sessionpkg.ErrACPRuntimeOwnerMissing):
		code = apperror.CodeACPRuntimeOwnerMissing
	default:
		return err
	}
	return apperror.Wrap(code, err, nil)
}
