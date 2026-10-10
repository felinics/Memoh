package application

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
)

func TestDiscussAgentAdmissionIncludesFinalPromptFraming(t *testing.T) {
	messages := make([]turn.DiscussMessage, 1000)
	for i := range messages {
		messages[i] = turn.DiscussMessage{Role: "user", Content: "abcd"}
	}
	messages[0] = turn.DiscussMessage{Role: "user", Content: "summary", CompactionArtifactID: "summary"}
	admitted, admission := admitDiscussAgentMessages(messages, 1000)
	if admission.ProtectedOverflow {
		t.Fatal("summary and newest source fit")
	}
	actual := turn.EstimateTokensFromBytes(len(discussAgentFullContextPrompt(admitted)))
	if actual > 1000 || admission.SelectedTokens != actual {
		t.Fatalf("final ACP prompt=%d selected=%d, budget=1000", actual, admission.SelectedTokens)
	}
	if len(admitted) < 2 || admitted[0].CompactionArtifactID != "summary" || admitted[len(admitted)-1].Content != "abcd" {
		t.Fatal("lost protected sources")
	}
}

func TestDiscussAgentAdmissionProtectsFinalPrompt(t *testing.T) {
	for _, messages := range [][]turn.DiscussMessage{
		{{Role: "user", Content: "current"}},
		{{Role: "user", Content: strings.Repeat("s", 4000), CompactionArtifactID: "summary"}, {Role: "user", Content: "current"}},
	} {
		if admitted, decision := admitDiscussAgentMessages(messages, 10); admitted != nil || !decision.ProtectedOverflow {
			t.Fatalf("prompt wrapper or protected sources exceed budget: %+v", decision)
		}
	}
}

func FuzzDiscussAgentAdmissionBoundsFinalPrompt(f *testing.F) {
	f.Add([]byte("a群\n test"), uint16(100))
	f.Fuzz(func(t *testing.T, data []byte, limit uint16) {
		if len(data) > 1024 {
			return
		}
		messages := make([]turn.DiscussMessage, len(data))
		for i, size := range data {
			messages[i] = turn.DiscussMessage{Role: " user ", Content: " \n" + strings.Repeat("群", int(size)%13) + "\n "}
			if i%17 == 0 {
				messages[i].CompactionArtifactID = "summary"
			}
		}
		budget := int(limit) + 1
		admitted, decision := admitDiscussAgentMessages(messages, budget)
		if decision.ProtectedOverflow {
			return
		}
		actual := turn.EstimateTokensFromBytes(len(discussAgentFullContextPrompt(admitted)))
		if actual > budget || actual != decision.SelectedTokens {
			t.Fatalf("actual=%d budget=%d selected=%d", actual, budget, decision.SelectedTokens)
		}
	})
}
