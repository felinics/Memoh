package core

import (
	"testing"

	"github.com/memohai/memoh/domains/model/template"
)

func TestTemplateDefinitionsKeepAllProviderFiles(t *testing.T) {
	defs := []template.Definition{
		{
			Name:   "DeepSeek",
			Driver: "openai-completions",
			Domain: template.DomainLLM,
		},
		{
			Name:   "OpenAI",
			Driver: "openai-responses",
			Domain: template.DomainLLM,
		},
		{
			Name:   "OpenAI Speech",
			Driver: "openai-speech",
			Domain: template.DomainSpeech,
		},
		{
			Name:   "Google Transcription",
			Driver: "google-transcription",
			Domain: template.DomainTranscription,
		},
	}

	if len(defs) != 4 {
		t.Fatalf("definition count = %d, want 4", len(defs))
	}
	for i := range defs {
		if defs[i].Name == "" || defs[i].Driver == "" {
			t.Fatalf("definition %d = %#v", i, defs[i])
		}
	}
}
