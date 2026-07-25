package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

const DefaultGmailProviderName = "Gmail"

// Service manages email provider CRUD and bindings.
type Service struct {
	providers emailport.ProviderStore
	bindings  emailport.BindingStore
	logger    *slog.Logger
	registry  *Registry
}

func NewService(log *slog.Logger, providers emailport.ProviderStore, bindings emailport.BindingStore, registry *Registry) *Service {
	return &Service{
		providers: providers,
		bindings:  bindings,
		logger:    log.With(slog.String("service", "email")),
		registry:  registry,
	}
}

func (s *Service) Registry() *Registry { return s.registry }

// ---- Provider CRUD ----

func (s *Service) ListMeta(_ context.Context) []ProviderMeta {
	return s.registry.ListMeta()
}

func (s *Service) CreateProvider(ctx context.Context, userID string, req CreateProviderRequest) (ProviderResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return ProviderResponse{}, errors.New("name is required")
	}
	if _, err := s.registry.Get(req.Provider); err != nil {
		return ProviderResponse{}, fmt.Errorf("unsupported provider: %s", req.Provider)
	}
	if len(req.Config) > 0 {
		if a, err := s.registry.Get(req.Provider); err == nil {
			normalized, normErr := a.NormalizeConfig(req.Config)
			if normErr != nil {
				return ProviderResponse{}, fmt.Errorf("invalid config: %w", normErr)
			}
			req.Config = normalized
		}
	}
	if req.Config == nil {
		req.Config = make(map[string]any)
	}
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("marshal config: %w", err)
	}
	row, err := s.providers.CreateProvider(ctx, emailport.CreateProviderInput{
		UserID:   userID,
		Name:     strings.TrimSpace(req.Name),
		Provider: string(req.Provider),
		Config:   configJSON,
	})
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("create email provider: %w", mapStoreErr(err))
	}
	return s.toProviderResponse(row), nil
}

func (s *Service) EnsureDefaultGmailProvider(ctx context.Context, userID string) error {
	_, err := s.providers.FindProviderByName(ctx, userID, DefaultGmailProviderName)
	if err == nil {
		return nil
	}
	if !errors.Is(err, emailport.ErrNotFound) {
		return fmt.Errorf("get default gmail provider: %w", err)
	}
	_, err = s.CreateProvider(ctx, userID, CreateProviderRequest{
		Name:     DefaultGmailProviderName,
		Provider: ProviderName("gmail"),
		Config:   map[string]any{},
	})
	return err
}

func (s *Service) GetProvider(ctx context.Context, userID, id string) (ProviderResponse, error) {
	row, err := s.providers.FindProviderForUser(ctx, userID, id)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("get email provider: %w", mapStoreErr(err))
	}
	return s.toProviderResponse(row), nil
}

func (s *Service) GetProviderInternal(ctx context.Context, id string) (ProviderResponse, error) {
	row, err := s.providers.FindProvider(ctx, id)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("get email provider: %w", mapStoreErr(err))
	}
	return s.toProviderResponse(row), nil
}

func (s *Service) ListProviders(ctx context.Context, userID, provider string) ([]ProviderResponse, error) {
	provider = strings.TrimSpace(provider)
	rows, err := s.providers.ListProvidersForUser(ctx, userID, provider)
	if err != nil {
		return nil, fmt.Errorf("list email providers: %w", mapStoreErr(err))
	}
	return s.providerResponses(rows), nil
}

func (s *Service) ListProvidersInternal(ctx context.Context, provider string) ([]ProviderResponse, error) {
	provider = strings.TrimSpace(provider)
	rows, err := s.providers.ListProviders(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("list email providers: %w", mapStoreErr(err))
	}
	return s.providerResponses(rows), nil
}

func (s *Service) providerResponses(rows []emailport.ProviderRecord) []ProviderResponse {
	items := make([]ProviderResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toProviderResponse(row))
	}
	return items
}

func (s *Service) UpdateProvider(ctx context.Context, userID, id string, req UpdateProviderRequest) (ProviderResponse, error) {
	current, err := s.providers.FindProviderForUser(ctx, userID, id)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("get email provider: %w", mapStoreErr(err))
	}
	name := current.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	provider := current.Provider
	if req.Provider != nil {
		if _, err := s.registry.Get(*req.Provider); err != nil {
			return ProviderResponse{}, fmt.Errorf("unsupported provider: %s", *req.Provider)
		}
		provider = string(*req.Provider)
	}
	config := current.Config
	if req.Config != nil {
		if a, aErr := s.registry.Get(ProviderName(provider)); aErr == nil {
			normalized, normErr := a.NormalizeConfig(req.Config)
			if normErr != nil {
				return ProviderResponse{}, fmt.Errorf("invalid config: %w", normErr)
			}
			req.Config = normalized
		}
		configJSON, marshalErr := json.Marshal(req.Config)
		if marshalErr != nil {
			return ProviderResponse{}, fmt.Errorf("marshal config: %w", marshalErr)
		}
		config = configJSON
	}
	updated, err := s.providers.UpdateProvider(ctx, emailport.UpdateProviderInput{
		ID:       id,
		UserID:   userID,
		Name:     name,
		Provider: provider,
		Config:   config,
	})
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("update email provider: %w", mapStoreErr(err))
	}
	return s.toProviderResponse(updated), nil
}

func (s *Service) DeleteProvider(ctx context.Context, userID, id string) error {
	return s.providers.DeleteProvider(ctx, userID, id)
}

func (s *Service) toProviderResponse(row emailport.ProviderRecord) ProviderResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			s.logger.Warn("email provider config unmarshal failed", slog.String("id", row.ID), slog.Any("error", err))
		}
	}
	cfg = sanitizeProviderConfig(row.Provider, cfg)
	return ProviderResponse{
		ID:        row.ID,
		UserID:    row.UserID,
		Name:      row.Name,
		Provider:  row.Provider,
		Config:    cfg,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func sanitizeProviderConfig(provider string, cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}
	clean := make(map[string]any, len(cfg))
	for key, value := range cfg {
		if provider == "gmail" && (key == "client_id" || key == "client_secret") {
			continue
		}
		clean[key] = value
	}
	return clean
}

// ---- Binding CRUD ----

func (s *Service) CreateBinding(ctx context.Context, botID string, req CreateBindingRequest) (BindingResponse, error) {
	canRead, canWrite, canDelete := true, true, false
	if req.CanRead != nil {
		canRead = *req.CanRead
	}
	if req.CanWrite != nil {
		canWrite = *req.CanWrite
	}
	if req.CanDelete != nil {
		canDelete = *req.CanDelete
	}
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return BindingResponse{}, fmt.Errorf("marshal config: %w", err)
	}
	row, err := s.bindings.CreateBinding(ctx, emailport.CreateBindingInput{
		BotID:           botID,
		EmailProviderID: req.EmailProviderID,
		EmailAddress:    strings.TrimSpace(req.EmailAddress),
		CanRead:         canRead,
		CanWrite:        canWrite,
		CanDelete:       canDelete,
		Config:          configJSON,
	})
	if err != nil {
		return BindingResponse{}, fmt.Errorf("create email binding: %w", mapStoreErr(err))
	}
	return s.toBindingResponse(row), nil
}

func (s *Service) GetBinding(ctx context.Context, id string) (BindingResponse, error) {
	row, err := s.bindings.FindBinding(ctx, id)
	if err != nil {
		return BindingResponse{}, fmt.Errorf("get email binding: %w", mapStoreErr(err))
	}
	return s.toBindingResponse(row), nil
}

func (s *Service) ListBindings(ctx context.Context, botID string) ([]BindingResponse, error) {
	rows, err := s.bindings.ListBindings(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("list email bindings: %w", mapStoreErr(err))
	}
	items := make([]BindingResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toBindingResponse(row))
	}
	return items, nil
}

func (s *Service) ListReadableBindingsByProvider(ctx context.Context, providerID string) ([]BindingResponse, error) {
	rows, err := s.bindings.ListReadableBindings(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("list readable bindings: %w", mapStoreErr(err))
	}
	items := make([]BindingResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toBindingResponse(row))
	}
	return items, nil
}

func (s *Service) GetBotBinding(ctx context.Context, botID string) (BindingResponse, error) {
	bindings, err := s.ListBindings(ctx, botID)
	if err != nil {
		return BindingResponse{}, err
	}
	if len(bindings) == 0 {
		return BindingResponse{}, fmt.Errorf("no email binding for bot %s", botID)
	}
	return bindings[0], nil
}

func (s *Service) UpdateBinding(ctx context.Context, id string, req UpdateBindingRequest) (BindingResponse, error) {
	current, err := s.bindings.FindBinding(ctx, id)
	if err != nil {
		return BindingResponse{}, fmt.Errorf("get email binding: %w", mapStoreErr(err))
	}
	emailAddr := current.EmailAddress
	if req.EmailAddress != nil {
		emailAddr = strings.TrimSpace(*req.EmailAddress)
	}
	canRead := current.CanRead
	if req.CanRead != nil {
		canRead = *req.CanRead
	}
	canWrite := current.CanWrite
	if req.CanWrite != nil {
		canWrite = *req.CanWrite
	}
	canDelete := current.CanDelete
	if req.CanDelete != nil {
		canDelete = *req.CanDelete
	}
	config := current.Config
	if req.Config != nil {
		configJSON, marshalErr := json.Marshal(req.Config)
		if marshalErr != nil {
			return BindingResponse{}, fmt.Errorf("marshal config: %w", marshalErr)
		}
		config = configJSON
	}
	updated, err := s.bindings.UpdateBinding(ctx, emailport.UpdateBindingInput{
		ID:           id,
		EmailAddress: emailAddr,
		CanRead:      canRead,
		CanWrite:     canWrite,
		CanDelete:    canDelete,
		Config:       config,
	})
	if err != nil {
		return BindingResponse{}, fmt.Errorf("update email binding: %w", mapStoreErr(err))
	}
	return s.toBindingResponse(updated), nil
}

func (s *Service) DeleteBinding(ctx context.Context, id string) error {
	return s.bindings.DeleteBinding(ctx, id)
}

func (s *Service) toBindingResponse(row emailport.BindingRecord) BindingResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			s.logger.Warn("email binding config unmarshal failed", slog.String("id", row.ID), slog.Any("error", err))
		}
	}
	return BindingResponse{
		ID:              row.ID,
		BotID:           row.BotID,
		EmailProviderID: row.EmailProviderID,
		EmailAddress:    row.EmailAddress,
		CanRead:         row.CanRead,
		CanWrite:        row.CanWrite,
		CanDelete:       row.CanDelete,
		Config:          cfg,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

// ProviderConfig returns the deserialized config for a given provider ID.
func (s *Service) ProviderConfig(ctx context.Context, providerID string) (ProviderName, map[string]any, error) {
	resp, err := s.GetProviderInternal(ctx, providerID)
	if err != nil {
		return "", nil, err
	}
	return ProviderName(resp.Provider), resp.Config, nil
}
