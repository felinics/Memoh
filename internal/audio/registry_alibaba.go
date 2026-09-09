package audio

import (
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/audio/adapter/alibabacloud"
	"github.com/felinics/memoh/internal/models"
)

func alibabaTranscriptionDefinition() ProviderDefinition {
	schema := ConfigSchema{Fields: []FieldSchema{
		stringField("language", "Language", "Optional language code (zh, en, yue, etc.); leave empty for automatic detection or mixed languages", false, "", 10),
		boolField("enable_itn", "Normalize numbers", "Convert spoken Chinese and English numbers into digits", false, 20),
		advancedStringField("context", "Context", "Optional background text and vocabulary to aid recognition", false, "", 30),
	}}
	modelList := []ModelInfo{{
		ID: alibabacloud.DefaultModel, Name: "Qwen3 ASR Flash",
		Description:  "Short audio transcription with automatic language detection",
		ConfigSchema: schema, Capabilities: ModelCapabilities{ConfigSchema: schema},
	}}
	return ProviderDefinition{
		ClientType:  models.ClientTypeAlibabaTranscription,
		DisplayName: "Alibaba Cloud Transcription", Icon: "bailian-color",
		Description: "DashScope Qwen ASR (Alibaba Cloud Bailian)",
		ConfigSchema: ConfigSchema{Fields: []FieldSchema{
			secretField("api_key", "API Key", "DashScope API key for the selected region", true, 10),
			stringField("base_url", "Base URL", "OpenAI-compatible HTTP base URL; defaults to Beijing", false, alibabacloud.DefaultBaseURL, 20),
		}},
		DefaultTranscriptionModel: alibabacloud.DefaultModel,
		SupportsTranscriptionList: true, TranscriptionModels: modelList,
		TranscriptionFactory: func(config map[string]any) (sdk.TranscriptionProvider, error) {
			return alibabacloud.New(configString(config, "api_key"), configString(config, "base_url"))
		},
		Order: 81,
	}
}
