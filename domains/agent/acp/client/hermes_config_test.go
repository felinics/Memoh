package client

import (
	"context"
	"strings"
	"testing"
)

func TestWriteHermesManagedConfigWritesConfigAndEnv(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	err := WriteHermesManagedConfig(context.Background(), client, HermesManagedConfig{
		Home: "/data/.memoh-hermes",
		Managed: map[string]string{
			"provider": "openrouter",
			"model":    "anthropic/claude-sonnet-4",
			"api_key":  "sk-test'quoted",
		},
	})
	if err != nil {
		t.Fatalf("WriteHermesManagedConfig() error = %v", err)
	}
	config, ok := findWrite(server.writes(), "/data/.memoh-hermes/config.yaml")
	if !ok {
		t.Fatalf("missing Hermes config write: %#v", server.writes())
	}
	content := string(config.Content)
	for _, want := range []string{
		`model:`,
		`provider: "openrouter"`,
		`default: "anthropic/claude-sonnet-4"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("config missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "sk-test") {
		t.Fatalf("config leaked API key:\n%s", content)
	}
	env, ok := findWrite(server.writes(), "/data/.memoh-hermes/.env")
	if !ok {
		t.Fatalf("missing Hermes env write: %#v", server.writes())
	}
	if got := string(env.Content); !strings.Contains(got, `OPENROUTER_API_KEY='sk-test\'quoted'`) {
		t.Fatalf("env content = %q", got)
	}
}

func TestWriteHermesManagedConfigNormalizesOpenAIProvider(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	err := WriteHermesManagedConfig(context.Background(), client, HermesManagedConfig{
		Home: "/data/.memoh-hermes",
		Managed: map[string]string{
			"provider":   "openai",
			"model":      "gpt-5.4",
			"api_key":    "sk-test",
			"max_tokens": "8192",
		},
	})
	if err != nil {
		t.Fatalf("WriteHermesManagedConfig() error = %v", err)
	}
	config, ok := findWrite(server.writes(), "/data/.memoh-hermes/config.yaml")
	if !ok {
		t.Fatalf("missing Hermes config write: %#v", server.writes())
	}
	if content := string(config.Content); !strings.Contains(content, `provider: "openai-api"`) ||
		!strings.Contains(content, `max_tokens: 8192`) {
		t.Fatalf("config content =\n%s", content)
	}
}

func TestWriteHermesManagedConfigNormalizesGeminiProvider(t *testing.T) {
	client, server := newRecordingBridgeClient(t)
	err := WriteHermesManagedConfig(context.Background(), client, HermesManagedConfig{
		Home: "/data/.memoh-hermes",
		Managed: map[string]string{
			"provider": "google-ai-studio",
			"model":    "gemini-3.5-flash",
			"api_key":  "AIza-test",
		},
	})
	if err != nil {
		t.Fatalf("WriteHermesManagedConfig() error = %v", err)
	}
	config, ok := findWrite(server.writes(), "/data/.memoh-hermes/config.yaml")
	if !ok {
		t.Fatalf("missing Hermes config write: %#v", server.writes())
	}
	content := string(config.Content)
	if !strings.Contains(content, `provider: "gemini"`) ||
		!strings.Contains(content, `default: "gemini-3.5-flash"`) ||
		strings.Contains(content, `max_tokens`) {
		t.Fatalf("config content =\n%s", content)
	}
	env, ok := findWrite(server.writes(), "/data/.memoh-hermes/.env")
	if !ok {
		t.Fatalf("missing Hermes env write: %#v", server.writes())
	}
	if got := string(env.Content); !strings.Contains(got, `GOOGLE_API_KEY='AIza-test'`) {
		t.Fatalf("env content = %q", got)
	}
}

func TestWriteHermesManagedConfigCustomProviderRequiresBaseURL(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	err := WriteHermesManagedConfig(context.Background(), client, HermesManagedConfig{
		Home: "/data/.memoh-hermes",
		Managed: map[string]string{
			"provider": "custom",
			"model":    "my-model",
			"api_key":  "sk-test",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("WriteHermesManagedConfig() error = %v, want base_url validation", err)
	}
}

func TestWriteHermesManagedConfigRejectsInvalidBaseURL(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	for _, baseURL := range []string{"localhost:1234", "ftp://llm.example/v1"} {
		err := WriteHermesManagedConfig(context.Background(), client, HermesManagedConfig{
			Home: "/data/.memoh-hermes",
			Managed: map[string]string{
				"provider": "custom",
				"model":    "my-model",
				"base_url": baseURL,
				"api_key":  "sk-test",
			},
		})
		if err == nil || !strings.Contains(err.Error(), "absolute http(s) URL") {
			t.Fatalf("WriteHermesManagedConfig(base_url=%q) error = %v, want URL validation", baseURL, err)
		}
	}
}

func TestWriteHermesManagedConfigRejectsInvalidMaxTokens(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	err := WriteHermesManagedConfig(context.Background(), client, HermesManagedConfig{
		Home: "/data/.memoh-hermes",
		Managed: map[string]string{
			"provider":   "openrouter",
			"model":      "anthropic/claude-sonnet-4",
			"api_key":    "sk-test",
			"max_tokens": "0",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("WriteHermesManagedConfig() error = %v, want max_tokens validation", err)
	}
}

func TestWriteHermesManagedConfigRejectsUnknownProvider(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	err := WriteHermesManagedConfig(context.Background(), client, HermesManagedConfig{
		Home: "/data/.memoh-hermes",
		Managed: map[string]string{
			"provider": "anthropic",
			"model":    "claude",
			"api_key":  "sk-test",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported Hermes provider") {
		t.Fatalf("WriteHermesManagedConfig() error = %v, want unsupported provider", err)
	}
}

func TestWriteHermesManagedConfigRejectsUnsafeDotenvValue(t *testing.T) {
	client, _ := newRecordingBridgeClient(t)
	err := WriteHermesManagedConfig(context.Background(), client, HermesManagedConfig{
		Home: "/data/.memoh-hermes",
		Managed: map[string]string{
			"provider": "openrouter",
			"model":    "anthropic/claude-sonnet-4",
			"api_key":  "sk-${TOKEN}",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "dotenv variable interpolation") {
		t.Fatalf("WriteHermesManagedConfig() error = %v, want dotenv interpolation validation", err)
	}
}
