package capabilities

import (
	"testing"

	"github.com/memohai/memoh/internal/reasoning"
)

func TestOpenRouterReadsOffAbilityAndDefaultState(t *testing.T) {
	t.Parallel()

	src, err := NewOpenRouterSource([]byte(`{"data":[
		{"id":"google/gemini-2.5-pro","reasoning":{"mandatory":true}},
		{"id":"google/gemini-2.5-flash","reasoning":{"mandatory":false}},
		{"id":"anthropic/claude-opus-4.6","reasoning":{"mandatory":false,"default_enabled":false}},
		{"id":"openai/o-next","reasoning":{"mandatory":false,"default_enabled":true}},
		{"id":"vendor/silent","reasoning":{}},
		{"id":"vendor/no-reasoning"}
	]}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	cases := []struct {
		name      string
		id        string
		wantKnown bool
		wantOff   string
		wantOn    any
	}{
		{"mandatory means it cannot be turned off", "google/gemini-2.5-pro", true, reasoning.OffSupportRejected, nil},
		{"not mandatory means it can", "google/gemini-2.5-flash", true, reasoning.OffSupportAccepted, nil},
		{"off by default is recorded separately from off-ability", "anthropic/claude-opus-4.6", true, reasoning.OffSupportAccepted, false},
		{"on by default is the case that needs an explicit disable", "openai/o-next", true, reasoning.OffSupportAccepted, true},
		{"an empty reasoning object states nothing", "vendor/silent", true, "", nil},
		{"a model without the object is unknown", "vendor/no-reasoning", false, "", nil},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			caps, ok := src.Resolve(tt.id)
			if ok != tt.wantKnown {
				t.Fatalf("known = %v, want %v", ok, tt.wantKnown)
			}
			if !ok {
				return
			}
			if caps.ReasoningOffSupport != tt.wantOff {
				t.Errorf("off support = %q, want %q", caps.ReasoningOffSupport, tt.wantOff)
			}
			switch want := tt.wantOn.(type) {
			case nil:
				if caps.ReasoningDefaultOn != nil {
					t.Errorf("default-on = %v, want unknown", *caps.ReasoningDefaultOn)
				}
			case bool:
				if caps.ReasoningDefaultOn == nil || *caps.ReasoningDefaultOn != want {
					t.Errorf("default-on = %v, want %v", caps.ReasoningDefaultOn, want)
				}
			}
		})
	}
}

// Routing suffixes name a delivery choice, not a different model's capabilities.
func TestOpenRouterIgnoresRoutingSuffixes(t *testing.T) {
	t.Parallel()

	src, err := NewOpenRouterSource([]byte(
		`{"data":[{"id":"deepseek/deepseek-v4:free","reasoning":{"mandatory":false}}]}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, id := range []string{"deepseek/deepseek-v4", "deepseek/deepseek-v4:free", "DeepSeek/DeepSeek-V4"} {
		if _, ok := src.Resolve(id); !ok {
			t.Errorf("%q should resolve to the same model", id)
		}
	}
}
