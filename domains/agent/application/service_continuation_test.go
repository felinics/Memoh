package application

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
	userinput "github.com/memohai/memoh/domains/agent/decision/input"
	"github.com/memohai/memoh/domains/agent/engine"
)

func TestPrepareContinuationRunConfigReplacesStaleContextAndSetsCapabilities(t *testing.T) {
	t.Parallel()

	resolver := &Service{userInput: &userinput.Service{}}
	eventCh := make(chan WSStreamEvent)
	base := engine.RunConfig{
		Query:    "stale query",
		Messages: []sdk.Message{sdk.UserMessage("stale context")},
		ContextFrags: []fragment.ContextFrag{{
			ID:   "stale-fragment",
			Kind: fragment.KindConversationEvent,
		}},
	}

	got, err := resolver.prepareContinuationRunConfig(
		context.Background(),
		base,
		history.ScopeFallback{},
		fragment.Scope{},
		eventCh,
	)
	if err != nil {
		t.Fatalf("prepareContinuationRunConfig() error = %v", err)
	}
	if got.Query != "" || len(got.Messages) != 0 {
		t.Fatalf("continuation retained stale context: %#v", got)
	}
	for _, frag := range got.ContextFrags {
		if frag.ID == "stale-fragment" {
			t.Fatalf("continuation retained stale fragment: %#v", frag)
		}
	}
	if !got.LiveToolStream || !got.CanRequestUserInput {
		t.Fatalf("continuation capabilities = live:%v input:%v, want true/true", got.LiveToolStream, got.CanRequestUserInput)
	}
}

func TestPrepareContinuationRunConfigPropagatesArtifactProjectionFailure(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("artifact projection unavailable")
	resolver := serviceWithCompactionReads(&recordingCompactionLogQueries{listErr: sentinel})
	got, err := resolver.prepareContinuationRunConfig(
		context.Background(),
		engine.RunConfig{Query: "must not survive"},
		history.ScopeFallback{},
		fragment.Scope{SessionID: "00000000-0000-0000-0000-00000000f401"},
		nil,
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("prepareContinuationRunConfig() error = %v, want %v", err, sentinel)
	}
	if got.Query != "" || len(got.Messages) != 0 || len(got.ContextFrags) != 0 {
		t.Fatalf("failed continuation returned partial config: %#v", got)
	}
}
