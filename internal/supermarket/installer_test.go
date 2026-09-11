package supermarket

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInstallerPreparationLimit(t *testing.T) {
	installer := NewInstaller(nil, nil, nil)
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
	first, err := AcquireInstallationResources(ctx, "app\x00bot\x00native\x00openai\x00documents")
	if err != nil {
		t.Fatalf("acquire first resource: %v", err)
	}
	defer first()

	otherCtx, cancelOther := context.WithCancel(ctx)
	defer cancelOther()
	other, err := AcquireInstallationResources(otherCtx, "app\x00bot\x00native\x00openai\x00spreadsheets")
	if err != nil {
		t.Fatalf("different resource was blocked: %v", err)
	}
	other()

	waitCtx, cancelWait := context.WithCancel(ctx)
	cancelWait()
	blocked, err := AcquireInstallationResources(waitCtx, "app\x00bot\x00native\x00openai\x00documents")
	if blocked != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("matching resource acquire = (%v, %v), want canceled", blocked != nil, err)
	}

	first()
	reacquired, err := AcquireInstallationResources(ctx, "app\x00bot\x00native\x00openai\x00documents")
	if err != nil {
		t.Fatalf("reacquire released resource: %v", err)
	}
	reacquired()
}

func TestInstallationResourceLocksSortAndDeduplicateKeys(t *testing.T) {
	release, err := AcquireInstallationResources(context.Background(), "b", "a", "b", " ")
	if err != nil {
		t.Fatalf("acquire resources: %v", err)
	}
	release()
	if len(installationResourceLocks.items) != 0 {
		t.Fatalf("resource locks leaked: %+v", installationResourceLocks.items)
	}
}

func TestValidateAppRejectsUnboundedArtifact(t *testing.T) {
	skill := CatalogSkill{
		RegistryID: "registry", AppID: "app", SkillID: "skill",
		InstallID: "registry+app+skill",
		Artifact: SkillArtifact{
			Format: "memoh_skill_v1", ContentType: "application/gzip",
			Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Size:   1, UncompressedSize: 1, ArchiveSize: 1, FileCount: 0,
			DownloadURL: "/api/artifacts/skill/digest",
		},
	}
	pkg := AppDescriptor{
		AppSummary: AppSummary{
			SchemaVersion: "2", RegistryID: "registry", AppID: "app", SkillCount: 1,
		},
		Revision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Skills:   []CatalogSkill{skill},
	}
	if err := validateApp(pkg, "registry", "app"); err == nil {
		t.Fatal("validateApp accepted an Artifact without a positive file count")
	}
}

func TestValidateAppRejectsIdentityCountAndDuplicates(t *testing.T) {
	validSkill := func(id string) CatalogSkill {
		return CatalogSkill{
			RegistryID: "registry", AppID: "app", SkillID: id,
			InstallID: "registry+app+" + id,
			Artifact: SkillArtifact{
				Format: "memoh_skill_v1", ContentType: "application/gzip",
				Digest: strings.Repeat("a", 64), Size: 1, UncompressedSize: 1,
				ArchiveSize: 1, FileCount: 1, DownloadURL: "/api/artifacts/skill/digest",
			},
		}
	}
	validApp := func() AppDescriptor {
		return AppDescriptor{
			AppSummary: AppSummary{
				SchemaVersion: "2", RegistryID: "registry", AppID: "app", SkillCount: 1,
			},
			Revision: strings.Repeat("b", 64), Skills: []CatalogSkill{validSkill("skill")},
		}
	}
	tests := map[string]func(*AppDescriptor){
		"registry identity": func(pkg *AppDescriptor) { pkg.RegistryID = "other" },
		"member count":      func(pkg *AppDescriptor) { pkg.SkillCount = 2 },
		"member app":        func(pkg *AppDescriptor) { pkg.Skills[0].AppID = "other" },
		"duplicate member": func(pkg *AppDescriptor) {
			pkg.Skills = append(pkg.Skills, pkg.Skills[0])
			pkg.SkillCount = 2
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			pkg := validApp()
			mutate(&pkg)
			if err := validateApp(pkg, "registry", "app"); err == nil {
				t.Fatal("validateApp accepted invalid App")
			}
		})
	}
	pkg := validApp()
	pkg.Skills[0].Artifact.UncompressedSize = maxAppArtifactsUncompressed + 1
	if err := validateAppBudget(pkg.Skills); err == nil {
		t.Fatal("validateAppBudget accepted an oversized App")
	}
}
