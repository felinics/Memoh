package protocolgen

import (
	"fmt"
	"sort"
	"strings"
)

// emitTaggedUnion renders a serde internally-tagged union: one wrapper struct
// with a pointer per known variant, the observed tag value, and the raw bytes
// for unknown-variant tolerance. Variant payloads are emitted as their own
// named structs (from the variant titles) with the tag property stripped.
func (g *generator) emitTaggedUnion(b *strings.Builder, cls *classified) error {
	name := cls.name
	writeDoc(b, cls.schema.Description, "")
	fmt.Fprintf(b, "// %s is an internally-tagged union (tag property %q).\n", name, cls.tagProperty)
	fmt.Fprintf(b, "// Unknown variants decode without error: only Tag and Raw() are populated.\n")
	fmt.Fprintf(b, "type %s struct {\n", name)
	fmt.Fprintf(b, "\t// Tag is the value of the %q property observed on decode.\n", cls.tagProperty)
	b.WriteString("\tTag string\n")
	for _, v := range cls.variants {
		fmt.Fprintf(b, "\t%s *%s\n", exportedName(v.TagValue), v.GoName)
	}
	b.WriteString("\traw rawMessage\n")
	b.WriteString("}\n\n")

	// Tag value constants.
	b.WriteString("const (\n")
	for _, v := range cls.variants {
		fmt.Fprintf(b, "\t%sTag%s = %q\n", name, exportedName(v.TagValue), v.TagValue)
	}
	b.WriteString(")\n\n")

	fmt.Fprintf(b, "func (u *%s) UnmarshalJSON(data []byte) error {\n", name)
	// Reset first: encoding/json reuses slice elements and callers reuse
	// variables, so stale variant pointers from a previous decode must not
	// survive. The raw copy is freshly allocated because Raw() escapes.
	fmt.Fprintf(b, "\t*u = %s{}\n", name)
	b.WriteString("\tu.raw = append(rawMessage(nil), data...)\n")
	fmt.Fprintf(b, "\tvar probe struct {\n\t\tTag string `json:%q`\n\t}\n", cls.tagProperty)
	b.WriteString("\tif err := jsonUnmarshal(data, &probe); err != nil {\n\t\treturn err\n\t}\n")
	b.WriteString("\tu.Tag = probe.Tag\n")
	b.WriteString("\tswitch probe.Tag {\n")
	for _, v := range cls.variants {
		fmt.Fprintf(b, "\tcase %q:\n", v.TagValue)
		fmt.Fprintf(b, "\t\tu.%s = new(%s)\n", exportedName(v.TagValue), v.GoName)
		fmt.Fprintf(b, "\t\treturn jsonUnmarshal(data, u.%s)\n", exportedName(v.TagValue))
	}
	b.WriteString("\t}\n\treturn nil\n}\n\n")

	fmt.Fprintf(b, "func (u %s) MarshalJSON() ([]byte, error) {\n", name)
	b.WriteString("\tswitch {\n")
	for _, v := range cls.variants {
		fmt.Fprintf(b, "\tcase u.%s != nil:\n", exportedName(v.TagValue))
		fmt.Fprintf(b, "\t\treturn marshalTagged(%q, %q, u.%s)\n", cls.tagProperty, v.TagValue, exportedName(v.TagValue))
	}
	b.WriteString("\t}\n")
	b.WriteString("\tif len(u.raw) > 0 {\n\t\treturn u.raw, nil\n\t}\n")
	fmt.Fprintf(b, "\treturn nil, errNoVariant(%q)\n", name)
	b.WriteString("}\n\n")

	fmt.Fprintf(b, "// Raw returns the original JSON for this union value, if it was decoded.\n")
	fmt.Fprintf(b, "func (u %s) Raw() []byte { return u.raw }\n\n", name)

	// Variant payload structs, tag property stripped.
	for _, v := range cls.variants {
		if err := g.emitStruct(b, v.GoName, v.Schema, map[string]bool{cls.tagProperty: true}); err != nil {
			return err
		}
	}
	return nil
}

// emitMixedUnion renders a serde externally-tagged union whose variants are
// bare strings (unit variants) and/or single-key objects (payload variants).
func (g *generator) emitMixedUnion(b *strings.Builder, cls *classified) error {
	name := cls.name

	type payload struct {
		Key    string
		GoName string
		Expr   string
	}
	payloads := make([]payload, 0, len(cls.objectVariants))
	for _, v := range cls.objectVariants {
		expr, _, err := g.baseExpr(v.Payload, name, v.Key)
		if err != nil {
			return err
		}
		payloads = append(payloads, payload{Key: v.Key, GoName: exportedName(v.Key), Expr: expr})
	}
	sort.Slice(payloads, func(i, j int) bool { return payloads[i].Key < payloads[j].Key })
	units := append([]string(nil), cls.unitValues...)
	sort.Strings(units)

	writeDoc(b, cls.schema.Description, "")
	fmt.Fprintf(b, "// %s is a mixed union: on the wire it is either a bare string\n", name)
	fmt.Fprintf(b, "// (Unit) or a single-key object (one payload field set). Unknown variants\n")
	fmt.Fprintf(b, "// decode without error and are retained in Raw().\n")
	fmt.Fprintf(b, "type %s struct {\n", name)
	b.WriteString("\t// Unit holds the bare-string variant value, if that form was used.\n")
	b.WriteString("\tUnit string\n")
	for _, p := range payloads {
		fmt.Fprintf(b, "\t%s *%s\n", p.GoName, p.Expr)
	}
	b.WriteString("\traw rawMessage\n")
	b.WriteString("}\n\n")

	if len(units) > 0 {
		b.WriteString("const (\n")
		for _, v := range units {
			fmt.Fprintf(b, "\t%sUnit%s = %q\n", name, exportedName(v), v)
		}
		b.WriteString(")\n\n")
	}

	fmt.Fprintf(b, "func (u *%s) UnmarshalJSON(data []byte) error {\n", name)
	fmt.Fprintf(b, "\t*u = %s{}\n", name)
	b.WriteString("\tu.raw = append(rawMessage(nil), data...)\n")
	b.WriteString("\tif isJSONString(data) {\n\t\treturn jsonUnmarshal(data, &u.Unit)\n\t}\n")
	b.WriteString("\tvar obj map[string]rawMessage\n")
	b.WriteString("\tif err := jsonUnmarshal(data, &obj); err != nil {\n\t\treturn err\n\t}\n")
	b.WriteString("\tif len(obj) != 1 {\n\t\treturn nil\n\t}\n")
	b.WriteString("\tfor key, payload := range obj {\n")
	b.WriteString("\t\tswitch key {\n")
	for _, p := range payloads {
		fmt.Fprintf(b, "\t\tcase %q:\n", p.Key)
		fmt.Fprintf(b, "\t\t\tu.%s = new(%s)\n", p.GoName, p.Expr)
		fmt.Fprintf(b, "\t\t\treturn jsonUnmarshal(payload, u.%s)\n", p.GoName)
	}
	b.WriteString("\t\t}\n\t}\n\treturn nil\n}\n\n")

	fmt.Fprintf(b, "func (u %s) MarshalJSON() ([]byte, error) {\n", name)
	b.WriteString("\tif u.Unit != \"\" {\n\t\treturn jsonMarshal(u.Unit)\n\t}\n")
	b.WriteString("\tswitch {\n")
	for _, p := range payloads {
		fmt.Fprintf(b, "\tcase u.%s != nil:\n", p.GoName)
		fmt.Fprintf(b, "\t\treturn marshalKeyed(%q, u.%s)\n", p.Key, p.GoName)
	}
	b.WriteString("\t}\n")
	b.WriteString("\tif len(u.raw) > 0 {\n\t\treturn u.raw, nil\n\t}\n")
	fmt.Fprintf(b, "\treturn nil, errNoVariant(%q)\n", name)
	b.WriteString("}\n\n")

	fmt.Fprintf(b, "// Raw returns the original JSON for this union value, if it was decoded.\n")
	fmt.Fprintf(b, "func (u %s) Raw() []byte { return u.raw }\n\n", name)
	return nil
}
