package application

import (
	"context"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/workdir"
)

// ErrWorkspaceTargetWorkdirConflict is returned when a request tries to move
// a workdir-bound session onto a different workspace target. The binding is
// immutable: the workdir's directory only exists on the workdir's target, so
// honoring the switch would produce a session whose working directory does
// not exist.
var ErrWorkspaceTargetWorkdirConflict = errors.New(
	"this session is bound to a workdir; its workspace target cannot be changed")

// sessionWorkdirResolver is the slice of *workdir.Service the application
// needs: resolving a stored workdir binding to its target and directory.
type sessionWorkdirResolver interface {
	ResolveForSession(ctx context.Context, botID, workdirID string) (workdir.Resolved, error)
}

// SetWorkdirResolver configures resolution of session workdir bindings.
func (s *Service) SetWorkdirResolver(resolver sessionWorkdirResolver) {
	s.workdirs = resolver
}

// resolveSessionWorkdirBinding loads the session's workdir binding, if any.
// Errors fail the turn rather than degrade: silently dropping the binding
// would run the turn in the wrong directory — the exact bug class workdir
// bindings exist to eliminate.
func (s *Service) resolveSessionWorkdirBinding(ctx context.Context, botID, sessionID string) (workdir.Resolved, bool, error) {
	if s == nil || s.workdirs == nil || s.sessionService == nil {
		return workdir.Resolved{}, false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return workdir.Resolved{}, false, nil
	}
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return workdir.Resolved{}, false, err
	}
	if strings.TrimSpace(sess.WorkdirID) == "" {
		return workdir.Resolved{}, false, nil
	}
	resolved, err := s.workdirs.ResolveForSession(ctx, botID, sess.WorkdirID)
	if err != nil {
		return workdir.Resolved{}, false, err
	}
	return resolved, true, nil
}
