package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The browser snapshot combines three sources into one tree:
//
//   - Accessibility.getFullAXTree for roles, computed names, values, states,
//     and hierarchy (the semantic source);
//   - DOM.getDocument + DOM.querySelectorAll to map accessibility nodes onto
//     the interactive DOM elements the page script can pin (via backend DOM
//     node ids);
//   - the page script for geometry and the actual element handles refs
//     resolve to.
//
// When the accessibility domain is unavailable the listing degrades to the
// flat DOM scan and says so in `source`.

type axValue struct {
	Value any `json:"value"`
}

type axProperty struct {
	Name  string  `json:"name"`
	Value axValue `json:"value"`
}

type axNode struct {
	NodeID           string       `json:"nodeId"`
	Ignored          bool         `json:"ignored"`
	Role             axValue      `json:"role"`
	Name             axValue      `json:"name"`
	Value            axValue      `json:"value"`
	Properties       []axProperty `json:"properties"`
	ChildIDs         []string     `json:"childIds"`
	ParentID         string       `json:"parentId"`
	BackendDOMNodeID int          `json:"backendDOMNodeId"`
}

func (n axNode) role() string { return axString(n.Role.Value) }
func (n axNode) name() string { return axString(n.Name.Value) }

func axString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		raw, _ := json.Marshal(t)
		return string(raw)
	}
}

// axStates renders the properties the model acts on.
func (n axNode) states() []string {
	var out []string
	for _, p := range n.Properties {
		v := p.Value.Value
		switch p.Name {
		case "focused", "disabled", "selected", "pressed", "required", "readonly", "busy", "multiselectable", "modal":
			if b, ok := v.(bool); ok && b {
				out = append(out, p.Name)
			}
		case "checked":
			switch s := axString(v); s {
			case "true":
				out = append(out, "checked")
			case "mixed":
				out = append(out, "checked=mixed")
			}
		case "expanded":
			if b, ok := v.(bool); ok {
				if b {
					out = append(out, "expanded")
				} else {
					out = append(out, "collapsed")
				}
			}
		case "invalid":
			if s := axString(v); s != "" && s != "false" {
				out = append(out, "invalid")
			}
		case "level":
			if s := axString(v); s != "" && n.role() == "heading" {
				out = append(out, "h"+s)
			}
		case "hasPopup":
			if s := axString(v); s != "" && s != "false" {
				out = append(out, "haspopup="+s)
			}
		}
	}
	return out
}

func (n axNode) hidden() bool {
	for _, p := range n.Properties {
		if p.Name == "hidden" {
			if b, ok := p.Value.Value.(bool); ok && b {
				return true
			}
		}
	}
	return false
}

// axStructuralRoles are non-interactive roles worth showing as tree context
// when they carry a name or contain interactive descendants.
var axStructuralRoles = map[string]bool{
	"heading": true, "dialog": true, "alertdialog": true, "navigation": true, "main": true, "banner": true,
	"contentinfo": true, "complementary": true, "search": true, "form": true, "region": true, "article": true,
	"list": true, "listitem": true, "table": true, "grid": true, "treegrid": true, "tablist": true, "menu": true,
	"menubar": true, "tree": true, "toolbar": true, "group": true, "radiogroup": true, "listbox": true,
	"status": true, "alert": true, "log": true, "progressbar": true, "meter": true, "figure": true, "img": true,
	"image": true, "tabpanel": true, "rowgroup": true, "row": true, "columnheader": true, "rowheader": true,
}

// browserInteractive is one element the page script can pin: its position in
// the unfiltered querySelectorAll list (which the CDP side uses to find its
// backend node id), plus what the DOM says about it.
type browserInteractive struct {
	AllIndex int     `json:"allIndex"`
	Tag      string  `json:"tag"`
	Type     string  `json:"type"`
	Role     string  `json:"role"`
	Name     string  `json:"name"`
	Selector string  `json:"selector"`
	Left     float64 `json:"left"`
	Top      float64 `json:"top"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Password bool    `json:"password"`

	backendID int
	ref       string
}

func (it browserInteractive) bounds() string {
	return fmt.Sprintf("@%d,%d %dx%d", int(it.Left), int(it.Top), int(it.Width), int(it.Height))
}

// browserSnapshotResult is what one observation of a tab produced.
type browserSnapshotResult struct {
	Nodes       []guiNode
	Source      string
	AXNodes     int
	Interactive int
	RefsByKey   map[string]string
	MaxRef      int
	Reused      int
	Truncated   bool
}

// takeAXSnapshot observes the page. baseline (may be nil) supplies the refs to
// reuse; scopeBackendID (0 for the whole document) restricts the emitted tree
// to one subtree while refs are still assigned page-wide.
func (p *cdpPage) takeAXSnapshot(ctx context.Context, snapshotID string, baseline *guiSnapshotBaseline, scopeBackendID int) (*browserSnapshotResult, error) {
	var interactive []browserInteractive
	if err := p.evaluateObject(ctx, `memohListInteractive()`, &interactive); err != nil {
		return nil, err
	}
	result := &browserSnapshotResult{Source: "cdp_accessibility", Interactive: len(interactive), RefsByKey: map[string]string{}}

	// DOM node ids: the CDP querySelectorAll list and the page script's
	// querySelectorAll list use the same selector on the same document, so
	// they share document order; AllIndex joins them.
	backendByAll, axErr := p.backendIDsForInteractive(ctx)
	if axErr != nil {
		result.Source = "dom"
	}
	var tree []axNode
	if axErr == nil {
		tree, axErr = p.fullAXTree(ctx)
		if axErr != nil {
			result.Source = "dom"
		}
	}
	for i := range interactive {
		if backendByAll != nil && interactive[i].AllIndex < len(backendByAll) {
			interactive[i].backendID = backendByAll[interactive[i].AllIndex]
		}
	}

	// Refs: reuse the baseline's id for the same backend node, fresh ids
	// above its maximum for the rest, dense from e1 without a baseline.
	nextRef := 1
	if baseline != nil {
		nextRef = baseline.MaxRef + 1
	}
	byBackend := map[int]*browserInteractive{}
	for i := range interactive {
		it := &interactive[i]
		key := interactiveKey(it)
		if baseline != nil {
			if ref, ok := baseline.RefsByKey[key]; ok {
				it.ref = ref
				result.Reused++
			}
		}
		if it.ref == "" {
			it.ref = "e" + strconv.Itoa(nextRef)
			nextRef++
		}
		result.RefsByKey[key] = it.ref
		if n, err := strconv.Atoi(strings.TrimPrefix(it.ref, "e")); err == nil && n > result.MaxRef {
			result.MaxRef = n
		}
		if it.backendID > 0 {
			byBackend[it.backendID] = it
		}
	}
	assignments := make([]map[string]any, 0, len(interactive))
	for _, it := range interactive {
		assignments = append(assignments, map[string]any{"allIndex": it.AllIndex, "ref": it.ref})
	}
	raw, _ := json.Marshal(assignments)
	if _, err := p.evaluate(ctx, fmt.Sprintf(`memohPinSnapshot(%s, %s)`, jsQuote(snapshotID), string(raw))); err != nil {
		return nil, err
	}
	p.snapshotID = snapshotID

	if result.Source == "dom" {
		for _, it := range interactive {
			result.Nodes = append(result.Nodes, domNode(it))
		}
		return result, nil
	}
	result.AXNodes = len(tree)
	result.Nodes = buildAXLines(tree, byBackend, scopeBackendID)
	if len(result.Nodes) >= guiSnapshotWalkLimit {
		result.Truncated = true
		result.Nodes = result.Nodes[:guiSnapshotWalkLimit]
	}
	return result, nil
}

func interactiveKey(it *browserInteractive) string {
	if it.backendID > 0 {
		return "dom:" + strconv.Itoa(it.backendID)
	}
	return "sel:" + it.Selector
}

// domNode renders an interactive element from DOM information only (the
// degraded path).
func domNode(it browserInteractive) guiNode {
	line := "- " + it.Role
	if name := strings.TrimSpace(it.Name); name != "" {
		line += " " + jsQuote(name)
	}
	line += " [ref=" + it.ref + "] " + it.bounds()
	return guiNode{Key: interactiveKey(&it), Ref: it.ref, Line: line, Fingerprint: strings.Replace(line, "[ref="+it.ref+"]", "", 1)}
}

// backendIDsForInteractive returns, per position in the page script's
// unfiltered interactive querySelectorAll list, the backend DOM node id.
func (p *cdpPage) backendIDsForInteractive(ctx context.Context) ([]int, error) {
	rawDoc, err := p.conn.Call(ctx, "DOM.getDocument", map[string]any{"depth": -1, "pierce": false})
	if err != nil {
		return nil, err
	}
	var doc struct {
		Root domTreeNode `json:"root"`
	}
	if err := json.Unmarshal(rawDoc, &doc); err != nil {
		return nil, err
	}
	backendByNode := map[int]int{}
	doc.Root.collect(backendByNode)
	rawList, err := p.conn.Call(ctx, "DOM.querySelectorAll", map[string]any{"nodeId": doc.Root.NodeID, "selector": memohInteractiveSelectorCSS})
	if err != nil {
		return nil, err
	}
	var list struct {
		NodeIDs []int `json:"nodeIds"`
	}
	if err := json.Unmarshal(rawList, &list); err != nil {
		return nil, err
	}
	out := make([]int, len(list.NodeIDs))
	for i, id := range list.NodeIDs {
		out[i] = backendByNode[id]
	}
	return out, nil
}

type domTreeNode struct {
	NodeID        int           `json:"nodeId"`
	BackendNodeID int           `json:"backendNodeId"`
	Children      []domTreeNode `json:"children"`
	ShadowRoots   []domTreeNode `json:"shadowRoots"`
}

func (n domTreeNode) collect(into map[int]int) {
	into[n.NodeID] = n.BackendNodeID
	for _, c := range n.Children {
		c.collect(into)
	}
	for _, c := range n.ShadowRoots {
		c.collect(into)
	}
}

func (p *cdpPage) fullAXTree(ctx context.Context) ([]axNode, error) {
	if _, err := p.conn.Call(ctx, "Accessibility.enable", nil); err != nil {
		return nil, err
	}
	raw, err := p.conn.Call(ctx, "Accessibility.getFullAXTree", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Nodes) == 0 {
		return nil, errors.New("accessibility tree is empty")
	}
	return out.Nodes, nil
}

// buildAXLines walks the accessibility tree and emits the nodes worth
// showing: every interactive element (with its ref and geometry) and the
// named structural containers around them, indented by their depth among
// emitted ancestors.
func buildAXLines(tree []axNode, byBackend map[int]*browserInteractive, scopeBackendID int) []guiNode {
	byID := make(map[string]*axNode, len(tree))
	for i := range tree {
		byID[tree[i].NodeID] = &tree[i]
	}
	var root *axNode
	for i := range tree {
		if tree[i].ParentID == "" {
			root = &tree[i]
			break
		}
	}
	if root == nil {
		root = &tree[0]
	}
	if scopeBackendID > 0 {
		for i := range tree {
			if tree[i].BackendDOMNodeID == scopeBackendID {
				root = &tree[i]
				break
			}
		}
	}

	// keep[nodeId] is true when the node itself is shown; kept descendants
	// are counted bottom-up so unnamed containers only appear around
	// something actionable.
	keptDesc := map[string]int{}
	var count func(n *axNode) int
	count = func(n *axNode) int {
		total := 0
		for _, id := range n.ChildIDs {
			if c, ok := byID[id]; ok {
				total += count(c)
			}
		}
		keptDesc[n.NodeID] = total
		if nodeShown(n, byBackend, total) {
			total++
		}
		return total
	}
	count(root)

	var out []guiNode
	var walk func(n *axNode, depth int)
	walk = func(n *axNode, depth int) {
		shown := nodeShown(n, byBackend, keptDesc[n.NodeID])
		if shown {
			out = append(out, axLine(n, byBackend, depth))
			depth++
		}
		if len(out) >= guiSnapshotWalkLimit {
			return
		}
		for _, id := range n.ChildIDs {
			if c, ok := byID[id]; ok {
				walk(c, depth)
			}
		}
	}
	walk(root, 0)
	return out
}

func nodeShown(n *axNode, byBackend map[int]*browserInteractive, keptDescendants int) bool {
	if n.Ignored || n.hidden() {
		return false
	}
	if _, ok := byBackend[n.BackendDOMNodeID]; ok && n.BackendDOMNodeID > 0 {
		return true
	}
	role := n.role()
	if !axStructuralRoles[role] {
		return false
	}
	if role == "heading" || role == "dialog" || role == "alertdialog" || role == "alert" || role == "status" || role == "img" || role == "image" || role == "progressbar" || role == "meter" {
		return strings.TrimSpace(n.name()) != "" || keptDescendants > 0
	}
	return keptDescendants > 0 || strings.TrimSpace(n.name()) != ""
}

func axLine(n *axNode, byBackend map[int]*browserInteractive, depth int) guiNode {
	role := n.role()
	name := strings.TrimSpace(n.name())
	line := strings.Repeat("  ", depth) + "- " + role
	if name != "" {
		if len([]rune(name)) > 80 {
			name = string([]rune(name)[:80]) + "…"
		}
		line += " " + jsQuote(name)
	}
	key := "ax:" + n.NodeID
	ref := ""
	if it, ok := byBackend[n.BackendDOMNodeID]; ok && n.BackendDOMNodeID > 0 {
		ref = it.ref
		key = interactiveKey(it)
		line += " [ref=" + ref + "] " + it.bounds()
		if it.Password {
			line += ` value="•••"`
		} else if v := axString(n.Value.Value); v != "" {
			if len([]rune(v)) > 80 {
				v = string([]rune(v)[:80]) + "…"
			}
			line += " value=" + jsQuote(v)
		}
	} else if v := axString(n.Value.Value); v != "" && role != "StaticText" {
		if len([]rune(v)) > 80 {
			v = string([]rune(v)[:80]) + "…"
		}
		line += " value=" + jsQuote(v)
	}
	if states := n.states(); len(states) > 0 {
		line += " (" + strings.Join(states, ", ") + ")"
	}
	fingerprint := line
	if ref != "" {
		fingerprint = strings.Replace(line, "[ref="+ref+"] ", "", 1)
	}
	return guiNode{Key: key, Ref: ref, Line: line, Fingerprint: fingerprint}
}

// scopeBackendID resolves a scope ref (from the tab's latest snapshot) to
// the backend DOM node id its element has.
func (p *cdpPage) scopeBackendID(ctx context.Context, ref string) (int, error) {
	wrapped := "(async () => {\n" + mustElementHelper + "\nreturn elementByRef(" + jsQuote(ref) + ", " + jsQuote(p.snapshotID) + ");\n})()"
	raw, err := p.conn.Call(ctx, "Runtime.evaluate", map[string]any{"expression": wrapped, "awaitPromise": true, "returnByValue": false})
	if err != nil {
		return 0, err
	}
	var out struct {
		Result struct {
			ObjectID string `json:"objectId"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, err
	}
	if out.ExceptionDetails != nil {
		return 0, fmt.Errorf("%s", pageExceptionMessage(out.ExceptionDetails.Exception.Description, out.ExceptionDetails.Text))
	}
	if out.Result.ObjectID == "" {
		return 0, fmt.Errorf("scope_ref %s did not resolve to an element", ref)
	}
	rawNode, err := p.conn.Call(ctx, "DOM.describeNode", map[string]any{"objectId": out.Result.ObjectID})
	if err != nil {
		return 0, err
	}
	var desc struct {
		Node struct {
			BackendNodeID int `json:"backendNodeId"`
		} `json:"node"`
	}
	if err := json.Unmarshal(rawNode, &desc); err != nil {
		return 0, err
	}
	if desc.Node.BackendNodeID == 0 {
		return 0, fmt.Errorf("scope_ref %s has no DOM node", ref)
	}
	return desc.Node.BackendNodeID, nil
}

// pageGeneration identifies the current document so two captures can be
// checked for having seen the same page.
func (p *cdpPage) pageGeneration(ctx context.Context) string {
	value, err := p.evaluateString(ctx, `location.href + "|" + String(performance.timeOrigin)`)
	if err != nil {
		return ""
	}
	return value
}

// browserProbe reports what the tab's backends can do right now.
func (p *cdpPage) browserProbe(ctx context.Context) map[string]any {
	out := map[string]any{"cdp": "connected", "input": "cdp Input domain"}
	if nodes, err := p.fullAXTree(ctx); err != nil {
		out["accessibility"] = map[string]any{"available": false, "error": err.Error(), "fallback": "dom scan"}
	} else {
		out["accessibility"] = map[string]any{"available": true, "nodes": len(nodes), "source": "cdp_accessibility"}
	}
	if metrics, err := p.viewportMetrics(ctx); err != nil {
		out["screenshot"] = map[string]any{"available": false, "error": err.Error()}
	} else {
		out["screenshot"] = map[string]any{"available": true, "viewport": metrics}
	}
	if url, err := p.evaluateString(ctx, "location.href"); err == nil {
		out["url"] = url
	}
	return out
}

// viewportMetrics returns the CSS viewport size, device pixel ratio, and
// scroll offset, which fix how screenshot pixels map to page coordinates.
func (p *cdpPage) viewportMetrics(ctx context.Context) (map[string]any, error) {
	var m struct {
		W   float64 `json:"w"`
		H   float64 `json:"h"`
		DPR float64 `json:"dpr"`
		SX  float64 `json:"sx"`
		SY  float64 `json:"sy"`
		PW  float64 `json:"pw"`
		PH  float64 `json:"ph"`
	}
	if err := p.evaluateObject(ctx, `({ w: window.innerWidth, h: window.innerHeight, dpr: window.devicePixelRatio, sx: window.scrollX, sy: window.scrollY, pw: document.documentElement.scrollWidth, ph: document.documentElement.scrollHeight })`, &m); err != nil {
		return nil, err
	}
	return map[string]any{
		"css_width": m.W, "css_height": m.H, "device_pixel_ratio": m.DPR,
		"scroll_x": m.SX, "scroll_y": m.SY, "page_width": m.PW, "page_height": m.PH,
	}, nil
}
