package supermarket

type Author struct {
	Name  string `json:"name" validate:"required"`
	Email string `json:"email" validate:"required"`
} // @name handlers.SupermarketAuthor

type RegistryListResponse struct {
	Data []Registry `json:"data" validate:"required"`
} // @name handlers.SupermarketRegistryListResponse

type Registry struct {
	ID              string `json:"id" validate:"required"`
	Name            string `json:"name" validate:"required"`
	Enabled         bool   `json:"enabled" validate:"required"`
	Priority        int    `json:"priority" validate:"required"`
	Adapter         string `json:"adapter" validate:"required"`
	Revision        string `json:"revision,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
	SkillCount      int    `json:"skill_count" validate:"required"`
	AppCount        int    `json:"app_count" validate:"required"`
	CategoryCount   int    `json:"category_count" validate:"required"`
	SkippedAppCount int    `json:"skipped_app_count" validate:"required"`
} // @name handlers.SupermarketRegistry

type SkillCategoryListResponse struct {
	Data []SkillCategory `json:"data" validate:"required"`
} // @name handlers.SupermarketSkillCategoryListResponse

type SkillCategoryRegistry struct {
	ID    string `json:"id" validate:"required"`
	Count int    `json:"count" validate:"required"`
} // @name handlers.SupermarketSkillCategoryRegistry

type SkillCategory struct {
	ID         string                  `json:"id" validate:"required"`
	Name       string                  `json:"name" validate:"required"`
	Count      int                     `json:"count" validate:"required"`
	Registries []SkillCategoryRegistry `json:"registries" validate:"required"`
} // @name handlers.SupermarketSkillCategory

type SkillSource struct {
	Type       string `json:"type" validate:"required"`
	Revision   string `json:"revision" validate:"required"`
	Path       string `json:"path" validate:"required"`
	Repository string `json:"repository,omitempty"`
} // @name handlers.SupermarketSkillSource

type SkillArtifact struct {
	Format           string `json:"format" validate:"required"`
	Digest           string `json:"digest" validate:"required"`
	Size             int64  `json:"size" validate:"required"`
	UncompressedSize int64  `json:"uncompressed_size" validate:"required"`
	ArchiveSize      int64  `json:"archive_size" validate:"required"`
	FileCount        int    `json:"file_count" validate:"required"`
	ContentType      string `json:"content_type" validate:"required"`
	DownloadURL      string `json:"download_url" validate:"required"`
} // @name handlers.SupermarketSkillArtifact

type SkillIconAsset struct {
	Digest      string `json:"digest" validate:"required"`
	Size        int64  `json:"size" validate:"required"`
	ContentType string `json:"content_type" validate:"required"`
} // @name handlers.SupermarketSkillIconAsset

type SkillIcon struct {
	Card       *SkillIconAsset `json:"card,omitempty"`
	Detail     *SkillIconAsset `json:"detail,omitempty"`
	Dark       *SkillIconAsset `json:"dark,omitempty"`
	BrandColor string          `json:"brand_color,omitempty"`
} // @name handlers.SupermarketSkillIcon

type CatalogSkill struct {
	SchemaVersion  string        `json:"schema_version" validate:"required"`
	RegistryID     string        `json:"registry_id" validate:"required"`
	AppID          string        `json:"app_id" validate:"required"`
	SkillID        string        `json:"skill_id" validate:"required"`
	InstallID      string        `json:"install_id" validate:"required"`
	Name           string        `json:"name" validate:"required"`
	Description    string        `json:"description" validate:"required"`
	Author         Author        `json:"author" validate:"required"`
	Homepage       string        `json:"homepage,omitempty"`
	Tags           []string      `json:"tags" validate:"required"`
	Category       string        `json:"category" validate:"required"`
	CategoryName   string        `json:"category_name" validate:"required"`
	SourceCategory string        `json:"source_category,omitempty"`
	Source         SkillSource   `json:"source" validate:"required"`
	Files          []string      `json:"files" validate:"required"`
	Icon           *SkillIcon    `json:"icon,omitempty"`
	Artifact       SkillArtifact `json:"artifact" validate:"required"`
} // @name handlers.SupermarketCatalogSkill

type CatalogSkillListResponse struct {
	Total int            `json:"total" validate:"required"`
	Page  int            `json:"page" validate:"required"`
	Limit int            `json:"limit" validate:"required"`
	Data  []CatalogSkill `json:"data" validate:"required"`
} // @name handlers.SupermarketCatalogSkillListResponse

type AppSkillCategory struct {
	ID         string `json:"id" validate:"required"`
	Name       string `json:"name" validate:"required"`
	SkillCount int    `json:"skill_count" validate:"required"`
} // @name handlers.SupermarketAppSkillCategory

// AppTranslation is one localized name and description of an App.
type AppTranslation struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
} // @name handlers.SupermarketAppTranslation

// AppConnectorReference names a Connect-It connector type an App uses.
type AppConnectorReference struct {
	Type     string `json:"type" validate:"required"`
	Required bool   `json:"required" validate:"required"`
} // @name handlers.SupermarketAppConnector

// AppMetadata is the reviewed App manifest as published by the
// registry: display metadata plus the workspace dependency and connector
// references the App carries. Dependency definitions are not embedded;
// they stay in the dependency registry and are resolved by ID.
type AppMetadata struct {
	Version      string                    `json:"version,omitempty"`
	Author       *Author                   `json:"author,omitempty"`
	Homepage     string                    `json:"homepage,omitempty"`
	Repository   string                    `json:"repository,omitempty"`
	License      string                    `json:"license,omitempty"`
	Category     string                    `json:"category" validate:"required"`
	CategoryName string                    `json:"category_name" validate:"required"`
	Translations map[string]AppTranslation `json:"translations,omitempty"`
	Dependencies []string                  `json:"dependencies" validate:"required"`
	Connectors   []AppConnectorReference   `json:"connectors" validate:"required"`
} // @name handlers.SupermarketAppMetadata

// AppCategoryRegistry counts the Apps of one category in a Registry.
type AppCategoryRegistry struct {
	ID    string `json:"id" validate:"required"`
	Count int    `json:"count" validate:"required"`
} // @name handlers.SupermarketAppCategoryRegistry

// AppCategory is one entry of the shared category table with localized
// names and per-registry App counts.
type AppCategory struct {
	ID         string                `json:"id" validate:"required"`
	Name       string                `json:"name" validate:"required"`
	Names      map[string]string     `json:"names" validate:"required"`
	Order      int                   `json:"order" validate:"required"`
	AppCount   int                   `json:"app_count" validate:"required"`
	Registries []AppCategoryRegistry `json:"registries" validate:"required"`
} // @name handlers.SupermarketAppCategory

type AppCategoryListResponse struct {
	Data []AppCategory `json:"data" validate:"required"`
} // @name handlers.SupermarketAppCategoryListResponse

type AppSummary struct {
	SchemaVersion string   `json:"schema_version" validate:"required"`
	RegistryID    string   `json:"registry_id" validate:"required"`
	AppID         string   `json:"app_id" validate:"required"`
	Name          string   `json:"name" validate:"required"`
	Description   string   `json:"description" validate:"required"`
	Tags          []string `json:"tags" validate:"required"`
	AppMetadata
	Categories      []AppSkillCategory `json:"categories" validate:"required"`
	SkillCount      int                `json:"skill_count" validate:"required"`
	DependencyCount int                `json:"dependency_count" validate:"required"`
	ConnectorCount  int                `json:"connector_count" validate:"required"`
	Icon            *SkillIcon         `json:"icon,omitempty"`
} // @name handlers.SupermarketAppSummary

type AppDescriptor struct {
	AppSummary
	Revision string         `json:"revision" validate:"required"`
	Skills   []CatalogSkill `json:"skills" validate:"required"`
} // @name handlers.SupermarketAppDescriptor

type AppReleaseSkill struct {
	SchemaVersion  string        `json:"schema_version"`
	RegistryID     string        `json:"registry_id"`
	AppID          string        `json:"app_id"`
	SkillID        string        `json:"skill_id"`
	InstallID      string        `json:"install_id"`
	Name           string        `json:"name"`
	Description    string        `json:"description"`
	Author         Author        `json:"author"`
	Homepage       string        `json:"homepage,omitempty"`
	Tags           []string      `json:"tags"`
	Category       string        `json:"category"`
	CategoryName   string        `json:"category_name"`
	SourceCategory string        `json:"source_category,omitempty"`
	Files          []string      `json:"files"`
	Icon           *SkillIcon    `json:"icon,omitempty"`
	Artifact       SkillArtifact `json:"artifact"`
}

type AppRelease struct {
	SchemaVersion string     `json:"schema_version"`
	RegistryID    string     `json:"registry_id"`
	AppID         string     `json:"app_id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Tags          []string   `json:"tags"`
	Icon          *SkillIcon `json:"icon,omitempty"`
	AppMetadata
	Skills []AppReleaseSkill `json:"skills"`
}

type AppListResponse struct {
	Total int          `json:"total" validate:"required"`
	Page  int          `json:"page" validate:"required"`
	Limit int          `json:"limit" validate:"required"`
	Data  []AppSummary `json:"data" validate:"required"`
} // @name handlers.SupermarketAppListResponse

type ArtifactDownloadDescriptor struct {
	Digest      string
	Size        int64
	DownloadURL string
}
