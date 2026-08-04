package project

import (
	"context"
	"errors"
	"strings"

	"github.com/memohai/memoh/internal/db"
	dbstore "github.com/memohai/memoh/internal/db/store"
	"github.com/memohai/memoh/internal/workspace"
)

// ResolveForSession resolves a session's project binding to the target it
// pins and the working directory it dictates. It reads only stored state —
// no bridge round-trip — so it is safe on the turn hot path. Archived
// projects still resolve: a session's working directory never changes
// underneath it.
func (s *Service) ResolveForSession(ctx context.Context, botID, projectID string) (Resolved, error) {
	if s == nil || s.store == nil {
		return Resolved{}, errors.New("project service not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return Resolved{}, ErrProjectNotFound
	}
	record, err := s.store.GetProject(ctx, botID, projectID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Resolved{}, ErrProjectNotFound
		}
		return Resolved{}, err
	}
	return resolvedFromRecord(record), nil
}

func resolvedFromRecord(record dbstore.BotProjectRecord) Resolved {
	targetID := workspace.WorkspaceTargetNative
	if record.TargetKind == TargetKindRemote {
		targetID = record.RemoteBindingID
	}
	return Resolved{
		ProjectID: record.ID,
		TargetID:  targetID,
		Kind:      record.TargetKind,
		WorkDir:   record.Path,
	}
}
