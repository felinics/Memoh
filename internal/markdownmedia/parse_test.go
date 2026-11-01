package markdownmedia

import (
	"strings"
	"testing"
)

func TestReferencesPreserveSourceRanges(t *testing.T) {
	for _, syntax := range []string{
		"![截图](/data/a.png)", "[报告](</data/报告 (1).pdf>)", "![](/data/a.png)",
		"![**截图**](/data/a.png)", "![图][asset]\n\n[asset]: /data/a.png",
	} {
		t.Run(syntax, func(t *testing.T) {
			source := "前文\n\n" + syntax + "\n\n后文"
			refs := Parse(source)
			if len(refs) != 1 {
				t.Fatalf("got %+v", refs)
			}
			r := refs[0]
			end := len(syntax)
			if syntax == "![图][asset]\n\n[asset]: /data/a.png" {
				end = len("![图][asset]")
			}
			if got := source[r.Start:r.End]; got != syntax[:end] {
				t.Fatalf("range %d:%d = %q, want %q", r.Start, r.End, got, syntax[:end])
			}
		})
	}
}

func TestNonMediaSyntaxDoesNotPublish(t *testing.T) {
	source := "`![图](/data/a.png)`\n\n```md\n![图](/data/a.png)\n```\n\n\\![图](https://example.com/a.png)\n[代码](/data/main.go:42) [代码](/data/main.go#L42) [网页](https://example.com)\n![未闭合](/data/a.png"
	if refs := Parse(source); len(refs) != 0 {
		t.Fatalf("unexpected references: %+v", refs)
	}
}

func TestPresentationExpandsSharedDefinitionsBeforeSplitting(t *testing.T) {
	source := "![图][image]\n\n[网站][site]\n\n[文件][file]\n\n[image]: https://example.com/a.png\n[site]: https://example.com\n[file]: /data/report.txt\n  \"报告\"\n"
	refs := Parse(source)
	var bindings []Binding
	for _, ref := range refs {
		bindings = append(bindings, Binding{Reference: ref})
	}
	got := Render(source, bindings, func(Binding, string) string { return "<asset>" })
	if strings.Contains(got, "/data/") || strings.Contains(got, "[image]:") || strings.Contains(got, "[site]:") || !strings.Contains(got, "[网站](<https://example.com>)") || strings.Count(got, "<asset>") != 2 {
		t.Fatalf("presentation=%q", got)
	}
}

func TestLinkedImagePreservesArchivedImageAndDestination(t *testing.T) {
	source := "before [![preview](/data/a.png)](https://example.com) after"
	refs := Parse(source)
	if len(refs) != 1 {
		t.Fatalf("refs=%+v", refs)
	}
	bindings := []Binding{{Reference: refs[0]}}
	rendered := Render(source, bindings, func(Binding, string) string { return "![preview](/archived/image)" })
	if rendered != "before [![preview](/archived/image)](<https://example.com>) after" {
		t.Fatalf("rendered=%q", rendered)
	}
	var prose strings.Builder
	mediaCount := 0
	Visit(source, bindings, func(text string) { prose.WriteString(text) }, func(Binding) { mediaCount++ })
	// IM publishes the image after its paragraph; the link keeps its label.
	if mediaCount != 1 || prose.String() != "before [preview](<https://example.com>) after" {
		t.Fatalf("media=%d prose=%q", mediaCount, prose.String())
	}
}

func TestReferenceLinkContainingImageRetainsLabelAndDestination(t *testing.T) {
	source := "before [**open** ![preview](/data/a.png) here][site] after\n\n[site]: https://example.com\n"
	refs := Parse(source)
	if len(refs) != 1 {
		t.Fatalf("refs=%+v", refs)
	}
	bindings := []Binding{{Reference: refs[0]}}
	rendered := Render(source, bindings, func(Binding, string) string { return "![preview](/archived/image)" })
	if !strings.Contains(rendered, "[**open** ![preview](/archived/image) here](<https://example.com>)") || strings.Contains(rendered, "[site]") || strings.Contains(rendered, "/data/") {
		t.Fatalf("rendered=%q", rendered)
	}
	var prose strings.Builder
	images := 0
	Visit(source, bindings, func(text string) { prose.WriteString(text) }, func(Binding) { images++ })
	if images != 1 || !strings.Contains(prose.String(), "before [open preview here](<https://example.com>) after") || strings.Contains(prose.String(), "[site]") {
		t.Fatalf("images=%d prose=%q", images, prose.String())
	}
}

func TestVisitPublishesMediaAfterItsBlock(t *testing.T) {
	source := "Intro **see ![a](/data/a.png) here** now.\n\n- [report](/data/r.txt)\n- [missing](/data/m.txt)\n\n```md\n![code](/data/c.png)\n```\n\nOutro"
	refs := Parse(source)
	if len(refs) != 3 {
		t.Fatalf("refs=%+v", refs)
	}
	bindings := []Binding{{Reference: refs[0]}, {Reference: refs[1]}, {Reference: refs[2], ErrorCode: "media.reference_unavailable"}}
	var events []string
	Visit(source, bindings, func(text string) { events = append(events, "prose:"+text) }, func(b Binding) { events = append(events, "media:"+b.Target) })
	want := []string{
		"prose:Intro **see here** now.\n\n",
		"media:/data/a.png",
		"prose:- report\n- missing\n\n",
		"media:/data/r.txt",
		"media:/data/m.txt",
		"prose:```md\n![code](/data/c.png)\n```\n\nOutro",
	}
	if strings.Join(events, "|") != strings.Join(want, "|") {
		t.Fatalf("events=%q\nwant  =%q", events, want)
	}
}

func TestTargetsAndLabelsResolveMarkdownEscapes(t *testing.T) {
	refs := Parse("![a \\[b\\]](/data/a\\_b.png) [report\\_v2](/data/r&amp;d.pdf)")
	if len(refs) != 2 || refs[0].Target != "/data/a_b.png" || refs[0].Label != "a [b]" || refs[1].Target != "/data/r&d.pdf" || refs[1].Label != "report_v2" {
		t.Fatalf("refs=%+v", refs)
	}
}

func TestOnlyTrailingLineNumbersAreSourceLocations(t *testing.T) {
	for target, media := range map[string]bool{
		"/data/shot 10:30:00.png": true, "/data/report:2024.png": true,
		"/data/main.go:42": false, "/data/main.go:42:7": false, "/data/main.go:10-20": false,
	} {
		if got := len(Parse("[x](<"+target+">)")) == 1; got != media {
			t.Errorf("%q media=%v want %v", target, got, media)
		}
	}
}

func TestRebuiltMarkdownKeepsLabelsAsWritten(t *testing.T) {
	source := "see [**report**\\_v2](/data/r.pdf) and ![a \\[b\\]](/data/a.png)"
	refs := Parse(source)
	bindings := []Binding{{Reference: refs[0]}, {Reference: refs[1]}}
	var prose strings.Builder
	Visit(source, bindings, func(text string) { prose.WriteString(text) }, func(Binding) {})
	if prose.String() != "see **report**\\_v2 and " {
		t.Fatalf("prose=%q", prose.String())
	}
	got := Render(source, bindings[1:], func(_ Binding, label string) string { return "![" + label + "](/archived)" })
	if got != "see [**report**\\_v2](/data/r.pdf) and ![a \\[b\\]](/archived)" {
		t.Fatalf("rendered=%q", got)
	}
}
