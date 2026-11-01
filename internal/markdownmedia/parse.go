// Package markdownmedia identifies explicit media references in message bodies.
// It owns Markdown semantics, but neither agent execution nor platform delivery.
package markdownmedia

import (
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type Reference struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Target string `json:"target"`
	Label  string `json:"label"`
	Image  bool   `json:"image"`
}

// Parse returns byte ranges in the original UTF-8 Markdown. Code and escaped
// syntax are excluded by the same parser that recognizes links and images.
func parseDocument(source string) (ast.Node, []text.Segment) {
	inlines := parser.DefaultInlineParsers()
	for i := range inlines {
		if inlines[i].Value == parser.NewLinkParser() {
			inlines[i].Value = &positionedLinks{InlineParser: parser.NewLinkParser()}
		}
	}
	definitions := &positionedDefinitions{}
	transformers := parser.DefaultParagraphTransformers()
	for i := range transformers {
		if transformers[i].Value == parser.LinkReferenceParagraphTransformer {
			transformers[i].Value = definitions
		}
	}
	p := parser.NewParser(parser.WithBlockParsers(parser.DefaultBlockParsers()...),
		parser.WithInlineParsers(inlines...), parser.WithParagraphTransformers(transformers...))
	data := []byte(source)
	doc := p.Parse(text.NewReader(data))
	return doc, definitions.removed
}

func Parse(source string) []Reference {
	doc, _ := parseDocument(source)
	data := []byte(source)
	var refs []Reference
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var destination []byte
		image := false
		switch node := n.(type) {
		case *ast.Image:
			destination, image = node.Destination, true
		case *ast.Link:
			destination = node.Destination
		default:
			return ast.WalkContinue, nil
		}
		// Resolve escapes and entities as a CommonMark renderer would, so the
		// archived path is the one the reader sees.
		target := string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(destination))))
		if !isMediaTarget(target, image) {
			return ast.WalkContinue, nil
		}
		start, ok := n.AttributeString("media-start")
		if !ok {
			return ast.WalkContinue, nil
		}
		end, ok := n.AttributeString("media-end")
		if !ok {
			return ast.WalkContinue, nil
		}
		if decoded, err := url.PathUnescape(target); err == nil && strings.HasPrefix(target, "/") {
			target = decoded
		}
		refs = append(refs, Reference{Start: start.(int), End: end.(int), Target: target, Label: nodeLabel(n, data), Image: image})
		return ast.WalkSkipChildren, nil
	})
	sort.Slice(refs, func(i, j int) bool { return refs[i].Start < refs[j].Start })
	return refs
}

var sourceLocation = regexp.MustCompile(`:\d+(?:[-:]\d+)*$`)

func isMediaTarget(target string, image bool) bool {
	if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") {
		if strings.ContainsAny(target, "#?") {
			return false
		}
		// Source locations (path:line, path:line:column, path:start-end) are
		// navigational links, not file-sharing requests.
		if sourceLocation.MatchString(target) {
			return false
		}
		// Only files inside the workspace are media. Application routes,
		// directories and paths that clean to outside /data stay links.
		if decoded, err := url.PathUnescape(target); err == nil {
			target = decoded
		}
		return strings.HasPrefix(path.Clean(target), WorkspaceRoot+"/") && !strings.HasSuffix(target, "/")
	}
	u, err := url.Parse(target)
	return image && err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// Wrap the upstream link parser to capture ranges while it still has its link
// opener nodes. Re-scanning rendered text would confuse duplicate labels,
// escaped brackets and reference-style links.
type positionedLinks struct{ parser.InlineParser }

func (p *positionedLinks) Parse(parent ast.Node, reader text.Reader, pc parser.Context) ast.Node {
	_, pos := reader.Position()
	start := pos.Start
	if reader.Peek() == ']' {
		for n := parent.LastChild(); n != nil; n = n.PreviousSibling() {
			if n.Kind().String() == "LinkLabelState" {
				if v, ok := n.AttributeString("media-start"); ok {
					start = v.(int)
				}
				break
			}
		}
	}
	n := p.InlineParser.Parse(parent, reader, pc)
	if n != nil {
		n.SetAttributeString("media-start", start)
		n.SetAttributeString("media-label-end", pos.Start)
		_, end := reader.Position()
		n.SetAttributeString("media-end", end.Start)
	}
	return n
}

func (p *positionedLinks) CloseBlock(parent ast.Node, reader text.Reader, pc parser.Context) {
	if closer, ok := p.InlineParser.(parser.CloseBlocker); ok {
		closer.CloseBlock(parent, reader, pc)
	}
}

// Capture definition lines removed by Goldmark, including multiline titles.
// Presentation removes these after expanding reference links to inline links.
type positionedDefinitions struct{ removed []text.Segment }

func (p *positionedDefinitions) Transform(node *ast.Paragraph, reader text.Reader, pc parser.Context) {
	before := append([]text.Segment(nil), node.Lines().Sliced(0, node.Lines().Len())...)
	parser.LinkReferenceParagraphTransformer.Transform(node, reader, pc)
	remaining := map[int]bool{}
	for i := 0; i < node.Lines().Len(); i++ {
		remaining[node.Lines().At(i).Start] = true
	}
	for _, line := range before {
		if !remaining[line.Start] {
			p.removed = append(p.removed, line)
		}
	}
}

func nodeLabel(node ast.Node, source []byte) string {
	var label strings.Builder
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch text := child.(type) {
		case *ast.Text:
			label.Write(text.Value(source))
			if text.SoftLineBreak() || text.HardLineBreak() {
				label.WriteByte(' ')
			}
		case *ast.String:
			label.Write(text.Value)
		}
		return ast.WalkContinue, nil
	})
	// The label is display text: escapes and entities are resolved.
	return string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations([]byte(label.String())))))
}
