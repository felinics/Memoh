package workdir

import (
	"context"
	"errors"
	"strings"

	"github.com/felinics/memoh/internal/db"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/workspace"
)

// ResolveForSession resolves a session's workdir binding to the target it
// pins and the working directory it dictates. It reads only stored state —
// no bridge round-trip — so it is safe on the turn hot path. Archived
// workdirs still resolve: a session's working directory never changes
// underneath it.
func (s *Service) ResolveForSession(ctx context.Context, botID, workdirID string) (Resolved, error) {
	if s == nil || s.store == nil {
		return Resolved{}, errors.New("workdir service not configured")
	}
	workdirID = strings.TrimSpace(workdirID)
	if workdirID == "" {
		return Resolved{}, ErrWorkdirNotFound
	}
	record, err := s.store.GetWorkdir(ctx, botID, workdirID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return Resolved{}, ErrWorkdirNotFound
		}
		return Resolved{}, err
	}
	return resolvedFromRecord(record), nil
}

func resolvedFromRecord(record dbstore.BotWorkdirRecord) Resolved {
	targetID := workspace.WorkspaceTargetNative
	if record.TargetKind == TargetKindRemote {
		targetID = record.RemoteBindingID
	}
	return Resolved{
		WorkdirID: record.ID,
		TargetID:  targetID,
		Kind:      record.TargetKind,
		WorkDir:   record.Path,
	}
}
