package input

import (
	"errors"
	"fmt"
	"testing"
)

func selectPayload() UIPayload {
	return UIPayload{
		Version: PayloadVersion,
		Questions: []UIQuestion{
			{
				ID:   "q1",
				Text: "Which plan?",
				Kind: QuestionKindSingleSelect,
				Options: []UIOption{
					{ID: "q1.o1", Label: "Plan A"},
					{ID: "q1.o2", Label: "Plan B"},
				},
				AllowCustom: true,
			},
			{
				ID:   "q2",
				Text: "Which features?",
				Kind: QuestionKindMultiSelect,
				Options: []UIOption{
					{ID: "q2.o1", Label: "Search"},
					{ID: "q2.o2", Label: "Sync"},
				},
			},
			{
				ID:   "q3",
				Text: "Anything else?",
				Kind: QuestionKindText,
			},
		},
	}
}

func TestSubmittedResultBuildsAnswers(t *testing.T) {
	t.Parallel()

	result, err := submittedResult(selectPayload(), []QuestionAnswer{
		{QuestionID: "q1", OptionIDs: []string{"q1.o2"}},
		{QuestionID: "q2", OptionIDs: []string{"q2.o1", "q2.o2"}},
		{QuestionID: "q3", Text: "ship it"},
	})
	if err != nil {
		t.Fatalf("submitted result: %v", err)
	}
	if result["status"] != StatusSubmitted {
		t.Fatalf("status = %#v", result["status"])
	}
	if result["instruction"] == "" {
		t.Fatalf("missing instruction: %#v", result)
	}
	answers, ok := result["answers"].([]map[string]any)
	if !ok || len(answers) != 3 {
		t.Fatalf("unexpected answers: %#v", result["answers"])
	}
	selected, ok := answers[0]["selected"].([]map[string]any)
	if !ok || len(selected) != 1 || selected[0]["id"] != "q1.o2" || selected[0]["label"] != "Plan B" {
		t.Fatalf("unexpected q1 selection: %#v", answers[0])
	}
	multi, ok := answers[1]["selected"].([]map[string]any)
	if !ok || len(multi) != 2 {
		t.Fatalf("unexpected q2 selection: %#v", answers[1])
	}
	if answers[2]["text"] != "ship it" || answers[2]["question"] != "Anything else?" {
		t.Fatalf("unexpected q3 answer: %#v", answers[2])
	}
}

func TestSubmittedResultCustomText(t *testing.T) {
	t.Parallel()

	result, err := submittedResult(selectPayload(), []QuestionAnswer{
		{QuestionID: "q1", CustomText: "my own plan"},
		{QuestionID: "q2", OptionIDs: []string{"q2.o1"}},
		{QuestionID: "q3", Text: "nothing else"},
	})
	if err != nil {
		t.Fatalf("submitted result: %v", err)
	}
	answers := result["answers"].([]map[string]any)
	if answers[0]["custom_text"] != "my own plan" {
		t.Fatalf("unexpected custom answer: %#v", answers[0])
	}
	if _, hasSelected := answers[0]["selected"]; hasSelected {
		t.Fatalf("custom answer should not carry selections: %#v", answers[0])
	}
}

func TestSubmittedResultSkippedQuestion(t *testing.T) {
	t.Parallel()

	result, err := submittedResult(selectPayload(), []QuestionAnswer{
		{QuestionID: "q1", OptionIDs: []string{"q1.o1"}},
		{QuestionID: "q2", Skipped: true},
		{QuestionID: "q3", Text: "done"},
	})
	if err != nil {
		t.Fatalf("submitted result: %v", err)
	}
	answers := result["answers"].([]map[string]any)
	if answers[1]["skipped"] != true || answers[1]["question"] != "Which features?" {
		t.Fatalf("unexpected skipped answer: %#v", answers[1])
	}
}

func TestSubmittedResultValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		answers []QuestionAnswer
	}{
		{"missing answer", []QuestionAnswer{
			{QuestionID: "q1", OptionIDs: []string{"q1.o1"}},
			{QuestionID: "q2", OptionIDs: []string{"q2.o1"}},
		}},
		{"unknown question", []QuestionAnswer{
			{QuestionID: "q9", OptionIDs: []string{"q1.o1"}},
		}},
		{"duplicate answer", []QuestionAnswer{
			{QuestionID: "q1", OptionIDs: []string{"q1.o1"}},
			{QuestionID: "q1", OptionIDs: []string{"q1.o2"}},
			{QuestionID: "q2", OptionIDs: []string{"q2.o1"}},
			{QuestionID: "q3", Text: "ok"},
		}},
		{"unknown option", []QuestionAnswer{
			{QuestionID: "q1", OptionIDs: []string{"nope"}},
			{QuestionID: "q2", OptionIDs: []string{"q2.o1"}},
			{QuestionID: "q3", Text: "ok"},
		}},
		{"multiple options on single select", []QuestionAnswer{
			{QuestionID: "q1", OptionIDs: []string{"q1.o1", "q1.o2"}},
			{QuestionID: "q2", OptionIDs: []string{"q2.o1"}},
			{QuestionID: "q3", Text: "ok"},
		}},
		{"option and custom on single select", []QuestionAnswer{
			{QuestionID: "q1", OptionIDs: []string{"q1.o1"}, CustomText: "extra"},
			{QuestionID: "q2", OptionIDs: []string{"q2.o1"}},
			{QuestionID: "q3", Text: "ok"},
		}},
		{"custom text without allow_custom", []QuestionAnswer{
			{QuestionID: "q1", OptionIDs: []string{"q1.o1"}},
			{QuestionID: "q2", CustomText: "extra"},
			{QuestionID: "q3", Text: "ok"},
		}},
		{"empty selection", []QuestionAnswer{
			{QuestionID: "q1"},
			{QuestionID: "q2", OptionIDs: []string{"q2.o1"}},
			{QuestionID: "q3", Text: "ok"},
		}},
		{"text on select question", []QuestionAnswer{
			{QuestionID: "q1", Text: "Plan A"},
			{QuestionID: "q2", OptionIDs: []string{"q2.o1"}},
			{QuestionID: "q3", Text: "ok"},
		}},
		{"skipped with answer", []QuestionAnswer{
			{QuestionID: "q1", OptionIDs: []string{"q1.o1"}},
			{QuestionID: "q2", OptionIDs: []string{"q2.o1"}, Skipped: true},
			{QuestionID: "q3", Text: "ok"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := submittedResult(selectPayload(), tc.answers); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestCanceledResultIsToolResultPayload(t *testing.T) {
	t.Parallel()

	result := canceledResult("")
	if result["status"] != StatusCanceled {
		t.Fatalf("status = %#v", result["status"])
	}
	if result["reason"] != "user_canceled" {
		t.Fatalf("unexpected cancel reason: %#v", result["reason"])
	}
	if result["instruction"] == "" {
		t.Fatalf("missing instruction: %#v", result)
	}
}

func TestServiceCanRespond(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil)
	pending := Request{ID: "req-1", Status: StatusPending}
	if svc.CanRespond(pending) {
		t.Fatal("waiter-backed request without a live waiter should not be answerable")
	}
	release := svc.RegisterWaiter(pending.ID)
	if !svc.CanRespond(pending) {
		t.Fatal("waiter-backed request with a live waiter should be answerable")
	}
	release()
	if svc.CanRespond(pending) {
		t.Fatal("request should stop being answerable after waiter release")
	}

	terminal := pending
	terminal.Status = StatusSubmitted
	release = svc.RegisterWaiter(terminal.ID)
	defer release()
	if svc.CanRespond(terminal) {
		t.Fatal("terminal request should not be answerable even with a waiter")
	}
}

func TestParseAskUserPayloadGeneratesIDs(t *testing.T) {
	t.Parallel()

	payload, err := ParseAskUserPayload(map[string]any{
		"questions": []any{
			map[string]any{
				"text": "Which plan?",
				"kind": "single_select",
				"options": []any{
					map[string]any{"label": "Plan A", "description": "cheap"},
					map[string]any{"label": "Plan B"},
				},
				"allow_custom": true,
			},
			map[string]any{
				"text": "Anything else?",
				"kind": "text",
			},
		},
	})
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if payload.Version != PayloadVersion || len(payload.Questions) != 2 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	q1 := payload.Questions[0]
	if q1.ID != "q1" || q1.Kind != QuestionKindSingleSelect || !q1.AllowCustom {
		t.Fatalf("unexpected q1: %#v", q1)
	}
	if len(q1.Options) != 2 || q1.Options[0].ID != "q1.o1" || q1.Options[0].Label != "Plan A" || q1.Options[0].Description != "cheap" {
		t.Fatalf("unexpected q1 options: %#v", q1.Options)
	}
	q2 := payload.Questions[1]
	if q2.ID != "q2" || q2.Kind != QuestionKindText {
		t.Fatalf("unexpected q2: %#v", q2)
	}
}

func TestParseAskUserPayloadRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	twoOptions := []any{
		map[string]any{"label": "A"},
		map[string]any{"label": "B"},
	}
	cases := []struct {
		name  string
		input any
	}{
		{"nil input", nil},
		{"empty object", map[string]any{}},
		{"legacy v1 shape", map[string]any{"question": "Which plan?", "options": []any{"A", "B"}}},
		{"empty questions", map[string]any{"questions": []any{}}},
		{"too many questions", map[string]any{"questions": []any{
			map[string]any{"text": "1", "kind": "text"},
			map[string]any{"text": "2", "kind": "text"},
			map[string]any{"text": "3", "kind": "text"},
			map[string]any{"text": "4", "kind": "text"},
			map[string]any{"text": "5", "kind": "text"},
		}}},
		{"missing text", map[string]any{"questions": []any{
			map[string]any{"kind": "text"},
		}}},
		{"missing kind", map[string]any{"questions": []any{
			map[string]any{"text": "Which plan?", "options": twoOptions},
		}}},
		{"alias kind", map[string]any{"questions": []any{
			map[string]any{"text": "Which plan?", "kind": "multi", "options": twoOptions},
		}}},
		{"select without options", map[string]any{"questions": []any{
			map[string]any{"text": "Which plan?", "kind": "single_select"},
		}}},
		{"select with one option", map[string]any{"questions": []any{
			map[string]any{"text": "Which plan?", "kind": "single_select", "options": []any{map[string]any{"label": "A"}}},
		}}},
		{"string option", map[string]any{"questions": []any{
			map[string]any{"text": "Which plan?", "kind": "single_select", "options": []any{"A", "B"}},
		}}},
		{"option without label", map[string]any{"questions": []any{
			map[string]any{"text": "Which plan?", "kind": "single_select", "options": []any{map[string]any{"label": "A"}, map[string]any{"description": "no label"}}},
		}}},
		{"options on text question", map[string]any{"questions": []any{
			map[string]any{"text": "Say more", "kind": "text", "options": twoOptions},
		}}},
		{"allow_custom on text question", map[string]any{"questions": []any{
			map[string]any{"text": "Say more", "kind": "text", "allow_custom": true},
		}}},
		{"non-string text", map[string]any{"questions": []any{
			map[string]any{"text": 123, "kind": "text"},
		}}},
		{"non-string placeholder", map[string]any{"questions": []any{
			map[string]any{"text": "Say more", "kind": "text", "placeholder": 7},
		}}},
		{"non-string option label", map[string]any{"questions": []any{
			map[string]any{"text": "Which plan?", "kind": "single_select", "options": []any{
				map[string]any{"label": 1}, map[string]any{"label": "B"},
			}},
		}}},
		{"unknown top-level field", map[string]any{
			"questions": []any{map[string]any{"text": "Say more", "kind": "text"}},
			"multiple":  true,
		}},
		{"unknown question field", map[string]any{"questions": []any{
			map[string]any{"text": "Which plan?", "kind": "single_select", "options": twoOptions, "input_type": "text"},
		}}},
		{"unknown option field", map[string]any{"questions": []any{
			map[string]any{"text": "Which plan?", "kind": "single_select", "options": []any{
				map[string]any{"label": "A", "value": "a"}, map[string]any{"label": "B"},
			}},
		}}},
		{"too many options", map[string]any{"questions": []any{
			map[string]any{"text": "Pick", "kind": "single_select", "options": manyOptions(21)},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseAskUserPayload(tc.input)
			if err == nil {
				t.Fatalf("expected error for %#v", tc.input)
			}
			if !errors.Is(err, ErrInvalidAskUserInput) {
				t.Fatalf("expected ErrInvalidAskUserInput, got %v", err)
			}
		})
	}
}

func manyOptions(n int) []any {
	options := make([]any, 0, n)
	for i := 0; i < n; i++ {
		options = append(options, map[string]any{"label": fmt.Sprintf("Option %d", i+1)})
	}
	return options
}

func TestPayloadFromStoredDecodesV2(t *testing.T) {
	t.Parallel()

	payload := PayloadFromStored(map[string]any{
		"version": 2,
		"questions": []any{
			map[string]any{
				"id":   "q1",
				"text": "Which features?",
				"kind": "multi_select",
				"options": []any{
					map[string]any{"id": "q1.o1", "label": "Search"},
					map[string]any{"id": "q1.o2", "label": "Sync"},
				},
			},
		},
	})
	if len(payload.Questions) != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	q := payload.Questions[0]
	if q.ID != "q1" || q.Kind != QuestionKindMultiSelect {
		t.Fatalf("unexpected question: %#v", q)
	}
	if len(q.Options) != 2 || q.Options[1].Label != "Sync" {
		t.Fatalf("unexpected options: %#v", q.Options)
	}
}

func TestPayloadFromStoredUpgradesLegacySingleSelect(t *testing.T) {
	t.Parallel()

	payload := PayloadFromStored(map[string]any{
		"question": "Which plan?",
		"options": []any{
			map[string]any{"id": "a", "label": "Plan A", "value": "A"},
			map[string]any{"id": "custom", "label": "Custom answer", "input_type": "text", "placeholder": "Type an answer"},
		},
	})
	if len(payload.Questions) != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	q := payload.Questions[0]
	if q.Kind != QuestionKindSingleSelect || q.Text != "Which plan?" {
		t.Fatalf("unexpected question: %#v", q)
	}
	// The v1 custom-text option becomes question-level allow_custom.
	if !q.AllowCustom || q.Placeholder != "Type an answer" {
		t.Fatalf("expected custom option upgrade: %#v", q)
	}
	if len(q.Options) != 1 || q.Options[0].ID != "a" || q.Options[0].Label != "Plan A" {
		t.Fatalf("unexpected options: %#v", q.Options)
	}
}

func TestPayloadFromStoredUpgradesLegacyMultipleAndText(t *testing.T) {
	t.Parallel()

	multi := PayloadFromStored(map[string]any{
		"question": "Pick plans",
		"multiple": true,
		"options": []any{
			map[string]any{"id": "a", "label": "Plan A"},
			map[string]any{"id": "b", "label": "Plan B"},
		},
	})
	if multi.Questions[0].Kind != QuestionKindMultiSelect {
		t.Fatalf("expected multi_select upgrade: %#v", multi.Questions[0])
	}

	text := PayloadFromStored(map[string]any{
		"question":   "What do you think?",
		"input_type": "text",
	})
	if text.Questions[0].Kind != QuestionKindText || text.Questions[0].AllowCustom {
		t.Fatalf("expected text upgrade: %#v", text.Questions[0])
	}
}

func TestValidateAskUserInputRejectsMissingQuestions(t *testing.T) {
	t.Parallel()

	for _, input := range []any{nil, map[string]any{}, map[string]any{"question": "old shape"}} {
		if err := ValidateAskUserInput(input); err == nil {
			t.Fatalf("expected invalid input error for %#v", input)
		}
	}
}
