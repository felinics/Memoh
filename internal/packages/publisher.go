package packages

import (
	"context"

	"github.com/felinics/memoh/internal/supermarket"
)

// installerPublisher adapts *supermarket.Installer to SkillPublisher and
// RegistryClient.
type installerPublisher struct {
	installer *supermarket.Installer
}

// NewSupermarketPublisher exposes a Supermarket installer as the Skill
// publisher and registry client of the packages service.
func NewSupermarketPublisher(installer *supermarket.Installer) interface {
	SkillPublisher
	RegistryClient
} {
	return &installerPublisher{installer: installer}
}

func (p *installerPublisher) ResolveTargetID(ctx context.Context, botID, targetID string) (string, error) {
	return p.installer.ResolveTargetID(ctx, botID, targetID)
}

func (p *installerPublisher) PublishSkills(ctx context.Context, botID, targetID string, pkg supermarket.SkillPackageDescriptor, expectedRevision string) (SkillTransaction, []supermarket.InstallSkillResponse, error) {
	publication, err := p.installer.PublishSkills(ctx, botID, targetID, pkg, expectedRevision)
	if err != nil {
		return nil, nil, err
	}
	return publication, publication.Skills, nil
}

func (p *installerPublisher) RemoveSkills(ctx context.Context, botID, targetID, registryID, packageID, revision string) (SkillTransaction, error) {
	removal, err := p.installer.RemoveSkills(ctx, botID, targetID, registryID, packageID, revision)
	if err != nil {
		return nil, err
	}
	return removal, nil
}

func (p *installerPublisher) FetchRelease(ctx context.Context, registryID, packageID, revision string) (supermarket.SkillPackageDescriptor, error) {
	return p.installer.FetchRelease(ctx, registryID, packageID, revision)
}

func (p *installerPublisher) FetchCurrentPackage(ctx context.Context, registryID, packageID string) (supermarket.SkillPackageDescriptor, error) {
	return p.installer.FetchCurrentPackage(ctx, registryID, packageID)
}
