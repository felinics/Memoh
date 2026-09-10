package supermarket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/felinics/memoh/internal/apperror"
	skillset "github.com/felinics/memoh/internal/skills"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	maxSkillArtifactCompressedBytes   = 6 * 1024 * 1024
	maxSkillArtifactUncompressedBytes = 5 * 1024 * 1024
	maxSkillArtifactArchiveBytes      = 5 * 1024 * 1024
	maxSkillArtifactFiles             = 1_000
	maxPackageSkills                  = 128
	maxPackageArtifactsCompressed     = 128 * 1024 * 1024
	maxPackageArtifactsUncompressed   = 128 * 1024 * 1024
	maxPackageArtifactsArchive        = 128 * 1024 * 1024
	maxPackageArtifactFiles           = 10_000
)

// InstallSkillResponse describes one Skill written into the workspace.
type InstallSkillResponse struct {
	OK                bool   `json:"ok" validate:"required"`
	RegistryID        string `json:"registry_id" validate:"required"`
	PackageID         string `json:"package_id" validate:"required"`
	SkillID           string `json:"skill_id" validate:"required"`
	InstallID         string `json:"install_id" validate:"required"`
	WorkspaceTargetID string `json:"workspace_target_id" validate:"required"`
	ArtifactDigest    string `json:"artifact_digest" validate:"required"`
	FilesWritten      int    `json:"files_written" validate:"required"`
} // @name handlers.InstallRegistrySkillResponse

type preparedSkill struct {
	skill   CatalogSkill
	archive skillset.Archive
}

type preparedPackage struct {
	descriptor  SkillPackageDescriptor
	skills      []preparedSkill
	workspaceOS string
}

// SkillPublication is a staged Skill change: the new Skills are in place but
// the previous copy is kept until Commit. Rollback restores it. A Package
// without Skills stages the removal of any Skills a previous revision left.
type SkillPublication struct {
	publication       *skillset.PackagePublication
	removal           *skillset.PackageRemoval
	Skills            []InstallSkillResponse
	WorkspaceTargetID string
}

func (p *SkillPublication) Commit(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return errors.Join(p.publication.Commit(ctx), p.removal.Commit(ctx))
}

func (p *SkillPublication) Rollback(ctx context.Context) error {
	if p == nil {
		return nil
	}
	return errors.Join(p.publication.Rollback(ctx), p.removal.Rollback(ctx))
}

// SkillRemoval is a staged removal of a Package's Skills.
type SkillRemoval struct {
	removal           *skillset.PackageRemoval
	WorkspaceTargetID string
}

func (r *SkillRemoval) Commit(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.removal.Commit(ctx)
}

func (r *SkillRemoval) Rollback(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.removal.Rollback(ctx)
}

// ResolveTargetID normalizes a workspace target reference to its ID.
func (i *Installer) ResolveTargetID(ctx context.Context, botID, targetID string) (string, error) {
	if i == nil || i.workspaces == nil {
		return "", errors.New("supermarket installer is not configured")
	}
	target, err := i.workspaces.ResolveWorkspaceTarget(workspace.WithWorkspaceTarget(ctx, targetID), botID, targetID)
	if err != nil {
		return "", &WorkspaceTargetError{Err: err}
	}
	return target.TargetID, nil
}

// FetchRelease downloads and validates one immutable Package release.
func (i *Installer) FetchRelease(ctx context.Context, registryID, packageID, revision string) (SkillPackageDescriptor, error) {
	registryID, packageID, err := validatePackageIdentity(registryID, packageID)
	if err != nil {
		return SkillPackageDescriptor{}, err
	}
	revision = strings.TrimSpace(revision)
	if !isCanonicalSHA256(revision) {
		return SkillPackageDescriptor{}, &StatusError{Status: http.StatusBadRequest, Message: "revision is invalid"}
	}
	pkg, err := i.fetchPackageRelease(ctx, registryID, packageID, revision)
	if err != nil {
		return SkillPackageDescriptor{}, err
	}
	if pkg.Revision != revision {
		return SkillPackageDescriptor{}, invalidPackage(errors.New("registry Package revision does not match the request"))
	}
	if err := validatePackage(pkg, registryID, packageID); err != nil {
		return SkillPackageDescriptor{}, invalidPackage(err)
	}
	if err := validatePackageBudget(pkg.Skills); err != nil {
		return SkillPackageDescriptor{}, invalidPackage(err)
	}
	return pkg, nil
}

// FetchCurrentPackage reads the Package descriptor the registry currently
// publishes, which names the newest revision.
func (i *Installer) FetchCurrentPackage(ctx context.Context, registryID, packageID string) (SkillPackageDescriptor, error) {
	registryID, packageID, err := validatePackageIdentity(registryID, packageID)
	if err != nil {
		return SkillPackageDescriptor{}, err
	}
	if i == nil || i.client == nil {
		return SkillPackageDescriptor{}, errors.New("supermarket installer is not configured")
	}
	pkg, err := i.client.FetchCurrentPackage(ctx, registryID, packageID)
	if err != nil {
		return SkillPackageDescriptor{}, registryFetchError(err)
	}
	if err := validatePackage(pkg, registryID, packageID); err != nil {
		return SkillPackageDescriptor{}, invalidPackage(err)
	}
	return pkg, nil
}

// PublishSkills downloads the Skills of a validated release and stages them
// into the workspace target. expectedRevision is the revision the caller has
// recorded for the Package, or empty when it is new; a workspace copy that
// does not match it is replaced. The caller commits after recording the
// installation, or rolls back.
func (i *Installer) PublishSkills(ctx context.Context, botID, targetID string, pkg SkillPackageDescriptor, expectedRevision string) (*SkillPublication, error) {
	if i == nil || i.workspaces == nil {
		return nil, apperror.Wrap(apperror.CodeRegistryPackageInstallFailed, errors.New("skill Package installer is not configured"), nil)
	}
	targetCtx := workspace.WithWorkspaceTarget(ctx, targetID)
	target, err := i.workspaces.ResolveWorkspaceTarget(targetCtx, botID, targetID)
	if err != nil {
		return nil, &WorkspaceTargetError{Err: err}
	}
	if target.Client == nil {
		return nil, apperror.Wrap(apperror.CodeRegistryPackageInstallFailed, errors.New("workspace is not reachable"), nil)
	}
	release, err := i.acquirePreparation(targetCtx)
	if err != nil {
		return nil, err
	}
	defer release()
	consistent, err := skillset.ReconcilePackage(targetCtx, target.Client, pkg.RegistryID, pkg.PackageID, expectedRevision)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeRegistryPackageInstallFailed, fmt.Errorf("recover Registry Package state: %w", err), nil)
	}
	if !consistent && i.logger != nil {
		i.logger.Warn("Skill Package files did not match the recorded revision; replacing them",
			slog.String("registry_id", pkg.RegistryID), slog.String("package_id", pkg.PackageID),
			slog.String("workspace_target_id", target.TargetID), slog.String("recorded_revision", expectedRevision),
		)
	}
	if len(pkg.Skills) == 0 {
		removal, err := skillset.PreparePackageRemoval(targetCtx, target.Client, pkg.RegistryID, pkg.PackageID)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeRegistryPackageInstallFailed, fmt.Errorf("clear previous Registry Package Skills: %w", err), nil)
		}
		return &SkillPublication{removal: removal, Skills: []InstallSkillResponse{}, WorkspaceTargetID: target.TargetID}, nil
	}
	prepared, err := i.preparePackage(targetCtx, target.Info.OS, pkg)
	if err != nil {
		return nil, err
	}
	publication, published, err := publishPackage(targetCtx, target.Client, prepared, target.TargetID)
	if err != nil {
		return nil, err
	}
	return &SkillPublication{publication: publication, Skills: published, WorkspaceTargetID: target.TargetID}, nil
}

// RemoveSkills stages the removal of a Package's Skills from the workspace.
func (i *Installer) RemoveSkills(ctx context.Context, botID, targetID, registryID, packageID, revision string) (*SkillRemoval, error) {
	if i == nil || i.workspaces == nil {
		return nil, errors.New("skill Package installer is not configured")
	}
	targetCtx := workspace.WithWorkspaceTarget(ctx, targetID)
	target, err := i.workspaces.ResolveWorkspaceTarget(targetCtx, botID, targetID)
	if err != nil {
		return nil, &WorkspaceTargetError{Err: err}
	}
	if target.Client == nil {
		return nil, apperror.Wrap(apperror.CodeRegistryPackageInstallFailed, errors.New("workspace is not reachable"), nil)
	}
	consistent, err := skillset.ReconcilePackage(targetCtx, target.Client, registryID, packageID, revision)
	if err != nil {
		return nil, fmt.Errorf("recover Skill Package state: %w", err)
	}
	if !consistent && i.logger != nil {
		i.logger.Warn("Skill Package files did not match the recorded revision; removing the managed path",
			slog.String("registry_id", registryID), slog.String("package_id", packageID),
			slog.String("workspace_target_id", target.TargetID), slog.String("recorded_revision", revision),
		)
	}
	removal, err := skillset.PreparePackageRemoval(targetCtx, target.Client, registryID, packageID)
	if err != nil {
		return nil, err
	}
	return &SkillRemoval{removal: removal, WorkspaceTargetID: target.TargetID}, nil
}

func validatePackageIdentity(registryID, packageID string) (string, string, error) {
	registryID = strings.TrimSpace(registryID)
	if !skillset.IsValidRegistryID(registryID) {
		return "", "", &StatusError{Status: http.StatusBadRequest, Message: "registry_id is invalid"}
	}
	packageID = strings.TrimSpace(packageID)
	if !skillset.IsValidRegistryComponent(packageID) {
		return "", "", &StatusError{Status: http.StatusBadRequest, Message: "package_id is invalid"}
	}
	return registryID, packageID, nil
}

func (i *Installer) fetchPackageRelease(ctx context.Context, registryID, packageID, revision string) (SkillPackageDescriptor, error) {
	if i == nil || i.client == nil {
		return SkillPackageDescriptor{}, errors.New("supermarket installer is not configured")
	}
	pkg, err := i.client.FetchPackageRelease(ctx, registryID, packageID, revision)
	if err == nil {
		return pkg, nil
	}
	return SkillPackageDescriptor{}, registryFetchError(err)
}

func registryFetchError(err error) error {
	switch ErrorKindOf(err) {
	case ErrorNotFound:
		return apperror.New(apperror.CodeRegistryPackageNotFound, nil)
	case ErrorUnavailable:
		return apperror.Wrap(apperror.CodeRegistryUnavailable, fmt.Errorf("fetch Registry Package: %w", err), nil)
	default:
		return invalidPackage(fmt.Errorf("invalid Registry Package: %w", err))
	}
}

func (i *Installer) preparePackage(ctx context.Context, workspaceOS string, pkg SkillPackageDescriptor) (preparedPackage, error) {
	prepared := preparedPackage{descriptor: pkg, skills: make([]preparedSkill, 0, len(pkg.Skills)), workspaceOS: workspaceOS}
	for _, skill := range pkg.Skills {
		item, err := i.prepareSkill(ctx, skill)
		if err != nil {
			return preparedPackage{}, err
		}
		prepared.skills = append(prepared.skills, item)
	}
	return prepared, nil
}

func (i *Installer) prepareSkill(ctx context.Context, skill CatalogSkill) (preparedSkill, error) {
	artifact := skill.Artifact
	content, err := i.client.DownloadArtifact(ctx, ArtifactDownloadDescriptor{Digest: artifact.Digest, Size: artifact.Size, DownloadURL: artifact.DownloadURL})
	if err != nil {
		code := apperror.CodeRegistryPackageInvalid
		if ErrorKindOf(err) == ErrorUnavailable {
			code = apperror.CodeRegistryUnavailable
		}
		return preparedSkill{}, apperror.Wrap(code, fmt.Errorf("download Registry Skill Artifact: %w", err), nil)
	}
	archive, err := skillset.ReadArchiveWithLimits(content, artifact.UncompressedSize, artifact.ArchiveSize, artifact.FileCount)
	if err != nil {
		return preparedSkill{}, invalidPackage(err)
	}
	if archive.UncompressedSize() != artifact.UncompressedSize || archive.ArchiveSize() != artifact.ArchiveSize || archive.FileCount() != artifact.FileCount {
		return preparedSkill{}, invalidPackage(errors.New("registry Skill Artifact contents do not match its descriptor"))
	}
	return preparedSkill{skill: skill, archive: archive}, nil
}

func publishPackage(ctx context.Context, client *bridge.Client, prepared preparedPackage, workspaceTargetID string) (*skillset.PackagePublication, []InstallSkillResponse, error) {
	if client == nil {
		return nil, nil, apperror.Wrap(apperror.CodeRegistryPackageInstallFailed, errors.New("workspace is not reachable"), nil)
	}
	members := make([]skillset.PackageArchive, 0, len(prepared.skills))
	for _, item := range prepared.skills {
		members = append(members, skillset.PackageArchive{SkillID: item.skill.SkillID, Archive: item.archive})
	}
	publication, err := skillset.PublishPackage(
		ctx,
		client,
		prepared.workspaceOS,
		prepared.descriptor.RegistryID,
		prepared.descriptor.PackageID,
		prepared.descriptor.Revision,
		members,
	)
	if err != nil {
		return nil, nil, apperror.Wrap(apperror.CodeRegistryPackageInstallFailed, fmt.Errorf("publish Registry Package: %w", err), nil)
	}
	installed := make([]InstallSkillResponse, 0, len(prepared.skills))
	for _, item := range prepared.skills {
		installed = append(installed, InstallSkillResponse{OK: true, RegistryID: item.skill.RegistryID, PackageID: item.skill.PackageID, SkillID: item.skill.SkillID, InstallID: item.skill.InstallID, WorkspaceTargetID: workspaceTargetID, ArtifactDigest: item.skill.Artifact.Digest, FilesWritten: item.archive.FileCount()})
	}
	return publication, installed, nil
}

func validatePackage(pkg SkillPackageDescriptor, registryID, packageID string) error {
	if pkg.SchemaVersion != "1" || pkg.RegistryID != registryID || pkg.PackageID != packageID || !isCanonicalSHA256(pkg.Revision) || len(pkg.Skills) > maxPackageSkills || pkg.SkillCount != len(pkg.Skills) {
		return errors.New("registry Package release is invalid")
	}
	if len(pkg.Skills) == 0 && len(pkg.Dependencies) == 0 && len(pkg.Connectors) == 0 {
		return errors.New("registry Package release is empty")
	}
	seen := make(map[string]struct{}, len(pkg.Skills))
	for _, skill := range pkg.Skills {
		if _, exists := seen[skill.SkillID]; exists {
			return errors.New("registry Package contains duplicate Skills")
		}
		seen[skill.SkillID] = struct{}{}
		if err := validateSkill(skill, registryID, packageID, skill.SkillID); err != nil {
			return err
		}
	}
	seenDependencies := make(map[string]struct{}, len(pkg.Dependencies))
	for _, dependency := range pkg.Dependencies {
		if strings.TrimSpace(dependency) == "" {
			return errors.New("registry Package dependency reference is invalid")
		}
		if _, exists := seenDependencies[dependency]; exists {
			return errors.New("registry Package contains duplicate dependency references")
		}
		seenDependencies[dependency] = struct{}{}
	}
	seenConnectors := make(map[string]struct{}, len(pkg.Connectors))
	for _, connector := range pkg.Connectors {
		if strings.TrimSpace(connector.Type) == "" {
			return errors.New("registry Package connector reference is invalid")
		}
		if _, exists := seenConnectors[connector.Type]; exists {
			return errors.New("registry Package contains duplicate connector references")
		}
		seenConnectors[connector.Type] = struct{}{}
	}
	return nil
}

func validateSkill(skill CatalogSkill, registryID, packageID, skillID string) error {
	artifact := skill.Artifact
	if skill.RegistryID != registryID || skill.PackageID != packageID || skill.SkillID != skillID || skill.InstallID != strings.Join([]string{registryID, packageID, skillID}, "+") || !skillset.IsValidName(skill.InstallID) {
		return errors.New("registry Skill identity is invalid")
	}
	if artifact.Format != "memoh_skill_v1" || artifact.ContentType != "application/gzip" || !isCanonicalSHA256(artifact.Digest) || artifact.Size < 1 || artifact.Size > maxSkillArtifactCompressedBytes || artifact.UncompressedSize < 1 || artifact.UncompressedSize > maxSkillArtifactUncompressedBytes || artifact.ArchiveSize < 1 || artifact.ArchiveSize > maxSkillArtifactArchiveBytes || artifact.FileCount < 1 || artifact.FileCount > maxSkillArtifactFiles || strings.TrimSpace(artifact.DownloadURL) == "" {
		return errors.New("registry Skill Artifact descriptor is invalid")
	}
	return nil
}

func validatePackageBudget(skills []CatalogSkill) error {
	var compressed, uncompressed, archive int64
	files := 0
	for _, skill := range skills {
		artifact := skill.Artifact
		if artifact.Size > maxPackageArtifactsCompressed-compressed || artifact.UncompressedSize > maxPackageArtifactsUncompressed-uncompressed || artifact.ArchiveSize > maxPackageArtifactsArchive-archive || artifact.FileCount > maxPackageArtifactFiles-files {
			return errors.New("registry Package exceeds the aggregate Artifact limits")
		}
		compressed += artifact.Size
		uncompressed += artifact.UncompressedSize
		archive += artifact.ArchiveSize
		files += artifact.FileCount
	}
	return nil
}

func invalidPackage(err error) error {
	return apperror.Wrap(apperror.CodeRegistryPackageInvalid, err, nil)
}
