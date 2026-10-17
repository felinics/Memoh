package models

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"testing"

	"github.com/felinics/memoh/internal/reasoning"
	"github.com/felinics/memoh/internal/registry"
)

// legacyReasoningOptions is what version 1 offered for a stored config: the
// mode bridged from the id, the effort list as stored, and the Google guard
// for rows without a dialect.
func legacyReasoningOptions(v1 LegacyModelConfig, modelID, clientType string) reasoning.Options {
	mode := reasoning.ResolveMode(v1.ThinkingMode, slices.Contains(v1.Compatibilities, CompatReasoning), modelID)
	if clientType == string(ClientTypeGoogleGenerativeAI) && mode != reasoning.ModeAlways && mode != reasoning.ModeNone && v1.ReasoningDialect == "" {
		return reasoning.Options{Supported: true}
	}
	return reasoning.OptionsFor(mode, reasoning.NormalizeAdvertised(v1.ReasoningEfforts), clientType, v1.ReasoningOffSupport)
}

// Every shipped template must resolve after the upgrade exactly as it did
// before: same options for every protocol, same wire declarations, same
// capabilities. The upgrade runs on every stored row the first time it is
// read, so a difference here changes a live model.
func TestUpgradeKeepsShippedTemplatesResolvingTheSame(t *testing.T) {
	t.Parallel()

	defs, err := registry.Load(slog.Default(), "../../conf/providers")
	if err != nil {
		t.Fatal(err)
	}
	clientTypes := []string{
		string(ClientTypeOpenAICompletions), string(ClientTypeOpenAIResponses),
		string(ClientTypeAnthropicMessages), string(ClientTypeGoogleGenerativeAI),
	}
	checked := 0
	for _, def := range defs {
		for _, entry := range def.Models {
			if entry.Type != "" && entry.Type != string(ModelTypeChat) {
				continue
			}
			raw, err := json.Marshal(entry.Config)
			if err != nil {
				t.Fatal(err)
			}
			var v1 LegacyModelConfig
			if err := json.Unmarshal(raw, &v1); err != nil {
				t.Fatal(err)
			}
			v2, err := DecodeModelConfig(raw, ConfigContext{ModelID: entry.ModelID, Type: ModelTypeChat})
			if err != nil {
				t.Fatalf("%s/%s: %v", def.Name, entry.ModelID, err)
			}
			if v2.SchemaVersion != ModelConfigSchemaVersion {
				t.Fatalf("%s/%s: schema version %d", def.Name, entry.ModelID, v2.SchemaVersion)
			}
			model := Model{ModelID: entry.ModelID, Type: ModelTypeChat, Config: v2}
			for _, clientType := range append(clientTypes, def.ClientType) {
				want := legacyReasoningOptions(v1, entry.ModelID, clientType)
				if got := model.ReasoningOptions(clientType); !reflect.DeepEqual(got, want) {
					t.Errorf("%s/%s on %s: options %+v, want %+v", def.Name, entry.ModelID, clientType, got, want)
				}
			}
			if v1.ReasoningDialect != v2.ReasoningDialect() || v1.ReasoningOffSupport != v2.ReasoningOffSupport() && v2.Reasoning != nil && v2.ThinkingMode() != reasoning.ModeAlways {
				t.Errorf("%s/%s: dialect %q off %q, want %q %q", def.Name, entry.ModelID, v2.ReasoningDialect(), v2.ReasoningOffSupport(), v1.ReasoningDialect, v1.ReasoningOffSupport)
			}
			if !reflect.DeepEqual(v1.ThinkingBudgetMin, v2.ThinkingBudgetMin()) || !reflect.DeepEqual(v1.ThinkingBudgetMax, v2.ThinkingBudgetMax()) {
				t.Errorf("%s/%s: budget changed", def.Name, entry.ModelID)
			}
			for _, token := range []string{CompatVision, CompatFileInput, CompatToolCall, CompatImageOutput} {
				if slices.Contains(v1.Compatibilities, token) != v2.Supports(token) {
					t.Errorf("%s/%s: capability %s changed", def.Name, entry.ModelID, token)
				}
			}
			if !reflect.DeepEqual(v1.ContextWindow, v2.ContextWindow) || !reflect.DeepEqual(v1.MaxOutputTokens, v2.MaxOutputTokens) {
				t.Errorf("%s/%s: limits changed", def.Name, entry.ModelID)
			}
			checked++
		}
	}
	if checked < 1000 {
		t.Fatalf("checked %d chat templates, want the full catalog", checked)
	}
}

func TestDecodeModelConfigVersions(t *testing.T) {
	t.Parallel()

	t.Run("empty is an empty current config", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{"", "null", "{}"} {
			got, err := DecodeModelConfig([]byte(raw), ConfigContext{Type: ModelTypeEmbedding})
			if err != nil || got.SchemaVersion != ModelConfigSchemaVersion || got.Reasoning != nil || got.Modalities != nil {
				t.Fatalf("%q: %+v, %v", raw, got, err)
			}
		}
	})

	t.Run("missing version reads as version 1", func(t *testing.T) {
		t.Parallel()
		raw := `{"compatibilities":["vision","tool-call","reasoning"],"context_window":200000,"description":"kept"}`
		got, err := DecodeModelConfig([]byte(raw), ConfigContext{ModelID: "claude-opus-4-7", Type: ModelTypeChat})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Accepts(ModalityImage) || !got.ToolCall || got.Description == nil || *got.Description != "kept" {
			t.Fatalf("upgraded = %+v", got)
		}
		// An undeclared mode is read from the id, once, at upgrade time.
		if got.ThinkingMode() != reasoning.ModeAdaptive || got.Reasoning.Dialect != reasoning.DialectAdaptive {
			t.Fatalf("mode = %q, dialect = %q, want adaptive", got.ThinkingMode(), got.Reasoning.Dialect)
		}
	})

	t.Run("version 2 decodes as stored", func(t *testing.T) {
		t.Parallel()
		raw := `{"schema_version":2,"tool_call":true,"reasoning":{"efforts":["low","high"],"replay":{"field":"reasoning_details"}}}`
		got, err := DecodeModelConfig([]byte(raw), ConfigContext{Type: ModelTypeChat})
		if err != nil {
			t.Fatal(err)
		}
		if !got.ToolCall || got.Modalities != nil || got.ReasoningReplay() == nil || got.ReasoningReplay().Field != ReasoningReplayDetails {
			t.Fatalf("decoded = %+v", got)
		}
	})

	t.Run("a newer version keeps the fields this build knows", func(t *testing.T) {
		t.Parallel()
		got, err := DecodeModelConfig([]byte(`{"schema_version":9,"tool_call":true,"future":1}`), ConfigContext{Type: ModelTypeChat})
		if err != nil || !got.ToolCall {
			t.Fatalf("decoded = %+v, %v", got, err)
		}
	})

	t.Run("non-chat models keep only their own fields", func(t *testing.T) {
		t.Parallel()
		got, err := DecodeModelConfig([]byte(`{"dimensions":1536,"compatibilities":["vision"]}`), ConfigContext{Type: ModelTypeEmbedding})
		if err != nil || got.Dimensions == nil || *got.Dimensions != 1536 || got.Modalities != nil {
			t.Fatalf("decoded = %+v, %v", got, err)
		}
	})
}

func TestUpgradeReasoningShapes(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		v1      LegacyModelConfig
		want    *ModelReasoning
		mode    string
		efforts []string
	}{
		{
			name: "no reasoning",
			v1:   LegacyModelConfig{Compatibilities: []string{CompatToolCall}},
			mode: reasoning.ModeNone,
		},
		{
			name: "always reasons without a control",
			v1:   LegacyModelConfig{ThinkingMode: reasoning.ModeAlways, ReasoningOffSupport: reasoning.OffSupportRejected},
			want: &ModelReasoning{Off: reasoning.OffSupportRejected},
			mode: reasoning.ModeAlways,
		},
		{
			name:    "an empty tier list keeps the default tiers",
			v1:      LegacyModelConfig{ThinkingMode: reasoning.ModeToggle},
			want:    &ModelReasoning{Efforts: []string{"low", "medium", "high"}},
			mode:    reasoning.ModeToggle,
			efforts: []string{"low", "medium", "high"},
		},
		{
			name:    "disable becomes a toggle and leaves off support alone",
			v1:      LegacyModelConfig{ThinkingMode: reasoning.ModeToggle, ReasoningEfforts: []string{"disable", "low", "high"}, ReasoningDialect: reasoning.DialectTier},
			want:    &ModelReasoning{Toggle: true, Efforts: []string{"low", "high"}, Dialect: reasoning.DialectTier},
			mode:    reasoning.ModeToggle,
			efforts: []string{"low", "high", "disable"},
		},
		{
			name:    "a switch without tiers",
			v1:      LegacyModelConfig{ThinkingMode: reasoning.ModeToggle, ReasoningEfforts: []string{"enabled", "disable"}, ReasoningDialect: reasoning.DialectToggle, ReasoningOffSupport: reasoning.OffSupportAccepted},
			want:    &ModelReasoning{Toggle: true, Dialect: reasoning.DialectToggle, Off: reasoning.OffSupportAccepted},
			mode:    reasoning.ModeToggle,
			efforts: []string{"enabled", "disable"},
		},
		{
			name:    "adaptive becomes the adaptive dialect",
			v1:      LegacyModelConfig{ThinkingMode: reasoning.ModeAdaptive, ReasoningEfforts: []string{"low", "max"}, ReasoningOffSupport: reasoning.OffSupportAccepted, ReasoningDefaultOn: ptrBool(false)},
			want:    &ModelReasoning{Efforts: []string{"low", "max"}, Dialect: reasoning.DialectAdaptive, Off: reasoning.OffSupportAccepted, Default: &ReasoningDefault{Enabled: ptrBool(false)}},
			mode:    reasoning.ModeAdaptive,
			efforts: []string{"low", "max"},
		},
		{
			name:    "budget bounds move under budget",
			v1:      LegacyModelConfig{ThinkingMode: reasoning.ModeToggle, ReasoningEfforts: []string{"low", "high"}, ReasoningDialect: reasoning.DialectBudget, ThinkingBudgetMin: ptrInt(128), ThinkingBudgetMax: ptrInt(32768)},
			want:    &ModelReasoning{Efforts: []string{"low", "high"}, Dialect: reasoning.DialectBudget, Budget: &ReasoningBudget{Min: ptrInt(128), Max: ptrInt(32768)}},
			mode:    reasoning.ModeToggle,
			efforts: []string{"low", "high"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.v1.Upgrade(ConfigContext{ModelID: "m", Type: ModelTypeChat})
			if !reflect.DeepEqual(got.Reasoning, tt.want) {
				t.Fatalf("reasoning = %+v, want %+v", got.Reasoning, tt.want)
			}
			if got.ThinkingMode() != tt.mode {
				t.Fatalf("mode = %q, want %q", got.ThinkingMode(), tt.mode)
			}
			if !slices.Equal(got.ReasoningEfforts(), tt.efforts) {
				t.Fatalf("efforts = %v, want %v", got.ReasoningEfforts(), tt.efforts)
			}
		})
	}
}

// A config built in code without a version must still be written as the
// current one; otherwise the next read would run the version 1 upgrade over
// it and drop every field version 1 does not have.
func TestModelConfigAlwaysMarshalsTheCurrentVersion(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(ModelConfig{ToolCall: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeModelConfig(raw, ConfigContext{Type: ModelTypeChat})
	if err != nil || !got.ToolCall {
		t.Fatalf("round trip = %s -> %+v, %v", raw, got, err)
	}
}

func TestDecodeRequestModelConfig(t *testing.T) {
	t.Parallel()

	ctx := ConfigContext{ModelID: "m", Type: ModelTypeChat}
	got, err := DecodeRequestModelConfig([]byte(`{"tool_call":true,"reasoning":{"efforts":["low"]}}`), ctx)
	if err != nil || !got.ToolCall || got.Reasoning == nil {
		t.Fatalf("current shape without a version = %+v, %v", got, err)
	}
	got, err = DecodeRequestModelConfig([]byte(`{"compatibilities":["tool-call"],"thinking_mode":"toggle"}`), ctx)
	if err != nil || !got.ToolCall || got.ThinkingMode() != reasoning.ModeToggle {
		t.Fatalf("version 1 request = %+v, %v", got, err)
	}

	var req AddRequest
	if err := json.Unmarshal([]byte(`{"model_id":"claude-sonnet-4-6","type":"chat","config":{"compatibilities":["reasoning"]}}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.ModelID != "claude-sonnet-4-6" || req.Config.ThinkingMode() != reasoning.ModeAdaptive {
		t.Fatalf("request = %+v", req)
	}
}

// Merging in the version 1 vocabulary must not lose what version 1 cannot say.
func TestMergeLegacyConfigKeepsVersion2Fields(t *testing.T) {
	t.Parallel()

	existing := ModelConfig{
		Modalities:       &ModelModalities{Input: []Modality{ModalityText, ModalityAudio}, Output: []Modality{ModalityText}},
		StructuredOutput: true,
		Reasoning: &ModelReasoning{
			Efforts: []string{"low", "high"},
			Default: &ReasoningDefault{Effort: "high"},
			Replay:  &ReasoningReplay{Field: ReasoningReplayDetails, Required: true},
		},
	}
	merged := existing.Legacy()
	merged.Compatibilities = append(merged.Compatibilities, CompatVision)

	got := MergeLegacyConfig(existing, merged, ConfigContext{ModelID: "m", Type: ModelTypeChat})
	if !got.Accepts(ModalityImage) || !got.Accepts(ModalityAudio) || !got.StructuredOutput {
		t.Fatalf("modalities/structured output = %+v", got)
	}
	if got.Reasoning == nil || got.Reasoning.Replay == nil || got.Reasoning.Replay.Field != ReasoningReplayDetails ||
		got.Reasoning.Default == nil || got.Reasoning.Default.Effort != "high" {
		t.Fatalf("reasoning = %+v", got.Reasoning)
	}
}

func ptrBool(v bool) *bool { return &v }
func ptrInt(v int) *int    { return &v }

func TestInferUndeclaredDialectOnWrite(t *testing.T) {
	t.Parallel()

	claude := inferUndeclaredDialect(ModelConfig{Reasoning: &ModelReasoning{Efforts: []string{"low"}}}, "anthropic/claude-sonnet-4-6")
	if claude.Reasoning.Dialect != reasoning.DialectAdaptive {
		t.Fatalf("claude dialect = %q, want adaptive", claude.Reasoning.Dialect)
	}
	declared := inferUndeclaredDialect(ModelConfig{Reasoning: &ModelReasoning{Dialect: reasoning.DialectBudget}}, "claude-sonnet-4-6")
	if declared.Reasoning.Dialect != reasoning.DialectBudget {
		t.Fatalf("a declared dialect was overwritten: %q", declared.Reasoning.Dialect)
	}
	other := inferUndeclaredDialect(ModelConfig{Reasoning: &ModelReasoning{}}, "gpt-5.1")
	if other.Reasoning.Dialect != "" {
		t.Fatalf("non-Claude dialect = %q, want empty", other.Reasoning.Dialect)
	}
}

// The switch shapes the old model form could save, which no shipped template
// used: a switch declared by the disable token alone, and one declared by the
// enabled token alone. Each must keep its version 1 off answer and the effort
// version 1 put on the wire, on every protocol.
func TestUpgradeKeepsFormSwitchShapesResolvingTheSame(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		efforts    []string
		clientType string
		canDisable bool
		effort     string
	}{
		{name: "disable alone on OpenAI", efforts: []string{"disable"}, clientType: string(ClientTypeOpenAICompletions), canDisable: true, effort: reasoning.EffortMedium},
		{name: "disable alone on Anthropic", efforts: []string{"disable"}, clientType: string(ClientTypeAnthropicMessages), canDisable: true, effort: reasoning.EffortMedium},
		{name: "enabled alone on OpenAI", efforts: []string{"enabled"}, clientType: string(ClientTypeOpenAICompletions), canDisable: false, effort: reasoning.EffortEnabled},
		{name: "enabled alone on Anthropic", efforts: []string{"enabled"}, clientType: string(ClientTypeAnthropicMessages), canDisable: true, effort: reasoning.EffortEnabled},
		{name: "enabled and disable on OpenAI", efforts: []string{"enabled", "disable"}, clientType: string(ClientTypeOpenAICompletions), canDisable: true, effort: reasoning.EffortEnabled},
		{name: "nothing on OpenAI", clientType: string(ClientTypeOpenAICompletions), canDisable: false, effort: reasoning.EffortMedium},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v1 := LegacyModelConfig{ThinkingMode: reasoning.ModeToggle, ReasoningEfforts: tt.efforts}
			raw, err := json.Marshal(v1)
			if err != nil {
				t.Fatal(err)
			}
			v2, err := DecodeModelConfig(raw, ConfigContext{ModelID: "m", Type: ModelTypeChat, ClientType: tt.clientType})
			if err != nil {
				t.Fatal(err)
			}
			model := Model{ModelID: "m", Type: ModelTypeChat, Config: v2}
			got := model.ReasoningOptions(tt.clientType)
			if want := legacyReasoningOptions(v1, "m", tt.clientType); got.CanDisable != want.CanDisable || got.CanDisable != tt.canDisable {
				t.Fatalf("can disable = %v, want %v (version 1 said %v)", got.CanDisable, tt.canDisable, want.CanDisable)
			}
			rc := reasoning.ResolveConfig(v2.ThinkingMode(), v2.ReasoningEfforts(), got, "", "")
			if rc == nil || !rc.Active || rc.Effort != tt.effort {
				t.Fatalf("resolved = %+v, want active with effort %q", rc, tt.effort)
			}
		})
	}
}

// Without the provider's client type the upgrade cannot tell where a switch
// advertised by the enabled token alone could be turned off, so it leaves the
// off answer unstated rather than guessing a protocol.
func TestUpgradeLeavesEnabledOnlySwitchOffUnstatedWithoutClientType(t *testing.T) {
	t.Parallel()

	v1 := LegacyModelConfig{ThinkingMode: reasoning.ModeToggle, ReasoningEfforts: []string{"enabled"}}
	got := v1.Upgrade(ConfigContext{ModelID: "m", Type: ModelTypeChat})
	if want := (&ModelReasoning{Toggle: true}); !reflect.DeepEqual(got.Reasoning, want) {
		t.Fatalf("reasoning = %+v, want %+v", got.Reasoning, want)
	}
}

// Legacy reads "none" for a model without reasoning, as version 1 stored it.
// Discovery reports a mode for every model, and an empty mode here made every
// non-reasoning model look changed on each managed refresh.
func TestLegacyReadsNoneWithoutReasoning(t *testing.T) {
	t.Parallel()

	if got := (ModelConfig{ToolCall: true}).Legacy().ThinkingMode; got != reasoning.ModeNone {
		t.Fatalf("thinking mode = %q, want %q", got, reasoning.ModeNone)
	}
}

// Only a switch advertised by the enabled token alone needs the provider's
// client type to upgrade; the stored-row reader looks the provider up for
// those rows and no others.
func TestUpgradeNeedsClientTypeOnlyForEnabledOnlySwitches(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]bool{
		`{"thinking_mode":"toggle","reasoning_efforts":["enabled"]}`:           true,
		`{"thinking_mode":"toggle","reasoning_efforts":["enabled","disable"]}`: false,
		`{"thinking_mode":"toggle","reasoning_efforts":["disable"]}`:           false,
		`{"thinking_mode":"toggle","reasoning_efforts":["low","high"]}`:        false,
		`{"compatibilities":["tool-call"]}`:                                    false,
		`{"schema_version":2,"reasoning":{"toggle":true}}`:                     false,
		``: false,
	} {
		if got := upgradeNeedsClientType([]byte(raw)); got != want {
			t.Errorf("upgradeNeedsClientType(%s) = %v, want %v", raw, got, want)
		}
	}
}

// Legacy and Upgrade are inverses for every reasoning shape version 2 stores,
// so a refresh that merges in the version 1 vocabulary (fillExistingModel)
// writes back exactly what the catalog imported instead of adding tiers.
func TestLegacyUpgradeRoundTripKeepsReasoning(t *testing.T) {
	t.Parallel()

	zero, low, high := 0, 128, 24576
	for _, tt := range []struct {
		name      string
		reasoning *ModelReasoning
	}{
		{"budget switch (Gemini 2.5 Flash)", &ModelReasoning{Toggle: true, Budget: &ReasoningBudget{Min: &zero, Max: &high}, Off: reasoning.OffSupportAccepted, Dialect: reasoning.DialectBudget}},
		{"budget without a switch (Gemini 2.5 Pro)", &ModelReasoning{Budget: &ReasoningBudget{Min: &low, Max: &high}, Off: reasoning.OffSupportRejected, Dialect: reasoning.DialectBudget}},
		{"plain switch", &ModelReasoning{Toggle: true, Off: reasoning.OffSupportAccepted, Dialect: reasoning.DialectToggle}},
		{"switch that cannot turn off", &ModelReasoning{Toggle: true, Off: reasoning.OffSupportRejected}},
		{"tiers", &ModelReasoning{Efforts: []string{"low", "high"}, Dialect: reasoning.DialectTier}},
		{"tiers with a switch", &ModelReasoning{Toggle: true, Efforts: []string{"low", "medium", "high"}, Dialect: reasoning.DialectTier}},
		{"adaptive", &ModelReasoning{Efforts: []string{"low", "max"}, Dialect: reasoning.DialectAdaptive, Off: reasoning.OffSupportAccepted}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := ModelConfig{Reasoning: tt.reasoning}
			got := c.Legacy().Upgrade(ConfigContext{ModelID: "m", Type: ModelTypeChat, ClientType: string(ClientTypeOpenAICompletions)})
			if !reflect.DeepEqual(got.Reasoning, tt.reasoning) {
				t.Fatalf("round trip = %+v, want %+v", got.Reasoning, tt.reasoning)
			}
		})
	}
}
