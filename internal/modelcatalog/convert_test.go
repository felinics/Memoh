package modelcatalog

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/felinics/memoh/internal/models"
)

func TestFormerBrowserCatalogFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/models-dev.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("testdata/converted.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]struct {
		Protocol string  `json:"protocol"`
		Models   []Model `json:"models"`
	}
	if err = json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	for source, want := range expected {
		t.Run(source, func(t *testing.T) {
			got, err := convert(data, source, want.Protocol)
			if err != nil {
				t.Fatal(err)
			}
			index := func(items []Model) map[string]Model {
				result := map[string]Model{}
				for _, item := range items {
					result[item.ModelID] = item
				}
				return result
			}
			if !reflect.DeepEqual(index(got), index(want.Models)) {
				t.Fatalf("server conversion changed former fixture\n got: %#v\nwant: %#v", index(got), index(want.Models))
			}
		})
	}
}

func TestDecodeRejectsInvalidEnvelopeAndCloudRelayShape(t *testing.T) {
	for _, raw := range []string{"{}", "null", `{"error":{"message":"upstream failed"}}`, `{"p":{"models":null}}`, `{"data":[{"id":"m","tool_call":true}]}`} {
		if _, err := decode([]byte(raw)); err == nil {
			t.Errorf("accepted invalid envelope: %s", raw)
		}
	}
}

func TestProviderSchemaFailuresAreIsolated(t *testing.T) {
	data, err := decode([]byte(`{
  "healthy":{"models":{"m":{"id":"m","tool_call":true}}},
  "broken":{"models":{"bad":{"id":"bad","tool_call":"invalid"}}},
  "future":{"models":{"m":{"id":"m","reasoning":true,"reasoning_options":[{"type":"effort","values":["ultra","low"]}]}}}
 }`))
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := convert(data, "healthy", "openai-completions")
	if err != nil || len(healthy) != 1 || !healthy[0].Config.ToolCall {
		t.Fatalf("unrelated provider blocked healthy models: %v, %v", healthy, err)
	}
	if _, err := convert(data, "broken", "openai-completions"); err == nil {
		t.Fatal("the selected broken source must fail before any import")
	}
	if _, err := convert(data, "future", "openai-completions"); err == nil {
		t.Fatal("unsupported controls must not reach a model config")
	}
}

func TestEmptyCatalogIsNotEndpointDiscovery(t *testing.T) {
	data, err := decode([]byte(`{"p":{"models":{"embed":{"id":"embed","type":"embedding"},"gone":{"id":"gone","status":"deprecated"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := convert(data, "p", "openai-completions")
	if err != nil || entries == nil || len(entries) != 0 {
		t.Fatalf("empty catalog = %#v, %v", entries, err)
	}
}

func TestReasoningBoundsAndUnsupportedGoogleSwitch(t *testing.T) {
	data, err := decode([]byte(`{"p":{"models":{
 "gemma":{"id":"gemma","reasoning":true,"reasoning_options":[{"type":"toggle"}]},
 "dynamic":{"id":"dynamic","reasoning":true,"reasoning_options":[{"type":"budget_tokens","min":-1,"max":32768}]},
 "nulls":{"id":"nulls","reasoning":true,"reasoning_options":[{"type":"effort","values":[null,"","low","high"]}]}
 }}}`))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := convert(data, "p", "google-generative-ai")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range entries {
		r := m.Config.Reasoning
		switch m.ModelID {
		case "gemma":
			if r.Toggle || r.Dialect != "" || r.Off != "rejected" {
				t.Fatalf("unsupported switch: %+v", r)
			}
		case "dynamic":
			if r.Budget.Min != nil || *r.Budget.Max != 32768 {
				t.Fatal(r)
			}
		case "nulls":
			if !reflect.DeepEqual(r.Efforts, []string{"low", "high"}) {
				t.Fatal(r)
			}
		}
		if m.Type != models.ModelTypeChat {
			t.Fatal(m.Type)
		}
	}
}
