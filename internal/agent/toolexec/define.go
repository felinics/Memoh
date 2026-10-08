package toolexec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"
)

// Define builds a tool whose arguments decode into T and whose parameter
// schema is inferred from T. Struct tags carry what the schema needs: the
// json tag names the property and marks it optional with omitempty, the
// jsonschema tag is its description, and a pointer field reads back as nil
// when the model omitted it. Constraints the tags cannot express — minimum,
// maximum, enum — are applied by the shape functions (Range, Enum, ...).
//
// The inferred schema is trimmed to the form Memoh's hand-written schemas
// used (see normalizeSchema), so a tool that moves onto a typed handler keeps
// the definition the model has been reading. internal/agent/tool pins that
// with a golden test per provider.
func Define[T any](name, description string, execute func(*ToolExecContext, T) (sdk.ToolOutput, error), shape ...func(*jsonschema.Schema)) Tool {
	return Tool{
		Name:        name,
		Description: description,
		Parameters:  SchemaFor[T](shape...),
		Execute:     Typed(execute),
	}
}

// SchemaFor infers the parameter schema of T (see Define) and applies the
// shape functions. It panics on a type the schema cannot express: the tools
// are built at startup, so that is a programming error, not a runtime one.
func SchemaFor[T any](shape ...func(*jsonschema.Schema)) *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("toolexec: cannot infer schema for %T: %v", *new(T), err))
	}
	normalizeSchema(schema)
	for _, fn := range shape {
		fn(schema)
	}
	return schema
}

// Typed adapts a handler over T to the executor contract: the arguments are
// decoded into T before the handler runs, and a document that does not decode
// is reported as the tool's error.
//
// Decoding keeps the tolerance the map-based helpers had: a whole-number
// float for an integer field, a numeric string for a numeric field and a
// number or boolean for a string field are accepted (coerceArguments). What
// still does not decode is reported by property name, never by Go type.
func Typed[T any](execute func(*ToolExecContext, T) (sdk.ToolOutput, error)) ToolExecuteFunc {
	return func(ctx *ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
		toolName := "tool"
		if ctx != nil && ctx.ToolName != "" {
			toolName = ctx.ToolName
		}
		typed, err := DecodeArguments[T](toolName, input)
		if err != nil {
			return sdk.ToolOutput{}, err
		}
		return execute(ctx, typed)
	}
}

// DecodeArguments is the decode Typed applies, for a handler that inspects
// the document before choosing a struct: the same case-variant rejection,
// lenient coercion, and property-named errors.
func DecodeArguments[T any](toolName string, input sdk.ToolArguments) (T, error) {
	var typed T
	if input.Valid() {
		if err := rejectCaseVariantKeys(input.JSON, reflect.TypeFor[T]()); err != nil {
			return typed, fmt.Errorf("invalid arguments for %s: %w", toolName, err)
		}
	}
	err := input.Unmarshal(&typed)
	if err != nil && input.Valid() {
		if coerced, changed := coerceArguments(input.JSON, reflect.TypeFor[T]()); changed {
			typed = *new(T)
			err = json.Unmarshal(coerced, &typed)
		}
	}
	if err != nil {
		return *new(T), describeDecodeError(toolName, err)
	}
	return typed, nil
}

// describeDecodeError turns a decode failure into text the model can act on:
// the property, what it must be and what arrived.
func describeDecodeError(toolName string, err error) error {
	var typeErr *json.UnmarshalTypeError
	// A type error that names its property, or that is the whole document
	// (encoding/json returned it unwrapped), reads as "<property> must be …".
	// One raised inside a custom UnmarshalJSON carries no property and is
	// wrapped by the decoder's own message, which already names it.
	if errors.As(err, &typeErr) && (typeErr.Field != "" || errors.Unwrap(err) == nil) {
		field := typeErr.Field
		if field == "" {
			field = "arguments"
		}
		return fmt.Errorf("invalid arguments for %s: %s must be %s, got %s", toolName, field, kindNoun(typeErr.Type), typeErr.Value)
	}
	if errors.Is(err, sdk.ErrInvalidToolArguments) {
		return fmt.Errorf("invalid arguments for %s: not a JSON object", toolName)
	}
	return fmt.Errorf("invalid arguments for %s: %w", toolName, err)
}

func kindNoun(t reflect.Type) string {
	if t == nil {
		return "a valid value"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.String:
		return "a string"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Struct, reflect.Map:
		return "an object"
	default:
		return "a valid value"
	}
}

var jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()

// rejectCaseVariantKeys refuses an object member that names a property only
// up to letter case ("Path" for "path"). encoding/json would accept it, but
// the approval policy and the hook payloads read the document by its exact
// keys, so such a member would let the tool act on an argument the policy
// never saw. Members that match nothing are ignored as before.
func rejectCaseVariantKeys(raw json.RawMessage, typ reflect.Type) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil // the decode below reports the syntax
	}
	return checkCaseVariantKeys(value, typ)
}

func checkCaseVariantKeys(value any, typ reflect.Type) error {
	if value == nil || typ == nil {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ != reflect.TypeFor[json.Number]() && reflect.PointerTo(typ).Implements(jsonUnmarshalerType) {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		fields := jsonFields(typ)
		for key := range obj {
			if _, exact := fields[key]; exact {
				continue
			}
			for _, name := range slices.Sorted(maps.Keys(fields)) {
				if strings.EqualFold(name, key) {
					return fmt.Errorf("unknown property %q (did you mean %q)", key, name)
				}
			}
		}
		for name, field := range fields {
			if v, present := obj[name]; present {
				if err := checkCaseVariantKeys(v, field); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		for _, item := range items {
			if err := checkCaseVariantKeys(item, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for _, v := range obj {
			if err := checkCaseVariantKeys(v, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonFields maps the JSON member names encoding/json reads for typ,
// embedded structs flattened, to the field types they decode into.
func jsonFields(typ reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous && field.Tag.Get("json") == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				for name, ft := range jsonFields(embedded) {
					if _, taken := out[name]; !taken {
						out[name] = ft
					}
				}
			}
			continue
		}
		if !field.IsExported() {
			continue
		}
		if name := jsonFieldName(field); name != "" {
			out[name] = field.Type
		}
	}
	return out
}

// coerceArguments rewrites the scalar values the map-based helpers used to
// accept so the typed decode accepts them too. It returns the rewritten
// document and whether anything changed. Fields with their own UnmarshalJSON
// are left alone; they define their own tolerance.
func coerceArguments(raw json.RawMessage, typ reflect.Type) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return raw, false
	}
	value, changed := coerceValue(value, typ)
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(value)
	if err != nil {
		return raw, false
	}
	return out, true
}

func coerceValue(value any, typ reflect.Type) (any, bool) {
	if value == nil || typ == nil {
		return value, false
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ != reflect.TypeFor[json.Number]() && reflect.PointerTo(typ).Implements(jsonUnmarshalerType) {
		return value, false
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := value.(map[string]any)
		if !ok {
			return value, false
		}
		changed := false
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Anonymous && field.Tag.Get("json") == "" {
				// encoding/json promotes an untagged embedded struct's fields
				// onto this object; coerce them against the same map.
				embedded := field.Type
				for embedded.Kind() == reflect.Pointer {
					embedded = embedded.Elem()
				}
				if embedded.Kind() == reflect.Struct {
					if _, c := coerceValue(obj, embedded); c {
						changed = true
					}
				}
				continue
			}
			if !field.IsExported() {
				continue
			}
			name := jsonFieldName(field)
			if name == "" {
				continue
			}
			if v, present := obj[name]; present {
				if coerced, c := coerceValue(v, field.Type); c {
					obj[name] = coerced
					changed = true
				}
			}
		}
		return obj, changed
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return value, false
		}
		changed := false
		for i := range items {
			if coerced, c := coerceValue(items[i], typ.Elem()); c {
				items[i] = coerced
				changed = true
			}
		}
		return items, changed
	case reflect.Map:
		obj, ok := value.(map[string]any)
		if !ok {
			return value, false
		}
		changed := false
		for k, v := range obj {
			if coerced, c := coerceValue(v, typ.Elem()); c {
				obj[k] = coerced
				changed = true
			}
		}
		return obj, changed
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return coerceInteger(value)
	case reflect.Float32, reflect.Float64:
		if s, ok := value.(string); ok {
			text := strings.TrimSpace(s)
			if !jsonNumberText(text) {
				return value, false
			}
			if f, err := strconv.ParseFloat(text, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
				return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), true
			}
		}
		return value, false
	case reflect.String:
		switch v := value.(type) {
		case json.Number:
			return v.String(), true
		case bool:
			return strconv.FormatBool(v), true
		}
		return value, false
	case reflect.Bool:
		if s, ok := value.(string); ok {
			if b, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
				return b, true
			}
		}
		return value, false
	}
	return value, false
}

// coerceInteger accepts a whole-number float ("2.0") and a numeric string
// ("2", " 2.0 ") for an integer field. An integer literal is taken exactly;
// only a fractional or exponent form goes through binary64, and then only
// while it is exact.
func coerceInteger(value any) (any, bool) {
	var text string
	switch v := value.(type) {
	case json.Number:
		text = v.String()
		if _, err := strconv.ParseInt(text, 10, 64); err == nil {
			return value, false
		}
	case string:
		text = strings.TrimSpace(v)
		if !jsonNumberText(text) {
			return value, false
		}
		if _, err := strconv.ParseInt(text, 10, 64); err == nil {
			return json.Number(text), true
		}
	default:
		return value, false
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) >= 1<<53 {
		return value, false
	}
	return json.Number(strconv.FormatInt(int64(f), 10)), true
}

// jsonNumberText reports whether text is a JSON number literal (optional
// leading minus, digits, optional fraction and exponent), which is the only
// string form coercion accepts for a numeric field.
func jsonNumberText(text string) bool {
	if text == "" {
		return false
	}
	var n json.Number
	return json.Unmarshal([]byte(text), &n) == nil
}

func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}
	return name
}

// normalizeSchema removes what jsonschema.For adds beyond the object /
// properties / required form: the additionalProperties:false every struct
// gets, the nullable type pair every pointer and slice gets, and the
// property order extension. An object always carries a properties member,
// even when empty. Nested schemas are normalized the same way.
func normalizeSchema(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if s.AdditionalProperties != nil && isFalseSchema(s.AdditionalProperties) {
		s.AdditionalProperties = nil
	}
	s.PropertyOrder = nil
	if len(s.Types) == 2 && s.Types[0] == "null" {
		s.Type = s.Types[1]
		s.Types = nil
	}
	if s.Type == "object" && s.Properties == nil {
		s.Properties = map[string]*jsonschema.Schema{}
	}
	for _, property := range s.Properties {
		normalizeSchema(property)
	}
	normalizeSchema(s.Items)
	normalizeSchema(s.AdditionalProperties)
}

// isFalseSchema recognises the schema jsonschema.For uses to disallow
// additional properties: an empty "not".
func isFalseSchema(s *jsonschema.Schema) bool {
	return s != nil && s.Not != nil && reflect.DeepEqual(*s.Not, jsonschema.Schema{})
}

// Property returns the schema of one top-level property, panicking when the
// struct has no such field: a constraint naming a missing property is a
// programming error, caught when the provider is built.
func Property(s *jsonschema.Schema, name string) *jsonschema.Schema {
	property, ok := s.Properties[name]
	if !ok {
		panic(fmt.Sprintf("toolexec: schema has no property %q", name))
	}
	return property
}

// Range bounds a numeric property with minimum and maximum.
func Range(name string, minimum, maximum float64) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) {
		p := Property(s, name)
		p.Minimum = &minimum
		p.Maximum = &maximum
	}
}

// Minimum bounds a numeric property from below.
func Minimum(name string, minimum float64) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) { Property(s, name).Minimum = &minimum }
}

// Maximum bounds a numeric property from above.
func Maximum(name string, maximum float64) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) { Property(s, name).Maximum = &maximum }
}

// Enum restricts a property to the listed values.
func Enum(name string, values ...any) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) { Property(s, name).Enum = values }
}

// Items applies shape functions to the item schema of an array property.
func Items(name string, shape ...func(*jsonschema.Schema)) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) {
		items := Property(s, name).Items
		if items == nil {
			panic(fmt.Sprintf("toolexec: property %q is not an array", name))
		}
		for _, fn := range shape {
			fn(items)
		}
	}
}

// Replace substitutes the inferred schema of one property with an explicit
// one, for a shape inference cannot produce (a nullable union, a custom
// decoder type).
func Replace(name string, schema *jsonschema.Schema) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) {
		Property(s, name)
		s.Properties[name] = schema
	}
}

// Describe sets a property's description at build time, for text that
// depends on the session rather than on the struct.
func Describe(name, description string) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) { Property(s, name).Description = description }
}

// Require replaces the required list, for tools whose required set depends
// on the session.
func Require(names ...string) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) { s.Required = names }
}

// Default records a property's default for the model to read; the handler
// still applies it when the property is omitted.
func Default(name string, value any) func(*jsonschema.Schema) {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("toolexec: default for %q: %v", name, err))
	}
	return func(s *jsonschema.Schema) { Property(s, name).Default = raw }
}

// Strict disallows additional properties on the schema it is applied to.
func Strict() func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) { s.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}} }
}

// ArrayBounds bounds the length of an array property.
func ArrayBounds(name string, minItems, maxItems int) func(*jsonschema.Schema) {
	return func(s *jsonschema.Schema) {
		p := Property(s, name)
		p.MinItems = &minItems
		p.MaxItems = &maxItems
	}
}

// EnumStrings is Enum for a list of strings built at runtime.
func EnumStrings(name string, values []string) func(*jsonschema.Schema) {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return Enum(name, out...)
}
