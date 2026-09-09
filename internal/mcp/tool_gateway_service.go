package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/felinics/memoh/internal/toolcontext"
)

const (
	defaultToolRegistryCacheTTL = 5 * time.Second
)

type cachedToolRegistry struct {
	expiresAt time.Time
	registry  *ToolRegistry
}

// ToolGatewayService federates tools from gateway sources, including external
// MCP connections and selected native Memoh tools exposed to ACP runtimes.
type ToolGatewayService struct {
	logger   *slog.Logger
	sources  []ToolSource
	cacheTTL time.Duration
	limit    ToolOutputLimit

	mu    sync.Mutex
	cache map[string]cachedToolRegistry
}

type ToolGatewayOption func(*ToolGatewayService)

func WithToolOutputLimit(limit ToolOutputLimit) ToolGatewayOption {
	return func(s *ToolGatewayService) {
		s.limit = limit
	}
}

func NewToolGatewayService(log *slog.Logger, sources []ToolSource, opts ...ToolGatewayOption) *ToolGatewayService {
	if log == nil {
		log = slog.Default()
	}
	filteredSources := make([]ToolSource, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			filteredSources = append(filteredSources, source)
		}
	}
	svc := &ToolGatewayService{
		logger:   log.With(slog.String("service", "tool_gateway")),
		sources:  filteredSources,
		cacheTTL: defaultToolRegistryCacheTTL,
		cache:    map[string]cachedToolRegistry{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc
}

func (s *ToolGatewayService) ListTools(ctx context.Context, session ToolSessionContext) ([]ToolDescriptor, error) {
	registry, err := s.getRegistry(ctx, session, false)
	if err != nil {
		return nil, err
	}
	return registry.List(), nil
}

func (s *ToolGatewayService) LookupTool(ctx context.Context, session ToolSessionContext, toolName string) (ToolDescriptor, bool, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return ToolDescriptor{}, false, nil
	}
	registry, err := s.getRegistry(ctx, session, false)
	if err != nil {
		return ToolDescriptor{}, false, err
	}
	if _, desc, ok := registry.Lookup(toolName); ok {
		return desc, true, nil
	}
	registry, err = s.getRegistry(ctx, session, true)
	if err != nil {
		return ToolDescriptor{}, false, err
	}
	_, desc, ok := registry.Lookup(toolName)
	return desc, ok, nil
}

func (s *ToolGatewayService) CallTool(ctx context.Context, session ToolSessionContext, payload ToolCallPayload) (map[string]any, error) {
	ctx, cancel := toolcontext.Bind(ctx, session)
	defer cancel()

	toolName := strings.TrimSpace(payload.Name)
	if toolName == "" {
		return nil, errors.New("tool name is required")
	}

	registry, err := s.getRegistry(ctx, session, false)
	if err != nil {
		return nil, err
	}
	source, _, ok := registry.Lookup(toolName)
	if !ok {
		registry, err = s.getRegistry(ctx, session, true)
		if err != nil {
			return nil, err
		}
		source, _, ok = registry.Lookup(toolName)
		if !ok {
			return s.limitResult(toolName, BuildToolErrorResult("tool not found: "+toolName)), nil
		}
	}

	arguments := payload.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	if err := toolcontext.ValidateRuntimeGuard(ctx, session); err != nil {
		return nil, err
	}
	result, err := source.CallTool(ctx, session, toolName, arguments)
	if err != nil {
		if errors.Is(err, ErrToolNotFound) {
			return s.limitResult(toolName, BuildToolErrorResult("tool not found: "+toolName)), nil
		}
		return s.limitResult(toolName, BuildToolErrorResult(err.Error())), nil
	}
	if result == nil {
		return s.limitResult(toolName, BuildToolSuccessResult(map[string]any{"ok": true})), nil
	}
	return s.limitResult(toolName, result), nil
}

func (s *ToolGatewayService) limitResult(toolName string, result map[string]any) map[string]any {
	limit := ToolOutputLimit{}
	if s != nil {
		limit = s.limit
	}
	return LimitToolResult(result, "tool result ("+toolName+")", limit)
}

func (s *ToolGatewayService) getRegistry(ctx context.Context, session ToolSessionContext, force bool) (*ToolRegistry, error) {
	botID := strings.TrimSpace(session.BotID)
	if botID == "" {
		return nil, errors.New("bot id is required")
	}
	cacheKey := toolRegistryCacheKey(session)
	if !force {
		s.mu.Lock()
		cached, ok := s.cache[cacheKey]
		if ok && time.Now().Before(cached.expiresAt) && cached.registry != nil {
			s.mu.Unlock()
			return cached.registry, nil
		}
		s.mu.Unlock()
	}

	registry := NewToolRegistry()
	for _, source := range s.sources {
		tools, err := source.ListTools(ctx, session)
		if err != nil {
			s.logger.Warn("list tools from source failed", slog.Any("error", err))
			continue
		}
		for _, tool := range tools {
			if err := registry.Register(source, tool); err != nil {
				s.logger.Warn("skip duplicated/invalid tool", slog.String("tool", tool.Name), slog.Any("error", err))
			}
		}
	}

	s.mu.Lock()
	s.cache[cacheKey] = cachedToolRegistry{
		expiresAt: time.Now().Add(s.cacheTTL),
		registry:  registry,
	}
	s.mu.Unlock()
	return registry, nil
}

func toolRegistryCacheKey(session ToolSessionContext) string {
	parts := []string{
		strings.TrimSpace(session.BotID),
		strings.TrimSpace(session.ChatID),
		strings.TrimSpace(session.RuntimeID),
		hashCacheKeySecret(session.RuntimeToken),
		strings.TrimSpace(session.SessionID),
		strings.TrimSpace(session.RunID),
		strings.TrimSpace(session.SessionType),
		strings.TrimSpace(session.RouteID),
		strings.TrimSpace(session.ChannelIdentityID),
		strings.TrimSpace(session.CurrentPlatform),
		strings.TrimSpace(session.ReplyTarget),
		strings.TrimSpace(session.ConversationType),
		hashCacheKeySecret(session.SessionToken),
	}
	if session.IsSubagent {
		parts = append(parts, "subagent")
	} else {
		parts = append(parts, "agent")
	}
	if session.SupportsImageInput {
		parts = append(parts, "vision")
	} else {
		parts = append(parts, "no-vision")
	}
	if session.CanRequestUserInput {
		parts = append(parts, "user-input")
	} else {
		parts = append(parts, "no-user-input")
	}
	if session.CanListUserInput {
		parts = append(parts, "list-user-input")
	} else {
		parts = append(parts, "no-list-user-input")
	}
	return strings.Join(parts, "\x00")
}

func hashCacheKeySecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
