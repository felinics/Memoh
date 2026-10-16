package application

import (
	"context"
	"errors"
	"strings"
)

// SessionResumeScopePage contains opaque tenant IDs, never cross-tenant rows.
// Hosted deployments can reuse their runtime reaper's scope catalog.
type SessionResumeScopePage struct {
	Scopes     []string
	NextCursor string
	Complete   bool
}

// SessionResumeScopeProvider binds every resume operation to its database scope.
// Implementations must enumerate through a restricted catalog, then use ordinary
// tenant-bound credentials for run queries, authorization and execution.
type SessionResumeScopeProvider interface {
	CurrentScope(context.Context) string
	ListScopePage(context.Context, string, int32) (SessionResumeScopePage, error)
	BindScope(context.Context, string) context.Context
}

type singletonSessionResumeScopes struct{ teamID string }

func (p singletonSessionResumeScopes) CurrentScope(context.Context) string { return p.teamID }
func (p singletonSessionResumeScopes) ListScopePage(context.Context, string, int32) (SessionResumeScopePage, error) {
	return SessionResumeScopePage{Scopes: []string{p.teamID}, Complete: true}, nil
}

func (singletonSessionResumeScopes) BindScope(ctx context.Context, _ string) context.Context {
	return ctx
}

// SetSessionResumeScopeProvider installs hosted tenancy before startup. The OSS
// default names only its singleton team; it never enumerates all session rows.
func (s *Service) SetSessionResumeScopeProvider(provider SessionResumeScopeProvider) {
	s.resumeScopes = provider
}

func (s *Service) sessionResumeScopes() SessionResumeScopeProvider {
	if s.resumeScopes != nil {
		return s.resumeScopes
	}
	return singletonSessionResumeScopes{teamID: s.allowedTeam}
}

var errResumeScopeMismatch = errors.New("session resume tenant scope mismatch")

func bindSessionResumeScope(ctx context.Context, provider SessionResumeScopeProvider, scope string) (context.Context, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return nil, errResumeScopeMismatch
	}
	scoped := provider.BindScope(ctx, scope)
	if scoped == nil || strings.TrimSpace(provider.CurrentScope(scoped)) != scope {
		return nil, errResumeScopeMismatch
	}
	return scoped, nil
}
