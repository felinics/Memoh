package tools

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// guiLocator says how an action finds the thing it acts on. The contract
// enforces the rule before anything is sent to the browser or desktop, so a
// bad combination (ref and coordinates, x without y, ...) fails with no side
// effect instead of being silently ignored by one execution branch.
type guiLocator int

const (
	// guiLocatorNone: the action takes no target locator at all.
	guiLocatorNone guiLocator = iota
	// guiLocatorElement: exactly one element key (ref or selector) is required.
	guiLocatorElement
	// guiLocatorElementOptional: zero or one element key.
	guiLocatorElementOptional
	// guiLocatorElementOrPoint: exactly one of an element key or an x/y pair.
	guiLocatorElementOrPoint
	// guiLocatorAnyOptional: zero or one of an element key or an x/y pair.
	guiLocatorAnyOptional
	// guiLocatorPoint: an x/y pair is required and element keys are rejected.
	guiLocatorPoint
)

// guiActionSpec is the single source of truth for one action: it drives the
// generated tool schema, the pre-execution validation, and the summary text
// the model reads. Keeping these in one place is what stops the enum, the
// description, and the switch statement from drifting apart again.
type guiActionSpec struct {
	Name    string
	Aliases []string
	// Summary is one clause describing what the action does; requirements
	// are appended automatically from the fields below.
	Summary  string
	Required []string
	Optional []string
	Locator  guiLocator
	// AllowEmpty lists required string parameters that may legitimately be
	// the empty string (fill's text meaning "clear the field").
	AllowEmpty []string
	// Validate runs after the generic checks for cross-parameter rules.
	Validate func(args map[string]any) error
}

// guiContract binds a tool's shared parameter schemas to its action specs.
type guiContract struct {
	ActionKey   string
	ElementKeys []string
	Params      map[string]map[string]any
	Actions     []guiActionSpec

	byName  map[string]*guiActionSpec
	aliases map[string]string
}

func newGUIContract(actionKey string, elementKeys []string, params map[string]map[string]any, actions []guiActionSpec) *guiContract {
	c := &guiContract{
		ActionKey:   actionKey,
		ElementKeys: elementKeys,
		Params:      params,
		Actions:     actions,
		byName:      make(map[string]*guiActionSpec, len(actions)),
		aliases:     make(map[string]string),
	}
	for i := range c.Actions {
		spec := &c.Actions[i]
		if _, dup := c.byName[spec.Name]; dup {
			panic("gui contract: duplicate action " + spec.Name)
		}
		c.byName[spec.Name] = spec
		for _, alias := range spec.Aliases {
			if _, dup := c.aliases[alias]; dup {
				panic("gui contract: duplicate alias " + alias)
			}
			c.aliases[alias] = spec.Name
		}
		for _, key := range spec.allKeys(c) {
			if key == actionKey {
				continue
			}
			if _, ok := params[key]; !ok {
				panic("gui contract: action " + spec.Name + " references unknown parameter " + key)
			}
		}
	}
	return c
}

// allKeys returns every parameter the action may carry, excluding the action
// key itself.
func (s *guiActionSpec) allKeys(c *guiContract) []string {
	keys := append([]string{}, s.Required...)
	keys = append(keys, s.Optional...)
	switch s.Locator {
	case guiLocatorElement, guiLocatorElementOptional:
		keys = append(keys, c.ElementKeys...)
	case guiLocatorElementOrPoint, guiLocatorAnyOptional:
		keys = append(keys, c.ElementKeys...)
		keys = append(keys, "x", "y")
	case guiLocatorPoint:
		keys = append(keys, "x", "y")
	case guiLocatorNone:
	}
	return keys
}

// canonical maps an action name or alias to its canonical name. Unknown names
// are returned lower-cased and trimmed so the caller can report them.
func (c *guiContract) canonical(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if target, ok := c.aliases[normalized]; ok {
		return target
	}
	return normalized
}

// enumValues lists canonical names followed by their aliases, in declaration
// order, for the generated schema.
func (c *guiContract) enumValues() []string {
	out := make([]string, 0, len(c.Actions)*2)
	for _, spec := range c.Actions {
		out = append(out, spec.Name)
	}
	for _, spec := range c.Actions {
		out = append(out, spec.Aliases...)
	}
	return out
}

// actionDescription renders a compact per-action reference the model can
// read: name, summary, then the locator and parameter requirements derived
// from the same spec that validation uses.
func (c *guiContract) actionDescription(intro string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(intro))
	b.WriteString("\n")
	for _, spec := range c.Actions {
		b.WriteString("- ")
		b.WriteString(spec.Name)
		if len(spec.Aliases) > 0 {
			b.WriteString(" (alias ")
			b.WriteString(strings.Join(spec.Aliases, ", "))
			b.WriteString(")")
		}
		b.WriteString(": ")
		b.WriteString(spec.Summary)
		var parts []string
		if locator := c.locatorText(spec.Locator); locator != "" {
			parts = append(parts, locator)
		}
		if len(spec.Required) > 0 {
			parts = append(parts, "requires "+strings.Join(spec.Required, ", "))
		}
		if len(spec.Optional) > 0 {
			parts = append(parts, "optional "+strings.Join(spec.Optional, ", "))
		}
		if len(parts) > 0 {
			b.WriteString(" [")
			b.WriteString(strings.Join(parts, "; "))
			b.WriteString("]")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (c *guiContract) locatorText(locator guiLocator) string {
	element := strings.Join(c.ElementKeys, "|")
	switch locator {
	case guiLocatorElement:
		return "target: " + element
	case guiLocatorElementOptional:
		return "optional target: " + element
	case guiLocatorElementOrPoint:
		return "target: " + element + " or x+y"
	case guiLocatorAnyOptional:
		return "optional target: " + element + " or x+y"
	case guiLocatorPoint:
		return "target: x+y"
	default:
		return ""
	}
}

// schema builds the strict JSON schema for the tool from the contract.
func (c *guiContract) schema(actionDescription string) map[string]any {
	properties := map[string]any{
		c.ActionKey: map[string]any{
			"type":        "string",
			"enum":        c.enumValues(),
			"description": actionDescription,
		},
	}
	names := make([]string, 0, len(c.Params))
	for name := range c.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		properties[name] = c.Params[name]
	}
	return browserObjectSchema(properties, []string{c.ActionKey})
}

// normalize validates args against the contract and returns the matching
// spec. On success args[ActionKey] holds the canonical action name.
func (c *guiContract) normalize(args map[string]any) (*guiActionSpec, error) {
	if args == nil {
		return nil, fmt.Errorf("%s is required", c.ActionKey)
	}
	raw := StringArg(args, c.ActionKey)
	if raw == "" {
		return nil, fmt.Errorf("%s is required", c.ActionKey)
	}
	name := c.canonical(raw)
	spec, ok := c.byName[name]
	if !ok {
		return nil, fmt.Errorf("unknown %s %q", c.ActionKey, raw)
	}
	args[c.ActionKey] = name

	allowed := map[string]bool{c.ActionKey: true}
	for _, key := range spec.allKeys(c) {
		allowed[key] = true
	}
	var unexpected []string
	for key, value := range args {
		if value == nil {
			continue
		}
		if !allowed[key] {
			unexpected = append(unexpected, key)
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		return nil, fmt.Errorf("%s %q does not accept parameter(s): %s", c.ActionKey, name, strings.Join(unexpected, ", "))
	}
	for key := range allowed {
		if key == c.ActionKey {
			continue
		}
		if err := c.checkParam(key, args); err != nil {
			return nil, err
		}
	}
	for _, key := range spec.Required {
		if !guiArgPresent(args, key, spec.allowsEmpty(key)) {
			return nil, fmt.Errorf("%s is required for %s", key, name)
		}
	}
	if err := c.checkLocator(spec, args); err != nil {
		return nil, err
	}
	if spec.Validate != nil {
		if err := spec.Validate(args); err != nil {
			return nil, err
		}
	}
	return spec, nil
}

func (s *guiActionSpec) allowsEmpty(key string) bool {
	for _, k := range s.AllowEmpty {
		if k == key {
			return true
		}
	}
	return false
}

// guiArgPresent reports whether a parameter was supplied. Empty strings count
// as absent unless allowEmpty is set, so "text": "" is a valid clear for fill
// but not a valid input for type.
func guiArgPresent(args map[string]any, key string, allowEmpty bool) bool {
	raw, ok := args[key]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case string:
		return allowEmpty || strings.TrimSpace(v) != ""
	case []any:
		return len(v) > 0
	case []string:
		return len(v) > 0
	default:
		return true
	}
}

func guiPointPresent(args map[string]any, xKey, yKey string) (bool, error) {
	_, hasX, err := IntArg(args, xKey)
	if err != nil {
		return false, err
	}
	_, hasY, err := IntArg(args, yKey)
	if err != nil {
		return false, err
	}
	if hasX != hasY {
		return false, fmt.Errorf("%s and %s must be provided together", xKey, yKey)
	}
	return hasX, nil
}

func (c *guiContract) elementKeysPresent(args map[string]any) []string {
	var present []string
	for _, key := range c.ElementKeys {
		if guiArgPresent(args, key, false) {
			present = append(present, key)
		}
	}
	return present
}

func (c *guiContract) checkLocator(spec *guiActionSpec, args map[string]any) error {
	elements := c.elementKeysPresent(args)
	if len(elements) > 1 {
		return fmt.Errorf("%s accepts only one of %s", spec.Name, strings.Join(c.ElementKeys, ", "))
	}
	hasPoint := false
	switch spec.Locator {
	case guiLocatorElementOrPoint, guiLocatorAnyOptional, guiLocatorPoint:
		var err error
		hasPoint, err = guiPointPresent(args, "x", "y")
		if err != nil {
			return err
		}
	case guiLocatorNone, guiLocatorElement, guiLocatorElementOptional:
	}
	element := strings.Join(c.ElementKeys, " or ")
	switch spec.Locator {
	case guiLocatorNone:
		return nil
	case guiLocatorElement:
		if len(elements) == 0 {
			return fmt.Errorf("%s is required for %s", element, spec.Name)
		}
	case guiLocatorElementOptional:
	case guiLocatorElementOrPoint:
		if len(elements) == 0 && !hasPoint {
			return fmt.Errorf("%s or x/y is required for %s", element, spec.Name)
		}
		if len(elements) > 0 && hasPoint {
			return fmt.Errorf("%s accepts either %s or x/y, not both", spec.Name, element)
		}
	case guiLocatorAnyOptional:
		if len(elements) > 0 && hasPoint {
			return fmt.Errorf("%s accepts either %s or x/y, not both", spec.Name, element)
		}
	case guiLocatorPoint:
		if !hasPoint {
			return fmt.Errorf("x and y are required for %s", spec.Name)
		}
	}
	return nil
}

// checkParam validates one supplied parameter against its schema entry:
// type, enum membership and numeric bounds.
func (c *guiContract) checkParam(key string, args map[string]any) error {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	schema := c.Params[key]
	typ, _ := schema["type"].(string)
	switch typ {
	case "string":
		value, ok := raw.(string)
		if !ok {
			switch raw.(type) {
			case float64, int, int64, bool:
				value = StringArg(args, key)
			default:
				return fmt.Errorf("%s must be a string", key)
			}
		}
		if enum, ok := schema["enum"].([]string); ok && len(enum) > 0 {
			normalized := strings.ToLower(strings.TrimSpace(value))
			found := false
			for _, candidate := range enum {
				if candidate == normalized {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%s must be one of %s", key, strings.Join(enum, ", "))
			}
			args[key] = normalized
		}
	case "integer", "number":
		if typ == "integer" {
			if f, ok := raw.(float64); ok && f != math.Trunc(f) {
				return fmt.Errorf("%s must be an integer", key)
			}
		}
		value, _, err := IntArg(args, key)
		if err != nil {
			return err
		}
		if minimum, ok := schemaNumber(schema["minimum"]); ok && float64(value) < minimum {
			return fmt.Errorf("%s must be at least %d", key, int(minimum))
		}
		if maximum, ok := schemaNumber(schema["maximum"]); ok && float64(value) > maximum {
			return fmt.Errorf("%s must be at most %d", key, int(maximum))
		}
	case "boolean":
		if _, ok := raw.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", key)
		}
	case "array":
		switch raw.(type) {
		case []any, []string:
		default:
			return fmt.Errorf("%s must be an array", key)
		}
	}
	return nil
}

func schemaNumber(raw any) (float64, bool) {
	switch v := raw.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

// guiExclusive rejects two parameters that must not appear together.
func guiExclusive(args map[string]any, a, b string) error {
	if guiArgPresent(args, a, true) && guiArgPresent(args, b, true) {
		return fmt.Errorf("%s and %s are mutually exclusive", a, b)
	}
	return nil
}

// guiClickCount resolves the click count for click-like actions. double_click
// is fixed at two; an explicit conflicting click_count is an error rather than
// a silent override.
func guiClickCount(args map[string]any, action string) (int, error) {
	value, ok, err := IntArg(args, "click_count")
	if err != nil {
		return 0, err
	}
	if action == "double_click" {
		if ok && value != 2 {
			return 0, errors.New("double_click always uses click_count 2; omit click_count or use click")
		}
		return 2, nil
	}
	if !ok {
		return 1, nil
	}
	return value, nil
}

// guiDurationMS resolves a fixed wait. duration_ms is canonical; legacyKey
// (Computer's old "amount", Browser's targetless "timeout") is still accepted
// on its own for older callers but never combined with the new field.
func guiDurationMS(args map[string]any, legacyKey string, fallback int) (int, error) {
	if err := guiExclusive(args, "duration_ms", legacyKey); err != nil {
		return 0, err
	}
	if value, ok, err := IntArg(args, "duration_ms"); err != nil {
		return 0, err
	} else if ok {
		return value, nil
	}
	if legacyKey != "" {
		if value, ok, err := IntArg(args, legacyKey); err != nil {
			return 0, err
		} else if ok {
			return value, nil
		}
	}
	return fallback, nil
}

// rawStringArg returns a string parameter verbatim. StringArg trims
// whitespace, which is wrong for text that is typed into a field.
func rawStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return ""
	}
	if s, ok := raw.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", raw)
}

// guiTimeoutMS resolves a readiness timeout with an action-specific default.
func guiTimeoutMS(args map[string]any, fallback int) (int, error) {
	value, ok, err := IntArg(args, "timeout")
	if err != nil {
		return 0, err
	}
	if !ok {
		return fallback, nil
	}
	return value, nil
}
