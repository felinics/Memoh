package model

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/identity/auth"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	providers "github.com/memohai/memoh/domains/model/provider"
	"github.com/memohai/memoh/domains/model/template"
	"github.com/memohai/memoh/internal/apperror"
	"github.com/memohai/memoh/internal/oauth"
)

type ProvidersHandler struct {
	service       *providers.Service
	modelsService *modelcatalog.Service
	logger        *slog.Logger
}

func NewProvidersHandler(log *slog.Logger, service *providers.Service, modelsService *modelcatalog.Service) *ProvidersHandler {
	return &ProvidersHandler{
		service:       service,
		modelsService: modelsService,
		logger:        log.With(slog.String("handler", "providers")),
	}
}

func (h *ProvidersHandler) Register(e *echo.Echo) {
	group := e.Group("/providers")
	group.POST("", h.Create)
	group.POST("/from-template", h.CreateFromTemplate)
	group.GET("", h.List)
	group.GET("/:id", h.Get)
	group.GET("/:id/models", h.ListModelsByProvider)
	group.GET("/name/:name", h.GetByName)
	group.PUT("/:id", h.Update)
	group.DELETE("/:id", h.Delete)
	group.GET("/count", h.Count)
	group.POST("/:id/test", h.Test)
	group.POST("/:id/import-models", h.ImportModels)
}

// CreateFromTemplate godoc
// @Summary Create a provider from a global template
// @Description Materialize a tenant-owned provider only when the user saves a template configuration
// @Tags providers
// @Accept json
// @Produce json
// @Param request body providers.CreateFromTemplateRequest true "Provider template configuration"
// @Success 201 {object} providers.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 409 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /providers/from-template [post].
func (h *ProvidersHandler) CreateFromTemplate(c echo.Context) error {
	var req providers.CreateFromTemplateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind provider from template", err).WithCode(apperror.CodeProviderTemplateRequestInvalid, nil)
	}
	resp, err := h.service.CreateFromTemplate(c.Request().Context(), req)
	if err != nil {
		return mapProviderCreateFromTemplateError("create provider from template", err)
	}
	return c.JSON(http.StatusCreated, resp)
}

// Create godoc
// @Summary Create a new LLM provider
// @Description Create a new LLM provider configuration
// @Tags providers
// @Accept json
// @Produce json
// @Param request body providers.CreateRequest true "Provider configuration"
// @Success 201 {object} providers.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /providers [post].
func (h *ProvidersHandler) Create(c echo.Context) error {
	var req providers.CreateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind provider", err)
	}

	// Validate required fields
	if req.Name == "" {
		return apperror.Required("name")
	}

	resp, err := h.service.Create(c.Request().Context(), req)
	if err != nil {
		return apperror.Internal("create provider", err)
	}

	return c.JSON(http.StatusCreated, resp)
}

// List godoc
// @Summary List all LLM providers
// @Description Get a list of all configured LLM providers
// @Tags providers
// @Accept json
// @Produce json
// @Success 200 {array} providers.GetResponse
// @Failure 500 {object} apperror.Problem
// @Router /providers [get].
func (h *ProvidersHandler) List(c echo.Context) error {
	resp, err := h.service.List(c.Request().Context())
	if err != nil {
		return apperror.Internal("list providers", err)
	}

	return c.JSON(http.StatusOK, resp)
}

// Get godoc
// @Summary Get provider by ID
// @Description Get a provider configuration by its ID
// @Tags providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} providers.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /providers/{id} [get].
func (h *ProvidersHandler) Get(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return apperror.Required("id")
	}

	resp, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return apperror.NotFound("get provider", err)
	}

	return c.JSON(http.StatusOK, resp)
}

// ListModelsByProvider godoc
// @Summary List provider models
// @Description Get models for a provider by id, optionally filtered by type
// @Tags providers
// @Param id path string true "Provider ID (UUID)"
// @Param type query string false "Model type (chat, embedding)"
// @Success 200 {array} modelcatalog.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /providers/{id}/models [get].
func (h *ProvidersHandler) ListModelsByProvider(c echo.Context) error {
	if h.modelsService == nil {
		return apperror.Internal("list provider models", nil)
	}
	id := c.Param("id")
	if strings.TrimSpace(id) == "" {
		return apperror.Required("id")
	}
	modelType := strings.TrimSpace(c.QueryParam("type"))
	var (
		resp []modelcatalog.GetResponse
		err  error
	)
	if modelType == "" {
		resp, err = h.modelsService.ListByProviderID(c.Request().Context(), id)
	} else {
		resp, err = h.modelsService.ListByProviderIDAndType(c.Request().Context(), id, modeldomain.ModelType(modelType))
	}
	if err != nil {
		if strings.Contains(err.Error(), "invalid") {
			return apperror.Invalid("list provider models", err)
		}
		return apperror.NotFound("list provider models", err)
	}
	return c.JSON(http.StatusOK, resp)
}

// GetByName godoc
// @Summary Get provider by name
// @Description Get a provider configuration by its name
// @Tags providers
// @Accept json
// @Produce json
// @Param name path string true "Provider name"
// @Success 200 {object} providers.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /providers/name/{name} [get].
func (h *ProvidersHandler) GetByName(c echo.Context) error {
	name := c.Param("name")
	if name == "" {
		return apperror.Required("name")
	}

	resp, err := h.service.GetByName(c.Request().Context(), name)
	if err != nil {
		return apperror.NotFound("get provider by name", err)
	}

	return c.JSON(http.StatusOK, resp)
}

// Update godoc
// @Summary Update provider
// @Description Update an existing provider configuration
// @Tags providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Param request body providers.UpdateRequest true "Updated provider configuration"
// @Success 200 {object} providers.GetResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /providers/{id} [put].
func (h *ProvidersHandler) Update(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return apperror.Required("id")
	}

	var req providers.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return apperror.Invalid("bind provider", err)
	}

	resp, err := h.service.Update(c.Request().Context(), id, req)
	if err != nil {
		return apperror.Internal("update provider", err)
	}

	return c.JSON(http.StatusOK, resp)
}

// Delete godoc
// @Summary Delete provider
// @Description Delete a provider configuration
// @Tags providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 204 "No Content"
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /providers/{id} [delete].
func (h *ProvidersHandler) Delete(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return apperror.Required("id")
	}

	if err := h.service.Delete(c.Request().Context(), id); err != nil {
		return apperror.Internal("delete provider", err)
	}

	return c.NoContent(http.StatusNoContent)
}

// Count godoc
// @Summary Count providers
// @Description Get the total count of providers
// @Tags providers
// @Accept json
// @Produce json
// @Success 200 {object} providers.CountResponse
// @Failure 500 {object} apperror.Problem
// @Router /providers/count [get].
func (h *ProvidersHandler) Count(c echo.Context) error {
	count, err := h.service.Count(c.Request().Context())
	if err != nil {
		return apperror.Internal("count providers", err)
	}

	return c.JSON(http.StatusOK, providers.CountResponse{Count: count})
}

// Test godoc
// @Summary Test provider connectivity
// @Description Probe a provider's base URL to check reachability, supported client types, and embedding support
// @Tags providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} providers.TestResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /providers/{id}/test [post].
func (h *ProvidersHandler) Test(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return apperror.Required("id")
	}

	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauth.WithUserID(ctx, userID)
	}

	resp, err := h.service.Test(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "invalid") {
			return apperror.Invalid("test provider", err)
		}
		return apperror.NotFound("test provider", err)
	}

	return c.JSON(http.StatusOK, resp)
}

// ImportModels godoc
// @Summary Import models from provider
// @Description Fetch models from provider and import them
// @Tags providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Success 200 {object} providers.ImportModelsResponse
// @Failure 400 {object} apperror.Problem
// @Failure 404 {object} apperror.Problem
// @Failure 500 {object} apperror.Problem
// @Router /providers/{id}/import-models [post].
func (h *ProvidersHandler) ImportModels(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return apperror.Required("id")
	}

	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauth.WithUserID(ctx, userID)
	}

	provider, err := h.service.Get(ctx, id)
	if err != nil {
		return apperror.NotFound("import provider models", err)
	}
	if !modeldomain.IsLLMClientType(modeldomain.ClientType(provider.ClientType)) {
		return apperror.Invalid("import provider models", nil)
	}

	remoteModels, err := h.service.FetchRemoteModels(ctx, id)
	if err != nil {
		return apperror.Internal("fetch remote models", err)
	}

	resp := providers.ImportModelsResponse{
		Models: make([]string, 0),
	}
	managedCatalog := providers.IsManagedModelCatalogClientType(modeldomain.ClientType(provider.ClientType))
	availableModelIDs := make(map[string]struct{}, len(remoteModels))

	// Bulk import lands disabled — the user picks which ones to expose in
	// model pickers afterward, to avoid flooding bot config with dozens of
	// freshly discovered modelcatalog.
	disabled := false

	for _, m := range remoteModels {
		availableModelIDs[m.ID] = struct{}{}
		modelType := modeldomain.ModelTypeChat
		if strings.TrimSpace(m.Type) == string(modeldomain.ModelTypeEmbedding) {
			modelType = modeldomain.ModelTypeEmbedding
		}
		compatibilities := m.Compatibilities
		if len(compatibilities) == 0 && modelType == modeldomain.ModelTypeChat && !m.CapabilitiesKnown {
			// No capability info at all (no upstream claim, no registry match):
			// fall back to a permissive default, but respect an explicit
			// "no reasoning" discovery so we don't advertise thinking falsely.
			compatibilities = []string{modeldomain.CompatVision, modeldomain.CompatToolCall}
			if m.ThinkingMode != modeldomain.ThinkingModeNone {
				compatibilities = append(compatibilities, modeldomain.CompatReasoning)
			}
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = m.ID
		}
		cfg := modeldomain.ModelConfig{
			Description:      m.Description,
			Compatibilities:  compatibilities,
			ReasoningEfforts: m.ReasoningEfforts,
			ThinkingMode:     m.ThinkingMode,
			ContextWindow:    m.ContextWindow,
			Dimensions:       m.Dimensions,
		}
		if managedCatalog {
			available := true
			cfg.CatalogAvailable = &available
		}
		_, err := h.modelsService.Create(ctx, modelcatalog.AddRequest{
			ModelID:    m.ID,
			Name:       name,
			ProviderID: id,
			Type:       modelType,
			Enable:     &disabled,
			Config:     cfg,
		})
		if err != nil {
			if errors.Is(err, modelcatalog.ErrModelIDAlreadyExists) {
				// Upsert/assert: re-importing fills in newly discovered
				// capabilities on existing models without clobbering user config.
				if h.fillExistingModel(ctx, id, m.ID, cfg, managedCatalog && m.CapabilitiesKnown) {
					resp.Updated++
				} else {
					resp.Skipped++
				}
				continue
			}
			h.logger.Warn("failed to import model", slog.String("model_id", m.ID), slog.Any("error", err))
			continue
		}

		resp.Created++
		resp.Models = append(resp.Models, m.ID)
	}
	if managedCatalog {
		h.markUnavailableManagedModels(ctx, id, availableModelIDs)
	}

	return c.JSON(http.StatusOK, resp)
}

func (h *ProvidersHandler) markUnavailableManagedModels(ctx context.Context, providerID string, available map[string]struct{}) {
	existingModels, err := h.modelsService.ListByProviderID(ctx, providerID)
	if err != nil {
		h.logger.WarnContext(ctx, "failed to list managed models for catalog reconciliation", slog.Any("error", err))
		return
	}
	for _, existing := range existingModels {
		if _, ok := available[existing.ModelID]; ok {
			continue
		}
		unavailable := false
		if existing.Config.CatalogAvailable != nil && !*existing.Config.CatalogAvailable {
			continue
		}
		config := existing.Config
		config.CatalogAvailable = &unavailable
		if _, err := h.modelsService.UpdateByProviderAndModelID(ctx, providerID, existing.ModelID, modelcatalog.UpdateRequest{
			ModelID:    existing.ModelID,
			Name:       existing.Name,
			ProviderID: existing.ProviderID,
			Type:       existing.Type,
			Config:     config,
		}); err != nil {
			h.logger.WarnContext(ctx, "failed to mark stale managed model unavailable", slog.String("model_id", existing.ModelID), slog.Any("error", err))
		}
	}
}

// fillExistingModel refreshes an existing model's capability-discovery fields
// from the latest trusted discovery. Managed catalogs replace authoritative
// capability fields exactly, including removal; generic discovery keeps its
// additive compatibility behavior because an empty field can mean unknown.
// Returns true if the model was changed and persisted.
//
// The lookup is provider-scoped because model_id is only unique per provider;
// same-named models under other providers must not affect this refresh.
func (h *ProvidersHandler) fillExistingModel(ctx context.Context, providerID, modelID string, discovered modeldomain.ModelConfig, replaceCapabilities bool) bool {
	existing, err := h.modelsService.GetByProviderAndModelID(ctx, providerID, modelID)
	if err != nil {
		return false
	}
	var merged modeldomain.ModelConfig
	var changed bool
	if replaceCapabilities {
		merged, changed = mergeManagedDiscoveredConfig(existing.Config, discovered)
	} else {
		merged, changed = mergeDiscoveredConfig(existing.Config, discovered)
	}
	if !changed {
		return false
	}
	if _, err := h.modelsService.UpdateByProviderAndModelID(ctx, providerID, modelID, modelcatalog.UpdateRequest{
		ModelID:    existing.ModelID,
		Name:       existing.Name,
		ProviderID: existing.ProviderID,
		Type:       existing.Type,
		Config:     merged,
	}); err != nil {
		h.logger.WarnContext(ctx, "failed to fill model capabilities", slog.String("model_id", modelID), slog.Any("error", err))
		return false
	}
	return true
}

func mergeManagedDiscoveredConfig(existing, discovered modeldomain.ModelConfig) (modeldomain.ModelConfig, bool) {
	out, changed := mergeDiscoveredConfig(existing, discovered)
	if !slices.Equal(out.Compatibilities, discovered.Compatibilities) {
		out.Compatibilities = append([]string(nil), discovered.Compatibilities...)
		changed = true
	}
	if !slices.Equal(out.ReasoningEfforts, discovered.ReasoningEfforts) {
		out.ReasoningEfforts = append([]string(nil), discovered.ReasoningEfforts...)
		changed = true
	}
	if discovered.ThinkingMode != "" && out.ThinkingMode != discovered.ThinkingMode {
		out.ThinkingMode = discovered.ThinkingMode
		changed = true
	}
	return out, changed
}

func mergeDiscoveredConfig(existing, discovered modeldomain.ModelConfig) (modeldomain.ModelConfig, bool) {
	out := existing
	changed := false
	if out.Description == nil && discovered.Description != nil {
		description := strings.TrimSpace(*discovered.Description)
		out.Description = &description
		changed = true
	}
	// Capability-discovery fields: a present discovery wins. The fetch layer
	// (applyCapabilities) has already let an explicit upstream claim take
	// precedence over the registry, so whatever arrives here is the freshest
	// trusted value and should replace the stored one. We only skip when the
	// discovery is empty (nothing learned this round → keep what we have).
	if discovered.ThinkingMode != "" && discovered.ThinkingMode != out.ThinkingMode {
		out.ThinkingMode = discovered.ThinkingMode
		changed = true
	}
	if len(discovered.ReasoningEfforts) > 0 && !slices.Equal(discovered.ReasoningEfforts, out.ReasoningEfforts) {
		out.ReasoningEfforts = append([]string(nil), discovered.ReasoningEfforts...)
		changed = true
	}
	if discovered.ContextWindow != nil && (out.ContextWindow == nil || *discovered.ContextWindow != *out.ContextWindow) {
		out.ContextWindow = discovered.ContextWindow
		changed = true
	}
	if discovered.CatalogAvailable != nil && (out.CatalogAvailable == nil || *discovered.CatalogAvailable != *out.CatalogAvailable) {
		out.CatalogAvailable = discovered.CatalogAvailable
		changed = true
	}
	// Compatibilities are additive: keep anything already present and add the
	// newly discovered tokens.
	for _, c := range discovered.Compatibilities {
		if !slices.Contains(out.Compatibilities, c) {
			out.Compatibilities = append(out.Compatibilities, c)
			changed = true
		}
	}
	return out, changed
}

func mapProviderCreateFromTemplateError(op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, template.ErrDomainInvalid):
		return apperror.Invalid(op, err).WithCode(apperror.CodeProviderTemplateDomainInvalid, nil)
	case errors.Is(err, template.ErrTemplateNotFound):
		return apperror.NotFound(op, err).WithCode(apperror.CodeProviderTemplateNotFound, nil)
	case errors.Is(err, template.ErrDomainMismatch):
		return apperror.Invalid(op, err).WithCode(apperror.CodeProviderTemplateDomainMismatch, nil)
	case errors.Is(err, providers.ErrProviderNameTaken):
		return apperror.Conflict(op, err).WithCode(apperror.CodeProviderNameTaken, nil)
	default:
		return apperror.Internal(op, err).WithCode(apperror.CodeProviderTemplateOperationFailed, nil)
	}
}
