package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"

	skillset "github.com/felinics/memoh/internal/skills"
	supermarketclient "github.com/felinics/memoh/internal/supermarket"
)

type SupermarketRegistryListResponse = supermarketclient.RegistryListResponse

type SupermarketRegistry = supermarketclient.Registry

type SupermarketSkillCategoryListResponse = supermarketclient.SkillCategoryListResponse

type SupermarketSkillCategoryRegistry = supermarketclient.SkillCategoryRegistry

type SupermarketSkillCategory = supermarketclient.SkillCategory

type SupermarketSkillSource = supermarketclient.SkillSource

type SupermarketSkillArtifact = supermarketclient.SkillArtifact

type SupermarketSkillIconAsset = supermarketclient.SkillIconAsset

type SupermarketSkillIcon = supermarketclient.SkillIcon

type SupermarketCatalogSkill = supermarketclient.CatalogSkill

type SupermarketCatalogSkillListResponse = supermarketclient.CatalogSkillListResponse

type SupermarketSkillPackageCategory = supermarketclient.SkillPackageCategory

type SupermarketSkillPackageSummary = supermarketclient.SkillPackageSummary

type SupermarketSkillPackageDescriptor = supermarketclient.SkillPackageDescriptor

type supermarketSkillPackageReleaseSkill = supermarketclient.SkillPackageReleaseSkill

type SupermarketSkillPackageRelease = supermarketclient.SkillPackageRelease

type SupermarketSkillPackageListResponse = supermarketclient.SkillPackageListResponse

type InstallRegistryPackageResponse = supermarketclient.InstallPackageResponse

type InstallRegistrySkillResponse = supermarketclient.InstallSkillResponse

// ListRegistries godoc
// @Summary List Skill Registries from supermarket
// @Tags supermarket
// @Success 200 {object} SupermarketRegistryListResponse
// @Failure 502 {object} ErrorResponse
// @Router /supermarket/registries [get].
func (h *SupermarketHandler) ListRegistries(c echo.Context) error {
	return h.proxy(c, "/api/registries")
}

// ListRegistryCategories godoc
// @Summary List categories in a Skill Registry
// @Tags supermarket
// @Param registry_id path string true "Registry ID"
// @Success 200 {object} SupermarketSkillCategoryListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /supermarket/registries/{registry_id}/categories [get].
func (h *SupermarketHandler) ListRegistryCategories(c echo.Context) error {
	registryID, err := requireRegistryComponent(c.Param("registry_id"), "registry_id")
	if err != nil {
		return err
	}
	return h.proxy(c, "/api/registries/"+url.PathEscape(registryID)+"/categories")
}

// ListSkills godoc
// @Summary List Skills across supermarket Registries
// @Tags supermarket
// @Param q query string false "Search query"
// @Param registry query string false "Registry ID"
// @Param package query string false "Package ID"
// @Param category query string false "Category ID"
// @Param tag query string false "Exact tag"
// @Param page query int false "Page number"
// @Param limit query int false "Items per page"
// @Param sort query string false "Sort order"
// @Success 200 {object} SupermarketCatalogSkillListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /supermarket/skills [get].
func (h *SupermarketHandler) ListSkills(c echo.Context) error {
	return h.proxy(c, "/api/skills")
}

// ListPackages godoc
// @Summary List Skill Packages across supermarket Registries
// @Tags supermarket
// @Param q query string false "Search query"
// @Param registry query string false "Registry ID"
// @Param category query string false "Category ID"
// @Param tag query string false "Exact tag"
// @Param page query int false "Page number"
// @Param limit query int false "Items per page"
// @Param sort query string false "Sort order"
// @Success 200 {object} SupermarketSkillPackageListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /supermarket/packages [get].
func (h *SupermarketHandler) ListPackages(c echo.Context) error {
	return h.proxy(c, "/api/packages")
}

// ListRegistryPackages godoc
// @Summary List Skill Packages in one Registry
// @Tags supermarket
// @Param registry_id path string true "Registry ID"
// @Param q query string false "Search query"
// @Param category query string false "Category ID"
// @Param tag query string false "Exact tag"
// @Param page query int false "Page number"
// @Param limit query int false "Items per page"
// @Param sort query string false "Sort order"
// @Success 200 {object} SupermarketSkillPackageListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /supermarket/registries/{registry_id}/packages [get].
func (h *SupermarketHandler) ListRegistryPackages(c echo.Context) error {
	registryID, err := requireRegistryID(c.Param("registry_id"), "registry_id")
	if err != nil {
		return err
	}
	return h.proxy(c, "/api/registries/"+url.PathEscape(registryID)+"/packages")
}

// GetRegistryPackage godoc
// @Summary Get a namespaced Skill Package
// @Tags supermarket
// @Param registry_id path string true "Registry ID"
// @Param package_id path string true "Package ID"
// @Success 200 {object} SupermarketSkillPackageDescriptor
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /supermarket/registries/{registry_id}/packages/{package_id} [get].
func (h *SupermarketHandler) GetRegistryPackage(c echo.Context) error {
	registryID, err := requireRegistryID(c.Param("registry_id"), "registry_id")
	if err != nil {
		return err
	}
	packageID, err := requireRegistryComponent(c.Param("package_id"), "package_id")
	if err != nil {
		return err
	}
	return h.proxy(c, registryPackageUpstreamPath(registryID, packageID))
}

// GetRegistryPackageRelease godoc
// @Summary Get an immutable Skill Package release
// @Tags supermarket
// @Param registry_id path string true "Registry ID"
// @Param package_id path string true "Package ID"
// @Param revision path string true "Package revision"
// @Success 200 {object} SupermarketSkillPackageDescriptor
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /supermarket/registries/{registry_id}/packages/{package_id}/releases/{revision} [get].
func (h *SupermarketHandler) GetRegistryPackageRelease(c echo.Context) error {
	registryID, err := requireRegistryID(c.Param("registry_id"), "registry_id")
	if err != nil {
		return err
	}
	packageID, err := requireRegistryComponent(c.Param("package_id"), "package_id")
	if err != nil {
		return err
	}
	revision := strings.TrimSpace(c.Param("revision"))
	if !isCanonicalDigest(revision) {
		return echo.NewHTTPError(http.StatusBadRequest, "revision is invalid")
	}
	pkg, err := h.upstream.FetchPackageRelease(c.Request().Context(), registryID, packageID, revision)
	if err != nil {
		if supermarketclient.ErrorKindOf(err) == supermarketclient.ErrorNotFound {
			return echo.NewHTTPError(http.StatusNotFound, "Skill Package release not found")
		}
		return echo.NewHTTPError(http.StatusBadGateway, "supermarket unreachable")
	}
	pkg.Categories = packageCategories(pkg.Skills)
	return c.JSON(http.StatusOK, pkg)
}

func packageCategories(skills []supermarketclient.CatalogSkill) []supermarketclient.SkillPackageCategory {
	result := make([]supermarketclient.SkillPackageCategory, 0)
	indexes := make(map[string]int)
	for _, skill := range skills {
		index, ok := indexes[skill.Category]
		if !ok {
			indexes[skill.Category] = len(result)
			result = append(result, supermarketclient.SkillPackageCategory{ID: skill.Category, Name: skill.CategoryName})
			index = len(result) - 1
		}
		result[index].SkillCount++
	}
	return result
}

// GetRegistrySkill godoc
// @Summary Get a namespaced Registry Skill
// @Tags supermarket
// @Param registry_id path string true "Registry ID"
// @Param package_id path string true "Package ID"
// @Param skill_id path string true "Skill ID"
// @Success 200 {object} SupermarketCatalogSkill
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /supermarket/registries/{registry_id}/packages/{package_id}/skills/{skill_id} [get].
func (h *SupermarketHandler) GetRegistrySkill(c echo.Context) error {
	registryID, packageID, skillID, err := registrySkillIdentity(
		c.Param("registry_id"), c.Param("package_id"), c.Param("skill_id"),
	)
	if err != nil {
		return err
	}
	return h.proxy(c, registrySkillUpstreamPath(registryID, packageID, skillID))
}

// GetRegistrySkillIcon proxies an immutable Skill icon from Supermarket.
// @Summary Get a mirrored Skill icon
// @Tags supermarket
// @Param digest path string true "SHA-256 digest"
// @Success 200 {file} binary
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 502 {object} ErrorResponse
// @Router /supermarket/artifacts/icon/{digest} [get].
func (h *SupermarketHandler) GetRegistrySkillIcon(c echo.Context) error {
	digest := strings.TrimSpace(c.Param("digest"))
	if len(digest) != sha256.Size*2 {
		return echo.NewHTTPError(http.StatusBadRequest, "digest is invalid")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "digest is invalid")
	}
	return h.proxySkillIcon(c, digest)
}

func (h *SupermarketHandler) proxySkillIcon(c echo.Context, digest string) error {
	headers := make(http.Header)
	if value := c.Request().Header.Get("If-None-Match"); value != "" {
		headers.Set("If-None-Match", value)
	}
	resp, err := h.upstream.GetWithHeaders(
		c.Request().Context(),
		"/api/artifacts/icon/"+digest,
		"image/svg+xml,image/png,image/jpeg,image/webp",
		headers,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "supermarket unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		copySkillIconHeaders(c.Response().Header(), resp.Header)
		return c.NoContent(http.StatusNotModified)
	}
	if resp.StatusCode != http.StatusOK {
		return echo.NewHTTPError(resp.StatusCode, "Skill icon unavailable")
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if contentType != "image/svg+xml" && contentType != "image/png" && contentType != "image/jpeg" && contentType != "image/webp" {
		return echo.NewHTTPError(http.StatusBadGateway, "Supermarket returned an unsupported Skill icon")
	}
	const maxSkillImageBytes = 512 * 1024
	content, err := io.ReadAll(io.LimitReader(resp.Body, maxSkillImageBytes+1))
	if err != nil || len(content) == 0 || len(content) > maxSkillImageBytes {
		return echo.NewHTTPError(http.StatusBadGateway, "Supermarket returned an invalid Skill icon")
	}
	actualDigest := sha256.Sum256(content)
	if hex.EncodeToString(actualDigest[:]) != digest {
		return echo.NewHTTPError(http.StatusBadGateway, "Skill icon digest mismatch")
	}
	copySkillIconHeaders(c.Response().Header(), resp.Header)
	c.Response().Header().Set(echo.HeaderContentType, contentType)
	return c.Blob(http.StatusOK, contentType, content)
}

func copySkillIconHeaders(target, source http.Header) {
	for _, name := range []string{"Cache-Control", "ETag", "X-Content-SHA256"} {
		if value := source.Get(name); value != "" {
			target.Set(name, value)
		}
	}
	// These images are served unauthenticated on our own origin and may be SVG,
	// which can carry script. Enforce a strict policy regardless of what the
	// upstream sent: sandbox blocks script execution on direct navigation and
	// nosniff prevents MIME confusion. Do not forward the upstream CSP — a laxer
	// upstream value must never be able to weaken this.
	target.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	target.Set("X-Content-Type-Options", "nosniff")
}

func requireRegistryComponent(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if !skillset.IsValidRegistryComponent(value) {
		return "", echo.NewHTTPError(http.StatusBadRequest, field+" is invalid")
	}
	return value, nil
}

func isCanonicalDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func requireRegistryID(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if !skillset.IsValidRegistryID(value) {
		return "", echo.NewHTTPError(http.StatusBadRequest, field+" is invalid")
	}
	return value, nil
}

func registrySkillIdentity(registryValue, packageValue, skillValue string) (string, string, string, error) {
	registryID, err := requireRegistryID(registryValue, "registry_id")
	if err != nil {
		return "", "", "", err
	}
	packageID, err := requireRegistryComponent(packageValue, "package_id")
	if err != nil {
		return "", "", "", err
	}
	skillID, err := requireRegistryComponent(skillValue, "skill_id")
	if err != nil {
		return "", "", "", err
	}
	return registryID, packageID, skillID, nil
}

func registrySkillUpstreamPath(registryID, packageID, skillID string) string {
	return "/api/registries/" + url.PathEscape(registryID) + "/packages/" + url.PathEscape(packageID) + "/skills/" + url.PathEscape(skillID)
}

func registryPackageUpstreamPath(registryID, packageID string) string {
	return "/api/registries/" + url.PathEscape(registryID) + "/packages/" + url.PathEscape(packageID)
}
