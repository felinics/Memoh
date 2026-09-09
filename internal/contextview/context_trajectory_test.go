package contextview

import (
	"context"
	"strings"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/context/trajectory"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

type contextTrajectoryTestSink struct {
	events []trajectory.Event
	texts  map[string]string
}

func (s *contextTrajectoryTestSink) Append(_ context.Context, event trajectory.Event, contents []trajectory.Content) error {
	if s.texts == nil {
		s.texts = make(map[string]string)
	}
	for _, content := range contents {
		s.texts[content.Hash] = string(content.Data)
	}
	s.events = append(s.events, event)
	return nil
}

func TestProviderTrajectoryPreservesSourceAndSelectedCurrentUser(t *testing.T) {
	sink := &contextTrajectoryTestSink{}
	holder := contextfrag.NewLifecycleHolder()
	recorder := trajectory.NewRecorder(sink)
	recorder.Bind("run", "session")
	holder.SetTrajectoryRecorder(recorder)
	frags := append(prefixSystemFrags(), currentMessageFrag("current", "ORIGINAL_CURRENT_USER"))
	ApplyProviderRunConfig(t.Context(), nil, agentpkg.RunConfig{ContextSourceFrags: frags, ContextLifecycle: holder})
	for _, stage := range []string{"context_collected", "context_selected"} {
		var content strings.Builder
		for _, event := range sink.events {
			if event.Stage != stage {
				continue
			}
			for _, block := range event.Blocks {
				for _, hash := range block.Chunks {
					content.WriteString(sink.texts[hash])
				}
			}
		}
		if !strings.Contains(content.String(), "ORIGINAL_CURRENT_USER") {
			t.Fatalf("%s lost the original current user fragment", stage)
		}
	}
}

func TestProviderTrajectoryKeepsRemovedFragmentBodyAtCollection(t *testing.T) {
	sink := &contextTrajectoryTestSink{}
	holder := contextfrag.NewLifecycleHolder()
	recorder := trajectory.NewRecorder(sink)
	recorder.Bind("run", "session")
	holder.SetTrajectoryRecorder(recorder)
	cfg := capabilityGateFixture()
	cfg.ContextLifecycle = holder
	got := ApplyProviderRunConfig(t.Context(), nil, cfg)
	if got.System != "base system" {
		t.Fatal("fixture did not remove unavailable skill guidance")
	}
	foundSource, foundDecision := false, false
	for _, event := range sink.events {
		for _, block := range event.Blocks {
			if block.Label == "system.skill.alpha" {
				if event.Stage != "context_collected" {
					t.Fatal("removed skill was presented as selected")
				}
				var text strings.Builder
				for _, hash := range block.Chunks {
					text.WriteString(sink.texts[hash])
				}
				foundSource = strings.Contains(text.String(), "Alpha 用法")
			}
			if event.Stage == "context_selected" && block.Kind == "selection" {
				var text strings.Builder
				for _, hash := range block.Chunks {
					text.WriteString(sink.texts[hash])
				}
				foundDecision = strings.Contains(text.String(), `"decision":"dropped"`)
			}
		}
	}
	if !foundSource || !foundDecision {
		t.Fatal("selection lost the removed body or its exclusion decision")
	}
}
