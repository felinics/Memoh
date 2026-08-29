package inbound

import (
	"fmt"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/models"
)

func listResult(total, page, pageSize int) *command.Result {
	items := make([]command.ListItem, 0, pageSize)
	for i := 0; i < pageSize && i < total; i++ {
		items = append(items, command.ListItem{Label: "item", Detail: "d"})
	}
	return &command.Result{
		Text: "list text",
		Interactive: &command.Interactive{
			Kind: command.InteractiveList,
			List: &command.ListView{
				Resource: "mcp", Action: "list",
				Items: items, Total: total, Page: page, PageSize: pageSize,
			},
		},
	}
}

func TestRenderResultTextFallbackWhenNoButtons(t *testing.T) {
	res := listResult(50, 0, 12)
	res.Interactive.List.ButtonText = "button list text"
	msg := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: false}, T: i18n.New("en")})
	if msg.Text != "list text" {
		t.Errorf("Text = %q, want 'list text'", msg.Text)
	}
	if len(msg.Actions) != 0 {
		t.Errorf("expected no actions for non-button channel, got %d", len(msg.Actions))
	}
}

func TestRenderListUsesButtonTextWhenButtonsAreAvailable(t *testing.T) {
	res := listResult(5, 0, 12)
	res.Interactive.List.ButtonText = "button list text"
	res.Interactive.List.Items[0].Action = &command.ItemAction{Resource: "search", Action: "set", Args: []string{"one"}}
	msg := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("en")})
	if msg.Text != "button list text" {
		t.Errorf("Text = %q, want button text", msg.Text)
	}
	if len(msg.Actions) == 0 {
		t.Fatal("expected row action buttons")
	}
}

func TestRenderResultFormatGate(t *testing.T) {
	res := &command.Result{Text: "**Title**\n- `value`"}
	// Markdown-capable channel: format set, markup preserved.
	md := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Markdown: true}, T: i18n.New("en")})
	if md.Format != channel.MessageFormatMarkdown {
		t.Errorf("markdown channel: Format = %q, want markdown", md.Format)
	}
	if md.Text != "**Title**\n- `value`" {
		t.Errorf("markdown channel should preserve markup, got %q", md.Text)
	}
	// RichText-only channel (e.g. feishu): also markdown.
	rich := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{RichText: true}, T: i18n.New("en")})
	if rich.Format != channel.MessageFormatMarkdown {
		t.Errorf("richtext channel: Format = %q, want markdown", rich.Format)
	}
	// Text-only channel: markers stripped, format stays plain.
	plain := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{}, T: i18n.New("en")})
	if plain.Format == channel.MessageFormatMarkdown {
		t.Error("text-only channel should not be markdown")
	}
	if plain.Format != channel.MessageFormatPlain {
		t.Errorf("text-only channel must set Format=plain explicitly (else outbound auto-detect promotes bullet-list output to markdown and validateMessageCapabilities rejects it on WeChat/Local/WechatOA): got %q", plain.Format)
	}
	if strings.Contains(plain.Text, "**") || strings.Contains(plain.Text, "`") {
		t.Errorf("text-only channel should strip markup, got %q", plain.Text)
	}
	if !strings.Contains(plain.Text, "Title") || !strings.Contains(plain.Text, "value") {
		t.Errorf("text-only strip lost content: %q", plain.Text)
	}
}

func TestRenderResultTextFallbackWhenNoInteractive(t *testing.T) {
	msg := renderResult(&command.Result{Text: "plain"}, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("en")})
	if msg.Text != "plain" || len(msg.Actions) != 0 {
		t.Errorf("got Text=%q actions=%d, want 'plain'/0", msg.Text, len(msg.Actions))
	}
}

func TestRenderListSinglePageNoButtons(t *testing.T) {
	msg := renderResult(listResult(5, 0, 12), RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("en")})
	if len(msg.Actions) != 0 {
		t.Errorf("single page should render no buttons, got %d", len(msg.Actions))
	}
}

func TestRenderListMultiPageNavButtons(t *testing.T) {
	// 50 items, 12/page => 5 pages. Page 0 should have: indicator, Next, Close.
	msg := renderResult(listResult(50, 0, 12), RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("en")})
	if len(msg.Actions) == 0 {
		t.Fatal("expected nav buttons on a multi-page list")
	}
	var hasNext, hasPrev, hasClose, hasIndicator bool
	navRow := -1
	for _, a := range msg.Actions {
		switch a.Label {
		case "Next ▶":
			hasNext = true
			navRow = a.Row
		case "◀ Prev":
			hasPrev = true
		case "✕ Close":
			hasClose = true
		case "1/5":
			hasIndicator = true
		}
	}
	if hasPrev {
		t.Error("page 0 should not have a Prev button")
	}
	if !hasNext || !hasClose || !hasIndicator {
		t.Errorf("missing buttons: next=%v close=%v indicator=%v", hasNext, hasClose, hasIndicator)
	}
	// Indicator and Next share the nav row; Close is on its own (later) row.
	for _, a := range msg.Actions {
		if a.Label == "1/5" && a.Row != navRow {
			t.Errorf("indicator row=%d, want nav row %d", a.Row, navRow)
		}
		if a.Label == "✕ Close" && a.Row <= navRow {
			t.Errorf("close row=%d should be after nav row %d", a.Row, navRow)
		}
	}
}

func TestRenderListLastPageHasPrevNotNext(t *testing.T) {
	// 50 items, 12/page, last page index = 4.
	msg := renderResult(listResult(50, 4, 12), RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("en")})
	var hasNext, hasPrev bool
	for _, a := range msg.Actions {
		switch a.Label {
		case "Next ▶":
			hasNext = true
		case "◀ Prev":
			hasPrev = true
		}
	}
	if hasNext {
		t.Error("last page should not have a Next button")
	}
	if !hasPrev {
		t.Error("last page should have a Prev button")
	}
}

func TestRenderRangeView(t *testing.T) {
	res := &command.Result{
		Text: "Token usage (7 days)",
		Interactive: &command.Interactive{
			Kind:  command.InteractiveRange,
			Range: &command.RangeView{Resource: "usage", Action: "summary", Current: "7d", Presets: []string{"24h", "7d", "30d", "all"}},
		},
	}
	msg := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true, Markdown: true}, T: i18n.New("en")})

	var labels []string
	var hasClose, currentMarked bool
	for _, a := range msg.Actions {
		labels = append(labels, a.Label)
		if a.Label == "✕ Close" {
			hasClose = true
			continue
		}
		if strings.Contains(a.Label, "●") {
			currentMarked = true
			if !strings.HasPrefix(a.Label, "7d") {
				t.Errorf("active preset marker on wrong button: %q", a.Label)
			}
		}
	}
	if !hasClose {
		t.Error("expected a Close button")
	}
	if !currentMarked {
		t.Errorf("expected ● on the active preset, labels=%v", labels)
	}
	// "all" renders as "All".
	var hasAll bool
	for _, a := range msg.Actions {
		if a.Label == "All" {
			hasAll = true
			if a.Value != command.EncodeRangeCallback("usage", "summary", "all") {
				t.Errorf("All button callback = %q", a.Value)
			}
		}
	}
	if !hasAll {
		t.Errorf("expected an 'All' preset button, labels=%v", labels)
	}
}

func TestRenderChoicesViewUsesSingleColumnForLongLabels(t *testing.T) {
	res := &command.Result{
		Text: "settings",
		Interactive: &command.Interactive{
			Kind: command.InteractiveChoices,
			Choices: &command.ChoicesView{
				Title: "settings",
				Choices: []command.ListItem{
					{Label: "Disable workspace display now", Action: &command.ItemAction{Resource: "settings", Action: "update"}},
					{Label: "Ask before tools", Action: &command.ItemAction{Resource: "settings", Action: "update"}},
				},
			},
		},
	}
	msg := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("en")})
	if len(msg.Actions) < 2 {
		t.Fatalf("expected actions, got %d", len(msg.Actions))
	}
	if msg.Actions[0].Row == msg.Actions[1].Row {
		t.Fatalf("long labels should use one column, rows: %+v", msg.Actions)
	}
}

func TestRenderModelPickerProviderGrid(t *testing.T) {
	res := &command.Result{
		Text: "models",
		Interactive: &command.Interactive{
			Kind: command.InteractiveModelPicker,
			Picker: &command.ModelPickerView{
				Level: command.LevelProviders,
				Providers: []command.PickerProvider{
					{Index: 0, Name: "Anthropic", HasCurrent: true},
					{Index: 1, Name: "OpenAI"},
					{Index: 2, Name: "DeepSeek"},
				},
				Page: 0, PageSize: 10, Total: 3,
			},
		},
	}
	msg := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("en")})

	// Provider buttons are 2 per row.
	provByRow := map[int]int{}
	var currentMarked bool
	for _, a := range msg.Actions {
		if a.Label == "✕ Close" {
			continue
		}
		provByRow[a.Row]++
		if strings.Contains(a.Label, "●") {
			currentMarked = true
		}
	}
	if !currentMarked {
		t.Error("expected ● marker on the provider holding the current model")
	}
	if provByRow[0] != 2 {
		t.Errorf("first row should have 2 provider buttons, got %d", provByRow[0])
	}
	// Provider taps drill into that provider's model list at page 0.
	if got := msg.Actions[0].Value; got != command.EncodeModelProviderCallback(0, 0) {
		t.Errorf("first provider callback = %q, want %q", got, command.EncodeModelProviderCallback(0, 0))
	}
}

func TestFormatStartWelcomeMessage(t *testing.T) {
	got := formatStartWelcomeMessage(i18n.New("en"), "")
	for _, want := range []string{
		"**👋 Hi there!**",
		"Just send me a message",
		"/help shows what I can do.",
		"/new starts a clean slate anytime.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q:\n%s", want, got)
		}
	}
	for _, omit := range []string{"Model:", "Reasoning:", "Context:", "New chat started"} {
		if strings.Contains(got, omit) {
			t.Errorf("/start welcome should not include %q:\n%s", omit, got)
		}
	}
}

func TestFormatStartWelcomeMessageTelegramGroup(t *testing.T) {
	got := formatStartWelcomeMessage(i18n.New("en"), "snowluocat_bot")
	for _, want := range []string{
		"/help shows what I can do.",
		"/new starts a clean slate anytime.",
		"for example `/help@snowluocat_bot`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Telegram group welcome missing %q:\n%s", want, got)
		}
	}
	if count := strings.Count(got, "snowluocat_bot"); count != 1 {
		t.Errorf("Telegram group welcome repeats bot username %d times, want once:\n%s", count, got)
	}
}

func TestFormatNewSessionMessage(t *testing.T) {
	got := formatNewSessionMessage(i18n.New("en"), "chat", command.CurrentContext{
		ChatModel:       "Claude Opus 4.7 (Anthropic)",
		ReasoningEffort: "medium", ContextWindow: "128.0K",
	}, "")
	// A fresh-start card confirms the full setup: model (+provider), reasoning,
	// and context budget. Header is bold; values are plain (display names/enums).
	for _, want := range []string{
		"**✨ New chat started.**",
		"Model: Claude Opus 4.7 (Anthropic)",
		"Reasoning: medium",
		"Context: 128.0K tokens",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q:\n%s", want, got)
		}
	}
	// Setup values are plain, and the closing single-token commands stay plain so
	// Telegram can render them as tap-to-send command entities.
	if strings.Contains(got, "`Claude Opus 4.7 (Anthropic)`") {
		t.Errorf("model value should be plain, not code-spanned:\n%s", got)
	}
	if !strings.Contains(got, "Tip: adjust anytime with /model or /reasoning.") {
		t.Errorf("expected tap-to-send command tip:\n%s", got)
	}
	// Markup strips cleanly for plain-text channels.
	plain := channel.StripInlineMarkup(got)
	if strings.Contains(plain, "**") || strings.Contains(plain, "`") {
		t.Errorf("channel.StripInlineMarkup left markers: %q", plain)
	}

	// Reasoning off is still shown (it sets expectations on a fresh start); an
	// unknown context window is omitted.
	off := formatNewSessionMessage(i18n.New("en"), "discussion", command.CurrentContext{ChatModel: "(none)", ReasoningEffort: models.ReasoningEffortDisable}, "")
	if !strings.Contains(off, "Reasoning: off") {
		t.Errorf("reasoning state should be confirmed on the fresh-start card: %s", off)
	}
	if !strings.Contains(off, "**✨ New discussion started.**") {
		t.Errorf("mode label not reflected: %s", off)
	}
	if strings.Contains(off, "Context:") {
		t.Errorf("unknown context window should be omitted: %s", off)
	}
}

func TestFormatNewSessionMessageTelegramGroup(t *testing.T) {
	got := formatNewSessionMessage(i18n.New("en"), "chat", command.CurrentContext{
		ChatModel: "Claude Opus 4.7 (Anthropic)",
	}, "@snowluocat_bot")
	for _, want := range []string{
		"/model",
		"/reasoning",
		"for example `/help@snowluocat_bot`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Telegram group new-session message missing %q:\n%s", want, got)
		}
	}
	if count := strings.Count(got, "snowluocat_bot"); count != 1 {
		t.Errorf("Telegram group new-session message repeats bot username %d times, want once:\n%s", count, got)
	}
}

func TestTelegramGroupBotUsername(t *testing.T) {
	group := channel.InboundMessage{
		Channel:      channel.ChannelTypeTelegram,
		Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
		Metadata:     map[string]any{"bot_username": "@snowluocat_bot"},
	}
	if got := telegramGroupBotUsername(group); got != "snowluocat_bot" {
		t.Fatalf("telegramGroupBotUsername(group) = %q", got)
	}
	group.Metadata = nil
	if got := telegramGroupBotUsername(group); got != "" {
		t.Fatalf("telegramGroupBotUsername(missing metadata) = %q, want empty", got)
	}
	group.Metadata = map[string]any{"bot_username": true}
	if got := telegramGroupBotUsername(group); got != "" {
		t.Fatalf("telegramGroupBotUsername(malformed metadata) = %q, want empty", got)
	}
	group.Conversation.Type = channel.ConversationTypePrivate
	if got := telegramGroupBotUsername(group); got != "" {
		t.Fatalf("telegramGroupBotUsername(private) = %q, want empty", got)
	}
	group.Conversation.Type = channel.ConversationTypeGroup
	group.Channel = channel.ChannelTypeDiscord
	if got := telegramGroupBotUsername(group); got != "" {
		t.Fatalf("telegramGroupBotUsername(discord) = %q, want empty", got)
	}
}

func TestRenderModelPickerModelLevel(t *testing.T) {
	// 20 models, 8/page => 3 pages. Page 1 (middle) should have Prev+Next,
	// a Back-to-providers button, ✓ on the selected model, and Close.
	models := make([]command.PickerModel, 0, 20)
	for i := 0; i < 20; i++ {
		models = append(models, command.PickerModel{DBID: fmt.Sprintf("model-%d", i), Name: "m", Selected: i == 9})
	}
	res := &command.Result{
		Text: "models",
		Interactive: &command.Interactive{
			Kind: command.InteractiveModelPicker,
			Picker: &command.ModelPickerView{
				Level: command.LevelModels, Models: models,
				ProviderIndex: 2, Page: 1, PageSize: 8, Total: 20,
			},
		},
	}
	msg := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("en")})

	var hasPrev, hasNext, hasBack, hasClose, hasSelected bool
	for _, a := range msg.Actions {
		switch a.Label {
		case "◀ Prev":
			hasPrev = true
		case "Next ▶":
			hasNext = true
		case "◀ Providers":
			hasBack = true
			if a.Value != command.EncodeListCallback("model", "list", nil, 0) {
				t.Errorf("back button callback = %q", a.Value)
			}
		case "✕ Close":
			hasClose = true
		}
		if strings.HasPrefix(a.Label, "✓ ") {
			hasSelected = true
		}
	}
	if !hasPrev || !hasNext {
		t.Errorf("middle page should have Prev and Next: prev=%v next=%v", hasPrev, hasNext)
	}
	if !hasBack || !hasClose {
		t.Errorf("model level should have Back and Close: back=%v close=%v", hasBack, hasClose)
	}
	// Selected model is flat index 9, which falls in page 1's slice [8,16).
	if !hasSelected {
		t.Error("selected model (flat 9) should be marked with ✓ on page 1")
	}
}

// TestTelegramCallbackTapRoundTrip pins the full Telegram button-tap loop:
// renderer produces buttons with callback_data → DecodeCallback parses it →
// SyntheticCommand produces the slash command to re-dispatch → Parse() round-trips
// to the same (Resource, Action, Args, Page). This is the chain a user's button
// tap travels through end-to-end. Unit tests cover each step individually; this
// test pins the *join*.
func TestTelegramCallbackTapRoundTrip(t *testing.T) {
	t.Parallel()

	tgCaps := channel.ChannelCapabilities{Text: true, Markdown: true, Buttons: true}
	loc := i18n.New("en")

	// 50 mcp connections, 12 per page; we're on page 0 looking at "Next ▶".
	msg := renderResult(listResult(50, 0, 12), RenderContext{Caps: tgCaps, T: loc})

	var nextValue string
	for _, a := range msg.Actions {
		if a.Label == "Next ▶" {
			nextValue = a.Value
			break
		}
	}
	if nextValue == "" {
		t.Fatal("Next ▶ button missing — renderer didn't emit pagination, can't test the loop")
	}

	// Step 1: decode the wire-format callback_data the bot received.
	parsed, ok := command.DecodeCallback(nextValue)
	if !ok {
		t.Fatalf("DecodeCallback(%q) returned ok=false — Telegram would silently drop the tap", nextValue)
	}
	if parsed.Kind != "list_page" {
		t.Fatalf("Kind = %q, want list_page (the renderer-encoder/decoder split has drifted)", parsed.Kind)
	}
	if parsed.Page != 1 {
		t.Fatalf("Page = %d, want 1 (Next on page 0 should land on page 1)", parsed.Page)
	}
	if parsed.Resource != "mcp" || parsed.Action != "list" {
		t.Fatalf("got Resource=%q Action=%q, want mcp/list", parsed.Resource, parsed.Action)
	}

	// Step 2: synthesize the re-dispatch command.
	syn := parsed.SyntheticCommand()
	if syn == "" {
		t.Fatal("SyntheticCommand returned empty — re-dispatch chain broken")
	}

	// Step 3: parse it back, confirm intent survived the encode→decode→synthesize→parse cycle.
	cmd, err := command.Parse(syn)
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v — Telegram tap would re-dispatch as a malformed command", syn, err)
	}
	if cmd.Resource != "mcp" || cmd.Action != "list" {
		t.Fatalf("re-parsed Resource=%q Action=%q, want mcp/list", cmd.Resource, cmd.Action)
	}
	if cmd.Page != 1 {
		t.Fatalf("re-parsed Page=%d, want 1 — pagination intent lost in the round-trip", cmd.Page)
	}
}
