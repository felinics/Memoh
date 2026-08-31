package protocolgen

import (
	"fmt"
	"sort"
	"strings"
)

// generator holds emission state: the classified closure plus nested inline
// types synthesized while resolving field expressions.
type generator struct {
	corpus *corpus
	defs   map[string]*classified

	// synthesized inline object/enum types, keyed by their Go name.
	extra map[string]*classified
	// emission order for synthesized types (creation order, then sorted).
	extraNames []string
}

func newGenerator(c *corpus, defs map[string]*classified) *generator {
	return &generator{corpus: c, defs: defs, extra: map[string]*classified{}}
}

// defGoName maps a schema definition name to its Go type name, honoring the
// hand-written overrides.
func defGoName(name string) string {
	if goName, ok := handWritten[name]; ok {
		return goName
	}
	return name
}

// registerExtra synthesizes a named struct for an inline object schema.
func (g *generator) registerExtra(name string, s *Schema) error {
	if existing, ok := g.extra[name]; ok {
		if existing.schema == s {
			return nil
		}
		return fmt.Errorf("synthesized type %q defined twice with different schemas", name)
	}
	if _, ok := g.defs[name]; ok {
		return fmt.Errorf("synthesized type %q collides with a schema definition", name)
	}
	cls, err := classify(name, s)
	if err != nil {
		return err
	}
	g.extra[name] = cls
	g.extraNames = append(g.extraNames, name)
	return nil
}

// fieldExpr resolves a property schema to a Go type expression for a struct
// field, applying the pointer/omitempty policy:
//   - slices and maps stay bare (nil is the absent value)
//   - optional or nullable scalars/structs become pointers
func (g *generator) fieldExpr(s *Schema, owner, field string, required bool) (expr string, omitempty bool, err error) {
	base, nullable, err := g.baseExpr(s, owner, field)
	if err != nil {
		return "", false, err
	}
	soft := strings.HasPrefix(base, "[]") || strings.HasPrefix(base, "map[") || base == "any"
	if soft {
		return base, !required, nil
	}
	if !required {
		return "*" + base, true, nil
	}
	if nullable {
		// Required but nullable: the key must stay present, so no omitempty —
		// a nil pointer marshals as the explicit null the schema allows.
		return "*" + base, false, nil
	}
	return base, false, nil
}

// baseExpr resolves the non-null Go type for a schema node, synthesizing
// nested named types for inline objects.
func (g *generator) baseExpr(s *Schema, owner, field string) (expr string, nullable bool, err error) {
	if s == nil || s.Any {
		return "any", false, nil
	}
	if s.Ref != "" {
		return defGoName(refName(s.Ref)), false, nil
	}
	if name, ok := s.singleAllOfRef(); ok {
		return defGoName(name), false, nil
	}
	if len(s.AnyOf) == 2 {
		var value *Schema
		nulls := 0
		for _, v := range s.AnyOf {
			if types, _ := v.nonNullTypes(); len(types) == 0 && len(v.Type) == 1 {
				nulls++
				continue
			}
			value = v
		}
		if nulls == 1 && value != nil {
			inner, _, err := g.baseExpr(value, owner, field)
			return inner, true, err
		}
	}
	if len(s.OneOf) > 0 || len(s.AnyOf) > 0 || len(s.AllOf) > 0 {
		// Inline unions synthesize a named type so the union machinery applies.
		name := owner + exportedName(field)
		if err := g.registerExtra(name, s); err != nil {
			return "", false, err
		}
		return name, false, nil
	}

	types, isNullable := s.nonNullTypes()
	if len(types) == 0 {
		return "any", false, nil
	}
	if len(types) > 1 {
		return "", false, fmt.Errorf("%s.%s: unsupported multi-type %v", owner, field, s.Type)
	}
	switch types[0] {
	case "string":
		return "string", isNullable, nil
	case "boolean":
		return "bool", isNullable, nil
	case "integer":
		if strings.HasPrefix(s.Format, "uint") {
			return "uint64", isNullable, nil
		}
		return "int64", isNullable, nil
	case "number":
		return "float64", isNullable, nil
	case "array":
		elem, _, err := g.baseExpr(s.Items, owner, field+"Item")
		if err != nil {
			return "", false, err
		}
		return "[]" + elem, isNullable, nil
	case "object":
		if len(s.Properties) > 0 {
			name := owner + exportedName(field)
			if err := g.registerExtra(name, s); err != nil {
				return "", false, err
			}
			return name, isNullable, nil
		}
		if s.AddlProps != nil && s.AddlProps.Schema != nil {
			elem, _, err := g.baseExpr(s.AddlProps.Schema, owner, field+"Value")
			if err != nil {
				return "", false, err
			}
			return "map[string]" + elem, isNullable, nil
		}
		return "map[string]any", isNullable, nil
	}
	return "", false, fmt.Errorf("%s.%s: unsupported type %q", owner, field, types[0])
}

// writeDoc renders a description as a Go doc comment (first paragraph only).
func writeDoc(b *strings.Builder, description, indent string) {
	if description == "" {
		return
	}
	paragraph, _, _ := strings.Cut(description, "\n\n")
	for _, line := range strings.Split(paragraph, "\n") {
		b.WriteString(indent + "// " + strings.TrimRight(line, " ") + "\n")
	}
}

// emitStruct renders a struct definition for an object schema.
func (g *generator) emitStruct(b *strings.Builder, name string, s *Schema, skipProps map[string]bool) error {
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	writeDoc(b, s.Description, "")
	fmt.Fprintf(b, "type %s struct {\n", name)

	props := make([]string, 0, len(s.Properties))
	for propName := range s.Properties {
		if skipProps[propName] {
			continue
		}
		props = append(props, propName)
	}
	sort.Strings(props)

	goNames := map[string]string{}
	for _, propName := range props {
		goName := exportedName(propName)
		if prev, ok := goNames[goName]; ok {
			return fmt.Errorf("%s: fields %q and %q map to the same Go name %s", name, prev, propName, goName)
		}
		goNames[goName] = propName
	}

	type collectionField struct {
		GoName string
		Expr   string
	}
	var requiredCollections []collectionField
	for _, propName := range props {
		prop := s.Properties[propName]
		expr, omitempty, err := g.fieldExpr(prop, name, propName, required[propName])
		if err != nil {
			return err
		}
		if required[propName] && (strings.HasPrefix(expr, "[]") || strings.HasPrefix(expr, "map[")) {
			requiredCollections = append(requiredCollections, collectionField{GoName: exportedName(propName), Expr: expr})
		}
		if prop != nil {
			writeDoc(b, prop.Description, "\t")
		}
		tag := propName
		if omitempty {
			tag += ",omitempty"
		}
		fmt.Fprintf(b, "\t%s %s `json:\"%s\"`\n", exportedName(propName), expr, tag)
	}
	b.WriteString("}\n\n")

	// serde rejects null for required sequences/maps, and Go marshals nil
	// slices/maps as null — normalize them to empty collections on the way out.
	if len(requiredCollections) > 0 {
		fmt.Fprintf(b, "func (v %s) MarshalJSON() ([]byte, error) {\n", name)
		fmt.Fprintf(b, "\ttype plain %s\n", name)
		b.WriteString("\tp := plain(v)\n")
		for _, f := range requiredCollections {
			fmt.Fprintf(b, "\tif p.%s == nil {\n\t\tp.%s = %s{}\n\t}\n", f.GoName, f.GoName, f.Expr)
		}
		b.WriteString("\treturn jsonMarshal(p)\n}\n\n")
	}
	return nil
}

// emitEnum renders a string enum type with value constants.
func (*generator) emitEnum(b *strings.Builder, cls *classified) error {
	values := cls.unitValues
	if values == nil {
		var err error
		values, err = cls.schema.enumStrings()
		if err != nil {
			return fmt.Errorf("%s: %w", cls.name, err)
		}
	}
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)

	writeDoc(b, cls.schema.Description, "")
	fmt.Fprintf(b, "type %s string\n\n", cls.name)
	b.WriteString("const (\n")
	for _, v := range sorted {
		fmt.Fprintf(b, "\t%s%s %s = %q\n", cls.name, exportedName(v), cls.name, v)
	}
	b.WriteString(")\n\n")
	return nil
}

// emitAlias renders primitive/array/map aliases.
func (g *generator) emitAlias(b *strings.Builder, cls *classified) error {
	writeDoc(b, cls.schema.Description, "")
	switch cls.kind {
	case kindAlias:
		types, _ := cls.schema.nonNullTypes()
		goType := map[string]string{"string": "string", "boolean": "bool", "number": "float64"}[types[0]]
		if types[0] == "integer" {
			goType = "int64"
			if strings.HasPrefix(cls.schema.Format, "uint") {
				goType = "uint64"
			}
		}
		if goType == "" {
			return fmt.Errorf("%s: unsupported alias type %v", cls.name, types)
		}
		fmt.Fprintf(b, "type %s = %s\n\n", cls.name, goType)
	case kindArrayAlias:
		elem, _, err := g.baseExpr(cls.schema.Items, cls.name, "Item")
		if err != nil {
			return err
		}
		fmt.Fprintf(b, "type %s = []%s\n\n", cls.name, elem)
	case kindMapAlias:
		elem := "any"
		if cls.schema.AddlProps != nil && cls.schema.AddlProps.Schema != nil {
			var err error
			elem, _, err = g.baseExpr(cls.schema.AddlProps.Schema, cls.name, "Value")
			if err != nil {
				return err
			}
		}
		fmt.Fprintf(b, "type %s = map[string]%s\n\n", cls.name, elem)
	case kindOpaqueUnion:
		candidates := make([]string, 0, len(cls.schema.AnyOf))
		for _, v := range cls.schema.AnyOf {
			candidates = append(candidates, refName(v.Ref))
		}
		fmt.Fprintf(b, "// %s is an untagged union with no discriminator; decode the raw\n", cls.name)
		fmt.Fprintf(b, "// JSON into one of: %s.\n", strings.Join(candidates, ", "))
		fmt.Fprintf(b, "type %s = rawMessage\n\n", cls.name)
	}
	return nil
}
