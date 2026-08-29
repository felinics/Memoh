package supermarket

type Author struct {
	Name  string `json:"name" validate:"required"`
	Email string `json:"email" validate:"required"`
} // @name handlers.SupermarketAuthor

type RegistryListResponse struct {
	Data []Registry `json:"data" validate:"required"`
} // @name handlers.SupermarketRegistryListResponse

type Registry struct {
	ID                  string `json:"id" validate:"required"`
	Name                string `json:"name" validate:"required"`
	Enabled             bool   `json:"enabled" validate:"required"`
	Priority            int    `json:"priority" validate:"required"`
	Adapter             string `json:"adapter" validate:"required"`
	Revision            string `json:"revision,omitempty"`
	PublishedAt         string `json:"published_at,omitempty"`
	SkillCount          int    `json:"skill_count" validate:"required"`
	PackageCount        int    `json:"package_count" validate:"required"`
	CategoryCount       int    `json:"category_count" validate:"required"`
	SkippedPackageCount int    `json:"skipped_package_count" validate:"required"`
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
	PackageID      string        `json:"package_id" validate:"required"`
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

type SkillPackageCategory struct {
	ID         string `json:"id" validate:"required"`
	Name       string `json:"name" validate:"required"`
	SkillCount int    `json:"skill_count" validate:"required"`
} // @name handlers.SupermarketSkillPackageCategory

type SkillPackageSummary struct {
	SchemaVersion string                 `json:"schema_version" validate:"required"`
	RegistryID    string                 `json:"registry_id" validate:"required"`
	PackageID     string                 `json:"package_id" validate:"required"`
	Name          string                 `json:"name" validate:"required"`
	Description   string                 `json:"description" validate:"required"`
	Tags          []string               `json:"tags" validate:"required"`
	Categories    []SkillPackageCategory `json:"categories" validate:"required"`
	SkillCount    int                    `json:"skill_count" validate:"required"`
	Icon          *SkillIcon             `json:"icon,omitempty"`
} // @name handlers.SupermarketSkillPackageSummary

type SkillPackageDescriptor struct {
	SkillPackageSummary
	Revision string         `json:"revision" validate:"required"`
	Skills   []CatalogSkill `json:"skills" validate:"required"`
} // @name handlers.SupermarketSkillPackageDescriptor

type SkillPackageReleaseSkill struct {
	SchemaVersion  string        `json:"schema_version"`
	RegistryID     string        `json:"registry_id"`
	PackageID      string        `json:"package_id"`
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

type SkillPackageRelease struct {
	SchemaVersion string                     `json:"schema_version"`
	RegistryID    string                     `json:"registry_id"`
	PackageID     string                     `json:"package_id"`
	Name          string                     `json:"name"`
	Description   string                     `json:"description"`
	Tags          []string                   `json:"tags"`
	Icon          *SkillIcon                 `json:"icon,omitempty"`
	Skills        []SkillPackageReleaseSkill `json:"skills"`
}

type SkillPackageListResponse struct {
	Total int                   `json:"total" validate:"required"`
	Page  int                   `json:"page" validate:"required"`
	Limit int                   `json:"limit" validate:"required"`
	Data  []SkillPackageSummary `json:"data" validate:"required"`
} // @name handlers.SupermarketSkillPackageListResponse

type ArtifactDownloadDescriptor struct {
	Digest      string
	Size        int64
	DownloadURL string
}
