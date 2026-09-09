// Command synccaps enriches the hand-maintained conf/providers templates with
// reasoning capabilities (thinking_mode + reasoning_efforts) derived from the
// LiteLLM model registry. It runs at BUILD time (locally or in CI), never in the
// server: the runtime reads capabilities straight from the templates.
//
// First-party providers whose model ids are canonical enough for a safe
// litellm match; reseller shells are pruned from the registry before matching,
// and a canonical miss leaves the template untouched (legacy toggle fallback)
// rather than borrowing a sibling entry's capabilities.
//
// Usage:
//
//	go run ./cmd/synccaps [-litellm URL|path] [-dir conf/providers] [-check]
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/felinics/memoh/internal/capabilities"
)

const defaultLitellmURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// firstPartyProviders are the templates whose model ids are concrete enough for
// a safe LiteLLM match after reseller entries are pruned. Catalog/local
// providers are excluded because matching their unstructured identifiers is
// unreliable.
var firstPartyProviders = []string{
	"anthropic", "openai", "google", "xai",
	"deepseek", "mistral", "moonshot", "qwen", "minimax",
	"openrouter",
	"azure-openai", "cerebras", "cloudflare", "fireworks",
	"perplexity", "together", "zai",
}

func main() {
	litellm := flag.String("litellm", defaultLitellmURL, "LiteLLM registry URL or local file path")
	dir := flag.String("dir", "conf/providers", "providers template directory")
	check := flag.Bool("check", false, "exit non-zero if any template would change (no writes)")
	flag.Parse()

	body, err := loadRegistry(*litellm)
	if err != nil {
		fail("load registry: %v", err)
	}
	resolver, err := capabilities.NewResolver(body)
	if err != nil {
		fail("parse registry: %v", err)
	}
	fmt.Fprintf(os.Stderr, "loaded %d first-party registry entries\n", resolver.Count())

	changed := false
	for _, p := range firstPartyProviders {
		path := filepath.Join(*dir, p+".yaml")
		n, err := enrichFile(path, resolver, *check)
		if err != nil {
			fail("%s: %v", path, err)
		}
		if n > 0 {
			changed = true
			fmt.Fprintf(os.Stderr, "%s: %d model(s) %s\n", path, n, verb(*check))
		}
	}

	if *check && changed {
		fmt.Fprintln(os.Stderr, "templates are stale; run `go run ./cmd/synccaps`")
		os.Exit(1)
	}
}

func verb(check bool) string {
	if check {
		return "would change"
	}
	return "updated"
}

func loadRegistry(src string) ([]byte, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		return os.ReadFile(src) //nolint:gosec // operator-provided registry file
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec,noctx // CLI-only, operator-provided URL is trusted
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// enrichFile applies derived capabilities to one provider template and returns
// the number of models whose thinking_mode/reasoning_efforts changed.
func enrichFile(path string, resolver *capabilities.Resolver, check bool) (int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-managed provider template
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return 0, err
	}
	if len(doc.Content) == 0 {
		return 0, nil
	}
	root := doc.Content[0]
	models := mapValue(root, "models")
	if models == nil || models.Kind != yaml.SequenceNode {
		return 0, nil
	}

	changed := 0
	for _, model := range models.Content {
		if model.Kind != yaml.MappingNode {
			continue
		}
		id := scalarValue(mapValue(model, "model_id"))
		if id == "" {
			continue
		}
		caps, ok := resolver.Resolve(id)
		if !ok {
			continue
		}
		// Positive discoveries fill reasoning controls. Explicit negative
		// discoveries only correct stale reasoning metadata; unknown stays
		// untouched so LiteLLM silence does not become a false "no reasoning".
		switch caps.ThinkingMode {
		case "toggle", "adaptive":
			if applyToModel(model, caps.ThinkingMode, caps.EffortLevels) {
				changed++
			}
		case "none":
			if applyNoReasonToModel(model) {
				changed++
			}
		}
		// Positive PDF discoveries add the file-input compatibility.
		// Additive only: registry silence or an explicit false never removes
		// a hand-maintained token (fill missing, never override).
		if caps.FileInput != nil && *caps.FileInput {
			if applyFileInputToModel(model) {
				changed++
			}
		}
	}

	if changed == 0 || check {
		return changed, nil
	}
	return changed, writeNode(path, &doc, modelEntriesHaveBlankLines(raw))
}

// applyToModel sets thinking_mode + reasoning_efforts inside a model's config
// mapping, creating config if absent. Returns true if anything changed.
func applyToModel(model *yaml.Node, mode string, efforts []string) bool {
	cfg := mapValue(model, "config")
	if cfg == nil {
		cfg = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapSet(model, "config", cfg)
	}
	wantMode := mode
	wantEfforts := efforts
	if len(wantEfforts) == 0 {
		wantEfforts = []string{"low", "medium", "high"}
	}
	// Hand-maintained tokens the registry cannot know about are preserved rather
	// than overwritten. LiteLLM's only off signal is the OpenAI-wire
	// supports_none_reasoning_effort flag, so where off travels another way
	// (DeepSeek toggle via chat_completions_compat, Gemini 2.5 Flash via a
	// zero budget) the token can only come from the template. The same holds for
	// minimal, which the registry reports for exactly one model family while
	// Gemini 3.x accepts it as its floor. Registry silence is not evidence of
	// absence.
	if existing := mapValue(cfg, "reasoning_efforts"); existing != nil {
		// Collected in canonical order and prepended once, so the list stays
		// weakest-to-strongest instead of reversing with each insertion.
		var carried []string
		for _, token := range []string{"disable", "minimal"} {
			if seqContains(existing, token) && !slices.Contains(wantEfforts, token) {
				carried = append(carried, token)
			}
		}
		if len(carried) > 0 {
			wantEfforts = append(carried, wantEfforts...)
		}
	}

	changed := false
	if scalarValue(mapValue(cfg, "thinking_mode")) != wantMode {
		mapSet(cfg, "thinking_mode", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: wantMode})
		changed = true
	}
	if !sameSeq(mapValue(cfg, "reasoning_efforts"), wantEfforts) {
		mapSet(cfg, "reasoning_efforts", flowSeq(wantEfforts))
		changed = true
	}
	return changed
}

// applyFileInputToModel appends the file-input compatibility when the registry
// reports native PDF input. Requires an existing compatibilities list that
// already carries vision — PDF ingestion is vision-dependent provider-side, so
// a pdf-without-vision registry row is treated as dirty data and skipped. The
// token lands right after vision to keep the canonical template ordering.
func applyFileInputToModel(model *yaml.Node) bool {
	cfg := mapValue(model, "config")
	if cfg == nil {
		return false
	}
	compat := mapValue(cfg, "compatibilities")
	if compat == nil || compat.Kind != yaml.SequenceNode {
		return false
	}
	visionIdx := -1
	for i, item := range compat.Content {
		switch item.Value {
		case "file-input":
			return false
		case "vision":
			visionIdx = i
		}
	}
	if visionIdx < 0 {
		return false
	}
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "file-input"}
	rest := append([]*yaml.Node{node}, compat.Content[visionIdx+1:]...)
	compat.Content = append(compat.Content[:visionIdx+1], rest...)
	return true
}

// applyNoReasonToModel records an explicit no-reasoning discovery only when the
// template already carries stale derived reasoning metadata. A hand-maintained
// always declaration is authoritative because registry negatives can lag the
// provider's first-party contract. It does not create a config block for ordinary
// non-reasoning models.
func applyNoReasonToModel(model *yaml.Node) bool {
	cfg := mapValue(model, "config")
	if cfg == nil || !hasReasoningResidue(cfg) {
		return false
	}
	if scalarValue(mapValue(cfg, "thinking_mode")) == "always" {
		return false
	}

	changed := false
	if scalarValue(mapValue(cfg, "thinking_mode")) != "none" {
		mapSet(cfg, "thinking_mode", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "none"})
		changed = true
	}
	if mapValue(cfg, "reasoning_efforts") != nil {
		mapDelete(cfg, "reasoning_efforts")
		changed = true
	}
	if removeSeqValue(mapValue(cfg, "compatibilities"), "reasoning") {
		if seq := mapValue(cfg, "compatibilities"); seq != nil && len(seq.Content) == 0 {
			mapDelete(cfg, "compatibilities")
		}
		changed = true
	}
	return changed
}

func hasReasoningResidue(cfg *yaml.Node) bool {
	mode := scalarValue(mapValue(cfg, "thinking_mode"))
	if mode != "" && mode != "none" {
		return true
	}
	if mapValue(cfg, "reasoning_efforts") != nil {
		return true
	}
	return seqContains(mapValue(cfg, "compatibilities"), "reasoning")
}

func flowSeq(items []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, it := range items {
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: it})
	}
	return n
}

func sameSeq(node *yaml.Node, want []string) bool {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) != len(want) {
		return false
	}
	for i, c := range node.Content {
		if c.Value != want[i] {
			return false
		}
	}
	return true
}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapDelete(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// mapSet replaces or appends key->value in a mapping node.
func mapSet(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func scalarValue(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func seqContains(node *yaml.Node, value string) bool {
	if node == nil || node.Kind != yaml.SequenceNode {
		return false
	}
	for _, c := range node.Content {
		if c.Value == value {
			return true
		}
	}
	return false
}

func removeSeqValue(node *yaml.Node, value string) bool {
	if node == nil || node.Kind != yaml.SequenceNode {
		return false
	}
	out := node.Content[:0]
	changed := false
	for _, c := range node.Content {
		if c.Value == value {
			changed = true
			continue
		}
		out = append(out, c)
	}
	node.Content = out
	return changed
}

func writeNode(path string, doc *yaml.Node, blankBetweenModels bool) error {
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}

	out := restoreBlankLines(buf.String(), blankBetweenModels)

	tmp, err := os.CreateTemp(filepath.Dir(path), ".synccaps-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(out); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path) //nolint:gosec // temp→target in same dir, safe atomic swap
}

// modelEntriesHaveBlankLines detects the file's existing model-entry spacing.
// Smaller catalogs are hand-formatted with a blank line between entries, while
// large imported catalogs such as OpenRouter are compact. Preserve that style so
// synccaps changes only capability metadata, not the whole catalog layout.
func modelEntriesHaveBlankLines(raw []byte) bool {
	lines := strings.Split(string(raw), "\n")
	modelEntries := 0
	blankBefore := 0
	for i, ln := range lines {
		if !strings.HasPrefix(ln, "  - model_id:") {
			continue
		}
		modelEntries++
		if i > 0 && strings.TrimSpace(lines[i-1]) == "" {
			blankBefore++
		}
	}
	if modelEntries > 200 {
		return false
	}
	return modelEntries > 1 && blankBefore*2 >= modelEntries
}

// restoreBlankLines re-inserts the blank-line spacing the YAML encoder strips:
// always one blank line before the top-level `models:` key, and optionally one
// between model entries when the original file used that style.
func restoreBlankLines(s string, blankBetweenModels bool) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(lines)+len(lines)/4)
	prevMeaningful := ""
	for _, ln := range lines {
		switch {
		case ln == "models:" && prevMeaningful != "":
			out = append(out, "")
		case blankBetweenModels && strings.HasPrefix(ln, "  - model_id:") && prevMeaningful != "" && prevMeaningful != "models:":
			out = append(out, "")
		}
		out = append(out, ln)
		if strings.TrimSpace(ln) != "" {
			prevMeaningful = ln
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "synccaps: "+format+"\n", args...)
	os.Exit(1)
}
