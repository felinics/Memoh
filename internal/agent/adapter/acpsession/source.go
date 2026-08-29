// Package acpsession adapts Chat thread metadata to the minimal descriptor
// consumed by the ACP runtime.
package acpsession

import (
	"context"
	"errors"
	"strings"

	acp "github.com/felinics/memoh/internal/agent/runtime/acp"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/workdir"
)

type Source struct {
	threads  threadGetter
	workdirs sessionWorkdirResolver
}

type threadGetter interface {
	Get(ctx context.Context, sessionID string) (thread.Thread, error)
}

type sessionWorkdirResolver interface {
	ResolveForSession(ctx context.Context, botID, workdirID string) (workdir.Resolved, error)
}

func NewSource(threads *thread.Service, workdirs *workdir.Service) *Source {
	return &Source{threads: threads, workdirs: workdirs}
}

func newSource(threads threadGetter, workdirs ...sessionWorkdirResolver) *Source {
	var resolver sessionWorkdirResolver
	if len(workdirs) > 0 {
		resolver = workdirs[0]
	}
	return &Source{threads: threads, workdirs: resolver}
}

func (s *Source) Get(ctx context.Context, sessionID string) (acp.SessionDescriptor, error) {
	if s == nil || s.threads == nil {
		return acp.SessionDescriptor{}, errors.New("thread service unavailable")
	}
	item, err := s.threads.Get(ctx, sessionID)
	if err != nil {
		return acp.SessionDescriptor{}, err
	}
	descriptor := acp.SessionDescriptor{
		BotID:           item.BotID,
		SessionType:     item.Type,
		Metadata:        item.Metadata,
		RuntimeMetadata: item.RuntimeMetadata,
		IsACP:           thread.IsACPRuntime(item),
	}
	if workdirID := strings.TrimSpace(item.WorkdirID); workdirID != "" {
		if s.workdirs == nil {
			return acp.SessionDescriptor{}, errors.New("workdir resolver unavailable for bound ACP session")
		}
		resolved, err := s.workdirs.ResolveForSession(ctx, item.BotID, workdirID)
		if err != nil {
			return acp.SessionDescriptor{}, err
		}
		descriptor.WorkspaceTargetID = strings.TrimSpace(resolved.TargetID)
		descriptor.WorkspaceTargetKind = strings.TrimSpace(resolved.Kind)
		descriptor.WorkdirPath = strings.TrimSpace(resolved.WorkDir)
	}
	return descriptor, nil
}
