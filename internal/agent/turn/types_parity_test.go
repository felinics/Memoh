package turn_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/attachment"
)

func TestContractTypesAreOwnedByTurn(t *testing.T) {
	const wantPackage = "github.com/felinics/memoh/internal/agent/turn"
	values := []any{
		turn.Attachment{},
		turn.SkillActivation{},
		turn.SkillActivationSkill{},
		turn.RequestedSkillContext{},
		turn.OutboundAssetRef{},
		turn.InjectMessage{},
		turn.ModelMessage{},
		turn.AssistantOutput{},
		turn.ContentPart{},
		turn.ToolCall{},
		turn.ToolCallFunction{},
		turn.QuestionAnswer{},
	}
	for _, value := range values {
		typ := reflect.TypeOf(value)
		if got := typ.PkgPath(); got != wantPackage {
			t.Errorf("%s is owned by %q, want %q", typ.Name(), got, wantPackage)
		}
	}
}

func TestContractWireJSONShapes(t *testing.T) {
	metadata := map[string]any{"nested": map[string]any{"ok": true}, "rank": float64(2)}
	content := json.RawMessage(
		`[{"type":"text","text":"hello","styles":["bold"],"metadata":{"source":"test"}}]`,
	)

	tests := []struct {
		name    string
		value   any
		want    string
		wantErr string
	}{
		{
			name: "attachment",
			value: turn.Attachment{
				Type: "image", Base64: "data:image/png;base64,AA==", Path: "/tmp/a.png",
				URL: "https://example.test/a.png", PlatformKey: "pk", ContentHash: "hash",
				Name: "a.png", Mime: "image/png", Size: 42, Metadata: metadata,
			},
			want: `{"type":"image","base64":"data:image/png;base64,AA==","path":"/tmp/a.png","url":"https://example.test/a.png","platform_key":"pk","content_hash":"hash","name":"a.png","mime":"image/png","size":42,"metadata":{"nested":{"ok":true},"rank":2}}`,
		},
		{
			name: "skill activation",
			value: turn.SkillActivation{
				Skills: []turn.SkillActivationSkill{{
					Name: "skill", DisplayName: "Skill", Description: "desc",
					SourceKind: "registry", State: "effective",
				}},
				Prompt: "prompt",
			},
			want: `{"skills":[{"name":"skill","display_name":"Skill","description":"desc","source_kind":"registry","state":"effective"}],"prompt":"prompt"}`,
		},
		{
			name: "skill activation skill",
			value: turn.SkillActivationSkill{
				Name: "skill", DisplayName: "Skill", Description: "desc",
				SourceKind: "registry", State: "effective",
			},
			want: `{"name":"skill","display_name":"Skill","description":"desc","source_kind":"registry","state":"effective"}`,
		},
		{
			name: "requested skill context remains internal",
			value: turn.RequestedSkillContext{
				Name: "skill", Description: "desc", Content: "body", SourceKind: "registry",
				OpaqueSourceID: "opaque", ContentHash: "hash", Identity: "identity",
			},
			want: `{}`,
		},
		{
			name: "outbound asset",
			value: turn.OutboundAssetRef{
				ContentHash: "hash", Role: "attachment", Ordinal: 3, Mime: "image/png",
				SizeBytes: 42, StorageKey: "key", Name: "a.png", Metadata: metadata,
			},
			want: `{"ContentHash":"hash","Role":"attachment","Ordinal":3,"Mime":"image/png","SizeBytes":42,"StorageKey":"key","Name":"a.png","Metadata":{"nested":{"ok":true},"rank":2}}`,
		},
		{
			name: "inject message remains internal",
			value: turn.InjectMessage{
				Text: "text", HeaderifiedText: "header",
				Attachments: []turn.Attachment{{Type: "file", ContentHash: "hash"}},
			},
			wantErr: "json: unsupported type: func()",
		},
		{
			name: "model message",
			value: turn.ModelMessage{
				Role: "assistant", Content: content, Usage: json.RawMessage(`{"ignored":true}`),
				ToolCalls: []turn.ToolCall{{
					ID: "call", Type: "function",
					Function: turn.ToolCallFunction{Name: "read", Arguments: `{"path":"a"}`},
				}},
				ToolCallID: "result", Name: "tool",
			},
			want: `{"role":"assistant","content":[{"type":"text","text":"hello","styles":["bold"],"metadata":{"source":"test"}}],"tool_calls":[{"id":"call","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}],"tool_call_id":"result","name":"tool"}`,
		},
		{
			name:  "assistant output",
			value: turn.AssistantOutput{Content: "hello", Parts: []turn.ContentPart{{Type: "text", Text: "hello"}}},
			want:  `{"Content":"hello","Parts":[{"type":"text","text":"hello"}]}`,
		},
		{
			name: "content part",
			value: turn.ContentPart{
				Type: "text", Text: "hello", URL: "https://example.test", Styles: []string{"bold"},
				Language: "en", ChannelIdentityID: "ci", Emoji: "ok", Metadata: metadata,
			},
			want: `{"type":"text","text":"hello","url":"https://example.test","styles":["bold"],"language":"en","channel_identity_id":"ci","emoji":"ok","metadata":{"nested":{"ok":true},"rank":2}}`,
		},
		{
			name: "tool call",
			value: turn.ToolCall{
				ID: "call", Type: "function",
				Function: turn.ToolCallFunction{Name: "read", Arguments: `{"path":"a"}`},
			},
			want: `{"id":"call","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}`,
		},
		{
			name:  "tool call function",
			value: turn.ToolCallFunction{Name: "read", Arguments: `{"path":"a"}`},
			want:  `{"name":"read","arguments":"{\"path\":\"a\"}"}`,
		},
		{
			name: "question answer",
			value: turn.QuestionAnswer{
				QuestionID: "q", OptionIDs: []string{"a", "b"}, CustomText: "custom",
				Text: "text", Skipped: true,
			},
			want: `{"question_id":"q","option_ids":["a","b"],"custom_text":"custom","text":"text","skipped":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.value)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("json.Marshal() error = nil, want %q", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("json.Marshal() error = %q, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("json.Marshal() =\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestContractWireJSONOmitsOptionalFields(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "attachment", value: turn.Attachment{Type: "file"}, want: `{"type":"file"}`},
		{
			name:  "skill activation",
			value: turn.SkillActivation{},
			want:  `{}`,
		},
		{
			name:  "skill activation skill",
			value: turn.SkillActivationSkill{Name: "skill"},
			want:  `{"name":"skill"}`,
		},
		{name: "model message", value: turn.ModelMessage{Role: "user"}, want: `{"role":"user"}`},
		{name: "content part", value: turn.ContentPart{Type: "text"}, want: `{"type":"text"}`},
		{
			name:  "tool call",
			value: turn.ToolCall{Type: "function"},
			want:  `{"type":"function","function":{"name":"","arguments":""}}`,
		},
		{
			name:  "question answer",
			value: turn.QuestionAnswer{QuestionID: "q"},
			want:  `{"question_id":"q"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("json.Marshal() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestModelMessageHelpers(t *testing.T) {
	tests := []struct {
		name      string
		content   json.RawMessage
		wantText  string
		wantParts []turn.ContentPart
		wantHas   bool
	}{
		{name: "empty"},
		{
			name:     "plain text",
			content:  turn.NewTextContent("  plain text  "),
			wantText: "  plain text  ",
			wantHas:  true,
		},
		{
			name:     "multipart text excludes reasoning",
			content:  json.RawMessage(`[{"type":"reasoning","text":"secret"},{"type":"text","text":" first "},{"type":"text","text":"second"}]`),
			wantText: " first \nsecond",
			wantParts: []turn.ContentPart{
				{Type: "reasoning", Text: "secret"},
				{Type: "text", Text: " first "},
				{Type: "text", Text: "second"},
			},
			wantHas: true,
		},
		{
			name:      "non-text multipart still counts as content",
			content:   json.RawMessage(`[{"type":"image","url":"https://example.test/a.png"}]`),
			wantParts: []turn.ContentPart{{Type: "image", URL: "https://example.test/a.png"}},
			wantHas:   true,
		},
		{
			name:    "invalid multipart shape",
			content: json.RawMessage(`{"not":"valid content"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := turn.ModelMessage{Content: tt.content}
			if got := message.TextContent(); got != tt.wantText {
				t.Errorf("TextContent() = %q, want %q", got, tt.wantText)
			}
			if got := message.ContentParts(); !reflect.DeepEqual(got, tt.wantParts) {
				t.Errorf("ContentParts() = %#v, want %#v", got, tt.wantParts)
			}
			if got := message.HasContent(); got != tt.wantHas {
				t.Errorf("HasContent() = %v, want %v", got, tt.wantHas)
			}
		})
	}

	toolOnly := turn.ModelMessage{ToolCalls: []turn.ToolCall{{Type: "function"}}}
	if !toolOnly.HasContent() {
		t.Error("tool-call-only message must have content")
	}
	if got, want := string(turn.NewTextContent("quoted\ntext")), `"quoted\ntext"`; got != want {
		t.Errorf("NewTextContent() = %s, want %s", got, want)
	}
}

func TestContentPartHasValue(t *testing.T) {
	tests := []struct {
		name string
		part turn.ContentPart
		want bool
	}{
		{name: "empty", part: turn.ContentPart{Type: "text"}},
		{name: "whitespace only", part: turn.ContentPart{Text: " ", URL: "\t", Emoji: "\n"}},
		{name: "text", part: turn.ContentPart{Text: "hello"}, want: true},
		{name: "url", part: turn.ContentPart{URL: "https://example.test"}, want: true},
		{name: "emoji", part: turn.ContentPart{Emoji: "ok"}, want: true},
		{
			name: "metadata alone",
			part: turn.ContentPart{Metadata: map[string]any{"source": "test"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.part.HasValue(); got != tt.want {
				t.Errorf("HasValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSkillActivationFiltersAndDeduplicatesSkills(t *testing.T) {
	items := []turn.RequestedSkillContext{
		{Name: " skill-a ", Description: " desc ", SourceKind: " registry ", Identity: "same"},
		{Name: "duplicate", SourceKind: "registry", Identity: " same "},
		{Name: " skill-b ", SourceKind: "builtin"},
		{Name: "skill-b", SourceKind: " builtin "},
		{Name: "  "},
	}

	got := turn.NewSkillActivation(items, "  ")
	want := &turn.SkillActivation{
		Skills: []turn.SkillActivationSkill{
			{
				Name: "skill-a", DisplayName: "skill-a", Description: "desc",
				SourceKind: "registry", State: "effective",
			},
			{
				Name: "skill-b", DisplayName: "skill-b",
				SourceKind: "builtin", State: "effective",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NewSkillActivation() = %#v, want %#v", got, want)
	}

	const wantJSON = `{"skills":[{"name":"skill-a","display_name":"skill-a","description":"desc","source_kind":"registry","state":"effective"},{"name":"skill-b","display_name":"skill-b","source_kind":"builtin","state":"effective"}]}`
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != wantJSON {
		t.Fatalf("activation JSON = %s, want %s", gotJSON, wantJSON)
	}

	const wantQuery = "The user activated the following skill for this turn without an additional prompt: skill-a, skill-b."
	if query := turn.SkillActivationModelQuery(got); query != wantQuery {
		t.Errorf("SkillActivationModelQuery() = %q, want %q", query, wantQuery)
	}
}

func TestSkillActivationModelQuery(t *testing.T) {
	if activation := turn.NewSkillActivation(nil, " user prompt "); !reflect.DeepEqual(
		activation,
		&turn.SkillActivation{Prompt: "user prompt"},
	) {
		t.Fatalf("prompt-only activation = %#v", activation)
	}
	if got := turn.SkillActivationModelQuery(&turn.SkillActivation{
		Skills: []turn.SkillActivationSkill{{Name: "ignored"}},
		Prompt: " answer ",
	}); got != "answer" {
		t.Errorf("prompt query = %q, want %q", got, "answer")
	}

	const wantFallback = "The user activated the following skill for this turn without an additional prompt: skill-a, Fancy."
	if got := turn.SkillActivationModelQuery(&turn.SkillActivation{
		Skills: []turn.SkillActivationSkill{
			{Name: " skill-a "},
			{Name: "ignored", DisplayName: " Fancy "},
			{Name: " "},
		},
	}); got != wantFallback {
		t.Errorf("fallback query = %q, want %q", got, wantFallback)
	}

	if turn.NewSkillActivation(nil, " ") != nil {
		t.Error("empty activation must be nil")
	}
	if got := turn.SkillActivationModelQuery(nil); got != "" {
		t.Errorf("nil activation query = %q, want empty", got)
	}
	if got := turn.SkillActivationModelQuery(&turn.SkillActivation{}); got != "" {
		t.Errorf("empty activation query = %q, want empty", got)
	}
}

func TestAttachmentBundleConversions(t *testing.T) {
	raw := attachment.Bundle{
		Type: " IMAGE ", Base64: " raw ", Path: " path ", URL: " url ",
		PlatformKey: " platform ", ContentHash: " hash ", Name: " name ",
		Mime: " IMAGE/PNG ", Size: 42, Metadata: map[string]any{"key": "value"},
	}
	gotAttachment := turn.AttachmentFromBundle(raw)
	wantAttachment := turn.Attachment{
		Type: " IMAGE ", Base64: " raw ", Path: " path ", URL: " url ",
		PlatformKey: " platform ", ContentHash: " hash ", Name: " name ",
		Mime: " IMAGE/PNG ", Size: 42, Metadata: map[string]any{"key": "value"},
	}
	if !reflect.DeepEqual(gotAttachment, wantAttachment) {
		t.Fatalf("AttachmentFromBundle() = %#v, want %#v", gotAttachment, wantAttachment)
	}

	if got, want := gotAttachment.Bundle(), raw.Normalize(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Attachment.Bundle() = %#v, want %#v", got, want)
	}

	normalized := attachment.Bundle{
		Type:        "image",
		URL:         "https://example.test/a.png",
		ContentHash: "hash",
		Name:        "a.png",
		Mime:        "image/png",
		Metadata:    map[string]any{"source": "test"},
	}.Normalize()
	if got := turn.AttachmentFromBundle(normalized).Bundle(); !reflect.DeepEqual(got, normalized) {
		t.Fatalf("normalized bundle round trip = %#v, want %#v", got, normalized)
	}
}
