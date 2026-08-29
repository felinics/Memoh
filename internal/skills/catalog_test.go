package skills

import (
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/slash"
)

func TestBuildSafeCatalogFiltersUnsafeEntries(t *testing.T) {
	entries := []Entry{
		testEntry("alpha", StateEffective, SourceKindManaged, "/skills/alpha", "alpha content"),
		testEntry("alpha", StateShadowed, SourceKindCompat, "/compat/alpha", "other"),
		testEntry("beta", StateEffective, SourceKindManaged, "/skills/beta", "beta content"),
		testEntry("disabled", StateDisabled, SourceKindManaged, "/skills/disabled", "disabled content"),
		testEntryWithMetadata("hidden", StateEffective, SourceKindManaged, "/skills/hidden", "hidden content", map[string]any{"hidden": true}),
		testEntryWithMetadata("malformed", StateEffective, SourceKindManaged, "/skills/malformed", "malformed content", map[string]any{"internal": "maybe"}),
	}
	catalog, err := BuildSafeCatalog(entries)
	if err != nil {
		t.Fatalf("BuildSafeCatalog: %v", err)
	}
	if len(catalog) != 2 {
		t.Fatalf("catalog len = %d, want 2: %#v", len(catalog), catalog)
	}
	if catalog[0].Name != "alpha" || catalog[1].Name != "beta" {
		t.Fatalf("catalog names = %#v, want alpha and beta", catalog)
	}
}

func TestBuildSafeCatalogFiltersMalformedParsedMetadata(t *testing.T) {
	raw := "---\nname: malformed\ndescription: Malformed\nmetadata: runtime-visible\n---\n\n# Malformed"
	malformed := entryFromParsed(ParseFile(raw, "malformed"), raw, Root{
		Path:    ManagedDirPath,
		Kind:    SourceKindManaged,
		Managed: true,
	}, pathJoin(ManagedDirPath, "malformed", "SKILL.md"))
	malformed.State = StateEffective
	valid := testEntry("valid", StateEffective, SourceKindManaged, pathJoin(ManagedDirPath, "valid", "SKILL.md"), "valid content")

	catalog, err := BuildSafeCatalog([]Entry{malformed, valid})
	if err != nil {
		t.Fatalf("BuildSafeCatalog: %v", err)
	}
	if len(catalog) != 1 {
		t.Fatalf("catalog len = %d, want 1: %#v", len(catalog), catalog)
	}
	if catalog[0].Name != "valid" {
		t.Fatalf("catalog[0].Name = %q, want valid", catalog[0].Name)
	}
}

func TestResolveTextRequestedSkills(t *testing.T) {
	alpha := testEntry("alpha", StateEffective, SourceKindManaged, "/skills/alpha", "alpha content")
	beta := testEntry("beta", StateEffective, SourceKindManaged, "/skills/beta", "beta content")
	resolved, err := ResolveTextRequestedSkills([]Entry{alpha, beta}, []string{"alpha", "alpha", "beta"}, ResolveLimits{MaxCount: 5, MaxSingleBytes: 1024, MaxTotalBytes: 2048})
	if err != nil {
		t.Fatalf("ResolveTextRequestedSkills: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved len = %d, want 2", len(resolved))
	}
	if resolved[0].Name != "alpha" || resolved[1].Name != "beta" {
		t.Fatalf("resolved order = %#v", resolved)
	}
	if resolved[0].Identity == "" || resolved[0].Identity == resolved[1].Identity {
		t.Fatalf("bad identities: %#v", resolved)
	}
}

func TestResolveTextRequestedSkillsRejectsAmbiguous(t *testing.T) {
	entries := []Entry{
		testEntry("alpha", StateEffective, SourceKindManaged, "/skills/alpha", "alpha content"),
		testEntry("alpha", StateEffective, SourceKindCompat, "/compat/alpha", "other"),
	}
	_, err := ResolveTextRequestedSkills(entries, []string{"alpha"}, ResolveLimits{})
	assertSlashCode(t, err, slash.CodeRequestedSkillAmbiguous)
}

func TestResolveTextRequestedSkillsUsesEffectiveOverShadowed(t *testing.T) {
	entries := []Entry{
		testEntry("alpha", StateEffective, SourceKindManaged, "/skills/alpha", "alpha content"),
		testEntry("alpha", StateShadowed, SourceKindCompat, "/compat/alpha", "other"),
	}
	resolved, err := ResolveTextRequestedSkills(entries, []string{"alpha"}, ResolveLimits{})
	if err != nil {
		t.Fatalf("ResolveTextRequestedSkills: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Content != "alpha content" {
		t.Fatalf("resolved = %#v, want effective alpha", resolved)
	}
}

func TestResolveTextRequestedSkillsRejectsBlankName(t *testing.T) {
	entry := testEntry("alpha", StateEffective, SourceKindManaged, "/skills/alpha", "alpha content")
	_, err := ResolveTextRequestedSkills([]Entry{entry}, []string{" "}, ResolveLimits{})
	assertSlashCode(t, err, slash.CodeInvalidSkillSlashSyntax)
}

func TestResolveTextRequestedSkillsLimits(t *testing.T) {
	entry := testEntry("alpha", StateEffective, SourceKindManaged, "/skills/alpha", "alpha content")
	_, err := ResolveTextRequestedSkills([]Entry{entry}, []string{"alpha"}, ResolveLimits{MaxSingleBytes: 1})
	assertSlashCode(t, err, slash.CodeRequestedSkillContextTooLarge)
}

func testEntry(name, state, sourceKind, sourcePath, content string) Entry {
	return testEntryWithMetadata(name, state, sourceKind, sourcePath, content, nil)
}

func testEntryWithMetadata(name, state, sourceKind, sourcePath, content string, metadata map[string]any) Entry {
	return Entry{
		Name:        name,
		Description: name + " description",
		Content:     content,
		Raw:         content,
		Metadata:    metadata,
		SourcePath:  sourcePath,
		SourceRoot:  "/root",
		SourceKind:  sourceKind,
		State:       state,
	}
}

func assertSlashCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %s", code)
	}
	var slashErr slash.Error
	if !errors.As(err, &slashErr) {
		t.Fatalf("err = %T %[1]v, want slash.Error %s", err, code)
	}
	if slashErr.Code != code {
		t.Fatalf("code = %s, want %s", slashErr.Code, code)
	}
}
