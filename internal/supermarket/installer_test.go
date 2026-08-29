package supermarket

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInstallerPreparationLimit(t *testing.T) {
	installer := NewInstaller(nil, nil, nil, nil)
	first, err := installer.acquirePreparation(context.Background())
	if err != nil {
		t.Fatalf("acquire first preparation: %v", err)
	}
	second, err := installer.acquirePreparation(context.Background())
	if err != nil {
		first()
		t.Fatalf("acquire second preparation: %v", err)
	}
	defer first()
	defer second()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, err := installer.acquirePreparation(ctx)
	if release != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("saturated acquire = (%v, %v), want canceled", release != nil, err)
	}
}

func TestInstallationResourceLocksSerializeOnlyMatchingResources(t *testing.T) {
	ctx := context.Background()
	first, err := acquireInstallationResources(ctx, "package\x00bot\x00native\x00openai\x00documents")
	if err != nil {
		t.Fatalf("acquire first resource: %v", err)
	}
	defer first()

	otherCtx, cancelOther := context.WithCancel(ctx)
	defer cancelOther()
	other, err := acquireInstallationResources(otherCtx, "package\x00bot\x00native\x00openai\x00spreadsheets")
	if err != nil {
		t.Fatalf("different resource was blocked: %v", err)
	}
	other()

	waitCtx, cancelWait := context.WithCancel(ctx)
	cancelWait()
	blocked, err := acquireInstallationResources(waitCtx, "package\x00bot\x00native\x00openai\x00documents")
	if blocked != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("matching resource acquire = (%v, %v), want canceled", blocked != nil, err)
	}

	first()
	reacquired, err := acquireInstallationResources(ctx, "package\x00bot\x00native\x00openai\x00documents")
	if err != nil {
		t.Fatalf("reacquire released resource: %v", err)
	}
	reacquired()
}

func TestInstallationResourceLocksSortAndDeduplicateKeys(t *testing.T) {
	release, err := acquireInstallationResources(context.Background(), "b", "a", "b", " ")
	if err != nil {
		t.Fatalf("acquire resources: %v", err)
	}
	release()
	if len(installationResourceLocks.items) != 0 {
		t.Fatalf("resource locks leaked: %+v", installationResourceLocks.items)
	}
}

func TestValidatePackageRejectsUnboundedArtifact(t *testing.T) {
	skill := CatalogSkill{
		RegistryID: "registry", PackageID: "package", SkillID: "skill",
		InstallID: "registry+package+skill",
		Artifact: SkillArtifact{
			Format: "memoh_skill_v1", ContentType: "application/gzip",
			Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Size:   1, UncompressedSize: 1, ArchiveSize: 1, FileCount: 0,
			DownloadURL: "/api/artifacts/skill/digest",
		},
	}
	pkg := SkillPackageDescriptor{
		SkillPackageSummary: SkillPackageSummary{
			SchemaVersion: "1", RegistryID: "registry", PackageID: "package", SkillCount: 1,
		},
		Revision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Skills:   []CatalogSkill{skill},
	}
	if err := validatePackage(pkg, "registry", "package"); err == nil {
		t.Fatal("validatePackage accepted an Artifact without a positive file count")
	}
}

func TestValidatePackageRejectsIdentityCountAndDuplicates(t *testing.T) {
	validSkill := func(id string) CatalogSkill {
		return CatalogSkill{
			RegistryID: "registry", PackageID: "package", SkillID: id,
			InstallID: "registry+package+" + id,
			Artifact: SkillArtifact{
				Format: "memoh_skill_v1", ContentType: "application/gzip",
				Digest: strings.Repeat("a", 64), Size: 1, UncompressedSize: 1,
				ArchiveSize: 1, FileCount: 1, DownloadURL: "/api/artifacts/skill/digest",
			},
		}
	}
	validPackage := func() SkillPackageDescriptor {
		return SkillPackageDescriptor{
			SkillPackageSummary: SkillPackageSummary{
				SchemaVersion: "1", RegistryID: "registry", PackageID: "package", SkillCount: 1,
			},
			Revision: strings.Repeat("b", 64), Skills: []CatalogSkill{validSkill("skill")},
		}
	}
	tests := map[string]func(*SkillPackageDescriptor){
		"registry identity": func(pkg *SkillPackageDescriptor) { pkg.RegistryID = "other" },
		"member count":      func(pkg *SkillPackageDescriptor) { pkg.SkillCount = 2 },
		"member package":    func(pkg *SkillPackageDescriptor) { pkg.Skills[0].PackageID = "other" },
		"duplicate member": func(pkg *SkillPackageDescriptor) {
			pkg.Skills = append(pkg.Skills, pkg.Skills[0])
			pkg.SkillCount = 2
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			pkg := validPackage()
			mutate(&pkg)
			if err := validatePackage(pkg, "registry", "package"); err == nil {
				t.Fatal("validatePackage accepted invalid Package")
			}
		})
	}
	pkg := validPackage()
	pkg.Skills[0].Artifact.UncompressedSize = maxPackageArtifactsUncompressed + 1
	if err := validatePackageBudget(pkg.Skills); err == nil {
		t.Fatal("validatePackageBudget accepted an oversized Package")
	}
}
