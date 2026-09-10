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
	got := Render(source, bindings, func(_ Binding) string { return "<asset>" })
	if strings.Contains(got, "/data/") || strings.Contains(got, "[image]:") || strings.Contains(got, "[site]:") || !strings.Contains(got, "[网站](<https://example.com>)") || strings.Count(got, "<asset>") != 2 {
		t.Fatalf("presentation=%q", got)
	}
}
