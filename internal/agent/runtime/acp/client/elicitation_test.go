package client

import (
	"reflect"
	"strings"
	"testing"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

func TestElicitationFormMappingRoundTrip(t *testing.T) {
	input, mapping, err := elicitationFormInput("Configure the workspace", map[string]any{
		"type":     "object",
		"required": []any{"features", "mode", "name"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "Workspace name"},
			"mode": map[string]any{"type": "string", "oneOf": []any{
				map[string]any{"const": "fast", "title": "Fast"}, map[string]any{"const": "safe", "title": "Safe"},
			}},
			"features": map[string]any{"type": "array", "items": map[string]any{
				"type": "string", "enum": []any{"search", "export"}, "enumNames": []any{"Search", "Export"},
			}},
			"color": map[string]any{"type": "string", "enum": []any{"red", "blue"}},
			"color_other": map[string]any{
				"type": "string", "description": "Enter another color",
				"_meta": map[string]any{"_askUserQuestionCustomAnswer": map[string]any{
					"isCustomAnswer": true, "questionId": "color",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("elicitationFormInput() error = %v", err)
	}
	payload, err := userinput.ParseAskUserPayload(input)
	if err != nil {
		t.Fatalf("ParseAskUserPayload() error = %v", err)
	}
	if len(payload.Questions) != 4 || payload.Questions[0].Required == nil || *payload.Questions[0].Required ||
		!payload.Questions[0].AllowCustom || payload.Questions[0].Placeholder != "Enter another color" ||
		payload.Questions[1].Kind != userinput.QuestionKindMultiSelect ||
		payload.Questions[2].Kind != userinput.QuestionKindSingleSelect ||
		payload.Questions[3].Kind != userinput.QuestionKindText || payload.Questions[3].Required == nil ||
		!*payload.Questions[3].Required {
		t.Fatalf("mapped questions = %#v", payload.Questions)
	}

	content, err := mapping.content(userinput.Request{Result: map[string]any{"answers": []any{
		map[string]any{"question_id": "q1", "question": "color", "custom_text": "violet"},
		map[string]any{"question_id": "q2", "question": "features", "selected": []any{
			map[string]any{"id": "q2.o1"}, map[string]any{"id": "q2.o2"},
		}},
		map[string]any{"question_id": "q3", "question": "mode", "selected": []any{map[string]any{"id": "q3.o2"}}},
		map[string]any{"question_id": "q4", "question": "name", "text": "Ada"},
	}}})
	if err != nil {
		t.Fatalf("mapping.content() error = %v", err)
	}
	want := map[string]any{
		"color_other": "violet", "features": []string{"search", "export"}, "mode": "safe", "name": "Ada",
	}
	if !reflect.DeepEqual(content, want) {
		t.Fatalf("content = %#v, want %#v", content, want)
	}
}

// Accepting a form promises the returned content honors every declared
// constraint, so shapes we cannot honor must be rejected at the protocol
// boundary — never after the user already submitted.
func TestElicitationFormInputRejectsUnhonorableSchemas(t *testing.T) {
	custom := func(target string) map[string]any {
		return map[string]any{
			"type": "string",
			"_meta": map[string]any{"_askUserQuestionCustomAnswer": map[string]any{
				"isCustomAnswer": true, "questionId": target,
			}},
		}
	}
	choice := map[string]any{"type": "string", "enum": []any{"red", "blue"}}
	cases := []struct {
		name    string
		schema  map[string]any
		wantErr string
	}{
		{
			name: "required target with custom companion",
			schema: map[string]any{
				"type": "object", "required": []any{"color"},
				"properties": map[string]any{"color": choice, "color_other": custom("color")},
			},
			wantErr: "cannot take a custom answer",
		},
		{
			name: "custom targeting custom",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"color": choice, "other": custom("color"), "extra": custom("other")},
			},
			wantErr: "cannot target custom-answer property",
		},
		{
			name: "self-targeting custom",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"color": choice, "other": custom("other")},
			},
			wantErr: "cannot target custom-answer property",
		},
		{
			name: "custom targeting unknown property",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"color": choice, "other": custom("missing")},
			},
			wantErr: "targets unknown property",
		},
		{
			name: "two customs for one target",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"color": choice, "a": custom("color"), "b": custom("color")},
			},
			wantErr: "more than one custom-answer field",
		},
		{
			name: "required custom property",
			schema: map[string]any{
				"type": "object", "required": []any{"other"},
				"properties": map[string]any{"color": choice, "other": custom("color")},
			},
			wantErr: "cannot be required",
		},
		{
			name: "custom companion on a text property",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}, "other": custom("name")},
			},
			wantErr: "cannot have a custom-answer companion",
		},
		{
			name: "password input",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"token": map[string]any{"type": "string", "format": "password"}},
			},
			wantErr: "secret input",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := elicitationFormInput("Configure", tc.schema)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("elicitationFormInput() error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}
