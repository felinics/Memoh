// Package protocolgen generates the Go types for the codex app-server v2
// protocol from the vendored JSON Schema snapshot under schema/.
//
// The snapshot is produced by the pinned codex CLI (`codex app-server
// generate-json-schema`, non-experimental). Generic JSON-Schema code
// generators cannot represent the serde union encodings this protocol uses,
// so this package implements a small purpose-built generator that handles
// exactly the schema shapes present in the snapshot and fails loudly on
// anything else.
package protocolgen

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

//go:embed schema/*.json
var schemaFS embed.FS

// Schema models the subset of JSON Schema draft-07 that the codex snapshot
// actually uses. Loading fails if a document contains anything outside it.
// The boolean schema form (`true` = anything) is represented by Any.
type Schema struct {
	// Any marks the boolean schema `true`, which accepts any value.
	Any bool `json:"-"`

	Ref         string             `json:"$ref"`
	SchemaTag   string             `json:"$schema"`
	Type        typeList           `json:"type"`
	Properties  map[string]*Schema `json:"properties"`
	Required    []string           `json:"required"`
	Items       *Schema            `json:"items"`
	AddlProps   *addlProps         `json:"additionalProperties"`
	Enum        []json.RawMessage  `json:"enum"`
	OneOf       []*Schema          `json:"oneOf"`
	AnyOf       []*Schema          `json:"anyOf"`
	AllOf       []*Schema          `json:"allOf"`
	Format      string             `json:"format"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Definitions map[string]*Schema `json:"definitions"`
	Default     json.RawMessage    `json:"default"`
	Minimum     json.RawMessage    `json:"minimum"`
	MinLength   json.RawMessage    `json:"minLength"`
}

func (s *Schema) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if bytes.Equal(trimmed, []byte("true")) {
		*s = Schema{Any: true}
		return nil
	}
	if bytes.Equal(trimmed, []byte("false")) {
		return errors.New("boolean schema `false` is not supported")
	}
	type plain Schema
	var p plain
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return err
	}
	*s = Schema(p)
	return nil
}

// typeList accepts both `"type": "string"` and `"type": ["string", "null"]`.
type typeList []string

func (t *typeList) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = typeList{s}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	*t = list
	return nil
}

// addlProps accepts boolean or schema forms of additionalProperties.
type addlProps struct {
	Bool   *bool
	Schema *Schema
}

func (a *addlProps) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false")) {
		v := trimmed[0] == 't'
		a.Bool = &v
		return nil
	}
	a.Schema = new(Schema)
	return json.Unmarshal(b, a.Schema)
}

// nonNullTypes returns the schema's type list with "null" removed and reports
// whether "null" was present.
func (s *Schema) nonNullTypes() (types []string, nullable bool) {
	for _, t := range s.Type {
		if t == "null" {
			nullable = true
			continue
		}
		types = append(types, t)
	}
	return types, nullable
}

// singleAllOfRef unwraps the `allOf: [{$ref}]` + description pattern, the only
// allOf form present in the snapshot.
func (s *Schema) singleAllOfRef() (string, bool) {
	if len(s.AllOf) == 1 && s.AllOf[0].Ref != "" {
		return refName(s.AllOf[0].Ref), true
	}
	return "", false
}

func refName(ref string) string {
	const prefix = "#/definitions/"
	if !strings.HasPrefix(ref, prefix) {
		panic(fmt.Sprintf("protocolgen: unsupported $ref %q", ref))
	}
	return strings.TrimPrefix(ref, prefix)
}

// enumStrings decodes an enum whose values must all be strings.
func (s *Schema) enumStrings() ([]string, error) {
	out := make([]string, 0, len(s.Enum))
	for _, raw := range s.Enum {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("non-string enum value %s", raw)
		}
		out = append(out, v)
	}
	return out, nil
}

// corpus is the merged definition table plus the top-level union documents.
type corpus struct {
	defs map[string]*Schema
	// clientRequest, serverRequest, serverNotification map method name to the
	// oneOf variant schema describing that method's envelope.
	clientRequest      map[string]*Schema
	serverRequest      map[string]*Schema
	serverNotification map[string]*Schema
}

// standalone response documents vendored as separate files: file name (minus
// .json) is the type name.
var standaloneDocs = []string{
	"CommandExecutionRequestApprovalResponse",
	"FileChangeRequestApprovalResponse",
	"PermissionsRequestApprovalResponse",
	"ToolRequestUserInputResponse",
	"McpServerElicitationRequestResponse",
	"ChatgptAuthTokensRefreshResponse",
}

func loadCorpus() (*corpus, error) {
	c := &corpus{defs: map[string]*Schema{}}

	bundle, err := loadDoc("schema/codex_app_server_protocol.v2.schemas.json")
	if err != nil {
		return nil, err
	}
	if err := c.mergeDefs(bundle.Definitions, "v2 bundle"); err != nil {
		return nil, err
	}

	serverReq, err := loadDoc("schema/ServerRequest.json")
	if err != nil {
		return nil, err
	}
	if err := c.mergeDefs(serverReq.Definitions, "ServerRequest.json"); err != nil {
		return nil, err
	}

	for _, name := range standaloneDocs {
		doc, err := loadDoc("schema/" + name + ".json")
		if err != nil {
			return nil, err
		}
		if err := c.mergeDefs(doc.Definitions, name+".json"); err != nil {
			return nil, err
		}
		// The document root itself is the named type.
		root := *doc
		root.Definitions = nil
		if err := c.mergeDefs(map[string]*Schema{name: &root}, name+".json root"); err != nil {
			return nil, err
		}
	}

	c.clientRequest, err = methodVariants(c.defs["ClientRequest"], "ClientRequest")
	if err != nil {
		return nil, err
	}
	c.serverNotification, err = methodVariants(c.defs["ServerNotification"], "ServerNotification")
	if err != nil {
		return nil, err
	}
	serverReqRoot := *serverReq
	serverReqRoot.Definitions = nil
	c.serverRequest, err = methodVariants(&serverReqRoot, "ServerRequest")
	if err != nil {
		return nil, err
	}
	return c, nil
}

func loadDoc(path string) (*Schema, error) {
	raw, err := schemaFS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Schema
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// mergeDefs adds definitions, requiring byte-identical shapes on collision so
// the bundle and standalone documents cannot silently disagree.
func (c *corpus) mergeDefs(defs map[string]*Schema, source string) error {
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		def := defs[name]
		existing, ok := c.defs[name]
		if !ok {
			c.defs[name] = def
			continue
		}
		a, _ := json.Marshal(existing)
		b, _ := json.Marshal(def)
		if !bytes.Equal(a, b) {
			return fmt.Errorf("definition %q from %s conflicts with an earlier copy", name, source)
		}
	}
	return nil
}

// methodVariants indexes a method-envelope union (oneOf of objects carrying a
// const `method` property) by method name.
func methodVariants(s *Schema, unionName string) (map[string]*Schema, error) {
	if s == nil || len(s.OneOf) == 0 {
		return nil, fmt.Errorf("%s: missing or not a oneOf union", unionName)
	}
	out := make(map[string]*Schema, len(s.OneOf))
	for _, variant := range s.OneOf {
		methodSchema, ok := variant.Properties["method"]
		if !ok || len(methodSchema.Enum) != 1 {
			return nil, fmt.Errorf("%s: variant without singleton method enum", unionName)
		}
		methods, err := methodSchema.enumStrings()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", unionName, err)
		}
		out[methods[0]] = variant
	}
	return out, nil
}
