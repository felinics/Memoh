package botagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/agent/runtime/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agent/runtime/codex/codexcfg"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimekind"
)

var (
	ErrNotFound        = errors.New("bot agent not found")
	ErrNameTaken       = errors.New("bot agent name already taken")
	ErrInvalidRuntime  = errors.New("invalid bot agent runtime")
	ErrInvalidMetadata = errors.New("invalid bot agent metadata")
	ErrDefaultInUse    = errors.New("default bot agent cannot be disabled or deleted")
	ErrUnavailable     = errors.New("bot agent is unavailable")
	// ErrProviderDirectRuntime rejects new ACP agents for providers that now
	// run as direct runtimes (migration 0144 converted the existing rows).
	ErrProviderDirectRuntime = errors.New("this provider runs as a direct runtime; create the agent with runtime codex or claude-code")
)

// directRuntimeForProvider maps former ACP providers to their direct runtime.
func directRuntimeForProvider(provider string) (string, bool) {
	if runtimekind.IsDirect(provider) {
		return provider, true
	}
	return "", false
}

type ConfigurationError struct {
	Field string
}

func (e *ConfigurationError) Error() string {
	if e == nil || strings.TrimSpace(e.Field) == "" {
		return "bot agent configuration is incomplete"
	}
	return fmt.Sprintf("bot agent configuration is missing %s", e.Field)
}

type queries interface {
	BotAgentIsDefault(context.Context, sqlc.BotAgentIsDefaultParams) (bool, error)
	CountBotAgentCredentialRefs(context.Context, pgtype.UUID) (int64, error)
	CreateBotAgent(context.Context, sqlc.CreateBotAgentParams) (sqlc.BotAgent, error)
	FindActiveBotAgentByRuntimeProvider(context.Context, sqlc.FindActiveBotAgentByRuntimeProviderParams) (sqlc.BotAgent, error)
	GetBotAgentByID(context.Context, sqlc.GetBotAgentByIDParams) (sqlc.BotAgent, error)
	ListBotAgents(context.Context, pgtype.UUID) ([]sqlc.BotAgent, error)
	RevokeAgentCredentialByID(context.Context, pgtype.UUID) (sqlc.AgentCredential, error)
	SoftDeleteBotAgent(context.Context, sqlc.SoftDeleteBotAgentParams) (sqlc.BotAgent, error)
	UpdateBotAgent(context.Context, sqlc.UpdateBotAgentParams) (sqlc.BotAgent, error)
}

type transactionalQueries interface {
	SupportsTransactions() bool
	InTx(context.Context, func(dbstore.Queries) error) error
}

type Service struct {
	queries            queries
	logger             *slog.Logger
	credentialResolver func(ctx context.Context, botID, botAgentID string) string
}

// SetCredentialVerifier installs the per-instance decryptability check used
// by preflight. It must actually open the ciphertext: after a restart with a
// different-but-valid encryption key the credential row still exists and the
// key shape is fine, but every runtime start would fail — preflight has to
// fail the same way instead of deferring the error.
func (s *Service) SetCredentialResolver(resolver func(ctx context.Context, botID, botAgentID string) string) {
	if s != nil {
		s.credentialResolver = resolver
	}
}

func (s *Service) credentialAuthKind(ctx context.Context, agent BotAgent) string {
	if s == nil || s.credentialResolver == nil || agent.AgentCredentialID == "" {
		return ""
	}
	return s.credentialResolver(ctx, agent.BotID, agent.ID)
}

// ValidateConfiguration is the credential-store-aware preflight; see the
// package-level ValidateConfigurationWithStore for the pure form.
func (s *Service) ValidateConfiguration(ctx context.Context, agent BotAgent, botMetadata map[string]any) error {
	return ValidateConfigurationWithStore(agent, botMetadata, s.credentialAuthKind(ctx, agent))
}

func NewService(log *slog.Logger, q queries) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		queries: q,
		logger:  log.With(slog.String("service", "bot_agents")),
	}
}

func (s *Service) Create(ctx context.Context, botID string, req CreateRequest) (BotAgent, error) {
	if s == nil || s.queries == nil {
		return BotAgent{}, errors.New("bot agent queries not configured")
	}
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return BotAgent{}, ErrNotFound
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return BotAgent{}, ErrInvalidMetadata
	}
	runtime, metadata, err := normalizeDescriptor(req.Runtime, req.Metadata)
	if err != nil {
		return BotAgent{}, err
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return BotAgent{}, fmt.Errorf("marshal bot agent metadata: %w", err)
	}
	row, err := s.queries.CreateBotAgent(ctx, sqlc.CreateBotAgentParams{
		BotID:    pgBotID,
		Name:     name,
		Runtime:  runtime,
		Enabled:  true,
		Metadata: payload,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return BotAgent{}, ErrNameTaken
		}
		return BotAgent{}, err
	}
	return fromRow(row)
}

func (s *Service) List(ctx context.Context, botID string) ([]BotAgent, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return nil, ErrNotFound
	}
	rows, err := s.queries.ListBotAgents(ctx, pgBotID)
	if err != nil {
		return nil, err
	}
	items := make([]BotAgent, 0, len(rows))
	for _, row := range rows {
		item, err := fromRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) Get(ctx context.Context, botID, id string) (BotAgent, error) {
	pgBotID, pgID, err := parseIDs(botID, id)
	if err != nil {
		return BotAgent{}, ErrNotFound
	}
	row, err := s.queries.GetBotAgentByID(ctx, sqlc.GetBotAgentByIDParams{BotID: pgBotID, ID: pgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return BotAgent{}, ErrNotFound
	}
	if err != nil {
		return BotAgent{}, err
	}
	return fromRow(row)
}

// GetActive is used for new defaults and new sessions. Existing sessions may
// keep using Get so disabling an Agent does not terminate established work.
func (s *Service) GetActive(ctx context.Context, botID, id string) (BotAgent, error) {
	agent, err := s.Get(ctx, botID, id)
	if err != nil {
		return BotAgent{}, err
	}
	if !agent.Enabled || agent.DeletedAt != nil {
		return BotAgent{}, ErrUnavailable
	}
	return agent, nil
}

func (s *Service) FindActiveByProvider(ctx context.Context, botID, provider string) (BotAgent, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return BotAgent{}, ErrNotFound
	}
	provider = acpprofile.NormalizeAgentID(provider)
	runtime := RuntimeACP
	if direct, ok := directRuntimeForProvider(provider); ok {
		// Former ACP providers now run as direct runtimes; migrated rows keep
		// their provider metadata, so the same lookup shape still matches.
		runtime = direct
	} else if _, ok := acpprofile.Lookup(provider); !ok {
		return BotAgent{}, ErrInvalidMetadata
	}
	row, err := s.queries.FindActiveBotAgentByRuntimeProvider(ctx, sqlc.FindActiveBotAgentByRuntimeProviderParams{
		BotID:    pgBotID,
		Runtime:  runtime,
		Provider: provider,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return BotAgent{}, ErrNotFound
	}
	if err != nil {
		return BotAgent{}, err
	}
	return fromRow(row)
}

func (s *Service) Update(ctx context.Context, botID, id string, req UpdateRequest) (BotAgent, error) {
	current, err := s.Get(ctx, botID, id)
	if err != nil {
		return BotAgent{}, err
	}
	name := current.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
		if name == "" {
			return BotAgent{}, ErrInvalidMetadata
		}
	}
	enabled := current.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	metadata := current.Metadata
	metadataSet := req.Metadata != nil
	if metadataSet {
		_, metadata, err = normalizeDescriptor(current.Runtime, req.Metadata)
		if err != nil {
			return BotAgent{}, err
		}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return BotAgent{}, fmt.Errorf("marshal bot agent metadata: %w", err)
	}
	pgBotID, pgID, _ := parseIDs(botID, id)
	var row sqlc.BotAgent
	err = s.withBotMutationLock(ctx, pgBotID, func(q queries) error {
		var updateErr error
		row, updateErr = q.UpdateBotAgent(ctx, sqlc.UpdateBotAgentParams{
			Name:        name,
			Enabled:     enabled,
			MetadataSet: metadataSet,
			Metadata:    metadataJSON,
			BotID:       pgBotID,
			ID:          pgID,
		})
		if !errors.Is(updateErr, pgx.ErrNoRows) {
			return updateErr
		}
		if !enabled {
			isDefault, checkErr := q.BotAgentIsDefault(ctx, sqlc.BotAgentIsDefaultParams{BotID: pgBotID, ID: pgID})
			if checkErr != nil {
				return checkErr
			}
			if isDefault {
				return ErrDefaultInUse
			}
		}
		return ErrNotFound
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return BotAgent{}, ErrNameTaken
		}
		return BotAgent{}, err
	}
	return fromRow(row)
}

func (s *Service) Delete(ctx context.Context, botID, id string, beforeCommit func(BotAgent) error) error {
	pgBotID, pgID, err := parseIDs(botID, id)
	if err != nil {
		return ErrNotFound
	}
	err = s.withBotMutationLock(ctx, pgBotID, func(q queries) error {
		row, deleteErr := q.SoftDeleteBotAgent(ctx, sqlc.SoftDeleteBotAgentParams{BotID: pgBotID, ID: pgID})
		if !errors.Is(deleteErr, pgx.ErrNoRows) {
			if deleteErr != nil {
				return deleteErr
			}
			deleted, err := fromRow(row)
			if err != nil {
				return err
			}
			if beforeCommit != nil {
				if err := beforeCommit(deleted); err != nil {
					return err
				}
			}
			if !row.AgentCredentialID.Valid {
				return nil
			}
			refs, err := q.CountBotAgentCredentialRefs(ctx, row.AgentCredentialID)
			if err != nil {
				return err
			}
			if refs == 0 {
				_, err = q.RevokeAgentCredentialByID(ctx, row.AgentCredentialID)
			}
			return err
		}
		isDefault, checkErr := q.BotAgentIsDefault(ctx, sqlc.BotAgentIsDefaultParams{BotID: pgBotID, ID: pgID})
		if checkErr != nil {
			return checkErr
		}
		if isDefault {
			return ErrDefaultInUse
		}
		return ErrNotFound
	})
	if err != nil {
		return err
	}
	return nil
}

// withBotMutationLock serializes Agent availability changes with default-Agent
// assignment. Both paths lock the parent bot before reading or writing the
// child Agent, so they share one lock order and cannot publish a disabled
// default. Query-only test doubles retain the historical direct path.
func (s *Service) withBotMutationLock(ctx context.Context, botID pgtype.UUID, fn func(queries) error) error {
	txer, ok := s.queries.(transactionalQueries)
	if !ok || !txer.SupportsTransactions() {
		return fn(s.queries)
	}
	return txer.InTx(ctx, func(q dbstore.Queries) error {
		if _, err := q.LockBotForAgentMutation(ctx, botID); err != nil {
			return err
		}
		return fn(q)
	})
}

func DescriptorFor(agent BotAgent) (Descriptor, error) {
	runtime, metadata, err := normalizeDescriptor(agent.Runtime, agent.Metadata)
	if err != nil {
		return Descriptor{}, err
	}
	provider, _ := metadata[MetadataProviderKey].(string)
	return Descriptor{BotAgentID: agent.ID, Runtime: runtime, Provider: provider}, nil
}

func AcceptsCredential(agent BotAgent, authKind string) bool {
	switch agent.Runtime {
	case RuntimeCodex:
		cfg, err := codexcfg.ParseAgentConfig(agent.Metadata)
		if err != nil {
			return false
		}
		if cfg.Auth == codexcfg.AuthAPIKey {
			return authKind == agentcredential.AuthKindOpenAIAPIKey
		}
		return authKind == agentcredential.AuthKindOpenAICodexOAuth
	case RuntimeClaudeCode:
		cfg, err := claudecfg.ParseAgentConfig(agent.Metadata)
		if err != nil {
			return false
		}
		switch cfg.Auth {
		case claudecfg.AuthAPIKey:
			return authKind == agentcredential.AuthKindAnthropicAPIKey
		case claudecfg.AuthOAuthToken:
			return authKind == agentcredential.AuthKindClaudeCodeOAuth
		default:
			return false
		}
	default:
		return false
	}
}

// ValidateConfigurationWithStore validates the shared per-provider bot
// metadata without consulting the legacy metadata enabled flag.
// BotAgent.Enabled is the availability source of truth.
func ValidateConfigurationWithStore(agent BotAgent, botMetadata map[string]any, credentialAuthKind string) error {
	descriptor, err := DescriptorFor(agent)
	if err != nil {
		return err
	}
	switch descriptor.Runtime {
	case RuntimeACP:
		profile, ok := acpprofile.Lookup(descriptor.Provider)
		if !ok {
			return ErrInvalidMetadata
		}
		setup := acpprofile.ParseAgentSetup(botMetadata, descriptor.Provider)
		if field, missing := acpprofile.MissingRequiredManagedFieldForPreflight(profile, setup); missing {
			return &ConfigurationError{Field: field.ID}
		}
		return nil
	case RuntimeCodex:
		if _, err := codexcfg.ParseAgentConfig(agent.Metadata); err != nil {
			return &ConfigurationError{Field: "auth"}
		}
		expected := agentcredential.AuthKindOpenAICodexOAuth
		if cfg, _ := codexcfg.ParseAgentConfig(agent.Metadata); cfg.Auth == codexcfg.AuthAPIKey {
			expected = agentcredential.AuthKindOpenAIAPIKey
		}
		if credentialAuthKind != expected {
			return &ConfigurationError{Field: "agent_credential_id"}
		}
		return nil
	case RuntimeClaudeCode:
		cfg, err := claudecfg.ParseAgentConfig(agent.Metadata)
		if err != nil {
			return &ConfigurationError{Field: "auth"}
		}
		expected := ""
		switch cfg.Auth {
		case claudecfg.AuthAPIKey:
			expected = agentcredential.AuthKindAnthropicAPIKey
		case claudecfg.AuthOAuthToken:
			expected = agentcredential.AuthKindClaudeCodeOAuth
		}
		if credentialAuthKind != expected {
			return &ConfigurationError{Field: "agent_credential_id"}
		}
		return nil
	default:
		return ErrInvalidRuntime
	}
}

func normalizeDescriptor(runtime string, metadata map[string]any) (string, map[string]any, error) {
	runtime = strings.ToLower(strings.TrimSpace(runtime))
	switch runtime {
	case RuntimeACP:
		if metadata == nil {
			return "", nil, ErrInvalidMetadata
		}
		provider, ok := metadata[MetadataProviderKey].(string)
		if !ok {
			return "", nil, ErrInvalidMetadata
		}
		provider = acpprofile.NormalizeAgentID(provider)
		if _, direct := directRuntimeForProvider(provider); direct {
			return "", nil, ErrProviderDirectRuntime
		}
		if _, ok := acpprofile.Lookup(provider); !ok {
			return "", nil, ErrInvalidMetadata
		}
		normalized := make(map[string]any, len(metadata))
		for key, value := range metadata {
			normalized[key] = value
		}
		normalized[MetadataProviderKey] = provider
		return runtime, normalized, nil
	case RuntimeCodex, RuntimeClaudeCode:
		normalized := make(map[string]any, len(metadata)+1)
		for key, value := range metadata {
			if isCredentialField(key) {
				return "", nil, ErrInvalidMetadata
			}
			normalized[key] = value
		}
		// A direct runtime's provider identity is the runtime itself. Pinning
		// it keeps Descriptor.Provider non-empty for every consumer that
		// addresses agents by provider (default chat runtime settings, the
		// provider lookup) — migrated rows already carry the same value.
		normalized[MetadataProviderKey] = runtime
		return runtime, normalized, nil
	default:
		return "", nil, ErrInvalidRuntime
	}
}

func isCredentialField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "api_key", "oauth_token", "access_token", "refresh_token", "id_token", "password", "secret", "token":
		return true
	default:
		return false
	}
}

func parseIDs(botID, id string) (pgtype.UUID, pgtype.UUID, error) {
	pgBotID, err := db.ParseUUID(botID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return pgBotID, pgID, nil
}

func fromRow(row sqlc.BotAgent) (BotAgent, error) {
	metadata := map[string]any{}
	if len(row.Metadata) > 0 {
		if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
			return BotAgent{}, fmt.Errorf("decode bot agent metadata: %w", err)
		}
	}
	item := BotAgent{
		ID:        uuidString(row.ID),
		BotID:     uuidString(row.BotID),
		Name:      row.Name,
		Runtime:   row.Runtime,
		Enabled:   row.Enabled,
		Metadata:  metadata,
		CreatedAt: db.TimeFromPg(row.CreatedAt),
		UpdatedAt: db.TimeFromPg(row.UpdatedAt),
	}
	if row.AgentCredentialID.Valid {
		item.AgentCredentialID = row.AgentCredentialID.String()
	}
	if row.DeletedAt.Valid {
		deletedAt := row.DeletedAt.Time
		item.DeletedAt = &deletedAt
	}
	return item, nil
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}
