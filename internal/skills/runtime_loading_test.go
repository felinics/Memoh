package skills

import (
	"errors"
	"io"
	"testing"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func TestEffectiveSkillsPreservesPartialDiscoveryAndDisabledOverrides(t *testing.T) {
	client := newFakeClient()
	client.files[IndexFilePath] = `{"version":1,"overrides":{"/data/.agents/skills/disabled/SKILL.md":{"disabled":true}}}`
	client.listErrors[IndexDirPath] = bridge.ErrNotFound
	client.listErrors[LegacyDirPath] = errors.New("workspace directory unavailable")
	client.listings["/data/.agents/skills"] = []*pb.FileEntry{{Path: "good", IsDir: true}, {Path: "disabled", IsDir: true}, {Path: "bad", IsDir: true}}
	client.files["/data/.agents/skills/good/SKILL.md"] = "# Real content"
	client.files["/data/.agents/skills/disabled/SKILL.md"] = "# Disabled content"
	client.readErrors["/data/.agents/skills/bad/SKILL.md"] = io.ErrUnexpectedEOF
	original := client.files[IndexFilePath]
	entries, err := LoadEffective(t.Context(), client, []string{"/data/.agents/skills"})
	if err == nil || len(entries) != 1 || entries[0].Name != "good" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	if client.files[IndexFilePath] != original {
		t.Fatal("runtime discovery overwrote override index")
	}
}

func TestEffectiveSkillsDoesNotReactivateSkillsWithCorruptIndex(t *testing.T) {
	for _, raw := range []string{`{"overrides":`, `null`, `{"version":2}`} {
		client := newFakeClient()
		client.files[IndexFilePath] = raw
		entries, err := LoadEffective(t.Context(), client, nil)
		if err == nil || len(entries) != 0 || len(client.listCalls) != 0 || client.files[IndexFilePath] != raw {
			t.Fatalf("raw=%q entries=%+v err=%v", raw, entries, err)
		}
	}
}

func TestCatalogListingPreservesCorruptOverrideIndex(t *testing.T) {
	client := newFakeClient()
	raw := `{"overrides":`
	client.files[IndexFilePath] = raw
	entries, err := List(t.Context(), client, nil)
	if err == nil || len(entries) != 0 || client.files[IndexFilePath] != raw {
		t.Fatalf("entries=%+v err=%v index=%q", entries, err, client.files[IndexFilePath])
	}
}
