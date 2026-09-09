package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
)

type ProvidersHandler struct {
	service       *providers.Service
	modelsService *models.Service
	logger        *slog.Logger
}

func NewProvidersHandler(log *slog.Logger, service *providers.Service, modelsService *models.Service) *ProvidersHandler {
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
		return apperror.Wrap(apperror.CodeProviderTemplateRequestInvalid, err, nil)
	}
	resp, err := h.service.CreateFromTemplate(c.Request().Context(), req)
	if err != nil {
		return err
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
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /providers [post].
func (h *ProvidersHandler) Create(c echo.Context) error {
	var req providers.CreateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	// Validate required fields
	if req.Name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}

	resp, err := h.service.Create(c.Request().Context(), req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// @Failure 500 {object} ErrorResponse
// @Router /providers [get].
func (h *ProvidersHandler) List(c echo.Context) error {
	resp, err := h.service.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /providers/{id} [get].
func (h *ProvidersHandler) Get(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	resp, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}

	return c.JSON(http.StatusOK, resp)
}

// ListModelsByProvider godoc
// @Summary List provider models
// @Description Get models for a provider by id, optionally filtered by type
// @Tags providers
// @Param id path string true "Provider ID (UUID)"
// @Param type query string false "Model type (chat, embedding)"
// @Success 200 {array} models.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /providers/{id}/models [get].
func (h *ProvidersHandler) ListModelsByProvider(c echo.Context) error {
	if h.modelsService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "models service not configured")
	}
	id := c.Param("id")
	if strings.TrimSpace(id) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	modelType := strings.TrimSpace(c.QueryParam("type"))
	var (
		resp []models.GetResponse
		err  error
	)
	if modelType == "" {
		resp, err = h.modelsService.ListByProviderID(c.Request().Context(), id)
	} else {
		resp, err = h.modelsService.ListByProviderIDAndType(c.Request().Context(), id, models.ModelType(modelType))
	}
	if err != nil {
		if strings.Contains(err.Error(), "invalid") {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	provider, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return c.JSON(http.StatusOK, withReasoningForClientType(resp, provider.ClientType))
}

// GetByName godoc
// @Summary Get provider by name
// @Description Get a provider configuration by its name
// @Tags providers
// @Accept json
// @Produce json
// @Param name path string true "Provider name"
// @Success 200 {object} providers.GetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /providers/name/{name} [get].
func (h *ProvidersHandler) GetByName(c echo.Context) error {
	name := c.Param("name")
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}

	resp, err := h.service.GetByName(c.Request().Context(), name)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
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
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /providers/{id} [put].
func (h *ProvidersHandler) Update(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	var req providers.UpdateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	resp, err := h.service.Update(c.Request().Context(), id, req)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /providers/{id} [delete].
func (h *ProvidersHandler) Delete(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	if err := h.service.Delete(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// @Failure 500 {object} ErrorResponse
// @Router /providers/count [get].
func (h *ProvidersHandler) Count(c echo.Context) error {
	count, err := h.service.Count(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
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
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /providers/{id}/test [post].
func (h *ProvidersHandler) Test(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}

	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauthctx.WithUserID(ctx, userID)
	}

	resp, err := h.service.Test(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "invalid") {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
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
// @Param request body providers.ImportModelsRequest false "Explicit defaults for unknown custom chat models"
// @Success 200 {object} providers.ImportModelsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /providers/{id}/import-models [post].
func (h *ProvidersHandler) ImportModels(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "id is required")
	}
	var req providers.ImportModelsRequest
	if err := c.Bind(&req); err != nil && !errors.Is(err, io.EOF) {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := models.ValidateCompatibilities(req.DefaultCompatibilities); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	ctx := c.Request().Context()
	if userID, err := auth.UserIDFromContext(c); err == nil {
		ctx = oauthctx.WithUserID(ctx, userID)
	}

	provider, err := h.service.Get(ctx, id)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, fmt.Sprintf("provider not found: %v", err))
	}
	if !models.IsLLMClientType(models.ClientType(provider.ClientType)) {
		return echo.NewHTTPError(http.StatusBadRequest, "import models is not supported for speech providers")
	}

	remoteModels, err := h.service.FetchRemoteModels(ctx, id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("fetch remote models: %v", err))
	}

	resp := providers.ImportModelsResponse{
		Models: make([]string, 0),
	}
	managedCatalog := providers.IsManagedModelCatalogClientType(models.ClientType(provider.ClientType))
	availableModelIDs := make(map[string]struct{}, len(remoteModels))

	// Bulk import lands disabled — the user picks which ones to expose in
	// model pickers afterward, to avoid flooding bot config with dozens of
	// freshly discovered models.
	disabled := false

	for _, m := range remoteModels {
		availableModelIDs[m.ID] = struct{}{}
		modelType := models.ModelTypeChat
		if strings.TrimSpace(m.Type) == string(models.ModelTypeEmbedding) {
			modelType = models.ModelTypeEmbedding
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = m.ID
		}
		// The discovered config carries capabilities exactly as reported, so a
		// re-import refresh never inherits caller-supplied defaults; only the
		// create path below applies them.
		discoveredConfig := modelConfigFromRemote(m, append([]string(nil), m.Compatibilities...))
		if managedCatalog {
			available := true
			discoveredConfig.CatalogAvailable = &available
		}
		createConfig := discoveredConfig
		createConfig.Compatibilities = importedCompatibilities(
			m,
			modelType,
			req.DefaultCompatibilities,
			provider.ProviderTemplateID == "",
		)
		_, err := h.modelsService.Create(ctx, models.AddRequest{
			ModelID:    m.ID,
			Name:       name,
			ProviderID: id,
			Type:       modelType,
			Enable:     &disabled,
			Config:     createConfig,
		})
		if err != nil {
			if errors.Is(err, models.ErrModelIDAlreadyExists) {
				// Upsert/assert: re-importing fills in newly discovered
				// capabilities on existing models without clobbering user config.
				if h.fillExistingModel(ctx, id, m.ID, discoveredConfig, managedCatalog && m.CapabilitiesKnown) {
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

func importedCompatibilities(remote providers.RemoteModel, modelType models.ModelType, defaults []string, allowCustomDefaults bool) []string {
	if len(remote.Compatibilities) > 0 || modelType != models.ModelTypeChat || remote.CapabilitiesKnown || !allowCustomDefaults {
		return remote.Compatibilities
	}
	// Unknown means unknown unless the caller explicitly supplies defaults.
	// The backend never infers model capabilities from the client protocol.
	return append([]string(nil), defaults...)
}

func (h *ProvidersHandler) markUnavailableManagedModels(ctx context.Context, providerID string, available map[string]struct{}) {
	existingModels, err := h.modelsService.ListByProviderID(ctx, providerID)
	if err != nil {
		h.logger.Warn("failed to list managed models for catalog reconciliation", slog.Any("error", err))
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
		if _, err := h.modelsService.UpdateByProviderAndModelID(ctx, providerID, existing.ModelID, models.UpdateRequest{
			ModelID:    existing.ModelID,
			Name:       existing.Name,
			ProviderID: existing.ProviderID,
			Type:       existing.Type,
			Config:     config,
		}); err != nil {
			h.logger.Warn("failed to mark stale managed model unavailable", slog.String("model_id", existing.ModelID), slog.Any("error", err))
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
func (h *ProvidersHandler) fillExistingModel(ctx context.Context, providerID, modelID string, discovered models.ModelConfig, replaceCapabilities bool) bool {
	existing, err := h.modelsService.GetByProviderAndModelID(ctx, providerID, modelID)
	if err != nil {
		return false
	}
	var merged models.ModelConfig
	var changed bool
	if replaceCapabilities {
		merged, changed = mergeManagedDiscoveredConfig(existing.Config, discovered)
	} else {
		merged, changed = mergeDiscoveredConfig(existing.Config, discovered)
	}
	if !changed {
		return false
	}
	if _, err := h.modelsService.UpdateByProviderAndModelID(ctx, providerID, modelID, models.UpdateRequest{
		ModelID:    existing.ModelID,
		Name:       existing.Name,
		ProviderID: existing.ProviderID,
		Type:       existing.Type,
		Config:     merged,
	}); err != nil {
		h.logger.Warn("failed to fill model capabilities", slog.String("model_id", modelID), slog.Any("error", err))
		return false
	}
	return true
}

// modelConfigFromRemote builds the stored config for an imported model. It exists
// as a named function so a test can assert that every wire declaration survives
// the import path: a dialect or off-support token lost here is lost for every row
// this path creates, and the resulting model resolves onto the wrong wire — which
// is how these fields came to be missing from RemoteModel in the first place.
func modelConfigFromRemote(m providers.RemoteModel, compatibilities []string) models.ModelConfig {
	return models.ModelConfig{
		Description:         m.Description,
		Compatibilities:     compatibilities,
		ReasoningEfforts:    m.ReasoningEfforts,
		ThinkingMode:        m.ThinkingMode,
		ReasoningDialect:    m.ReasoningDialect,
		ReasoningOffSupport: m.ReasoningOffSupport,
		ReasoningDefaultOn:  m.ReasoningDefaultOn,
		ThinkingBudgetMin:   m.ThinkingBudgetMin,
		ThinkingBudgetMax:   m.ThinkingBudgetMax,
		ContextWindow:       m.ContextWindow,
		Dimensions:          m.Dimensions,
	}
}

// preserveDeclaredCapabilities lets generic discovery refresh the tier list while
// keeping capability tokens it structurally cannot know about.
//
// The disable token declares that a model can be turned off. For providers whose
// off travels outside the effort field — DeepSeek through chat_completions_compat,
// Gemini 2.5 Flash through a zero budget — the hand-maintained template is the only
// source. Managed Codex/Copilot catalogs skip this helper and replace the list
// exactly, so an upstream removal can revoke Off.
func preserveDeclaredCapabilities(stored, discovered []string) []string {
	out := append([]string(nil), discovered...)
	for _, token := range []string{models.ReasoningEffortDisable} {
		if slices.Contains(stored, token) && !slices.Contains(out, token) {
			out = append([]string{token}, out...)
		}
	}
	return out
}

func mergeManagedDiscoveredConfig(existing, discovered models.ModelConfig) (models.ModelConfig, bool) {
	out, changed := mergeDiscoveredConfig(existing, discovered)
	if !slices.Equal(out.Compatibilities, discovered.Compatibilities) {
		out.Compatibilities = append([]string(nil), discovered.Compatibilities...)
		changed = true
	}
	if !slices.Equal(out.ReasoningEfforts, discovered.ReasoningEfforts) {
		out.ReasoningEfforts = append([]string(nil), discovered.ReasoningEfforts...)
		changed = true
	}
	if out.ThinkingMode != discovered.ThinkingMode {
		out.ThinkingMode = discovered.ThinkingMode
		changed = true
	}
	if out.ReasoningDialect != discovered.ReasoningDialect {
		out.ReasoningDialect = discovered.ReasoningDialect
		changed = true
	}
	if out.ReasoningOffSupport != discovered.ReasoningOffSupport {
		out.ReasoningOffSupport = discovered.ReasoningOffSupport
		changed = true
	}
	if !sameBoolPointer(out.ReasoningDefaultOn, discovered.ReasoningDefaultOn) {
		out.ReasoningDefaultOn = discovered.ReasoningDefaultOn
		changed = true
	}
	if !sameIntPointer(out.ThinkingBudgetMin, discovered.ThinkingBudgetMin) {
		out.ThinkingBudgetMin = discovered.ThinkingBudgetMin
		changed = true
	}
	if !sameIntPointer(out.ThinkingBudgetMax, discovered.ThinkingBudgetMax) {
		out.ThinkingBudgetMax = discovered.ThinkingBudgetMax
		changed = true
	}
	return out, changed
}

func mergeDiscoveredConfig(existing, discovered models.ModelConfig) (models.ModelConfig, bool) {
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
	if len(discovered.ReasoningEfforts) > 0 {
		if wanted := preserveDeclaredCapabilities(out.ReasoningEfforts, discovered.ReasoningEfforts); !slices.Equal(wanted, out.ReasoningEfforts) {
			out.ReasoningEfforts = wanted
			changed = true
		}
	}
	if discovered.ReasoningDialect != "" && discovered.ReasoningDialect != out.ReasoningDialect {
		out.ReasoningDialect = discovered.ReasoningDialect
		changed = true
	}
	if discovered.ReasoningOffSupport != "" && discovered.ReasoningOffSupport != out.ReasoningOffSupport {
		out.ReasoningOffSupport = discovered.ReasoningOffSupport
		changed = true
	}
	if discovered.ReasoningDefaultOn != nil && !sameBoolPointer(out.ReasoningDefaultOn, discovered.ReasoningDefaultOn) {
		out.ReasoningDefaultOn = discovered.ReasoningDefaultOn
		changed = true
	}
	if discovered.ThinkingBudgetMin != nil && !sameIntPointer(out.ThinkingBudgetMin, discovered.ThinkingBudgetMin) {
		out.ThinkingBudgetMin = discovered.ThinkingBudgetMin
		changed = true
	}
	if discovered.ThinkingBudgetMax != nil && !sameIntPointer(out.ThinkingBudgetMax, discovered.ThinkingBudgetMax) {
		out.ThinkingBudgetMax = discovered.ThinkingBudgetMax
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

func sameBoolPointer(left, right *bool) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameIntPointer(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
