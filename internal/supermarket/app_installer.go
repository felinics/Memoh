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
	maxAppSkills                      = 128
	maxAppArtifactsCompressed         = 128 * 1024 * 1024
	maxAppArtifactsUncompressed       = 128 * 1024 * 1024
	maxAppArtifactsArchive            = 128 * 1024 * 1024
	maxAppArtifactFiles               = 10_000
)

// InstallSkillResponse describes one Skill written into the workspace.
type InstallSkillResponse struct {
	OK                bool   `json:"ok" validate:"required"`
	RegistryID        string `json:"registry_id" validate:"required"`
	AppID             string `json:"app_id" validate:"required"`
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

type preparedApp struct {
	descriptor  AppDescriptor
	skills      []preparedSkill
	workspaceOS string
}

// SkillPublication is a staged Skill change: the new Skills are in place but
// the previous copy is kept until Commit. Rollback restores it. A App
// without Skills stages the removal of any Skills a previous revision left.
type SkillPublication struct {
	publication       *skillset.AppPublication
	removal           *skillset.AppRemoval
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

// SkillRemoval is a staged removal of an App's Skills.
type SkillRemoval struct {
	removal           *skillset.AppRemoval
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

// FetchRelease downloads and validates one immutable App release.
func (i *Installer) FetchRelease(ctx context.Context, registryID, appID, revision string) (AppDescriptor, error) {
	registryID, appID, err := validateAppIdentity(registryID, appID)
	if err != nil {
		return AppDescriptor{}, err
	}
	revision = strings.TrimSpace(revision)
	if !isCanonicalSHA256(revision) {
		return AppDescriptor{}, &StatusError{Status: http.StatusBadRequest, Message: "revision is invalid"}
	}
	pkg, err := i.fetchAppRelease(ctx, registryID, appID, revision)
	if err != nil {
		return AppDescriptor{}, err
	}
	if pkg.Revision != revision {
		return AppDescriptor{}, invalidApp(errors.New("registry App revision does not match the request"))
	}
	if err := validateApp(pkg, registryID, appID); err != nil {
		return AppDescriptor{}, invalidApp(err)
	}
	if err := validateAppBudget(pkg.Skills); err != nil {
		return AppDescriptor{}, invalidApp(err)
	}
	return pkg, nil
}

// FetchCurrentApp reads the App descriptor the registry currently
// publishes, which names the newest revision.
func (i *Installer) FetchCurrentApp(ctx context.Context, registryID, appID string) (AppDescriptor, error) {
	registryID, appID, err := validateAppIdentity(registryID, appID)
	if err != nil {
		return AppDescriptor{}, err
	}
	if i == nil || i.client == nil {
		return AppDescriptor{}, errors.New("supermarket installer is not configured")
	}
	pkg, err := i.client.FetchCurrentApp(ctx, registryID, appID)
	if err != nil {
		return AppDescriptor{}, registryFetchError(err)
	}
	if err := validateApp(pkg, registryID, appID); err != nil {
		return AppDescriptor{}, invalidApp(err)
	}
	return pkg, nil
}

// PublishSkills downloads the Skills of a validated release and stages them
// into the workspace target. expectedRevision is the revision the caller has
// recorded for the App, or empty when it is new; a workspace copy that
// does not match it is replaced. The caller commits after recording the
// installation, or rolls back.
func (i *Installer) PublishSkills(ctx context.Context, botID, targetID string, pkg AppDescriptor, expectedRevision string) (*SkillPublication, error) {
	if i == nil || i.workspaces == nil {
		return nil, apperror.Wrap(apperror.CodeRegistryAppInstallFailed, errors.New("skill App installer is not configured"), nil)
	}
	targetCtx := workspace.WithWorkspaceTarget(ctx, targetID)
	target, err := i.workspaces.ResolveWorkspaceTarget(targetCtx, botID, targetID)
	if err != nil {
		return nil, &WorkspaceTargetError{Err: err}
	}
	if target.Client == nil {
		return nil, apperror.Wrap(apperror.CodeRegistryAppInstallFailed, errors.New("workspace is not reachable"), nil)
	}
	release, err := i.acquirePreparation(targetCtx)
	if err != nil {
		return nil, err
	}
	defer release()
	consistent, err := skillset.ReconcileApp(targetCtx, target.Client, pkg.RegistryID, pkg.AppID, expectedRevision)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeRegistryAppInstallFailed, fmt.Errorf("recover Registry App state: %w", err), nil)
	}
	if !consistent && i.logger != nil {
		i.logger.Warn("Skill App files did not match the recorded revision; replacing them",
			slog.String("registry_id", pkg.RegistryID), slog.String("app_id", pkg.AppID),
			slog.String("workspace_target_id", target.TargetID), slog.String("recorded_revision", expectedRevision),
		)
	}
	if len(pkg.Skills) == 0 {
		removal, err := skillset.PrepareAppRemoval(targetCtx, target.Client, pkg.RegistryID, pkg.AppID)
		if err != nil {
			return nil, apperror.Wrap(apperror.CodeRegistryAppInstallFailed, fmt.Errorf("clear previous Registry App Skills: %w", err), nil)
		}
		return &SkillPublication{removal: removal, Skills: []InstallSkillResponse{}, WorkspaceTargetID: target.TargetID}, nil
	}
	prepared, err := i.prepareApp(targetCtx, target.Info.OS, pkg)
	if err != nil {
		return nil, err
	}
	publication, published, err := publishApp(targetCtx, target.Client, prepared, target.TargetID)
	if err != nil {
		return nil, err
	}
	return &SkillPublication{publication: publication, Skills: published, WorkspaceTargetID: target.TargetID}, nil
}

// RemoveSkills stages the removal of an App's Skills from the workspace.
func (i *Installer) RemoveSkills(ctx context.Context, botID, targetID, registryID, appID, revision string) (*SkillRemoval, error) {
	if i == nil || i.workspaces == nil {
		return nil, errors.New("skill App installer is not configured")
	}
	targetCtx := workspace.WithWorkspaceTarget(ctx, targetID)
	target, err := i.workspaces.ResolveWorkspaceTarget(targetCtx, botID, targetID)
	if err != nil {
		return nil, &WorkspaceTargetError{Err: err}
	}
	if target.Client == nil {
		return nil, apperror.Wrap(apperror.CodeRegistryAppInstallFailed, errors.New("workspace is not reachable"), nil)
	}
	consistent, err := skillset.ReconcileApp(targetCtx, target.Client, registryID, appID, revision)
	if err != nil {
		return nil, fmt.Errorf("recover Skill App state: %w", err)
	}
	if !consistent && i.logger != nil {
		i.logger.Warn("Skill App files did not match the recorded revision; removing the managed path",
			slog.String("registry_id", registryID), slog.String("app_id", appID),
			slog.String("workspace_target_id", target.TargetID), slog.String("recorded_revision", revision),
		)
	}
	removal, err := skillset.PrepareAppRemoval(targetCtx, target.Client, registryID, appID)
	if err != nil {
		return nil, err
	}
	return &SkillRemoval{removal: removal, WorkspaceTargetID: target.TargetID}, nil
}

func validateAppIdentity(registryID, appID string) (string, string, error) {
	registryID = strings.TrimSpace(registryID)
	if !skillset.IsValidRegistryID(registryID) {
		return "", "", &StatusError{Status: http.StatusBadRequest, Message: "registry_id is invalid"}
	}
	appID = strings.TrimSpace(appID)
	if !skillset.IsValidRegistryComponent(appID) {
		return "", "", &StatusError{Status: http.StatusBadRequest, Message: "app_id is invalid"}
	}
	return registryID, appID, nil
}

func (i *Installer) fetchAppRelease(ctx context.Context, registryID, appID, revision string) (AppDescriptor, error) {
	if i == nil || i.client == nil {
		return AppDescriptor{}, errors.New("supermarket installer is not configured")
	}
	pkg, err := i.client.FetchAppRelease(ctx, registryID, appID, revision)
	if err == nil {
		return pkg, nil
	}
	return AppDescriptor{}, registryFetchError(err)
}

func registryFetchError(err error) error {
	switch ErrorKindOf(err) {
	case ErrorNotFound:
		return apperror.New(apperror.CodeRegistryAppNotFound, nil)
	case ErrorUnavailable:
		return apperror.Wrap(apperror.CodeRegistryUnavailable, fmt.Errorf("fetch Registry App: %w", err), nil)
	default:
		return invalidApp(fmt.Errorf("invalid Registry App: %w", err))
	}
}

func (i *Installer) prepareApp(ctx context.Context, workspaceOS string, pkg AppDescriptor) (preparedApp, error) {
	prepared := preparedApp{descriptor: pkg, skills: make([]preparedSkill, 0, len(pkg.Skills)), workspaceOS: workspaceOS}
	for _, skill := range pkg.Skills {
		item, err := i.prepareSkill(ctx, skill)
		if err != nil {
			return preparedApp{}, err
		}
		prepared.skills = append(prepared.skills, item)
	}
	return prepared, nil
}

func (i *Installer) prepareSkill(ctx context.Context, skill CatalogSkill) (preparedSkill, error) {
	artifact := skill.Artifact
	content, err := i.client.DownloadArtifact(ctx, ArtifactDownloadDescriptor{Digest: artifact.Digest, Size: artifact.Size, DownloadURL: artifact.DownloadURL})
	if err != nil {
		code := apperror.CodeRegistryAppInvalid
		if ErrorKindOf(err) == ErrorUnavailable {
			code = apperror.CodeRegistryUnavailable
		}
		return preparedSkill{}, apperror.Wrap(code, fmt.Errorf("download Registry Skill Artifact: %w", err), nil)
	}
	archive, err := skillset.ReadArchiveWithLimits(content, artifact.UncompressedSize, artifact.ArchiveSize, artifact.FileCount)
	if err != nil {
		return preparedSkill{}, invalidApp(err)
	}
	if archive.UncompressedSize() != artifact.UncompressedSize || archive.ArchiveSize() != artifact.ArchiveSize || archive.FileCount() != artifact.FileCount {
		return preparedSkill{}, invalidApp(errors.New("registry Skill Artifact contents do not match its descriptor"))
	}
	return preparedSkill{skill: skill, archive: archive}, nil
}

func publishApp(ctx context.Context, client *bridge.Client, prepared preparedApp, workspaceTargetID string) (*skillset.AppPublication, []InstallSkillResponse, error) {
	if client == nil {
		return nil, nil, apperror.Wrap(apperror.CodeRegistryAppInstallFailed, errors.New("workspace is not reachable"), nil)
	}
	members := make([]skillset.AppArchive, 0, len(prepared.skills))
	for _, item := range prepared.skills {
		members = append(members, skillset.AppArchive{SkillID: item.skill.SkillID, Archive: item.archive})
	}
	publication, err := skillset.PublishApp(
		ctx,
		client,
		prepared.workspaceOS,
		prepared.descriptor.RegistryID,
		prepared.descriptor.AppID,
		prepared.descriptor.Revision,
		members,
	)
	if err != nil {
		return nil, nil, apperror.Wrap(apperror.CodeRegistryAppInstallFailed, fmt.Errorf("publish Registry App: %w", err), nil)
	}
	installed := make([]InstallSkillResponse, 0, len(prepared.skills))
	for _, item := range prepared.skills {
		installed = append(installed, InstallSkillResponse{OK: true, RegistryID: item.skill.RegistryID, AppID: item.skill.AppID, SkillID: item.skill.SkillID, InstallID: item.skill.InstallID, WorkspaceTargetID: workspaceTargetID, ArtifactDigest: item.skill.Artifact.Digest, FilesWritten: item.archive.FileCount()})
	}
	return publication, installed, nil
}

func validateApp(pkg AppDescriptor, registryID, appID string) error {
	if pkg.SchemaVersion != "2" || pkg.RegistryID != registryID || pkg.AppID != appID || !isCanonicalSHA256(pkg.Revision) || len(pkg.Skills) > maxAppSkills || pkg.SkillCount != len(pkg.Skills) {
		return errors.New("registry App release is invalid")
	}
	if len(pkg.Skills) == 0 && len(pkg.Dependencies) == 0 && len(pkg.Connectors) == 0 {
		return errors.New("registry App release is empty")
	}
	seen := make(map[string]struct{}, len(pkg.Skills))
	for _, skill := range pkg.Skills {
		if _, exists := seen[skill.SkillID]; exists {
			return errors.New("registry App contains duplicate Skills")
		}
		seen[skill.SkillID] = struct{}{}
		if err := validateSkill(skill, registryID, appID, skill.SkillID); err != nil {
			return err
		}
	}
	seenDependencies := make(map[string]struct{}, len(pkg.Dependencies))
	for _, dependency := range pkg.Dependencies {
		if strings.TrimSpace(dependency) == "" {
			return errors.New("registry App dependency reference is invalid")
		}
		if _, exists := seenDependencies[dependency]; exists {
			return errors.New("registry App contains duplicate dependency references")
		}
		seenDependencies[dependency] = struct{}{}
	}
	seenConnectors := make(map[string]struct{}, len(pkg.Connectors))
	for _, connector := range pkg.Connectors {
		if strings.TrimSpace(connector.Type) == "" {
			return errors.New("registry App connector reference is invalid")
		}
		if _, exists := seenConnectors[connector.Type]; exists {
			return errors.New("registry App contains duplicate connector references")
		}
		seenConnectors[connector.Type] = struct{}{}
	}
	return nil
}

func validateSkill(skill CatalogSkill, registryID, appID, skillID string) error {
	artifact := skill.Artifact
	if skill.RegistryID != registryID || skill.AppID != appID || skill.SkillID != skillID || skill.InstallID != strings.Join([]string{registryID, appID, skillID}, "+") || !skillset.IsValidName(skill.InstallID) {
		return errors.New("registry Skill identity is invalid")
	}
	if artifact.Format != "memoh_skill_v1" || artifact.ContentType != "application/gzip" || !isCanonicalSHA256(artifact.Digest) || artifact.Size < 1 || artifact.Size > maxSkillArtifactCompressedBytes || artifact.UncompressedSize < 1 || artifact.UncompressedSize > maxSkillArtifactUncompressedBytes || artifact.ArchiveSize < 1 || artifact.ArchiveSize > maxSkillArtifactArchiveBytes || artifact.FileCount < 1 || artifact.FileCount > maxSkillArtifactFiles || strings.TrimSpace(artifact.DownloadURL) == "" {
		return errors.New("registry Skill Artifact descriptor is invalid")
	}
	return nil
}

func validateAppBudget(skills []CatalogSkill) error {
	var compressed, uncompressed, archive int64
	files := 0
	for _, skill := range skills {
		artifact := skill.Artifact
		if artifact.Size > maxAppArtifactsCompressed-compressed || artifact.UncompressedSize > maxAppArtifactsUncompressed-uncompressed || artifact.ArchiveSize > maxAppArtifactsArchive-archive || artifact.FileCount > maxAppArtifactFiles-files {
			return errors.New("registry App exceeds the aggregate Artifact limits")
		}
		compressed += artifact.Size
		uncompressed += artifact.UncompressedSize
		archive += artifact.ArchiveSize
		files += artifact.FileCount
	}
	return nil
}

func invalidApp(err error) error {
	return apperror.Wrap(apperror.CodeRegistryAppInvalid, err, nil)
}
