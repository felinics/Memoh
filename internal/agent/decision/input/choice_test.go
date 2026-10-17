package input

import (
	"reflect"
	"testing"
)

func TestChoiceAnswersNeedEveryQuestionAnswered(t *testing.T) {
	t.Parallel()

	result := map[string]any{"answers": []any{
		map[string]any{"question_id": "q1", "selected": []any{map[string]any{"label": "A"}}, "custom_text": "and C"},
		map[string]any{"question_id": "q2", "text": "free text"},
	}}
	answers, ok := ChoiceAnswers(result, 2)
	if !ok || !reflect.DeepEqual(answers, []string{"A, and C", "free text"}) {
		t.Fatalf("answers = %#v, %v", answers, ok)
	}
	if _, ok := ChoiceAnswers(result, 3); ok {
		t.Fatal("an unanswered question must leave the request unanswered")
	}
}
