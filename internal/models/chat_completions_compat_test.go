package models

import "testing"

func TestLegacyChatCompletionsCompat(t *testing.T) {
	t.Parallel()

	openAICompletions := string(ClientTypeOpenAICompletions)
	tests := []struct {
		name       string
		clientType string
		config     map[string]any
		metadata   map[string]any
		want       string
	}{
		{
			name:       "nil maps",
			clientType: openAICompletions,
			want:       "",
		},
		{
			name:       "template key moonshot",
			clientType: openAICompletions,
			config:     map[string]any{"base_url": "https://proxy.example/v1"},
			metadata:   map[string]any{"template": map[string]any{"key": " Moonshot "}},
			want:       ChatCompletionsCompatKimi,
		},
		{
			name:       "preset id deepseek",
			clientType: openAICompletions,
			metadata:   map[string]any{"preset": map[string]any{"id": "deepseek"}},
			want:       ChatCompletionsCompatDeepSeek,
		},
		{
			name:       "registry source minimax",
			clientType: openAICompletions,
			metadata:   map[string]any{"registry": map[string]any{"source": "minimax.yaml"}},
			want:       ChatCompletionsCompatMiniMax,
		},
		{
			name:       "canonical origin",
			clientType: openAICompletions,
			config:     map[string]any{"base_url": "https://api.deepseek.com"},
			want:       ChatCompletionsCompatDeepSeek,
		},
		{
			name:       "canonical origin trailing slash",
			clientType: openAICompletions,
			config:     map[string]any{"base_url": "https://api.moonshot.ai/v1/"},
			want:       ChatCompletionsCompatKimi,
		},
		{
			name:       "path prefix beta",
			clientType: openAICompletions,
			config:     map[string]any{"base_url": "https://api.deepseek.com/beta"},
			want:       ChatCompletionsCompatDeepSeek,
		},
		{
			name:       "lookalike domain rejected",
			clientType: openAICompletions,
			config:     map[string]any{"base_url": "https://api.deepseek.com.evil.example/v1"},
			want:       "",
		},
		{
			name:       "official hostname embedded in proxy path rejected",
			clientType: openAICompletions,
			config:     map[string]any{"base_url": "https://gateway.example/https://api.deepseek.com/v1"},
			want:       "",
		},
		{
			name:       "explicit value already present",
			clientType: openAICompletions,
			config: map[string]any{
				"base_url":                     "https://api.deepseek.com/v1",
				ChatCompletionsCompatConfigKey: "custom",
			},
			want: "",
		},
		{
			name:       "non openai-completions client",
			clientType: string(ClientTypeAnthropicMessages),
			config:     map[string]any{"base_url": "https://api.moonshot.cn/anthropic"},
			want:       "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := LegacyChatCompletionsCompat(tt.clientType, tt.config, tt.metadata)
			if got != tt.want {
				t.Fatalf("LegacyChatCompletionsCompat(%q, %v, %v) = %q, want %q",
					tt.clientType, tt.config, tt.metadata, got, tt.want)
			}
		})
	}
}
