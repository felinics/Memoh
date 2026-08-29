package telegram

import (
	"strings"
	"testing"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/i18n"
)

const testAskUserRequestID = "0b5e7a1c-9f2d-4e3a-8b6c-1d2e3f4a5b6c"

func TestParseAskUserCallbackRoundTrip(t *testing.T) {
	t.Parallel()

	cb, ok := parseAskUserCallback(encodeAskUserCallback("s", testAskUserRequestID, "zh", 0, 1))
	if !ok || cb.Op != "s" || cb.RequestID != testAskUserRequestID || cb.Locale != "zh" || cb.QIndex != 0 || cb.OIndex != 1 {
		t.Fatalf("select = %#v ok=%v", cb, ok)
	}
	cb, ok = parseAskUserCallback(encodeAskUserCallback("n", testAskUserRequestID, "en", 2))
	if !ok || cb.Op != "n" || cb.Page != 2 {
		t.Fatalf("nav = %#v ok=%v", cb, ok)
	}
	cb, ok = parseAskUserCallback(encodeAskUserCallback("go", testAskUserRequestID, "en"))
	if !ok || cb.Op != "go" {
		t.Fatalf("go = %#v ok=%v", cb, ok)
	}
	if _, ok := parseAskUserCallback("respond:input-1:q1.o1"); ok {
		t.Fatal("legacy respond callback must not parse as ask_user")
	}
	if _, ok := parseAskUserCallback("m~noop"); ok {
		t.Fatal("interactive command callback must not parse as ask_user")
	}
	if _, ok := parseAskUserCallback("aui~s~not-a-uuid~en~0~0"); ok {
		t.Fatal("malformed request id must not parse")
	}
}

func TestAskUserCallbackFitsTelegramLimit(t *testing.T) {
	t.Parallel()

	callback := encodeAskUserCallback("s", testAskUserRequestID, "zh", 3, 19)
	if len(callback) > telegramMaxCallbackDataBytes {
		t.Fatalf("callback length = %d, exceeds Telegram limit", len(callback))
	}
}

func TestAskUserTextPromptStoreTakeIsOneShot(t *testing.T) {
	t.Parallel()

	store := newAskUserTextPromptStore()
	store.put(10, 20, askUserTextPrompt{RequestID: testAskUserRequestID, QuestionID: "q1", Locale: "zh"})
	prompt, ok := store.take(10, 20)
	if !ok || prompt.RequestID != testAskUserRequestID || prompt.QuestionID != "q1" {
		t.Fatalf("prompt = %#v ok=%v", prompt, ok)
	}
	if _, ok := store.take(10, 20); ok {
		t.Fatal("take must consume the binding")
	}
}

func twoQuestionPayload() userinput.UIPayload {
	return userinput.UIPayload{
		Version: userinput.PayloadVersion,
		Questions: []userinput.UIQuestion{
			{
				ID:   "q1",
				Text: "你平时主要用哪些编程语言？",
				Kind: userinput.QuestionKindMultiSelect,
				Options: []userinput.UIOption{
					{ID: "q1.o1", Label: "Python"},
					{ID: "q1.o2", Label: "JavaScript"},
					{ID: "q1.o3", Label: "Go"},
					{ID: "q1.o4", Label: "Rust"},
				},
			},
			{ID: "q2", Text: "有没有最近在做的项目想聊聊？", Kind: userinput.QuestionKindText},
		},
	}
}

func TestRenderAskUserPageMultiQuestionFlow(t *testing.T) {
	t.Parallel()

	loc := i18n.New("en")
	payload := twoQuestionPayload()
	state := userinput.TextInteractionState{}

	text, actions := renderAskUserPage(testAskUserRequestID, loc, payload, state)
	if text != "你平时主要用哪些编程语言？" {
		t.Fatalf("page 1 text = %q", text)
	}
	if !navRowEquals(actions, "1/2", "Skip →") {
		t.Fatalf("first-page nav = %v", actionLabels(actions))
	}

	// Toggle Go via the durable transition, then re-render.
	state = applyTestOp(t, payload, state, userinput.InteractionOp{Kind: userinput.OpToggleOption, QuestionIndex: 0, OptionIndex: 2})
	text, actions = renderAskUserPage(testAskUserRequestID, loc, payload, state)
	if strings.Contains(text, "Go") {
		t.Fatalf("button selection must not be duplicated in body: %q", text)
	}
	if !actionLabelsContain(actions, "✓ Go") || !navRowEquals(actions, "1/2", "Next →") {
		t.Fatalf("selected page actions = %v", actionLabels(actions))
	}

	// Explicit navigation to page 2.
	state = applyTestOp(t, payload, state, userinput.InteractionOp{Kind: userinput.OpNavigate, Page: 1})
	text, actions = renderAskUserPage(testAskUserRequestID, loc, payload, state)
	if text != "有没有最近在做的项目想聊聊？" {
		t.Fatalf("page 2 text = %q", text)
	}
	if !actionLabelsContain(actions, "Enter answer") {
		t.Fatalf("text input button missing: %v", actionLabels(actions))
	}
	if !navRowEquals(actions, "←", "2/2", "Skip and submit") {
		t.Fatalf("last-page nav = %v", actionLabels(actions))
	}

	// Replying to the last text question completes the set immediately.
	state = applyTestOp(t, payload, state, userinput.InteractionOp{Kind: userinput.OpSetText, QuestionID: "q2", Text: "Memoh Telegram 适配"})
	if !state.Completed {
		t.Fatalf("state should be completed: %#v", state)
	}
	q1, _ := state.Answer("q1")
	if len(q1.OptionIDs) != 1 || q1.OptionIDs[0] != "q1.o3" {
		t.Fatalf("q1 = %#v", q1)
	}
	q2, _ := state.Answer("q2")
	if q2.Text != "Memoh Telegram 适配" {
		t.Fatalf("q2 = %#v", q2)
	}
}

func TestAskUserSingleSelectAutoAdvancesAndToggleOff(t *testing.T) {
	t.Parallel()

	payload := userinput.UIPayload{Questions: []userinput.UIQuestion{
		{
			ID: "q1", Text: "速度？", Kind: userinput.QuestionKindSingleSelect,
			Options: []userinput.UIOption{{ID: "q1.o1", Label: "快"}, {ID: "q1.o2", Label: "慢"}},
		},
		{ID: "q2", Text: "备注？", Kind: userinput.QuestionKindText},
	}}
	state := userinput.TextInteractionState{}

	state = applyTestOp(t, payload, state, userinput.InteractionOp{Kind: userinput.OpSelectOption, QuestionIndex: 0, OptionIndex: 0})
	if state.QuestionIndex != 1 || state.Completed {
		t.Fatalf("single select must auto-advance: %#v", state)
	}

	// Back preserves the selection; re-tapping the same option clears it.
	state = applyTestOp(t, payload, state, userinput.InteractionOp{Kind: userinput.OpNavigate, Page: 0})
	if answer, ok := state.Answer("q1"); !ok || len(answer.OptionIDs) != 1 {
		t.Fatalf("back must preserve answer: %#v", state)
	}
	state = applyTestOp(t, payload, state, userinput.InteractionOp{Kind: userinput.OpSelectOption, QuestionIndex: 0, OptionIndex: 0})
	if _, ok := state.Answer("q1"); ok {
		t.Fatalf("re-select must clear answer: %#v", state)
	}
	loc := i18n.New("en")
	_, actions := renderAskUserPage(testAskUserRequestID, loc, payload, state)
	if !navRowEquals(actions, "1/2", "Skip →") {
		t.Fatalf("cleared actions = %v", actionLabels(actions))
	}
}

func TestAskUserSubmitFillsSkips(t *testing.T) {
	t.Parallel()

	payload := userinput.UIPayload{Questions: []userinput.UIQuestion{
		{ID: "q1", Text: "Q1", Kind: userinput.QuestionKindText},
		{ID: "q2", Text: "Q2", Kind: userinput.QuestionKindText},
	}}
	state := applyTestOp(t, payload, userinput.TextInteractionState{}, userinput.InteractionOp{Kind: userinput.OpSubmit})
	if !state.Completed || len(state.Answers) != 2 {
		t.Fatalf("submit with skips = %#v", state)
	}
	for _, answer := range state.Answers {
		if !answer.Skipped {
			t.Fatalf("answer must be skipped: %#v", answer)
		}
	}
}

func TestAskUserLastQuestionSelectCompletesWithSkips(t *testing.T) {
	t.Parallel()

	payload := userinput.UIPayload{Questions: []userinput.UIQuestion{
		{ID: "q1", Text: "Q1", Kind: userinput.QuestionKindText},
		{
			ID: "q2", Text: "Tea?", Kind: userinput.QuestionKindSingleSelect,
			Options: []userinput.UIOption{{ID: "q2.o1", Label: "Oolong"}, {ID: "q2.o2", Label: "Green"}},
		},
	}}
	state := applyTestOp(t, payload, userinput.TextInteractionState{QuestionIndex: 1},
		userinput.InteractionOp{Kind: userinput.OpSelectOption, QuestionIndex: 1, OptionIndex: 0})
	if !state.Completed {
		t.Fatalf("last-question select must complete: %#v", state)
	}
	q1, ok := state.Answer("q1")
	if !ok || !q1.Skipped {
		t.Fatalf("q1 must be filled as skipped: %#v", state)
	}
}

func TestFormatAskUserSubmittedSummary(t *testing.T) {
	t.Parallel()

	loc := i18n.New("en")
	payload := userinput.UIPayload{Questions: []userinput.UIQuestion{
		{
			ID: "q1", Text: "Tea?", Kind: userinput.QuestionKindSingleSelect,
			Options: []userinput.UIOption{{ID: "q1.o1", Label: "Oolong"}, {ID: "q1.o2", Label: "Green"}},
		},
		{ID: "q2", Text: "Notes?", Kind: userinput.QuestionKindText},
	}}
	state := userinput.TextInteractionState{Completed: true, Answers: []userinput.QuestionAnswer{
		{QuestionID: "q1", OptionIDs: []string{"q1.o1"}},
		{QuestionID: "q2", Text: "Current answer"},
	}}
	got := formatAskUserSubmittedSummary(loc, payload, state)
	want := "1. Tea?\nOolong\n\n2. Notes?\nCurrent answer"
	if got != want {
		t.Fatalf("summary = %q", got)
	}

	skipped := userinput.TextInteractionState{Completed: true, Answers: []userinput.QuestionAnswer{
		{QuestionID: "q1", Skipped: true},
		{QuestionID: "q2", Text: "Answer"},
	}}
	if got := formatAskUserSubmittedSummary(loc, payload, skipped); got != "1. Tea?\nSkipped\n\n2. Notes?\nAnswer" {
		t.Fatalf("skipped summary = %q", got)
	}
}

func TestAskUserFreeTextTransitionMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		questions     []userinput.UIQuestion
		questionID    string
		wantCompleted bool
		wantIndex     int
		wantCustom    bool
	}{
		{
			name: "text advances from middle",
			questions: []userinput.UIQuestion{
				{ID: "q1", Text: "Notes?", Kind: userinput.QuestionKindText},
				{ID: "q2", Text: "Done?", Kind: userinput.QuestionKindText},
			},
			questionID: "q1",
			wantIndex:  1,
		},
		{
			name:          "text submits on last page",
			questions:     []userinput.UIQuestion{{ID: "q1", Text: "Notes?", Kind: userinput.QuestionKindText}},
			questionID:    "q1",
			wantCompleted: true,
		},
		{
			name: "single custom advances",
			questions: []userinput.UIQuestion{
				{ID: "q1", Text: "Plan?", Kind: userinput.QuestionKindSingleSelect, AllowCustom: true, Options: []userinput.UIOption{{ID: "q1.o1", Label: "A"}, {ID: "q1.o2", Label: "B"}}},
				{ID: "q2", Text: "Done?", Kind: userinput.QuestionKindText},
			},
			questionID: "q1",
			wantIndex:  1,
			wantCustom: true,
		},
		{
			name: "multi custom stays for more choices",
			questions: []userinput.UIQuestion{{
				ID: "q1", Text: "Languages?", Kind: userinput.QuestionKindMultiSelect, AllowCustom: true,
				Options: []userinput.UIOption{{ID: "q1.o1", Label: "Go"}, {ID: "q1.o2", Label: "Rust"}},
			}},
			questionID: "q1",
			wantCustom: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload := userinput.UIPayload{Questions: tt.questions}
			state := applyTestOp(t, payload, userinput.TextInteractionState{},
				userinput.InteractionOp{Kind: userinput.OpSetText, QuestionID: tt.questionID, Text: "custom answer"})
			if state.Completed != tt.wantCompleted {
				t.Fatalf("completed = %v, state = %#v", state.Completed, state)
			}
			if !tt.wantCompleted && state.QuestionIndex != tt.wantIndex {
				t.Fatalf("index = %d, want %d", state.QuestionIndex, tt.wantIndex)
			}
			answer, _ := state.Answer(tt.questionID)
			if tt.wantCustom && answer.CustomText != "custom answer" {
				t.Fatalf("custom = %#v", answer)
			}
			if !tt.wantCustom && answer.Text != "custom answer" {
				t.Fatalf("text = %#v", answer)
			}
		})
	}
}

func TestPrepareTelegramAskUserBuildsActions(t *testing.T) {
	t.Parallel()

	tc := &channel.StreamToolCall{
		Name: "ask_user", Locale: "zh",
		Input: map[string]any{
			"user_input_id": testAskUserRequestID,
			"payload": map[string]any{
				"questions": []any{
					map[string]any{
						"id":   "q1",
						"text": "Speed?",
						"kind": "single_select",
						"options": []any{
							map[string]any{"id": "q1.o1", "label": "Fast"},
							map[string]any{"id": "q1.o2", "label": "Slow"},
						},
						"allow_custom": true,
					},
				},
			},
		},
	}
	text, actions, requestID, ok := prepareTelegramAskUser(tc)
	if !ok || requestID != testAskUserRequestID {
		t.Fatalf("prepare ok=%v id=%q", ok, requestID)
	}
	if text != "Speed?" {
		t.Fatalf("text = %q", text)
	}
	foundOther := false
	for _, a := range actions {
		if strings.Contains(a.Label, "其他") {
			foundOther = true
			if !strings.HasPrefix(a.Value, "aui~x~") {
				t.Fatalf("Other callback = %q, want aui~x~", a.Value)
			}
		}
		if strings.Contains(a.Value, "/respond") {
			t.Fatalf("action leaked /respond: %#v", a)
		}
		if len(a.Value) > telegramMaxCallbackDataBytes {
			t.Fatalf("callback %q exceeds Telegram limit", a.Value)
		}
	}
	if !foundOther {
		t.Fatalf("missing 其他 action: %v", actionLabels(actions))
	}
}

func TestRenderAskUserChoiceShowsOnlyQuestion(t *testing.T) {
	t.Parallel()

	tc := &channel.StreamToolCall{
		Name: "ask_user",
		Input: map[string]any{
			"user_input_id": testAskUserRequestID,
			"payload": map[string]any{
				"questions": []any{
					map[string]any{
						"text": "你更喜欢哪种编程语言？",
						"kind": "single_select",
						"options": []any{
							map[string]any{"id": "q1.o1", "label": "Python"},
							map[string]any{"id": "q1.o2", "label": "Go"},
						},
					},
				},
			},
		},
		Actions: []channel.Action{
			{Type: "user_input", Label: "Python", Value: "respond:input-1:q1.o1"},
			{Type: "user_input", Label: "Go", Value: "respond:input-1:q1.o2"},
		},
	}
	text, _ := renderToolCallPresentation(tc, channel.BuildToolCallStart(tc))
	if text != "你更喜欢哪种编程语言？" {
		t.Fatalf("rendered ask_user prompt = %q", text)
	}
}

func TestAskUserNavChromeLayout(t *testing.T) {
	t.Parallel()

	loc := i18n.New("en")
	payload := userinput.UIPayload{Questions: []userinput.UIQuestion{
		{ID: "q1", Text: "Q1", Kind: userinput.QuestionKindText},
		{ID: "q2", Text: "Q2", Kind: userinput.QuestionKindText},
		{ID: "q3", Text: "Q3", Kind: userinput.QuestionKindText},
	}}

	_, actions := renderAskUserPage(testAskUserRequestID, loc, payload, userinput.TextInteractionState{})
	if !navRowEquals(actions, "1/3", "Skip →") {
		t.Fatalf("first = %v", actionLabels(actions))
	}

	middle := userinput.TextInteractionState{QuestionIndex: 1, Answers: []userinput.QuestionAnswer{{QuestionID: "q1", Text: "a"}}}
	_, actions = renderAskUserPage(testAskUserRequestID, loc, payload, middle)
	if !navRowEquals(actions, "←", "2/3", "Skip →") {
		t.Fatalf("middle = %v", actionLabels(actions))
	}

	last := userinput.TextInteractionState{QuestionIndex: 2, Answers: []userinput.QuestionAnswer{
		{QuestionID: "q1", Text: "a"}, {QuestionID: "q2", Text: "b"},
	}}
	_, actions = renderAskUserPage(testAskUserRequestID, loc, payload, last)
	if !navRowEquals(actions, "←", "3/3", "Skip and submit") {
		t.Fatalf("last = %v", actionLabels(actions))
	}
}

// applyTestOp drives the shared userinput transition the adapter relies on;
// rejects fail the test because these flows only use valid ops.
func applyTestOp(t *testing.T, payload userinput.UIPayload, state userinput.TextInteractionState, op userinput.InteractionOp) userinput.TextInteractionState {
	t.Helper()
	next, outcome := userinput.ApplyInteractionOp(payload, state, op)
	if outcome.Reject != userinput.RejectNone {
		t.Fatalf("op %#v rejected: %s", op, outcome.Reject)
	}
	return next
}

func actionLabelsContain(actions []channel.Action, want string) bool {
	for _, a := range actions {
		if a.Label == want || strings.Contains(a.Label, want) {
			return true
		}
	}
	return false
}

func actionLabels(actions []channel.Action) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Label)
	}
	return out
}

// navRowEquals checks that the highest-numbered action row is exactly the
// expected left-to-right labels (page chrome).
func navRowEquals(actions []channel.Action, want ...string) bool {
	if len(actions) == 0 || len(want) == 0 {
		return false
	}
	maxRow := actions[0].Row
	for _, a := range actions {
		if a.Row > maxRow {
			maxRow = a.Row
		}
	}
	var got []string
	for _, a := range actions {
		if a.Row == maxRow {
			got = append(got, a.Label)
		}
	}
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
