package markdownmedia

import (
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type presentationEdit struct {
	start, end int
	text       string
}

// Visit emits ordered prose and archived references for IM delivery. Each
// message must be valid Markdown on its own, so references are published
// after the top-level block that contains them rather than by cutting through
// emphasis, lists or tables. Reference definitions are expanded first, since
// individual IM messages cannot share them.
func Visit(source string, bindings []Binding, prose func(string), media func(Binding)) {
	if len(bindings) == 0 {
		prose(source)
		return
	}
	doc, definitions := parseDocument(source)
	data := []byte(source)
	edits := linkEdits(doc, data, bindings, definitions, false)
	labels := labelSources(doc, source)
	for i := range bindings {
		b := &bindings[i]
		end, replacement := b.End, ""
		if !b.Image {
			// The file follows its block; the sentence keeps its label.
			replacement = markdownLabel(labels[b.Start], b.Label)
		} else if b.Start > 0 && end < len(source) && source[b.Start-1] == ' ' && source[end] == ' ' {
			end++
		}
		edits = append(edits, presentationEdit{start: b.Start, end: end, text: replacement})
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })

	starts := blockStarts(doc, source)
	var pending strings.Builder
	next := 0
	for block := range starts {
		end := len(source)
		if block+1 < len(starts) {
			end = starts[block+1]
		}
		pending.WriteString(applyEdits(source, starts[block], end, edits))
		published := false
		for ; next < len(bindings) && bindings[next].Start < end; next++ {
			if !published {
				prose(pending.String())
				pending.Reset()
				published = true
			}
			media(bindings[next])
		}
	}
	prose(pending.String())
}

// Render replaces only validated source ranges, preserving all other Markdown.
// replace receives the reference's label as Markdown source.
func Render(source string, bindings []Binding, replace func(b Binding, label string) string) string {
	if len(bindings) == 0 {
		return source
	}
	doc, definitions := parseDocument(source)
	edits := linkEdits(doc, []byte(source), bindings, definitions, true)
	labels := labelSources(doc, source)
	for i := range bindings {
		b := bindings[i]
		edits = append(edits, presentationEdit{start: b.Start, end: b.End, text: replace(b, markdownLabel(labels[b.Start], b.Label))})
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	return applyEdits(source, 0, len(source), edits)
}

// linkEdits removes reference definitions and rewrites the links that remain
// meaningful once the source is split or projected: reference links become
// inline links, and links wrapping an archived image keep their destination.
func linkEdits(doc ast.Node, data []byte, bindings []Binding, definitions []text.Segment, preserveMediaLinks bool) []presentationEdit {
	edits := make([]presentationEdit, 0, len(definitions))
	bound := map[int]bool{}
	for _, b := range bindings {
		bound[b.Start] = true
	}
	for _, line := range definitions {
		edits = append(edits, presentationEdit{start: line.Start, end: line.Stop})
	}
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
		target := strings.NewReplacer("<", "%3C", ">", "%3E").Replace(string(link.Destination))
		containsMedia := false
		for _, binding := range bindings {
			if binding.Start > start.(int) && binding.End < end.(int) {
				containsMedia = true
				break
			}
		}
		// Inline links are self-contained; only reference links lose their
		// definitions and need expanding.
		if !containsMedia && strings.HasSuffix(string(data[start.(int):end.(int)]), ")") {
			return ast.WalkSkipChildren, nil
		}
		if containsMedia && preserveMediaLinks {
			// A Web image can retain its enclosing link.
			labelEnd, _ := n.AttributeString("media-label-end")
			edits = append(edits, presentationEdit{start: labelEnd.(int), end: end.(int), text: "](<" + target + ">)"})
			return ast.WalkSkipChildren, nil
		}
		// An IM message cannot nest the image it publishes separately, so the
		// link keeps its text label, including the image's alternative text.
		label := escapeText(nodeLabel(n, data))
		if !containsMedia {
			labelEnd, _ := n.AttributeString("media-label-end")
			label = string(data[start.(int)+1 : labelEnd.(int)])
		}
		edits = append(edits, presentationEdit{start: start.(int), end: end.(int), text: "[" + label + "](<" + target + ">)"})
		return ast.WalkSkipChildren, nil
	})
	return edits
}

func applyEdits(source string, from, to int, edits []presentationEdit) string {
	var out strings.Builder
	pos := from
	for _, edit := range edits {
		if edit.start < pos || edit.end < edit.start || edit.end > to {
			continue
		}
		out.WriteString(source[pos:edit.start])
		out.WriteString(edit.text)
		pos = edit.end
	}
	out.WriteString(source[pos:to])
	return out.String()
}

// blockStarts returns the byte offset of each top-level block's first line.
// Blocks without a recoverable position, such as thematic breaks, join the
// block before them.
func blockStarts(doc ast.Node, source string) []int {
	starts := []int{0}
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		pos := firstPosition(child, source)
		if pos <= starts[len(starts)-1] {
			continue
		}
		starts = append(starts, pos)
	}
	return starts
}

func firstPosition(node ast.Node, source string) int {
	if fence, ok := node.(*ast.FencedCodeBlock); ok {
		// The opening fence line precedes the first content line.
		if fence.Info != nil {
			return lineStart(source, fence.Info.Segment.Start)
		}
		if fence.Lines().Len() > 0 {
			return lineStart(source, lineStart(source, fence.Lines().At(0).Start)-1)
		}
		return -1
	}
	first := -1
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		pos := -1
		if n.Type() == ast.TypeBlock && n.Lines().Len() > 0 {
			pos = n.Lines().At(0).Start
		} else if t, ok := n.(*ast.Text); ok {
			pos = t.Segment.Start
		}
		if pos >= 0 && (first < 0 || pos < first) {
			first = pos
		}
		return ast.WalkContinue, nil
	})
	if first < 0 {
		return -1
	}
	return lineStart(source, first)
}

func lineStart(source string, pos int) int {
	if pos <= 0 {
		return 0
	}
	return strings.LastIndexByte(source[:pos], '\n') + 1
}

// labelSources maps each link or image to its label as written, so rebuilt
// Markdown keeps the label's formatting and escapes.
func labelSources(doc ast.Node, source string) map[int]string {
	labels := map[int]string{}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		offset := 1
		switch n.(type) {
		case *ast.Image:
			offset = 2
		case *ast.Link:
		default:
			return ast.WalkContinue, nil
		}
		start, hasStart := n.AttributeString("media-start")
		end, hasEnd := n.AttributeString("media-label-end")
		if hasStart && hasEnd && start.(int)+offset <= end.(int) && end.(int) <= len(source) {
			labels[start.(int)] = source[start.(int)+offset : end.(int)]
		}
		return ast.WalkContinue, nil
	})
	return labels
}

// markdownLabel prefers the label as written. A label that itself holds a
// media reference falls back to its escaped display text, so a separately
// published file never leaks its path into prose.
func markdownLabel(raw, display string) string {
	if raw != "" && len(Parse(raw)) == 0 {
		return raw
	}
	return escapeText(display)
}

func escapeText(text string) string {
	return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`").Replace(text)
}
