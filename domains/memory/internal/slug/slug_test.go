package slug

import (
	"reflect"
	"testing"
)

func TestNodeSlugFallbacks(t *testing.T) {
	t.Parallel()

	if got := NodeSlug("bot-1:mem_1", "Alice Profile", "notes"); got != "alice-profile" {
		t.Fatalf("subject slug = %q", got)
	}
	if got := NodeSlug("bot-1:mem_1", "", "Project Notes"); got != "project-notes" {
		t.Fatalf("topic slug = %q", got)
	}
	if got := NodeSlug("bot-1:mem_1", "", ""); got != "mem-1" {
		t.Fatalf("id slug = %q", got)
	}
}

func TestParseMemoryLinksNormalizesWikiAndRelativeMarkdown(t *testing.T) {
	t.Parallel()

	got := ParseMemoryLinks("[web](https://example.com) [[Tech Stack]] [Alice](../identity/alice-profile.md)")
	want := []string{"tech-stack", "alice-profile"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseMemoryLinks() = %#v, want %#v", got, want)
	}
}

func TestRenderAndRestoreMarkdownFileLinks(t *testing.T) {
	t.Parallel()

	rendered := RenderWikiLinks("See [[Alice Profile]] and [[missing]].", func(_ string, normalized string) (string, bool) {
		if normalized == "alice-profile" {
			return "../identity/alice-profile.md", true
		}
		return "", false
	})
	if rendered != "See [Alice Profile](../identity/alice-profile.md) and [[missing]]." {
		t.Fatalf("rendered = %q", rendered)
	}
	restored := RestoreMarkdownFileLinks(rendered)
	if restored != "See [[alice-profile]] and [[missing]]." {
		t.Fatalf("restored = %q", restored)
	}
}
