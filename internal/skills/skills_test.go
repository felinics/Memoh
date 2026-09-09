package skills

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestParseFileFallbacks(t *testing.T) {
	raw := "# Use this skill\n\nDo something useful."
	got := ParseFile(raw, "plain-skill")

	if got.Name != "plain-skill" {
		t.Fatalf("expected name plain-skill, got %q", got.Name)
	}
	if got.Description != "plain-skill" {
		t.Fatalf("expected description plain-skill, got %q", got.Description)
	}
	if got.Content != raw {
		t.Fatalf("expected content to keep original markdown, got %q", got.Content)
	}
}

func TestParseFileMalformedMetadataFailsRuntimeClosed(t *testing.T) {
	raw := "---\nname: bad-metadata\ndescription: Bad metadata\nmetadata: runtime-visible\n---\n\n# Bad"
	parsed := ParseFile(raw, "fallback")
	entry := entryFromParsed(parsed, raw, Root{
		Path:    ManagedDirPath,
		Kind:    SourceKindManaged,
		Managed: true,
	}, pathJoin(ManagedDirPath, "bad-metadata", "SKILL.md"))
	entry.State = StateEffective
	entry = NormalizeRuntimeUsability(entry)

	if parsed.Name != "bad-metadata" {
		t.Fatalf("parsed.Name = %q, want bad-metadata", parsed.Name)
	}
	if entry.RuntimeUsable {
		t.Fatal("malformed metadata entry is runtime usable, want fail-closed")
	}
	if entry.RuntimeUnusableReason != "metadata" {
		t.Fatalf("RuntimeUnusableReason = %q, want metadata", entry.RuntimeUnusableReason)
	}
}

func TestResolveSupportsDisabledFallbackAndShadowing(t *testing.T) {
	items := []Entry{
		{Name: "alpha", SourcePath: "/data/skills/alpha/SKILL.md", Managed: true, SourceKind: SourceKindManaged},
		{Name: "alpha", SourcePath: "/data/.agents/skills/alpha/SKILL.md", SourceKind: SourceKindCompat},
		{Name: "beta", SourcePath: "/data/.agents/skills/beta/SKILL.md", SourceKind: SourceKindCompat},
	}

	resolved := resolve(items, map[string]indexOverride{
		"/data/skills/alpha/SKILL.md": {Disabled: true},
	})

	managedAlpha, ok := findBySourcePath(resolved, "/data/skills/alpha/SKILL.md")
	if !ok {
		t.Fatalf("managed alpha not found in resolved items")
	}
	if managedAlpha.State != StateDisabled {
		t.Fatalf("managed alpha state = %q, want disabled", managedAlpha.State)
	}
	compatAlpha, ok := findBySourcePath(resolved, "/data/.agents/skills/alpha/SKILL.md")
	if !ok {
		t.Fatalf("compat alpha not found in resolved items")
	}
	if compatAlpha.State != StateEffective {
		t.Fatalf("compat alpha state = %q, want effective", compatAlpha.State)
	}
	beta, ok := findBySourcePath(resolved, "/data/.agents/skills/beta/SKILL.md")
	if !ok {
		t.Fatalf("beta not found in resolved items")
	}
	if beta.State != StateEffective {
		t.Fatalf("beta state = %q, want effective", beta.State)
	}
}

func TestListReadsFullRawContentAndWritesIndex(t *testing.T) {
	client := newFakeClient()
	userPackage := pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage)
	client.listings[ManagedDirPath] = []*pb.FileEntry{{Path: UserSkillNamespace, IsDir: true}}
	client.listings[pathJoin(ManagedDirPath, UserSkillNamespace)] = []*pb.FileEntry{{Path: UserSkillPackage, IsDir: true}}
	client.listings[userPackage] = []*pb.FileEntry{{Path: "alpha", IsDir: true}}
	client.files[pathJoin(userPackage, "alpha", "SKILL.md")] = "---\nname: alpha\ndescription: Alpha\n---\n\n" + strings.Repeat("A", 7000)

	items, err := List(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].Raw) <= 7000 {
		t.Fatalf("expected full raw content, got len=%d", len(items[0].Raw))
	}
	if _, ok := client.files[IndexFilePath]; !ok {
		t.Fatalf("expected index file to be written")
	}
}

func TestApplyActionAdoptAndDisable(t *testing.T) {
	client := newFakeClient()
	externalPath := pathJoin("/data/.agents/skills", "alpha", "SKILL.md")
	client.listings["/data/.agents/skills"] = []*pb.FileEntry{{Path: "alpha", IsDir: true}}
	client.files[externalPath] = "---\nname: alpha\ndescription: Alpha\n---\n\n# Alpha"

	if err := ApplyAction(context.Background(), client, nil, ActionRequest{
		Action:     ActionAdopt,
		TargetPath: externalPath,
	}); err != nil {
		t.Fatalf("adopt returned error: %v", err)
	}
	if _, ok := client.files[pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha", "SKILL.md")]; !ok {
		t.Fatalf("expected managed copy after adopt")
	}

	if err := ApplyAction(context.Background(), client, nil, ActionRequest{
		Action:     ActionDisable,
		TargetPath: externalPath,
	}); err != nil {
		t.Fatalf("disable returned error: %v", err)
	}
	idx := readIndex(context.Background(), client)
	if !idx.Overrides[externalPath].Disabled {
		t.Fatalf("expected disabled override for %s", externalPath)
	}
}

func TestApplyActionRejectsRegistryPackageSkill(t *testing.T) {
	client := newFakeClient()
	targetPath := pathJoin(ManagedDirPath, "openai-api-curated", "docs", "xlsx", "SKILL.md")
	for _, action := range []string{ActionDisable, ActionEnable, ActionAdopt} {
		if err := ApplyAction(context.Background(), client, nil, ActionRequest{
			Action: action, TargetPath: targetPath,
		}); !errors.Is(err, ErrRegistrySkillReadOnly) {
			t.Fatalf("ApplyAction(%s) error = %v, want ErrRegistrySkillReadOnly", action, err)
		}
	}
}

func TestApplyActionAdoptRejectsInvalidManagedName(t *testing.T) {
	client := newFakeClient()
	externalPath := pathJoin("/data/.agents/skills", "escape", "SKILL.md")
	client.listings["/data/.agents/skills"] = []*pb.FileEntry{{Path: "escape", IsDir: true}}
	client.files[externalPath] = "---\nname: ..\ndescription: Escape\n---\n\n# Escape"

	err := ApplyAction(context.Background(), client, nil, ActionRequest{
		Action:     ActionAdopt,
		TargetPath: externalPath,
	})
	if !errors.Is(err, bridge.ErrBadRequest) {
		t.Fatalf("adopt err = %v, want ErrBadRequest", err)
	}
	if _, ok := client.files[pathJoin(ManagedDirPath, "..", "SKILL.md")]; ok {
		t.Fatalf("unexpected managed write for invalid adopted name")
	}
}

func TestApplyActionAdoptKeepsUserSkillSeparateFromRegistryNamespace(t *testing.T) {
	client := newFakeClient()
	registryID := "openai-api-curated"
	externalPath := pathJoin("/data/.agents/skills", registryID, "SKILL.md")
	client.listings["/data/.agents/skills"] = []*pb.FileEntry{{Path: registryID, IsDir: true}}
	client.files[externalPath] = "---\nname: " + registryID + "\ndescription: Compat\n---\n\n# Compat"

	registryRoot := pathJoin(ManagedDirPath, registryID)
	packageRoot := pathJoin(registryRoot, "docs")
	client.listings[ManagedDirPath] = []*pb.FileEntry{{Path: registryID, IsDir: true}}
	client.listings[registryRoot] = []*pb.FileEntry{{Path: "docs", IsDir: true}}
	client.listings[packageRoot] = []*pb.FileEntry{{Path: "xlsx", IsDir: true}}
	client.files[pathJoin(packageRoot, "xlsx", "SKILL.md")] = "---\nname: xlsx\ndescription: Spreadsheet\n---\n\n# Spreadsheet"

	err := ApplyAction(context.Background(), client, nil, ActionRequest{
		Action:     ActionAdopt,
		TargetPath: externalPath,
	})
	if err != nil {
		t.Fatalf("adopt error = %v", err)
	}
	if _, ok := client.files[pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, registryID, "SKILL.md")]; !ok {
		t.Fatal("adopt did not write the user Skill")
	}
	if _, ok := client.files[pathJoin(registryRoot, "SKILL.md")]; ok {
		t.Fatal("adopt wrote into the Registry namespace")
	}
}

func TestIsValidNameRejectsTraversalPatterns(t *testing.T) {
	for _, name := range []string{
		"",
		".",
		"..",
		".hidden",
		"alpha..beta",
		"../escape",
		"alpha/../beta",
	} {
		if IsValidName(name) {
			t.Fatalf("IsValidName(%q) = true, want false", name)
		}
	}

	for _, name := range []string{"alpha", "alpha-beta", "alpha_beta", "alpha.beta", "registry+package+skill"} {
		if !IsValidName(name) {
			t.Fatalf("IsValidName(%q) = false, want true", name)
		}
	}
}

func TestUserSkillDirForNameRejectsEscapingNames(t *testing.T) {
	for _, name := range []string{".", "..", ".alpha", "alpha..beta"} {
		if _, err := userSkillDirForName(name); !errors.Is(err, bridge.ErrBadRequest) {
			t.Fatalf("userSkillDirForName(%q) err = %v, want ErrBadRequest", name, err)
		}
	}

	dirPath, err := userSkillDirForName("alpha.beta")
	if err != nil {
		t.Fatalf("userSkillDirForName(valid) returned error: %v", err)
	}
	if dirPath != pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha.beta") {
		t.Fatalf("userSkillDirForName(valid) = %q, want canonical user path", dirPath)
	}
}

func TestDeletableSkillDirForSourcePath(t *testing.T) {
	flat, err := DeletableSkillDirForSourcePath(pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatalf("DeletableSkillDirForSourcePath(flat) error = %v", err)
	}
	if flat != pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha") {
		t.Fatalf("DeletableSkillDirForSourcePath(flat) = %q", flat)
	}

	if _, err := DeletableSkillDirForSourcePath(pathJoin(IndexDirPath, "skill-creator", "SKILL.md")); !errors.Is(err, ErrBuiltinSkillReadOnly) {
		t.Fatalf("DeletableSkillDirForSourcePath(builtin) err = %v, want ErrBuiltinSkillReadOnly", err)
	}

	registryPath := pathJoin(ManagedDirPath, "openai-api-curated", "docs", "xlsx", "SKILL.md")
	if _, err := DeletableSkillDirForSourcePath(registryPath); !errors.Is(err, ErrRegistrySkillReadOnly) {
		t.Fatalf("DeletableSkillDirForSourcePath(registry) error = %v, want ErrRegistrySkillReadOnly", err)
	}

	for _, sourcePath := range []string{
		"",
		"relative/SKILL.md",
		pathJoin(ManagedDirPath, "alpha", "README.md"),
		"/data/.agents/skills/alpha/SKILL.md",
		pathJoin(ManagedDirPath, "..", "escape", "SKILL.md"),
	} {
		if _, err := DeletableSkillDirForSourcePath(sourcePath); !errors.Is(err, bridge.ErrBadRequest) {
			t.Fatalf("DeletableSkillDirForSourcePath(%q) err = %v, want ErrBadRequest", sourcePath, err)
		}
	}
}

func TestPrunableSkillNamespaceDirs(t *testing.T) {
	got := PrunableSkillNamespaceDirs(pathJoin(ManagedDirPath, "openai-api-curated", "docs", "xlsx"))
	want := []string{
		pathJoin(ManagedDirPath, "openai-api-curated", "docs"),
		pathJoin(ManagedDirPath, "openai-api-curated"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("PrunableSkillNamespaceDirs(registry) = %+v, want %+v", got, want)
	}
	if got := PrunableSkillNamespaceDirs(pathJoin(ManagedDirPath, "alpha")); got != nil {
		t.Fatalf("PrunableSkillNamespaceDirs(invalid) = %+v, want nil", got)
	}
}

func TestPlanUpsertCreateRenameAndRejectRegistryEdit(t *testing.T) {
	create, err := PlanUpsert("---\nname: alpha\ndescription: A\n---\n\n# A\n", "")
	if err != nil {
		t.Fatalf("PlanUpsert(create) error = %v", err)
	}
	if create.WritePath != pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha", "SKILL.md") || create.RenameFromDir != "" {
		t.Fatalf("PlanUpsert(create) = %+v", create)
	}

	same, err := PlanUpsert("---\nname: alpha\ndescription: A\n---\n\n# A\n", pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatalf("PlanUpsert(same) error = %v", err)
	}
	if same.WritePath != pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha", "SKILL.md") || same.RenameFromDir != "" {
		t.Fatalf("PlanUpsert(same) = %+v", same)
	}

	rename, err := PlanUpsert("---\nname: beta\ndescription: B\n---\n\n# B\n", pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha", "SKILL.md"))
	if err != nil {
		t.Fatalf("PlanUpsert(rename) error = %v", err)
	}
	if rename.WritePath != pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "beta", "SKILL.md") {
		t.Fatalf("PlanUpsert(rename).WritePath = %q", rename.WritePath)
	}
	if rename.RenameFromDir != pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha") {
		t.Fatalf("PlanUpsert(rename).RenameFromDir = %q", rename.RenameFromDir)
	}

	registryPath := pathJoin(ManagedDirPath, "openai-api-curated", "docs", "xlsx", "SKILL.md")
	if _, err := PlanUpsert("---\nname: xlsx\ndescription: Sheet\n---\n\n# Sheet\n", registryPath); !errors.Is(err, ErrRegistrySkillReadOnly) {
		t.Fatalf("PlanUpsert(registry) error = %v, want ErrRegistrySkillReadOnly", err)
	}

	builtinPath := pathJoin(IndexDirPath, "skill-creator", "SKILL.md")
	if _, err := PlanUpsert("---\nname: skill-creator\ndescription: Creator\n---\n\n# Creator\n", builtinPath); !errors.Is(err, ErrBuiltinSkillReadOnly) {
		t.Fatalf("PlanUpsert(builtin) error = %v, want ErrBuiltinSkillReadOnly", err)
	}

	override, err := PlanUpsert("---\nname: alpha\ndescription: A\n---\n\n# A\n", "/data/.agents/skills/alpha/SKILL.md")
	if err != nil {
		t.Fatalf("PlanUpsert(override) error = %v", err)
	}
	if override.WritePath != pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "alpha", "SKILL.md") || override.RenameFromDir != "" {
		t.Fatalf("PlanUpsert(override) = %+v", override)
	}
}

func TestNamespacedSkillPaths(t *testing.T) {
	for _, tc := range []struct {
		registryID, packageID, skillID string
	}{
		{".", "pkg", "skill"},
		{"reg", "..", "skill"},
		{"reg", "pkg", "alpha/beta"},
		{"reg", "pkg", ".hidden"},
		{"user", "pkg", "skill"},
		{"Registry", "pkg", "skill"},
		{"reg", "pkg..v2", "skill"},
		{"reg", "pkg.", "skill"},
		{"reg", "-pkg", "skill"},
		{"reg", "pkg", "_skill"},
		{"reg", "con", "skill"},
		{"reg", "pkg", "nul.txt"},
		{"reg", "pkg", strings.Repeat("a", maxPortableResourceIDBytes+1)},
	} {
		if _, err := SkillDirForIDs(tc.registryID, tc.packageID, tc.skillID); !errors.Is(err, bridge.ErrBadRequest) {
			t.Fatalf("SkillDirForIDs(%q,%q,%q) err = %v, want ErrBadRequest", tc.registryID, tc.packageID, tc.skillID, err)
		}
	}

	dirPath, err := SkillDirForIDs("openai-api-curated", "docs", "xlsx")
	if err != nil {
		t.Fatalf("SkillDirForIDs(valid) error = %v", err)
	}
	want := pathJoin(ManagedDirPath, "openai-api-curated", "docs", "xlsx")
	if dirPath != want {
		t.Fatalf("SkillDirForIDs(valid) = %q, want %q", dirPath, want)
	}

	packagePath, err := SkillPackageDirForIDs("openai-api-curated", "docs")
	if err != nil {
		t.Fatalf("SkillPackageDirForIDs(valid) error = %v", err)
	}
	wantPackage := pathJoin(ManagedDirPath, "openai-api-curated", "docs")
	if packagePath != wantPackage {
		t.Fatalf("SkillPackageDirForIDs(valid) = %q, want %q", packagePath, wantPackage)
	}
	registryID, packageID, skillID, ok := RegistrySkillIDs(pathJoin(dirPath, "SKILL.md"))
	if !ok || registryID != "openai-api-curated" || packageID != "docs" || skillID != "xlsx" {
		t.Fatalf("RegistrySkillIDs() = %q/%q/%q, %v", registryID, packageID, skillID, ok)
	}
	if _, _, _, ok := RegistrySkillIDs(pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage, "xlsx", "SKILL.md")); ok {
		t.Fatal("RegistrySkillIDs() accepted a user-authored Skill")
	}
	dottedPath, err := SkillDirForIDs("openai.api", "docs.v2", "xlsx.reader")
	if err != nil {
		t.Fatalf("SkillDirForIDs(dotted) error = %v", err)
	}
	if dottedPath != pathJoin(ManagedDirPath, "openai.api", "docs.v2", "xlsx.reader") {
		t.Fatalf("SkillDirForIDs(dotted) = %q", dottedPath)
	}
}

func TestListDiscoversRegistrySkillsWithoutRenaming(t *testing.T) {
	client := newFakeClient()
	packagePath := pathJoin(ManagedDirPath, "openai-api-curated", "docs")
	skillPath := pathJoin(packagePath, "xlsx")
	client.listings[ManagedDirPath] = []*pb.FileEntry{{Path: "openai-api-curated", IsDir: true}}
	client.listings[pathJoin(ManagedDirPath, "openai-api-curated")] = []*pb.FileEntry{{Path: "docs", IsDir: true}}
	client.listings[packagePath] = []*pb.FileEntry{{Path: "xlsx", IsDir: true}}
	client.listings[skillPath] = []*pb.FileEntry{{Path: "SKILL.md"}}
	client.files[pathJoin(skillPath, "SKILL.md")] = "---\nname: xlsx\ndescription: Spreadsheet helper\n---\n\n# XLSX\n"
	items, err := List(context.Background(), client, []string{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	got, ok := findBySourcePath(items, pathJoin(skillPath, "SKILL.md"))
	if !ok {
		t.Fatalf("registry skill not discovered: %+v", items)
	}
	if got.Name != "xlsx" {
		t.Fatalf("name = %q, want xlsx", got.Name)
	}
	if got.SourceKind != SourceKindRegistry {
		t.Fatalf("source_kind = %q, want %q", got.SourceKind, SourceKindRegistry)
	}
	if !got.Managed {
		t.Fatal("registry skill should be managed")
	}
	if got.State != StateEffective {
		t.Fatalf("state = %q, want %q", got.State, StateEffective)
	}
}

func TestListDiscoversInstalledRegistrySkillDirectory(t *testing.T) {
	client := newFakeClient()
	packagePath := pathJoin(ManagedDirPath, "openai", "documents")
	skillDir := pathJoin(packagePath, "pdf")
	skillPath := pathJoin(skillDir, "SKILL.md")
	client.listings[ManagedDirPath] = []*pb.FileEntry{{Path: "openai", IsDir: true}}
	client.listings[pathJoin(ManagedDirPath, "openai")] = []*pb.FileEntry{{Path: "documents", IsDir: true}}
	client.listings[packagePath] = []*pb.FileEntry{{Path: "pdf", IsDir: true}}
	client.listings[skillDir] = []*pb.FileEntry{{Path: "SKILL.md"}}
	client.files[skillPath] = "---\nname: pdf\ndescription: PDF helper\n---\n\n# PDF\n"

	items, err := List(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	got, ok := findBySourcePath(items, skillPath)
	if !ok || got.SourceKind != SourceKindRegistry {
		t.Fatalf("installed Registry Skill = %+v, %v", got, ok)
	}
}

func TestListDiscoversUserAndRegistryNamespaces(t *testing.T) {
	client := newFakeClient()
	registryPath := pathJoin(ManagedDirPath, "openai-api-curated")
	packagePath := pathJoin(registryPath, "docs")
	skillPath := pathJoin(packagePath, "xlsx")
	userPackage := pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage)
	client.listings[ManagedDirPath] = []*pb.FileEntry{{Path: "openai-api-curated", IsDir: true}, {Path: UserSkillNamespace, IsDir: true}}
	client.listings[pathJoin(ManagedDirPath, UserSkillNamespace)] = []*pb.FileEntry{{Path: UserSkillPackage, IsDir: true}}
	client.listings[userPackage] = []*pb.FileEntry{{Path: "openai-api-curated", IsDir: true}}
	client.listings[registryPath] = []*pb.FileEntry{{Path: "docs", IsDir: true}}
	client.listings[packagePath] = []*pb.FileEntry{{Path: "xlsx", IsDir: true}}
	client.listings[skillPath] = []*pb.FileEntry{{Path: "SKILL.md"}}
	client.files[pathJoin(userPackage, "openai-api-curated", "SKILL.md")] = "---\nname: openai-api-curated\ndescription: User\n---\n\n# User\n"
	client.files[pathJoin(skillPath, "SKILL.md")] = "---\nname: xlsx\ndescription: Spreadsheet\n---\n\n# XLSX\n"
	items, err := List(context.Background(), client, []string{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if _, ok := findBySourcePath(items, pathJoin(userPackage, "openai-api-curated", "SKILL.md")); !ok {
		t.Fatalf("user skill not discovered: %+v", items)
	}
	registry, ok := findBySourcePath(items, pathJoin(skillPath, "SKILL.md"))
	if !ok {
		t.Fatalf("registry skill was not discovered: %+v", items)
	}
	if registry.SourceKind != SourceKindRegistry {
		t.Fatalf("source_kind = %q, want %q", registry.SourceKind, SourceKindRegistry)
	}
}

func TestListPrefersUserNamespaceOverSameNamedRegistrySkill(t *testing.T) {
	client := newFakeClient()
	userPackage := pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage)
	registryPackage := pathJoin(ManagedDirPath, "openai", "documents")
	userPath := pathJoin(userPackage, "pdf", "SKILL.md")
	registryPath := pathJoin(registryPackage, "pdf", "SKILL.md")
	client.listings[ManagedDirPath] = []*pb.FileEntry{{Path: "openai", IsDir: true}, {Path: UserSkillNamespace, IsDir: true}}
	client.listings[pathJoin(ManagedDirPath, "openai")] = []*pb.FileEntry{{Path: "documents", IsDir: true}}
	client.listings[registryPackage] = []*pb.FileEntry{{Path: "pdf", IsDir: true}}
	client.listings[pathJoin(registryPackage, "pdf")] = []*pb.FileEntry{{Path: "SKILL.md"}}
	client.listings[pathJoin(ManagedDirPath, UserSkillNamespace)] = []*pb.FileEntry{{Path: UserSkillPackage, IsDir: true}}
	client.listings[userPackage] = []*pb.FileEntry{{Path: "pdf", IsDir: true}}
	client.files[registryPath] = "---\nname: pdf\ndescription: Registry PDF\n---\n\n# Registry\n"
	client.files[userPath] = "---\nname: pdf\ndescription: User PDF\n---\n\n# User\n"
	items, err := List(context.Background(), client, []string{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	user, ok := findBySourcePath(items, userPath)
	if !ok || user.State != StateEffective {
		t.Fatalf("user Skill = %+v, want effective", user)
	}
	registry, ok := findBySourcePath(items, registryPath)
	if !ok || registry.State != StateShadowed || registry.ShadowedBy != userPath {
		t.Fatalf("registry Skill = %+v, want shadowed by %q", registry, userPath)
	}
}

func TestListKeepsBuiltinAndCompatAheadOfRegistrySkills(t *testing.T) {
	client := newFakeClient()
	registryNamespace := pathJoin(ManagedDirPath, "openai")
	registryPackage := pathJoin(registryNamespace, "tools")
	builtinPath := pathJoin(IndexDirPath, "shared-builtin", "SKILL.md")
	compatRoot := "/data/.agents/skills"
	compatPath := pathJoin(compatRoot, "shared-compat", "SKILL.md")
	registryBuiltinPath := pathJoin(registryPackage, "registry-builtin", "SKILL.md")
	registryCompatPath := pathJoin(registryPackage, "registry-compat", "SKILL.md")

	client.listings[ManagedDirPath] = []*pb.FileEntry{{Path: "openai", IsDir: true}}
	client.listings[registryNamespace] = []*pb.FileEntry{{Path: "tools", IsDir: true}}
	client.listings[registryPackage] = []*pb.FileEntry{
		{Path: "registry-builtin", IsDir: true},
		{Path: "registry-compat", IsDir: true},
	}
	client.listings[pathJoin(registryPackage, "registry-builtin")] = []*pb.FileEntry{{Path: "SKILL.md"}}
	client.listings[pathJoin(registryPackage, "registry-compat")] = []*pb.FileEntry{{Path: "SKILL.md"}}
	client.listings[IndexDirPath] = []*pb.FileEntry{{Path: "shared-builtin", IsDir: true}}
	client.listings[compatRoot] = []*pb.FileEntry{{Path: "shared-compat", IsDir: true}}
	client.files[builtinPath] = "---\nname: shared-builtin\ndescription: Built-in\n---\n\n# Built-in\n"
	client.files[compatPath] = "---\nname: shared-compat\ndescription: Compat\n---\n\n# Compat\n"
	client.files[registryBuiltinPath] = "---\nname: shared-builtin\ndescription: Registry\n---\n\n# Registry\n"
	client.files[registryCompatPath] = "---\nname: shared-compat\ndescription: Registry\n---\n\n# Registry\n"

	items, err := List(context.Background(), client, []string{compatRoot})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	for effectivePath, registryPath := range map[string]string{
		builtinPath: registryBuiltinPath,
		compatPath:  registryCompatPath,
	} {
		effective, ok := findBySourcePath(items, effectivePath)
		if !ok || effective.State != StateEffective {
			t.Fatalf("Skill at %q = %+v, want effective", effectivePath, effective)
		}
		registry, ok := findBySourcePath(items, registryPath)
		if !ok || registry.State != StateShadowed || registry.ShadowedBy != effectivePath {
			t.Fatalf("Registry Skill at %q = %+v, want shadowed by %q", registryPath, registry, effectivePath)
		}
	}
}

func TestDiscoveryRootsMatchDefaultPolicy(t *testing.T) {
	roots := DiscoveryRoots(nil)
	want := []Root{
		{Path: IndexDirPath, Kind: SourceKindManaged, Managed: true},
		{Path: LegacyDirPath, Kind: SourceKindLegacy, Managed: false},
		{Path: "/data/.agents/skills", Kind: SourceKindCompat, Managed: false},
		{Path: "/root/.agents/skills", Kind: SourceKindCompat, Managed: false},
	}
	if !slices.Equal(roots, want) {
		t.Fatalf("DiscoveryRoots() = %+v, want %+v", roots, want)
	}
}

func TestDiscoveryRootsUseConfiguredCompatRoots(t *testing.T) {
	roots := DiscoveryRoots([]string{
		" /custom/skills ",
		"/data/skills",
		"/data/.memoh/skills",
		"/custom/skills",
		"relative/skills",
		"/root/.openclaw/skills",
	})
	want := []Root{
		{Path: IndexDirPath, Kind: SourceKindManaged, Managed: true},
		{Path: LegacyDirPath, Kind: SourceKindLegacy, Managed: false},
		{Path: "/custom/skills", Kind: SourceKindCompat, Managed: false},
		{Path: "/root/.openclaw/skills", Kind: SourceKindCompat, Managed: false},
	}
	if !slices.Equal(roots, want) {
		t.Fatalf("DiscoveryRoots(custom) = %+v, want %+v", roots, want)
	}
}

func TestDiscoveryRootsAllowExplicitEmptyCompatRoots(t *testing.T) {
	roots := DiscoveryRoots([]string{})
	want := []Root{
		{Path: IndexDirPath, Kind: SourceKindManaged, Managed: true},
		{Path: LegacyDirPath, Kind: SourceKindLegacy, Managed: false},
	}
	if !slices.Equal(roots, want) {
		t.Fatalf("DiscoveryRoots(empty) = %+v, want %+v", roots, want)
	}
}

func TestListScansConfiguredDiscoveryRootsInOrder(t *testing.T) {
	client := newFakeClient()
	rawCompatRoots := []string(nil)
	for _, root := range DiscoveryRoots(rawCompatRoots) {
		client.listings[root.Path] = nil
	}
	userPackage := pathJoin(ManagedDirPath, UserSkillNamespace, UserSkillPackage)
	client.listings[ManagedDirPath] = []*pb.FileEntry{{Path: UserSkillNamespace, IsDir: true}}
	client.listings[pathJoin(ManagedDirPath, UserSkillNamespace)] = []*pb.FileEntry{{Path: UserSkillPackage, IsDir: true}}
	client.listings[userPackage] = []*pb.FileEntry{{Path: "alpha", IsDir: true}}
	client.files[pathJoin(userPackage, "alpha", "SKILL.md")] = "---\nname: alpha\ndescription: Alpha\n---\n\n# Alpha"

	items, err := List(context.Background(), client, rawCompatRoots)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(items) != 1 || items[0].SourceRoot != userPackage {
		t.Fatalf("List() items = %+v, want managed alpha only", items)
	}

	wantCalls := []string{ManagedDirPath, pathJoin(ManagedDirPath, UserSkillNamespace), userPackage}
	for _, root := range DiscoveryRoots(rawCompatRoots) {
		wantCalls = append(wantCalls, root.Path)
	}
	if !slices.Equal(client.listCalls, wantCalls) {
		t.Fatalf("ListDirAll calls = %+v, want %+v", client.listCalls, wantCalls)
	}
}

func TestContainerEnvUsesDataHomeAndXDGDirs(t *testing.T) {
	env := ContainerEnv(nil)
	for _, want := range []string{
		"HOME=/data",
		"XDG_CONFIG_HOME=/data/.config",
		"XDG_DATA_HOME=/data/.local/share",
		"XDG_CACHE_HOME=/data/.cache",
		"MEMOH_SKILL_DISCOVERY_ROOTS=/data/.agents/skills:/root/.agents/skills",
	} {
		if !slices.Contains(env, want) {
			t.Fatalf("env %+v does not contain %q", env, want)
		}
	}
}

func TestContainerEnvUsesConfiguredSkillDiscoveryRoots(t *testing.T) {
	env := ContainerEnv([]string{"/custom/skills", "/root/.openclaw/skills"})
	want := SkillDiscoveryRootsEnvVar + "=/custom/skills:/root/.openclaw/skills"
	if !slices.Contains(env, want) {
		t.Fatalf("env %+v does not contain %q", env, want)
	}
}

type fakeClient struct {
	listings   map[string][]*pb.FileEntry
	files      map[string]string
	listErrors map[string]error
	readErrors map[string]error
	listCalls  []string
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		listings:   make(map[string][]*pb.FileEntry),
		files:      make(map[string]string),
		listErrors: make(map[string]error),
		readErrors: make(map[string]error),
	}
}

func (f *fakeClient) ListDirAll(_ context.Context, p string, _ bool) ([]*pb.FileEntry, error) {
	f.listCalls = append(f.listCalls, p)
	if err := f.listErrors[p]; err != nil {
		return nil, err
	}
	items, ok := f.listings[p]
	if !ok {
		return nil, io.EOF
	}
	return items, nil
}

func (f *fakeClient) ReadRaw(_ context.Context, p string) (io.ReadCloser, error) {
	if err := f.readErrors[p]; err != nil {
		return nil, err
	}
	content, ok := f.files[p]
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (f *fakeClient) WriteRaw(_ context.Context, p string, r io.Reader) (int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	f.files[p] = string(data)
	return int64(len(data)), nil
}

func (f *fakeClient) DeleteFile(_ context.Context, p string, recursive bool) error {
	if recursive {
		for filePath := range f.files {
			if filePath == p || strings.HasPrefix(filePath, p+"/") {
				delete(f.files, filePath)
			}
		}
		return nil
	}
	delete(f.files, p)
	return nil
}

func (*fakeClient) Mkdir(_ context.Context, _ string) error {
	return nil
}

func pathJoin(parts ...string) string {
	return strings.Join(parts, "/")
}
