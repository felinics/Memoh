package protocolgen

import (
	"fmt"
	"sort"
)

// defKind classifies a named definition into one of the shapes the emitter
// knows how to render.
type defKind int

const (
	kindStruct defKind = iota
	kindEnum
	kindAlias       // named alias of a primitive
	kindArrayAlias  // named alias of an array
	kindMapAlias    // named alias of a map
	kindTaggedUnion // serde internally-tagged union (tag property + payload fields)
	kindMixedUnion  // serde externally-tagged union: bare-string units and/or single-key object variants
	kindOpaqueUnion // untagged anyOf union with no discriminator; kept as raw JSON
	kindMethodUnion // top-level method envelope unions; never emitted
	kindHandWritten // mapped to a hand-written type in the protocol package
)

// handWritten maps schema definitions to hand-written types in the protocol
// package, for shapes the generator should not attempt (RequestId is a
// string|number union that must round-trip byte-for-byte).
var handWritten = map[string]string{
	"FunctionCallOutputBody": "rawMessage",
	"RequestId":              "RequestID",
}

type classified struct {
	name   string
	kind   defKind
	schema *Schema

	// kindTaggedUnion
	tagProperty string
	variants    []taggedVariant

	// kindMixedUnion
	unitValues     []string
	objectVariants []mixedVariant
}

type taggedVariant struct {
	TagValue string
	GoName   string // from variant title
	Schema   *Schema
}

type mixedVariant struct {
	Key     string
	Payload *Schema
}

var methodUnionNames = map[string]bool{
	"ClientRequest":       true,
	"ServerNotification":  true,
	"JSONRPCMessage":      true,
	"JSONRPCRequest":      true,
	"JSONRPCResponse":     true,
	"JSONRPCNotification": true,
	"JSONRPCError":        true,
}

func classify(name string, s *Schema) (*classified, error) {
	c := &classified{name: name, schema: s}
	if methodUnionNames[name] {
		c.kind = kindMethodUnion
		return c, nil
	}
	if _, ok := handWritten[name]; ok {
		c.kind = kindHandWritten
		return c, nil
	}

	switch {
	case len(s.OneOf) > 0:
		return classifyOneOf(c, s)
	case len(s.AnyOf) > 0:
		// Untagged unions carry no discriminator, so decoding into a typed
		// wrapper would require structural trial; keep them raw and let the
		// consumer decode into the candidate types on demand.
		for _, v := range s.AnyOf {
			if v.Ref == "" {
				return nil, fmt.Errorf("definition %q: anyOf variant without $ref is not supported", name)
			}
		}
		c.kind = kindOpaqueUnion
		return c, nil
	case len(s.AllOf) > 0:
		return nil, fmt.Errorf("definition %q: top-level allOf is not supported", name)
	}

	types, _ := s.nonNullTypes()
	if len(types) != 1 {
		return nil, fmt.Errorf("definition %q: unsupported type list %v", name, s.Type)
	}
	switch types[0] {
	case "object":
		if len(s.Properties) > 0 || s.AddlProps == nil {
			c.kind = kindStruct
			return c, nil
		}
		c.kind = kindMapAlias
		return c, nil
	case "string":
		if len(s.Enum) > 0 {
			c.kind = kindEnum
			return c, nil
		}
		c.kind = kindAlias
		return c, nil
	case "integer", "number", "boolean":
		c.kind = kindAlias
		return c, nil
	case "array":
		c.kind = kindArrayAlias
		return c, nil
	}
	return nil, fmt.Errorf("definition %q: unsupported type %q", name, types[0])
}

func classifyOneOf(c *classified, s *Schema) (*classified, error) {
	var unitValues []string
	var objectVariants []mixedVariant
	var taggedVariants []*Schema

	for _, v := range s.OneOf {
		types, _ := v.nonNullTypes()
		switch {
		case len(types) == 1 && types[0] == "string" && len(v.Enum) > 0:
			values, err := v.enumStrings()
			if err != nil {
				return nil, fmt.Errorf("definition %q: %w", c.name, err)
			}
			unitValues = append(unitValues, values...)
		case len(types) == 1 && types[0] == "object" && len(v.Properties) == 1 && len(v.Required) == 1 && v.Properties[v.Required[0]] != nil && v.Properties[v.Required[0]].Enum == nil:
			key := v.Required[0]
			objectVariants = append(objectVariants, mixedVariant{Key: key, Payload: v.Properties[key]})
		case len(types) == 1 && types[0] == "object":
			taggedVariants = append(taggedVariants, v)
		default:
			return nil, fmt.Errorf("definition %q: oneOf variant with unsupported shape", c.name)
		}
	}

	switch {
	case len(taggedVariants) == len(s.OneOf):
		return classifyTagged(c, taggedVariants)
	case len(taggedVariants) == 0 && len(objectVariants) == 0:
		// Enum split across variants for per-value descriptions.
		c.kind = kindEnum
		c.unitValues = unitValues
		return c, nil
	case len(taggedVariants) == 0:
		c.kind = kindMixedUnion
		c.unitValues = unitValues
		c.objectVariants = objectVariants
		return c, nil
	}
	return nil, fmt.Errorf("definition %q: oneOf mixes tagged and other variant shapes", c.name)
}

func classifyTagged(c *classified, variants []*Schema) (*classified, error) {
	// Find the tag: a property present in every variant whose schema is a
	// single-valued string enum. Prefer "type" when it qualifies.
	tag := ""
	candidates := map[string]int{}
	for _, v := range variants {
		for propName, prop := range v.Properties {
			if prop != nil && len(prop.Enum) == 1 {
				if types, _ := prop.nonNullTypes(); len(types) == 1 && types[0] == "string" {
					candidates[propName]++
				}
			}
		}
	}
	if candidates["type"] == len(variants) {
		tag = "type"
	} else {
		for propName, count := range candidates {
			if count == len(variants) {
				if tag != "" {
					return nil, fmt.Errorf("definition %q: ambiguous union tag (%q vs %q)", c.name, tag, propName)
				}
				tag = propName
			}
		}
	}
	if tag == "" {
		return nil, fmt.Errorf("definition %q: no common tag property across oneOf variants", c.name)
	}

	c.kind = kindTaggedUnion
	c.tagProperty = tag
	for _, v := range variants {
		values, err := v.Properties[tag].enumStrings()
		if err != nil {
			return nil, fmt.Errorf("definition %q: %w", c.name, err)
		}
		goName := v.Title
		if !isGoIdentifier(goName) {
			// Titles are the upstream naming source of truth, but a few are
			// missing or carry Rust module paths (`Foov2::Bar`); synthesize
			// the same "<Value><Parent>" shape well-formed siblings use.
			goName = exportedName(values[0]) + c.name
		}
		c.variants = append(c.variants, taggedVariant{TagValue: values[0], GoName: goName, Schema: v})
	}
	sort.Slice(c.variants, func(i, j int) bool { return c.variants[i].TagValue < c.variants[j].TagValue })
	return c, nil
}

// closure walks $refs from the subset roots and returns every reachable
// definition, classified.
func closure(c *corpus) (map[string]*classified, error) {
	seen := map[string]*classified{}
	var visitSchema func(s *Schema) error

	visitDef := func(name string) error {
		if _, ok := seen[name]; ok {
			return nil
		}
		def, ok := c.defs[name]
		if !ok {
			return fmt.Errorf("unresolved $ref to %q", name)
		}
		cls, err := classify(name, def)
		if err != nil {
			return err
		}
		seen[name] = cls
		return visitSchema(def)
	}

	visitSchema = func(s *Schema) error {
		if s == nil {
			return nil
		}
		if s.Ref != "" {
			return visitDef(refName(s.Ref))
		}
		for _, group := range [][]*Schema{s.OneOf, s.AnyOf, s.AllOf} {
			for _, sub := range group {
				if err := visitSchema(sub); err != nil {
					return err
				}
			}
		}
		names := make([]string, 0, len(s.Properties))
		for propName := range s.Properties {
			names = append(names, propName)
		}
		sort.Strings(names)
		for _, propName := range names {
			if err := visitSchema(s.Properties[propName]); err != nil {
				return err
			}
		}
		if err := visitSchema(s.Items); err != nil {
			return err
		}
		if s.AddlProps != nil {
			if err := visitSchema(s.AddlProps.Schema); err != nil {
				return err
			}
		}
		return nil
	}

	root := func(name string) error { return visitDef(name) }

	for _, m := range clientMethods {
		variant, ok := c.clientRequest[m.Method]
		if !ok {
			return nil, fmt.Errorf("client method %q not present in ClientRequest union", m.Method)
		}
		if params, ok := variant.Properties["params"]; ok && params.Ref != "" {
			if err := root(refName(params.Ref)); err != nil {
				return nil, err
			}
		}
		if m.Response != "" {
			if err := root(m.Response); err != nil {
				return nil, err
			}
		}
	}
	for _, m := range serverRequestMethods {
		variant, ok := c.serverRequest[m.Method]
		if !ok {
			return nil, fmt.Errorf("server request %q not present in ServerRequest union", m.Method)
		}
		params, ok := variant.Properties["params"]
		if !ok || params.Ref == "" {
			return nil, fmt.Errorf("server request %q has no params $ref", m.Method)
		}
		if err := root(refName(params.Ref)); err != nil {
			return nil, err
		}
		if err := root(m.Response); err != nil {
			return nil, err
		}
	}
	for _, method := range serverNotifications {
		variant, ok := c.serverNotification[method]
		if !ok {
			return nil, fmt.Errorf("notification %q not present in ServerNotification union", method)
		}
		if params, ok := variant.Properties["params"]; ok && params.Ref != "" {
			if err := root(refName(params.Ref)); err != nil {
				return nil, err
			}
		}
	}
	return seen, nil
}
