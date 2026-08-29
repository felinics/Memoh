package application

import (
	"testing"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func TestContainsUserTextDetectsPreparedSteer(t *testing.T) {
	inputs := []messagepkg.PersistInput{
		{Role: "assistant", Content: []byte(`{"role":"assistant","content":"before"}`)},
		{Role: "user", Content: []byte(`{"role":"user","content":"adjust the plan"}`)},
		{Role: "assistant", Content: []byte(`{"role":"assistant","content":"after"}`)},
	}
	if !containsUserText(inputs, "adjust the plan") {
		t.Fatal("prepared steer user message was not detected")
	}
	if containsUserText(inputs, "different steer") {
		t.Fatal("unrelated user message was reported as the prepared steer")
	}
}
