package markdownmedia

import (
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
)

type presentationEdit struct {
	start, end int
	text       string
	binding    *Binding
}

// Visit emits ordered prose and archived references. Reference definitions are
// expanded before splitting, since individual IM messages cannot share them.
func Visit(source string, bindings []Binding, prose func(string), media func(Binding)) {
	if len(bindings) == 0 {
		prose(source)
		return
	}
	doc, definitions := parseDocument(source)
	edits := make([]presentationEdit, 0, len(bindings)+len(definitions))
	bound := map[int]bool{}
	for i := range bindings {
		b := &bindings[i]
		edits = append(edits, presentationEdit{start: b.Start, end: b.End, binding: b})
		bound[b.Start] = true
	}
	for _, line := range definitions {
		edits = append(edits, presentationEdit{start: line.Start, end: line.Stop})
	}
	data := []byte(source)
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		link, ok := n.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}
		start, hasStart := n.AttributeString("media-start")
		end, hasEnd := n.AttributeString("media-end")
		if !hasStart || !hasEnd || bound[start.(int)] {
			return ast.WalkSkipChildren, nil
		}
		label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(nodeLabel(n, data))
		target := strings.NewReplacer("<", "%3C", ">", "%3E").Replace(string(link.Destination))
		edits = append(edits, presentationEdit{start: start.(int), end: end.(int), text: "[" + label + "](<" + target + ">)"})
		return ast.WalkSkipChildren, nil
	})
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var text strings.Builder
	pos := 0
	for _, edit := range edits {
		if edit.start < pos || edit.end < edit.start || edit.end > len(source) {
			continue
		}
		text.WriteString(source[pos:edit.start])
		if edit.binding != nil {
			prose(text.String())
			text.Reset()
			media(*edit.binding)
		} else {
			text.WriteString(edit.text)
		}
		pos = edit.end
	}
	text.WriteString(source[pos:])
	prose(text.String())
}
