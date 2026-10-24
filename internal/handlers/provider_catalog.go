package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/modelcatalog"
	"github.com/felinics/memoh/internal/providers"
)

// CatalogModels godoc
// @Summary Preview a public provider model catalog
// @Description Fetch, validate and normalize models.dev metadata on the server without changing stored models
// @Tags providers
// @Produce json
// @Param source query string true "models.dev provider ID"
// @Param client_type query string true "Memoh client protocol"
// @Success 200 {object} modelcatalog.Response
// @Failure 400 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 503 {object} server.Problem
// @Router /providers/catalog/models [get].
func (h *ProvidersHandler) CatalogModels(c echo.Context) error {
	source, err := httpx.RequiredQuery(c, "source")
	if err != nil {
		return err
	}
	protocol, err := httpx.RequiredQuery(c, "client_type")
	if err != nil {
		return err
	}
	response, err := h.catalog.Models(c.Request().Context(), strings.TrimSpace(source), strings.TrimSpace(protocol))
	if err != nil {
		return catalogError(err)
	}
	return c.JSON(http.StatusOK, response)
}

func catalogError(err error) error {
	if !errors.Is(err, modelcatalog.ErrUnavailable) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return err
	}
	switch {
	case errors.Is(err, modelcatalog.ErrSourceNotFound):
		return apperror.Wrap(apperror.CodeProviderCatalogSourceNotFound, err, nil)
	case errors.Is(err, modelcatalog.ErrProtocol):
		return apperror.FieldInvalid("client_type", err)
	default:
		return apperror.Wrap(apperror.CodeProviderCatalogUnavailable, errs.WrapDependency(err, "read public model catalog"), nil)
	}
}

// ImportCatalogModels godoc
// @Summary Import a provider's public model catalog
// @Description Resolve the stored provider's models.dev source and normalize its catalog on the server
// @Tags providers
// @Accept json
// @Produce json
// @Param id path string true "Provider ID (UUID)"
// @Param request body providers.ImportCatalogModelsRequest false "Models to preserve from a preceding manual creation"
// @Success 200 {object} providers.ImportModelsResponse
// @Failure 400 {object} server.Problem
// @Failure 403 {object} server.Problem
// @Failure 404 {object} server.Problem
// @Failure 503 {object} server.Problem
// @Router /providers/{id}/import-catalog-models [post].
func (h *ProvidersHandler) ImportCatalogModels(c echo.Context) error {
	id, err := httpx.RequiredParam(c, "id")
	if err != nil {
		return err
	}
	var req providers.ImportCatalogModelsRequest
	if err := c.Bind(&req); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(req.SkipModelIDs) > 10000 {
		return apperror.FieldInvalid("skip_model_ids", errors.New("too many model IDs"))
	}
	provider, err := h.service.Get(c.Request().Context(), id)
	if err != nil {
		return resourceLookupError(err, "id", apperror.CodeProviderNotFound, "get catalog provider")
	}
	if providers.PublicCatalogManaged(provider.Metadata) {
		return apperror.New(apperror.CodeProviderManagedReadonly, nil)
	}
	source := providers.PublicCatalogSource(provider.Metadata)
	if source == "" {
		return apperror.New(apperror.CodeProviderCatalogSourceNotFound, nil)
	}
	catalog, err := h.catalog.Models(c.Request().Context(), source, provider.ClientType)
	if err != nil {
		return catalogError(err)
	}
	skip := make(map[string]struct{}, len(req.SkipModelIDs))
	for _, id := range req.SkipModelIDs {
		skip[strings.TrimSpace(id)] = struct{}{}
	}
	entries := make([]modelcatalog.Model, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		if _, ok := skip[m.ModelID]; ok {
			continue
		}
		entries = append(entries, m)
	}
	response := h.importCatalogModels(c.Request().Context(), provider, entries)
	response.Catalog = &catalog.Info
	return c.JSON(http.StatusOK, response)
}
