package command

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/i18n"
)

func TestItemActionTypeable(t *testing.T) {
	tests := []struct {
		name string
		a    *ItemAction
		want string
	}{
		{"nil receiver", nil, ""},
		{"empty resource", &ItemAction{Resource: "", Action: "list"}, ""},
		{"empty action", &ItemAction{Resource: "memory", Action: ""}, ""},
		{"resource and action only", &ItemAction{Resource: "memory", Action: "list"}, "/memory list"},
		{"single arg", &ItemAction{Resource: "memory", Action: "set", Args: []string{"alice"}}, "/memory set alice"},
		{"multiple args", &ItemAction{Resource: "model", Action: "set", Args: []string{"openai", "gpt-4o"}}, "/model set openai gpt-4o"},
		{"flag args", &ItemAction{Resource: "settings", Action: "update", Args: []string{"--heartbeat_enabled", "true"}}, "/settings update --heartbeat_enabled true"},
		{"empty arg skipped", &ItemAction{Resource: "memory", Action: "set", Args: []string{"alice", "", "  "}}, "/memory set alice"},
		{"whitespace trimmed", &ItemAction{Resource: " memory ", Action: " list "}, "/memory list"},
		{"arg with internal space gets quoted", &ItemAction{Resource: "memory", Action: "set", Args: []string{"daily report"}}, `/memory set "daily report"`},
		{"arg with tab gets quoted", &ItemAction{Resource: "schedule", Action: "get", Args: []string{"my\ttask"}}, `/schedule get "my	task"`},
		{"arg with newline gets quoted + newline replaced", &ItemAction{Resource: "x", Action: "y", Args: []string{"a\nb"}}, `/x y "a b"`},
		{"arg with double quote becomes single quote inside wrapper", &ItemAction{Resource: "x", Action: "y", Args: []string{`he said "hi"`}}, `/x y "he said 'hi'"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Typeable(); got != tc.want {
				t.Errorf("Typeable() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTypeableRoundTripsThroughParse pins the contract: any string produced
// by Typeable() must re-parse back to the same (Resource, Action, Args)
// triple. This is the property that makes copy-paste hints actually work.
func TestTypeableRoundTripsThroughParse(t *testing.T) {
	cases := []*ItemAction{
		{Resource: "memory", Action: "set", Args: []string{"alice"}},
		{Resource: "memory", Action: "set", Args: []string{"daily report"}},        // space in arg
		{Resource: "schedule", Action: "create", Args: []string{"morning report"}}, // space
		{Resource: "model", Action: "set", Args: []string{"openai", "gpt-4o"}},     // multiple args
	}
	for _, a := range cases {
		t.Run(strings.Join(a.Args, "+"), func(t *testing.T) {
			typed := a.Typeable()
			parsed, err := Parse(typed)
			if err != nil {
				t.Fatalf("Parse(%q): %v", typed, err)
			}
			if parsed.Resource != a.Resource {
				t.Errorf("Resource: got %q, want %q", parsed.Resource, a.Resource)
			}
			if parsed.Action != a.Action {
				t.Errorf("Action: got %q, want %q", parsed.Action, a.Action)
			}
			if len(parsed.Args) != len(a.Args) {
				t.Errorf("Args len: got %d %v, want %d %v", len(parsed.Args), parsed.Args, len(a.Args), a.Args)
			} else {
				for i := range a.Args {
					if parsed.Args[i] != a.Args[i] {
						t.Errorf("Args[%d]: got %q, want %q", i, parsed.Args[i], a.Args[i])
					}
				}
			}
		})
	}
}

func TestFallbackTrailer_NilAndEmpty(t *testing.T) {
	loc := i18n.New("en")
	if got := FallbackTrailer(nil, loc); got != "" {
		t.Errorf("nil Interactive: got %q, want \"\"", got)
	}
	if got := FallbackTrailer(&Interactive{Kind: InteractiveList, List: nil}, loc); got != "" {
		t.Errorf("nil List view: got %q, want \"\"", got)
	}
	if got := FallbackTrailer(&Interactive{Kind: InteractiveChoices, Choices: nil}, loc); got != "" {
		t.Errorf("nil Choices view: got %q, want \"\"", got)
	}
	if got := FallbackTrailer(&Interactive{Kind: InteractiveModelPicker, Picker: nil}, loc); got != "" {
		t.Errorf("nil Picker view: got %q, want \"\"", got)
	}
	if got := FallbackTrailer(&Interactive{Kind: InteractiveRange, Range: nil}, loc); got != "" {
		t.Errorf("nil Range view: got %q, want \"\"", got)
	}
}

func TestFallbackTrailer_List(t *testing.T) {
	loc := i18n.New("en")

	tests := []struct {
		name     string
		iv       *Interactive
		contains []string // substrings that must appear in trailer
		empty    bool
	}{
		{
			name: "homogeneous switch list (memory)",
			iv: &Interactive{Kind: InteractiveList, List: &ListView{
				Resource: "memory", Action: "list",
				Items: []ListItem{
					{Label: "Alice", Action: &ItemAction{Resource: "memory", Action: "set", Args: []string{"Alice"}}},
					{Label: "Bob", Action: &ItemAction{Resource: "memory", Action: "set", Args: []string{"Bob"}}},
				},
			}},
			contains: []string{"Switch with", "/memory set <name>"},
		},
		{
			name: "display-only list no extras (heartbeat logs)",
			iv: &Interactive{Kind: InteractiveList, List: &ListView{
				Resource: "heartbeat", Action: "logs",
				Items: []ListItem{{Label: "10:00 OK"}, {Label: "11:00 OK"}},
			}},
			empty: true,
		},
		{
			name: "display-only with cross-nav extras (email)",
			iv: &Interactive{Kind: InteractiveList, List: &ListView{
				Resource: "email", Action: "providers",
				Items: []ListItem{{Label: "smtp.gmail.com"}},
				ExtraActions: []ListItem{
					{Label: "Bindings", Action: &ItemAction{Resource: "email", Action: "bindings"}},
					{Label: "Outbox", Action: &ItemAction{Resource: "email", Action: "outbox"}},
				},
			}},
			contains: []string{"Open:", "/email bindings", "/email outbox"},
		},
		{
			name: "HintVerb=details override (mcp)",
			iv: &Interactive{Kind: InteractiveList, List: &ListView{
				Resource: "mcp", Action: "list",
				HintVerb: HintVerbDetails,
				Items:    []ListItem{{Label: "server-a"}},
			}},
			contains: []string{"See details with", "/mcp get <name>"},
		},
		{
			name: "heterogeneous actions falls back to open",
			iv: &Interactive{Kind: InteractiveList, List: &ListView{
				Items: []ListItem{
					{Action: &ItemAction{Resource: "memory", Action: "list"}},
					{Action: &ItemAction{Resource: "search", Action: "list"}},
				},
			}},
			contains: []string{"Open:", "/memory list", "/search list"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FallbackTrailer(tc.iv, loc)
			if tc.empty {
				if got != "" {
					t.Errorf("got %q, want \"\"", got)
				}
				return
			}
			for _, sub := range tc.contains {
				if !strings.Contains(got, sub) {
					t.Errorf("trailer %q does not contain %q", got, sub)
				}
			}
		})
	}
}

func TestFallbackTrailer_Choices(t *testing.T) {
	loc := i18n.New("en")

	tests := []struct {
		name     string
		iv       *Interactive
		contains []string
		empty    bool
	}{
		{
			name: "BodyEnumeratesChoices returns empty",
			iv: &Interactive{Kind: InteractiveChoices, Choices: &ChoicesView{
				BodyEnumeratesChoices: true,
				Choices: []ListItem{
					{Action: &ItemAction{Resource: "schedule", Action: "list"}},
				},
			}},
			empty: true,
		},
		{
			name: "no actionable choices returns empty",
			iv: &Interactive{Kind: InteractiveChoices, Choices: &ChoicesView{
				Choices: []ListItem{{Label: "display only"}},
			}},
			empty: true,
		},
		{
			name: "homogeneous pick shape (reasoning levels)",
			iv: &Interactive{Kind: InteractiveChoices, Choices: &ChoicesView{
				Choices: []ListItem{
					{Action: &ItemAction{Resource: "reasoning", Action: "set", Args: []string{"off"}}},
					{Action: &ItemAction{Resource: "reasoning", Action: "set", Args: []string{"low"}}},
					{Action: &ItemAction{Resource: "reasoning", Action: "set", Args: []string{"high"}}},
				},
			}},
			contains: []string{"Pick with", "/reasoning set <off|low|high>"},
		},
		{
			name: "homogeneous toggle shape (heartbeat flags)",
			iv: &Interactive{Kind: InteractiveChoices, Choices: &ChoicesView{
				Choices: []ListItem{
					{Action: &ItemAction{Resource: "settings", Action: "update", Args: []string{"--heartbeat_enabled", "true"}}},
					{Action: &ItemAction{Resource: "settings", Action: "update", Args: []string{"--heartbeat_enabled", "false"}}},
				},
			}},
			contains: []string{"Toggle:", "/settings update --heartbeat_enabled true", "/settings update --heartbeat_enabled false"},
		},
		{
			name: "heterogeneous cross-nav (settings worst case)",
			iv: &Interactive{Kind: InteractiveChoices, Choices: &ChoicesView{
				Choices: []ListItem{
					{Action: &ItemAction{Resource: "settings", Action: "update", Args: []string{"--heartbeat_enabled", "true"}}},
					{Action: &ItemAction{Resource: "reasoning", Action: "show"}},
					{Action: &ItemAction{Resource: "model", Action: "list"}},
					{Action: &ItemAction{Resource: "memory", Action: "list"}},
				},
			}},
			contains: []string{"Open:", "/settings update --heartbeat_enabled true", "/reasoning show", "/model list", "/memory list"},
		},
		{
			name: "homogeneous single no-arg button (WithButtons empty state)",
			iv: &Interactive{Kind: InteractiveChoices, Choices: &ChoicesView{
				Choices: []ListItem{
					{Action: &ItemAction{Resource: "help", Action: "mcp"}},
				},
			}},
			contains: []string{"Or type", "/help mcp"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FallbackTrailer(tc.iv, loc)
			if tc.empty {
				if got != "" {
					t.Errorf("got %q, want \"\"", got)
				}
				return
			}
			for _, sub := range tc.contains {
				if !strings.Contains(got, sub) {
					t.Errorf("trailer %q does not contain %q", got, sub)
				}
			}
		})
	}
}

func TestFallbackTrailer_ModelPicker(t *testing.T) {
	loc := i18n.New("en")

	t.Run("LevelProviders", func(t *testing.T) {
		got := FallbackTrailer(&Interactive{
			Kind:   InteractiveModelPicker,
			Picker: &ModelPickerView{Level: LevelProviders, Providers: []PickerProvider{{Name: "DeepSeek", Count: 4}}},
		}, loc)
		for _, sub := range []string{"Or type", "/model list <provider_name>"} {
			if !strings.Contains(got, sub) {
				t.Errorf("trailer %q does not contain %q", got, sub)
			}
		}
	})

	t.Run("LevelModels qualifies hint with provider", func(t *testing.T) {
		got := FallbackTrailer(&Interactive{
			Kind:   InteractiveModelPicker,
			Picker: &ModelPickerView{Level: LevelModels, ProviderName: "openai", Models: []PickerModel{{Name: "gpt-4o"}}},
		}, loc)
		// Must emit the provider-scoped two-arg form (single-arg /model set <name>
		// errors as ambiguous when the name exists under multiple providers).
		for _, sub := range []string{"Or type", "/model set openai <model_name>"} {
			if !strings.Contains(got, sub) {
				t.Errorf("trailer %q does not contain %q", got, sub)
			}
		}
	})

	t.Run("LevelModels quotes provider with spaces", func(t *testing.T) {
		got := FallbackTrailer(&Interactive{
			Kind:   InteractiveModelPicker,
			Picker: &ModelPickerView{Level: LevelModels, ProviderName: "Azure OpenAI", Models: []PickerModel{{Name: "gpt-4o"}}},
		}, loc)
		if !strings.Contains(got, `/model set "Azure OpenAI" <model_name>`) {
			t.Errorf("trailer %q should quote a space-bearing provider", got)
		}
	})

	t.Run("LevelModels without provider falls back to bare hint", func(t *testing.T) {
		got := FallbackTrailer(&Interactive{
			Kind:   InteractiveModelPicker,
			Picker: &ModelPickerView{Level: LevelModels, Models: []PickerModel{{Name: "gpt-4o"}}},
		}, loc)
		if !strings.Contains(got, "/model set <name>") {
			t.Errorf("trailer %q should fall back to the bare hint when provider is unknown", got)
		}
	})

	t.Run("LevelProviders empty returns empty trailer", func(t *testing.T) {
		got := FallbackTrailer(&Interactive{
			Kind:   InteractiveModelPicker,
			Picker: &ModelPickerView{Level: LevelProviders, Providers: nil},
		}, loc)
		if got != "" {
			t.Errorf("empty providers should yield empty trailer, got %q", got)
		}
	})

	t.Run("LevelModels empty returns empty trailer", func(t *testing.T) {
		got := FallbackTrailer(&Interactive{
			Kind:   InteractiveModelPicker,
			Picker: &ModelPickerView{Level: LevelModels, Models: nil},
		}, loc)
		if got != "" {
			t.Errorf("empty models should yield empty trailer, got %q", got)
		}
	})
}

func TestFallbackTrailer_Range(t *testing.T) {
	loc := i18n.New("en")

	t.Run("normal", func(t *testing.T) {
		got := FallbackTrailer(&Interactive{
			Kind: InteractiveRange,
			Range: &RangeView{
				Resource: "usage",
				Action:   "summary",
				Current:  "7d",
				Presets:  []string{"24h", "7d", "30d", "all"},
			},
		}, loc)
		for _, sub := range []string{"Time window:", "/usage summary --range <preset>", "24h", "7d", "30d", "all"} {
			if !strings.Contains(got, sub) {
				t.Errorf("trailer %q does not contain %q", got, sub)
			}
		}
	})

	t.Run("missing resource returns empty", func(t *testing.T) {
		got := FallbackTrailer(&Interactive{
			Kind:  InteractiveRange,
			Range: &RangeView{Action: "summary", Presets: []string{"24h"}},
		}, loc)
		if got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})
}

func TestFallbackTrailer_NoPageZeroArtifact(t *testing.T) {
	// Regression guard for the SyntheticCommand --page 0 artifact: trailers
	// must never emit "--page 0" since the typeable form is for users to type,
	// not for the callback decoder to round-trip.
	loc := i18n.New("en")
	cases := []*Interactive{
		{Kind: InteractiveList, List: &ListView{Resource: "memory", Action: "list", Items: []ListItem{
			{Action: &ItemAction{Resource: "memory", Action: "set", Args: []string{"Alice"}}},
		}}},
		{Kind: InteractiveChoices, Choices: &ChoicesView{Choices: []ListItem{
			{Action: &ItemAction{Resource: "reasoning", Action: "set", Args: []string{"low"}}},
		}}},
		{Kind: InteractiveModelPicker, Picker: &ModelPickerView{Level: LevelProviders}},
		{Kind: InteractiveRange, Range: &RangeView{Resource: "usage", Action: "summary", Presets: []string{"24h"}}},
	}
	for i, iv := range cases {
		got := FallbackTrailer(iv, loc)
		if strings.Contains(got, "--page 0") {
			t.Errorf("case %d trailer leaked --page 0: %q", i, got)
		}
	}
}

func TestFallbackTrailer_LocaleFallback(t *testing.T) {
	// A non-existent zh translation must fall back to en; never the raw key.
	loc := i18n.New("zh")
	got := FallbackTrailer(&Interactive{
		Kind: InteractiveList,
		List: &ListView{Items: []ListItem{
			{Action: &ItemAction{Resource: "memory", Action: "set", Args: []string{"x"}}},
		}},
	}, loc)
	if got == "" || strings.HasPrefix(got, "cmd.fallback.") {
		t.Errorf("zh trailer fell through to raw key or empty: %q", got)
	}
}

func TestVerbLineAllVerbs(t *testing.T) {
	loc := i18n.New("en")
	action := []*ItemAction{{Resource: "memory", Action: "set", Args: []string{"alice"}}}
	cases := map[HintVerb]string{
		HintVerbSwitch:  "/memory set alice",
		HintVerbPick:    "/memory set alice",
		HintVerbDetails: "/memory set alice",
		HintVerbMenu:    "/memory set alice",
		HintVerbToggle:  "/memory set alice",
		HintVerbOpen:    "/memory set alice",
	}
	for verb, wantCmd := range cases {
		t.Run(string(verb), func(t *testing.T) {
			got := verbLine(verb, action, loc)
			if got == "" {
				t.Fatalf("verbLine(%q) returned empty trailer", verb)
			}
			if !strings.Contains(got, wantCmd) {
				t.Errorf("verbLine(%q) = %q, missing %q", verb, got, wantCmd)
			}
		})
	}
	// HintVerbRange is the documented exception — it needs a RangeView's
	// presets, which row actions can't provide.
	t.Run("range yields empty by design", func(t *testing.T) {
		if got := verbLine(HintVerbRange, action, loc); got != "" {
			t.Errorf("verbLine(range) should be empty (needs RangeView): %q", got)
		}
	})
	t.Run("unknown verb yields empty", func(t *testing.T) {
		if got := verbLine(HintVerb("totally-bogus"), action, loc); got != "" {
			t.Errorf("unknown verb should yield empty: %q", got)
		}
	})
	t.Run("empty actions yields empty", func(t *testing.T) {
		if got := verbLine(HintVerbSwitch, nil, loc); got != "" {
			t.Errorf("empty actions should yield empty: %q", got)
		}
	})
}

// TestListOverrideTrailerAllVerbs covers the list-level HintVerb override
// dispatch. Verbs that don't naturally apply at list level (pick/toggle/open/
// range) return empty; details/switch/menu synthesize a pseudo command from
// the list's Resource/Action.
func TestListOverrideTrailerAllVerbs(t *testing.T) {
	loc := i18n.New("en")
	cases := []struct {
		verb        HintVerb
		lv          *ListView
		wantContain string // empty means trailer should be empty
	}{
		{HintVerbDetails, &ListView{Resource: "mcp", Action: "list"}, "/mcp get <name>"},
		{HintVerbSwitch, &ListView{Resource: "memory", Action: "list"}, "/memory set <name>"},
		{HintVerbMenu, &ListView{Resource: "schedule", Action: "list"}, ""}, // Menu at list level would self-reference; suppressed.
		{HintVerbPick, &ListView{Resource: "memory", Action: "list"}, ""},
		{HintVerbToggle, &ListView{Resource: "memory", Action: "list"}, ""},
		{HintVerbOpen, &ListView{Resource: "memory", Action: "list"}, ""},
		{HintVerbRange, &ListView{Resource: "usage", Action: "summary"}, ""},
		{HintVerbDetails, &ListView{Resource: ""}, ""}, // empty resource → empty
	}
	for _, tc := range cases {
		t.Run(string(tc.verb), func(t *testing.T) {
			got := listOverrideTrailer(tc.lv, tc.verb, loc)
			if tc.wantContain == "" {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantContain) {
				t.Errorf("trailer %q missing %q", got, tc.wantContain)
			}
		})
	}
}

// TestTrailerForList_HintVerbPlusExtras pins the bug fix where HintVerb=details
// on /mcp list (and /schedule list) used to short-circuit and drop the
// "All commands ▸" cross-nav ExtraAction. Now both lines must appear.
func TestTrailerForList_HintVerbPlusExtras(t *testing.T) {
	loc := i18n.New("en")
	got := FallbackTrailer(&Interactive{
		Kind: InteractiveList,
		List: &ListView{
			Resource: "mcp", Action: "list",
			HintVerb: HintVerbDetails,
			Items:    []ListItem{{Label: "server-a"}},
			ExtraActions: []ListItem{
				{Action: &ItemAction{Resource: "help", Action: "mcp"}},
			},
		},
	}, loc)
	for _, sub := range []string{"See details with", "/mcp get <name>", "Open:", "/help mcp"} {
		if !strings.Contains(got, sub) {
			t.Errorf("trailer %q missing required substring %q", got, sub)
		}
	}
}

// TestTrailerForList_UnknownHintVerbFallsBackToInference pins the BUG-1 fix:
// when listOverrideTrailer returns "" (e.g. unknown HintVerb value, or a
// verb that doesn't apply at list level), trailerForList must fall through
// to row-level inference instead of silently producing an empty trailer.
func TestTrailerForList_UnknownHintVerbFallsBackToInference(t *testing.T) {
	loc := i18n.New("en")
	// HintVerbToggle doesn't apply at list level (listOverrideTrailer returns
	// "" for it). The trailer should still pick up the row-level switch
	// inference from the actionable items.
	got := FallbackTrailer(&Interactive{
		Kind: InteractiveList,
		List: &ListView{
			Resource: "memory", Action: "list",
			HintVerb: HintVerbToggle,
			Items: []ListItem{
				{Label: "alice", Action: &ItemAction{Resource: "memory", Action: "set", Args: []string{"alice"}}},
				{Label: "bob", Action: &ItemAction{Resource: "memory", Action: "set", Args: []string{"bob"}}},
			},
		},
	}, loc)
	if !strings.Contains(got, "Switch with") || !strings.Contains(got, "/memory set <name>") {
		t.Errorf("unknown HintVerb at list level should fall through to row inference; got %q", got)
	}

	// Also exercise a truly unrecognized verb (typo / future-removed constant).
	got2 := FallbackTrailer(&Interactive{
		Kind: InteractiveList,
		List: &ListView{
			Resource: "memory", Action: "list",
			HintVerb: HintVerb("typo-value"),
			Items: []ListItem{
				{Label: "alice", Action: &ItemAction{Resource: "memory", Action: "set", Args: []string{"alice"}}},
			},
		},
	}, loc)
	if !strings.Contains(got2, "/memory set <name>") {
		t.Errorf("unrecognized HintVerb should fall through to row inference; got %q", got2)
	}
}

func TestPickValueClauseEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		actions []*ItemAction
		want    string
	}{
		{"empty actions", nil, "<value>"},
		{"all-empty args", []*ItemAction{{Args: []string{""}}, {Args: []string{"  "}}}, "<value>"},
		{"deduplicates encounter-order", []*ItemAction{
			{Args: []string{"low"}}, {Args: []string{"high"}}, {Args: []string{"low"}}, {Args: []string{"medium"}},
		}, "<low|high|medium>"},
		{"trims whitespace", []*ItemAction{{Args: []string{" low "}}, {Args: []string{" high "}}}, "<low|high>"},
		{"skips actions with no args", []*ItemAction{{Args: nil}, {Args: []string{"x"}}}, "<x>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickValueClause(tc.actions); got != tc.want {
				t.Errorf("pickValueClause = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasAnyArgs(t *testing.T) {
	cases := []struct {
		name    string
		actions []*ItemAction
		want    bool
	}{
		{"empty", nil, false},
		{"all-no-args", []*ItemAction{{Args: nil}, {Args: []string{}}}, false},
		{"all-blank-args", []*ItemAction{{Args: []string{"", "  "}}}, false},
		{"one-has-real-arg", []*ItemAction{{Args: nil}, {Args: []string{"x"}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAnyArgs(tc.actions); got != tc.want {
				t.Errorf("hasAnyArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestJoinActionCmdsSingleItemHasSpace pins the Open: {commands} layout
// contract: with one command the joiner emits " /cmd" (leading space) so the
// template "Open:{commands}" renders as "Open: /cmd" — not "Open:/cmd".
func TestJoinActionCmdsSingleItemHasSpace(t *testing.T) {
	got := joinActionCmds([]*ItemAction{{Resource: "email", Action: "outbox"}})
	if got != " `/email outbox`" {
		t.Errorf("single-item joiner = %q, want leading-space form", got)
	}
	multi := joinActionCmds([]*ItemAction{
		{Resource: "a", Action: "b"}, {Resource: "c", Action: "d"},
	})
	if !strings.HasPrefix(multi, "\n- ") {
		t.Errorf("multi-item joiner should start with newline-bullet, got %q", multi)
	}
}

// TestTrailerForList_HeartbeatStyleOpenSingleItemReadable verifies the
// rendered trailer reads cleanly when a list has a single ExtraAction and no
// row actions (e.g. an empty-state list with one nav button), exercising the
// joinActionCmds single-item branch through the template.
func TestTrailerForList_OpenSingleExtraReadable(t *testing.T) {
	loc := i18n.New("en")
	got := FallbackTrailer(&Interactive{
		Kind: InteractiveList,
		List: &ListView{
			Items:        []ListItem{{Label: "display-only"}},
			ExtraActions: []ListItem{{Action: &ItemAction{Resource: "help", Action: "settings"}}},
		},
	}, loc)
	if !strings.Contains(got, "Open: ") {
		t.Errorf("single-extras open trailer should have 'Open: ', got %q", got)
	}
	if strings.Contains(got, "Open:/") {
		t.Errorf("single-extras open trailer must not collapse to 'Open:/...', got %q", got)
	}
}
