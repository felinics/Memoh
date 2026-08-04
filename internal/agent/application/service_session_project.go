package application

import (
	"context"
	"errors"
	"strings"

	"github.com/memohai/memoh/internal/project"
)

// ErrWorkspaceTargetProjectConflict is returned when a request tries to move
// a project-bound session onto a different workspace target. The binding is
// immutable: the project's directory only exists on the project's target, so
// honoring the switch would produce a session whose working directory does
// not exist.
var ErrWorkspaceTargetProjectConflict = errors.New(
	"this session is bound to a project; its workspace target cannot be changed")

// sessionProjectResolver is the slice of *project.Service the application
// needs: resolving a stored project binding to its target and directory.
type sessionProjectResolver interface {
	ResolveForSession(ctx context.Context, botID, projectID string) (project.Resolved, error)
}

// SetProjectResolver configures resolution of session project bindings.
func (s *Service) SetProjectResolver(resolver sessionProjectResolver) {
	s.projects = resolver
}

// resolveSessionProjectBinding loads the session's project binding, if any.
// Errors fail the turn rather than degrade: silently dropping the binding
// would run the turn in the wrong directory — the exact bug class project
// bindings exist to eliminate.
func (s *Service) resolveSessionProjectBinding(ctx context.Context, botID, sessionID string) (project.Resolved, bool, error) {
	if s == nil || s.projects == nil || s.sessionService == nil {
		return project.Resolved{}, false, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return project.Resolved{}, false, nil
	}
	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return project.Resolved{}, false, err
	}
	if strings.TrimSpace(sess.ProjectID) == "" {
		return project.Resolved{}, false, nil
	}
	resolved, err := s.projects.ResolveForSession(ctx, botID, sess.ProjectID)
	if err != nil {
		return project.Resolved{}, false, err
	}
	return resolved, true, nil
}
