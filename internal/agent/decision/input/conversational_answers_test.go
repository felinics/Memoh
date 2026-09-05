package input

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestStoredSelectAnswerPolicy(t *testing.T) {
	for _, source := range []string{"", ProviderSourceACPMCP, ProviderSourceACPElicitation, ProviderSourceCodexElicitation, ProviderSourceCodexUserInput, "unknown"} {
		t.Run(source, func(t *testing.T) {
			queries := newFakeUserInputQueries()
			svc := NewService(slog.New(slog.DiscardHandler), queries)
			req, err := svc.CreatePending(context.Background(), CreatePendingInput{BotID: storeTestBotID, SessionID: storeTestSessionID, ToolCallID: "free-reply", ProviderMetadata: map[string]any{"source": source}, Input: map[string]any{"questions": []any{map[string]any{"text": "Choose?", "kind": QuestionKindSingleSelect, "options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}, "allow_custom": false}}}})
			if err != nil {
				t.Fatal(err)
			}
			native := source == "" || source == ProviderSourceACPMCP
			if req.UIPayload.Questions[0].AllowCustom != native {
				t.Fatal("incorrect creation policy")
			}
			// Simulate an already-issued card whose stored payload still disallows text.
			req.UIPayload.Questions[0].AllowCustom = false
			encoded, err := json.Marshal(req.UIPayload)
			if err != nil {
				t.Fatal(err)
			}
			queries.rows[req.ID].UiPayloadJson = encoded
			result, err := svc.AdvanceText(context.Background(), AdvanceTextInput{BotID: storeTestBotID, SessionID: storeTestSessionID, ExplicitID: req.ID, Text: "0"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Invalid == native || result.Request.Interaction.Completed != native {
				t.Fatalf("answer policy: %#v", result)
			}
			submitted, err := svc.Submit(context.Background(), SubmitInput{RequestID: req.ID, Answers: []QuestionAnswer{{QuestionID: "q1", CustomText: "0"}}})
			if !native {
				if err == nil {
					t.Fatal("external form accepted an unsupported answer")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			answers := AnswersFromResult(submitted.Result)
			if len(answers) != 1 || answers[0].CustomText != "0" || len(answers[0].Selected) != 0 {
				t.Fatalf("LLM did not receive original reply: %#v", answers)
			}
		})
	}
}

func TestConversationalMultiSelectPreservesWholeReply(t *testing.T) {
	q := UIQuestion{ID: "q1", Kind: QuestionKindMultiSelect, AllowCustom: true, Options: []UIOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}}
	for _, raw := range []string{"0", "A, but I want something else", ",", "我都不选，换一个问题"} {
		answer, err := parseTextAnswer(q, raw)
		if err != nil || answer.CustomText != raw || len(answer.OptionIDs) != 0 {
			t.Fatalf("%q: %#v, %v", raw, answer, err)
		}
		result, err := submittedResult(UIPayload{Questions: []UIQuestion{q}}, []QuestionAnswer{answer})
		if err != nil || AnswersFromResult(result)[0].CustomText != raw {
			t.Fatalf("result lost the reply: %#v, %v", result, err)
		}
	}
	answer, err := parseTextAnswer(q, "1, 2")
	if err != nil || len(answer.OptionIDs) != 2 || answer.CustomText != "" {
		t.Fatalf("selection shortcut: %#v, %v", answer, err)
	}
}
