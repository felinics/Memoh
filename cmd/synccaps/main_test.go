package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/capabilities"
)

func TestEnrichFileClearsStaleReasoningWhenRegistrySaysNone(t *testing.T) {
	t.Parallel()

	resolver, err := capabilities.NewResolver([]byte(`{
		"plain-model": {"mode": "chat", "supports_reasoning": false}
	}`))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "provider.yaml")
	if err := os.WriteFile(path, []byte(`name: Test
client_type: openai-completions
models:
  - model_id: plain-model
    name: Plain
    type: chat
    config:
      compatibilities: [vision, reasoning, tool-call]
      thinking_mode: toggle
      reasoning_efforts: [low, medium, high]
`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	changed, err := enrichFile(path, resolver, false)
	if err != nil {
		t.Fatalf("enrichFile: %v", err)
	}
	if changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	gotBytes, err := os.ReadFile(path) //nolint:gosec // test reads its own temp fixture
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := string(gotBytes)
	for _, want := range []string{
		"compatibilities: [vision, tool-call]",
		"thinking_mode: none",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "reasoning_efforts") {
		t.Fatalf("reasoning_efforts should be removed:\n%s", got)
	}
}

func TestEnrichFilePreservesHandMaintainedAlwaysMode(t *testing.T) {
	t.Parallel()

	resolver, err := capabilities.NewResolver([]byte(`{
		"always-model": {"mode": "chat", "supports_reasoning": false}
	}`))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "provider.yaml")
	raw := []byte(`name: Test
client_type: openai-completions
models:
  - model_id: always-model
    name: Always
    type: chat
    config:
      compatibilities: [reasoning, tool-call]
      thinking_mode: always
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	changed, err := enrichFile(path, resolver, false)
	if err != nil {
		t.Fatalf("enrichFile: %v", err)
	}
	if changed != 0 {
		t.Fatalf("changed = %d, want 0", changed)
	}
	got, err := os.ReadFile(path) //nolint:gosec // test reads its own temp fixture
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("hand-maintained always mode should remain authoritative:\n%s", got)
	}
}

func TestEnrichFileLeavesPlainNoReasonModelUntouched(t *testing.T) {
	t.Parallel()

	resolver, err := capabilities.NewResolver([]byte(`{
		"plain-model": {"mode": "chat", "supports_reasoning": false}
	}`))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "provider.yaml")
	raw := []byte(`name: Test
client_type: openai-completions
models:
  - model_id: plain-model
    name: Plain
    type: chat
    config:
      compatibilities: [vision, tool-call]
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	changed, err := enrichFile(path, resolver, false)
	if err != nil {
		t.Fatalf("enrichFile: %v", err)
	}
	if changed != 0 {
		t.Fatalf("changed = %d, want 0", changed)
	}
	got, err := os.ReadFile(path) //nolint:gosec // test reads its own temp fixture
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("plain no-reason model should remain untouched:\n%s", got)
	}
}

func TestEnrichFilePreservesCompactModelSpacing(t *testing.T) {
	t.Parallel()

	resolver, err := capabilities.NewResolver([]byte(`{
		"reasoning-a": {"mode": "chat", "supports_reasoning": true},
		"reasoning-b": {"mode": "chat", "supports_reasoning": true}
	}`))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "provider.yaml")
	if err := os.WriteFile(path, []byte(`name: Test
client_type: openai-completions
models:
  - model_id: reasoning-a
    name: A
    type: chat
  - model_id: reasoning-b
    name: B
    type: chat
`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	changed, err := enrichFile(path, resolver, false)
	if err != nil {
		t.Fatalf("enrichFile: %v", err)
	}
	if changed != 2 {
		t.Fatalf("changed = %d, want 2", changed)
	}
	gotBytes, err := os.ReadFile(path) //nolint:gosec // test reads its own temp fixture
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	got := string(gotBytes)
	if strings.Contains(got, "\n\n  - model_id: reasoning-b") {
		t.Fatalf("compact catalog gained blank model spacing:\n%s", got)
	}
	if !strings.Contains(got, "thinking_mode: toggle") {
		t.Fatalf("output missing enrichment:\n%s", got)
	}
}

func TestEnrichFilePreservesHandMaintainedMinimalToken(t *testing.T) {
	t.Parallel()

	// LiteLLM reports supports_minimal for one model family only, while Gemini 3.x
	// takes minimal as its floor. A template that declares it must survive re-sync
	// for the same reason the disable token does: registry silence is not evidence
	// of absence.
	resolver, err := capabilities.NewResolver([]byte(`{
		"tier-model": {"mode": "chat", "supports_reasoning": true}
	}`))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "provider.yaml")
	fixture := `name: Test
client_type: google-generative-ai
models:
  - model_id: tier-model
    name: Tier
    type: chat
    config:
      compatibilities: [reasoning, tool-call]
      thinking_mode: toggle
      reasoning_dialect: tier
      reasoning_efforts: [minimal, low, medium, high]
`
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	changed, err := enrichFile(path, resolver, false)
	if err != nil {
		t.Fatalf("enrichFile: %v", err)
	}
	if changed != 0 {
		got, _ := os.ReadFile(path) //nolint:gosec // test reads its own temp fixture
		t.Fatalf("changed = %d, want 0 (minimal and the dialect should survive):\n%s", changed, got)
	}
}

func TestEnrichFilePreservesHandMaintainedDisableToken(t *testing.T) {
	t.Parallel()

	// LiteLLM's only off signal is the OpenAI-wire supports_none_reasoning_effort
	// flag; for DeepSeek/MiniMax-style toggle-off (via chat_completions_compat)
	// the registry is structurally silent. A hand-placed disable token must
	// survive re-sync rather than being clobbered by the derived tier list.
	resolver, err := capabilities.NewResolver([]byte(`{
		"toggle-model": {"mode": "chat", "supports_reasoning": true}
	}`))
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "provider.yaml")
	fixture := `name: Test
client_type: openai-completions
models:
  - model_id: toggle-model
    name: Toggle
    type: chat
    config:
      compatibilities: [reasoning, tool-call]
      thinking_mode: toggle
      reasoning_efforts: [disable, low, medium, high]
`
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	changed, err := enrichFile(path, resolver, false)
	if err != nil {
		t.Fatalf("enrichFile: %v", err)
	}
	if changed != 0 {
		gotBytes, _ := os.ReadFile(path) //nolint:gosec // test reads its own temp fixture
		t.Fatalf("changed = %d, want 0 (disable token should be preserved):\n%s", changed, gotBytes)
	}
}
