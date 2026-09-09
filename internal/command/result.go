package command

import agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"

// InteractiveKind discriminates the structured payload carried by a Result.
type InteractiveKind string

const (
	// InteractiveList is a generic, paginated list of display rows.
	InteractiveList InteractiveKind = "list"
	// InteractiveModelPicker is the two-level provider→model drill-down picker.
	InteractiveModelPicker InteractiveKind = "model_picker"
	// InteractiveChoices is a flat one-shot set of selectable choices.
	InteractiveChoices InteractiveKind = "choices"
	// InteractiveRange is a time-window selector for time-series commands.
	InteractiveRange InteractiveKind = "range"
)

// Result is the neutral, platform-independent output of a command.
//
// Text is always a complete rendering usable by channels without rich UI.
// Interactive is optional structured data that capable renderers (e.g. the
// Telegram inline keyboard) may upgrade into buttons and pagination.
//
// Locale is the resolved command-UI locale ("en", "zh", …) the Text/Interactive
// labels were rendered in. Channels use it to localize their own renderer chrome
// (Close/Prev/Next/…) so the whole reply stays in one language. Empty means the
// server default (English).
type Result struct {
	Text          string
	Interactive   *Interactive
	Locale        string
	FeedbackError *agentfeedback.Error
}

// Interactive carries optional structured data for rich rendering. Exactly one
// of the typed views is set, selected by Kind.
type Interactive struct {
	Kind    InteractiveKind
	List    *ListView
	Picker  *ModelPickerView
	Choices *ChoicesView
	Range   *RangeView
}

// ListView is a generic paginated list. It is re-derivable by re-running the
// originating command with a page offset, so Resource/Action/Args round-trip
// through the callback data of pagination buttons.
//
// HintVerb is an optional explicit verb (one of HintVerb*) for trailer
// derivation on no-button channels — used when rows are display-only but the
// list has a paired typeable affordance (e.g. /mcp list rows are display-only
// but /mcp get <name> is the typeable next step). Empty = infer from structure.
//
// Note the asymmetry with ChoicesView.BodyEnumeratesChoices: ListView needs
// to pick WHICH verb to emit, ChoicesView only needs to choose WHETHER to
// emit. Different questions, different mechanisms — kept that way rather
// than forcing them into one shape that fits neither cleanly.
type ListView struct {
	Title        string
	ButtonText   string   // optional compact text for button-capable channels
	Resource     string   // command resource (e.g. "mcp"), round-trips in callback data
	Action       string   // command action (e.g. "list")
	Args         []string // narrowing args (e.g. a provider filter) that round-trip
	Items        []ListItem
	Total        int        // total items across all pages
	Page         int        // zero-based page index of this view
	PageSize     int        // items per page
	ExtraActions []ListItem // contextual action buttons below the list rows (e.g. "All commands")
	HintVerb     HintVerb   // optional fallback-trailer verb override (HintVerb*)
}

// ListItem is one row in a ListView. Action is nil for display-only rows.
//
// Callback optionally carries pre-encoded callback_data for button-capable
// renderers; when set it takes precedence over Action as the button value.
// Rows with only Callback stay out of the no-button fallback trailer (which
// derives typeable hints from Action), so such callers must carry the
// typeable form in the body text instead.
type ListItem struct {
	Label    string
	Detail   string
	Selected bool
	Action   *ItemAction
	Callback string
}

// ItemAction triggers a command when a row is tapped.
type ItemAction struct {
	Resource string
	Action   string
	Args     []string
}

// ModelPickerView is the two-level model picker (populated in the model-picker
// phase). Level selects whether Providers or Models is rendered.
type ModelPickerView struct {
	Level            PickerLevel
	Providers        []PickerProvider // populated at the provider level
	Models           []PickerModel    // populated at the model level
	ProviderIndex    int              // which provider we drilled into (model level)
	ProviderName     string           // name of that provider (model level), for the header
	Page             int
	PageSize         int
	Total            int
	CurrentModelDBID string // settings.ChatModelID, for ●/✓ marking
	CurrentDisplay   string // resolved current chat model "Name (Provider)", for the header
	Reasoning        string // current reasoning effort label, for the header
}

// PickerLevel is the drill-down level of a ModelPickerView.
type PickerLevel string

const (
	// LevelProviders renders the provider grid.
	LevelProviders PickerLevel = "providers"
	// LevelModels renders the paginated model list for one provider.
	LevelModels PickerLevel = "models"
)

// PickerProvider is one provider button in the picker. HasCurrent marks the
// provider that holds the currently-selected model (rendered with ●). Count is
// the number of chat models the provider offers.
type PickerProvider struct {
	Index      int
	Name       string
	Count      int
	HasCurrent bool
}

// PickerModel is one model button in the picker. Selected marks the active
// model (rendered with ✓). DBID is the model's stable id, carried in the
// selection callback so a list change between render and tap can't resolve the
// tap to a different model.
type PickerModel struct {
	DBID     string
	Name     string
	Provider string
	Selected bool
}

// ChoicesView is a flat set of selectable choices (no pagination).
//
// BodyEnumeratesChoices asserts that the body Text already enumerates every
// typeable affordance (e.g. /help <group>'s Usage block lists every
// sub-command verbatim), so the no-button fallback trailer should skip this
// surface to avoid duplicating what the body already says.
//
// This is a bool (not a HintVerb sentinel like ListView's HintVerb) because
// the question being answered is different: ChoicesView picks WHETHER to emit
// a trailer at all, ListView picks WHICH verb to use. See ListView.HintVerb.
type ChoicesView struct {
	Title                 string
	Choices               []ListItem
	Columns               int // optional keyboard columns; 0 lets renderers pick
	BodyEnumeratesChoices bool
}

// RangeView is a time-window selector for a time-series command. Selecting a
// preset re-runs "/{Resource} {Action} --range <preset>" in place.
type RangeView struct {
	Resource string
	Action   string
	Current  string   // the active preset key (normalized), for the ● marker
	Presets  []string // ordered preset keys, e.g. ["24h","7d","30d","all"]
}

// CurrentContext is the resolved current-state summary used to enrich /new and
// bare /model output. All fields are display-ready strings.
type CurrentContext struct {
	ChatModel string
	// ReasoningEffort carries models.ReasoningEffortDisable when reasoning is off.
	ReasoningEffort string
	ContextWindow   string // resolved chat-model context window (e.g. "128.0K"), "" if unknown
}
