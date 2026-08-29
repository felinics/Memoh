package native

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/messageconv"
)

// An interrupted step is persisted as a checkpoint and replayed on the next
// turn, so it has to preserve reasoning the way a completed step does: one part
// per block, each keeping its own dialect and opaque token. Flattening the
// blocks here would leave the checkpoint unreplayable even though the provider
// side is correct — and nothing would fail to compile.

func reasoningPartsOf(t *testing.T, step *sdk.StepResult) []sdk.ReasoningPart {
	t.Helper()
	if step == nil {
		t.Fatal("no snapshot produced")
	}
	var parts []sdk.ReasoningPart
	for _, part := range step.Messages[0].Content {
		if rp, ok := part.(sdk.ReasoningPart); ok {
			parts = append(parts, rp)
		}
	}
	return parts
}

func anthropicMeta(key, value string) map[string]any {
	return map[string]any{"anthropic": map[string]any{key: value}}
}

func googleMeta(key, value string) map[string]any {
	return map[string]any{"google": map[string]any{key: value}}
}

func TestInterruptedStepKeepsTextProviderMetadata(t *testing.T) {
	var capture interruptedStepCapture
	for _, part := range []sdk.StreamPart{
		&sdk.TextStartPart{ID: "t1"},
		&sdk.TextDeltaPart{ID: "t1", Text: "answer"},
		&sdk.TextEndPart{
			ID:               "t1",
			ProviderMetadata: googleMeta("thoughtSignature", "SIG_TEXT"),
		},
	} {
		capture.observe(part)
	}

	step := capture.snapshot(0)
	if step == nil {
		t.Fatal("no snapshot produced")
	}
	persisted := messageconv.SDKMessagesToModelMessages(step.Messages)
	if len(persisted) != 1 {
		t.Fatalf("persisted messages: got %d, want 1", len(persisted))
	}
	replayed := messageconv.ModelMessageToSDKMessage(persisted[0])
	text, ok := replayed.Content[0].(sdk.TextPart)
	if !ok {
		t.Fatalf("content[0] = %T, want TextPart", replayed.Content[0])
	}
	gm, _ := text.ProviderMetadata["google"].(map[string]any)
	if sig, _ := gm["thoughtSignature"].(string); sig != "SIG_TEXT" {
		t.Errorf("thought signature: got %q, want SIG_TEXT", sig)
	}
}

func TestInterruptedStepKeepsEveryReasoningBlockToken(t *testing.T) {
	var capture interruptedStepCapture
	for _, part := range []sdk.StreamPart{
		&sdk.ReasoningStartPart{ID: "b1", Format: sdk.ReasoningFormatAnthropic},
		&sdk.ReasoningDeltaPart{ID: "b1", Text: "AAA", Format: sdk.ReasoningFormatAnthropic},
		&sdk.ReasoningEndPart{
			ID: "b1", Format: sdk.ReasoningFormatAnthropic,
			ProviderMetadata: anthropicMeta("signature", "SIG_A"),
		},
		&sdk.ReasoningStartPart{ID: "b2", Format: sdk.ReasoningFormatAnthropic},
		&sdk.ReasoningDeltaPart{ID: "b2", Text: "BBB", Format: sdk.ReasoningFormatAnthropic},
		&sdk.ReasoningEndPart{
			ID: "b2", Format: sdk.ReasoningFormatAnthropic,
			ProviderMetadata: anthropicMeta("signature", "SIG_B"),
		},
		&sdk.TextDeltaPart{Text: "answer"},
	} {
		capture.observe(part)
	}

	parts := reasoningPartsOf(t, capture.snapshot(0))
	if len(parts) != 2 {
		t.Fatalf("reasoning parts: got %d, want 2 — blocks were merged", len(parts))
	}
	for i, want := range []struct{ text, sig string }{{"AAA", "SIG_A"}, {"BBB", "SIG_B"}} {
		if parts[i].Text != want.text {
			t.Errorf("part %d text: got %q, want %q", i, parts[i].Text, want.text)
		}
		if parts[i].Format != sdk.ReasoningFormatAnthropic {
			t.Errorf("part %d format: got %q, want %q", i, parts[i].Format, sdk.ReasoningFormatAnthropic)
		}
		am, _ := parts[i].ProviderMetadata["anthropic"].(map[string]any)
		if sig, _ := am["signature"].(string); sig != want.sig {
			t.Errorf("part %d signature: got %q, want %q", i, sig, want.sig)
		}
	}
}

// A reasoning block's opaque token is bound to the model that produced it.
// Stream parts may report that model at different points, so later non-empty
// values replace earlier aliases while omitted values must not erase it.
func TestInterruptedStepKeepsReasoningBlockModel(t *testing.T) {
	var capture interruptedStepCapture
	capture.observe(&sdk.ReasoningStartPart{
		ID: "b1", Model: "claude-sonnet-4", Format: sdk.ReasoningFormatAnthropic,
	})
	capture.observe(&sdk.ReasoningDeltaPart{
		ID: "b1", Text: "thought", Format: sdk.ReasoningFormatAnthropic,
	})
	if got := capture.reasoningBlocks.parts[0].Model; got != "claude-sonnet-4" {
		t.Fatalf("empty delta model erased start model: got %q", got)
	}
	capture.observe(&sdk.ReasoningDeltaPart{
		ID: "b1", Model: "anthropic/claude-sonnet-4", Format: sdk.ReasoningFormatAnthropic,
	})
	if got := capture.reasoningBlocks.parts[0].Model; got != "anthropic/claude-sonnet-4" {
		t.Fatalf("delta model was not merged: got %q", got)
	}
	capture.observe(&sdk.ReasoningEndPart{
		ID: "b1", Model: "claude-sonnet-4-20250514", Format: sdk.ReasoningFormatAnthropic,
		ProviderMetadata: anthropicMeta("signature", "SIG"),
	})

	step := capture.snapshot(0)
	if step == nil {
		t.Fatal("no snapshot produced")
	}
	if got := step.ReasoningParts[0].Model; got != "claude-sonnet-4-20250514" {
		t.Errorf("ReasoningParts[0].Model: got %q, want response model", got)
	}
	parts := reasoningPartsOf(t, step)
	if got := parts[0].Model; got != "claude-sonnet-4-20250514" {
		t.Errorf("message reasoning model: got %q, want response model", got)
	}
}

// A start marker only announces a block; without text or provider metadata it
// contains nothing that can be replayed and should not create a checkpoint.
func TestInterruptedStepDropsEmptyReasoningStart(t *testing.T) {
	var capture interruptedStepCapture
	capture.observe(&sdk.ReasoningStartPart{
		ID: "b1", Model: "claude-sonnet-4-20250514", Format: sdk.ReasoningFormatAnthropic,
	})
	capture.observe(&sdk.ReasoningDeltaPart{
		ID: "b1", Text: "   ", Format: sdk.ReasoningFormatAnthropic,
	})

	if step := capture.snapshot(0); step != nil {
		t.Fatalf("empty reasoning start produced a snapshot: %+v", step)
	}
}

// A redacted thinking block has no text at all. Its payload lives entirely in
// metadata and must still reach the checkpoint.
func TestInterruptedStepKeepsEmptyTextReasoningBlock(t *testing.T) {
	var capture interruptedStepCapture
	for _, part := range []sdk.StreamPart{
		&sdk.ReasoningStartPart{ID: "r1", Format: sdk.ReasoningFormatAnthropic},
		&sdk.ReasoningEndPart{
			ID: "r1", Format: sdk.ReasoningFormatAnthropic,
			ProviderMetadata: anthropicMeta("redactedData", "BLOB"),
		},
		&sdk.TextDeltaPart{Text: "answer"},
	} {
		capture.observe(part)
	}

	parts := reasoningPartsOf(t, capture.snapshot(0))
	if len(parts) != 1 {
		t.Fatalf("reasoning parts: got %d, want 1 — empty-text block was dropped", len(parts))
	}
	am, _ := parts[0].ProviderMetadata["anthropic"].(map[string]any)
	if data, _ := am["redactedData"].(string); data != "BLOB" {
		t.Errorf("redactedData: got %q, want BLOB", data)
	}
}

// Reasoning alone, with no answer text yet, is still worth checkpointing: it is
// exactly what an interruption mid-thinking leaves behind.
func TestInterruptedStepSnapshotsReasoningWithoutText(t *testing.T) {
	var capture interruptedStepCapture
	capture.observe(&sdk.ReasoningStartPart{ID: "b1", Format: sdk.ReasoningFormatAnthropic})
	capture.observe(&sdk.ReasoningEndPart{
		ID: "b1", Format: sdk.ReasoningFormatAnthropic,
		ProviderMetadata: anthropicMeta("redactedData", "BLOB"),
	})

	step := capture.snapshot(0)
	if step == nil {
		t.Fatal("reasoning-only interruption produced no snapshot")
	}
	if len(step.ReasoningParts) != 1 {
		t.Fatalf("ReasoningParts: got %d, want 1", len(step.ReasoningParts))
	}
}

// Reasoning leads the assistant message: providers enforce thinking-first
// ordering, and a trailing reasoning item is rejected outright by OpenAI.
func TestInterruptedStepPutsReasoningBeforeText(t *testing.T) {
	var capture interruptedStepCapture
	capture.observe(&sdk.ReasoningDeltaPart{ID: "b1", Text: "thought", Format: sdk.ReasoningFormatAnthropic})
	capture.observe(&sdk.TextDeltaPart{Text: "answer"})

	step := capture.snapshot(0)
	if step == nil {
		t.Fatal("no snapshot produced")
	}
	if _, ok := step.Messages[0].Content[0].(sdk.ReasoningPart); !ok {
		t.Errorf("content[0] = %T, want ReasoningPart to lead", step.Messages[0].Content[0])
	}
}

// The flat Reasoning field stays available for display, derived from the blocks.
func TestInterruptedStepFlatReasoningJoinsBlocks(t *testing.T) {
	var capture interruptedStepCapture
	capture.observe(&sdk.ReasoningDeltaPart{ID: "b1", Text: "AAA", Format: sdk.ReasoningFormatAnthropic})
	capture.observe(&sdk.ReasoningDeltaPart{ID: "b2", Text: "BBB", Format: sdk.ReasoningFormatAnthropic})

	step := capture.snapshot(0)
	if step == nil {
		t.Fatal("no snapshot produced")
	}
	if step.Reasoning != "AAABBB" {
		t.Errorf("Reasoning: got %q, want %q", step.Reasoning, "AAABBB")
	}
}

// Retained content must not leak across step boundaries.
func TestInterruptedStepResetsReasoningBetweenSteps(t *testing.T) {
	var capture interruptedStepCapture
	capture.observe(&sdk.ReasoningDeltaPart{ID: "b1", Text: "first", Format: sdk.ReasoningFormatAnthropic})
	capture.observe(&sdk.FinishStepPart{})
	capture.observe(&sdk.StartStepPart{})
	capture.observe(&sdk.ReasoningDeltaPart{ID: "b2", Text: "second", Format: sdk.ReasoningFormatAnthropic})

	parts := reasoningPartsOf(t, capture.snapshot(1))
	if len(parts) != 1 || parts[0].Text != "second" {
		t.Fatalf("parts: %+v, want only the second step's block", parts)
	}
}
