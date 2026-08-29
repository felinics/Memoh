package edge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/audio"
)

const TtsTypeEdge audio.TtsType = "edge"

const edgeModelReadAloud = "edge-read-aloud"

type EdgeAdapter struct {
	logger *slog.Logger
	client *EdgeWsClient
}

func NewEdgeAdapter(log *slog.Logger) *EdgeAdapter {
	return &EdgeAdapter{
		logger: log.With(slog.String("adapter", "edge")),
		client: NewEdgeWsClient(),
	}
}

// NewEdgeAdapterWithClient for testing: inject custom WebSocket client (e.g. mock server).
func NewEdgeAdapterWithClient(log *slog.Logger, client *EdgeWsClient) *EdgeAdapter {
	return &EdgeAdapter{
		logger: log.With(slog.String("adapter", "edge")),
		client: client,
	}
}

func (*EdgeAdapter) Type() audio.TtsType {
	return TtsTypeEdge
}

func (*EdgeAdapter) Meta() audio.TtsMeta {
	return audio.TtsMeta{
		Provider:    "Microsoft Edge",
		Description: "Microsoft Edge TTS",
	}
}

func (*EdgeAdapter) DefaultModel() string {
	return edgeModelReadAloud
}

var edgeFormats = []string{
	"audio-24khz-48kbitrate-mono-mp3",
	"audio-24khz-96kbitrate-mono-mp3",
	"webm-24khz-16bit-mono-opus",
}

var edgeSpeedConstraint = &audio.ParamConstraint{
	Options: []float64{0.5, 1.0, 2.0, 3.0},
	Default: 1.0,
}

var edgePitchConstraint = &audio.ParamConstraint{
	Min:     -100,
	Max:     100,
	Default: 0,
}

func (*EdgeAdapter) Models() []audio.ModelInfo {
	var voices []audio.VoiceInfo
	for lang, ids := range EdgeTTSVoices {
		for _, id := range ids {
			name := strings.TrimPrefix(id, lang+"-")
			name = strings.TrimSuffix(name, "Neural")
			voices = append(voices, audio.VoiceInfo{ID: id, Lang: lang, Name: name})
		}
	}
	return []audio.ModelInfo{
		{
			ID:          edgeModelReadAloud,
			Name:        "Edge Read Aloud",
			Description: "Built-in Edge Read Aloud speech model",
			Capabilities: audio.ModelCapabilities{
				Voices:  voices,
				Formats: edgeFormats,
				Speed:   edgeSpeedConstraint,
				Pitch:   edgePitchConstraint,
			},
		},
	}
}

func (*EdgeAdapter) ResolveModel(model string) (string, error) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return edgeModelReadAloud, nil
	}
	if !strings.EqualFold(trimmed, edgeModelReadAloud) {
		return "", fmt.Errorf("edge tts: unsupported model: %s", model)
	}
	return edgeModelReadAloud, nil
}

func (a *EdgeAdapter) Synthesize(ctx context.Context, text string, _ string, config audio.AudioConfig) ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("edge tts: invalid config: %w", err)
	}
	return a.client.Synthesize(ctx, text, config)
}

func (a *EdgeAdapter) Stream(ctx context.Context, text string, _ string, config audio.AudioConfig) (chan []byte, chan error) {
	if err := config.Validate(); err != nil {
		errCh := make(chan error, 1)
		errCh <- fmt.Errorf("edge tts: invalid config: %w", err)
		close(errCh)
		return nil, errCh
	}
	return a.client.Stream(ctx, text, config)
}
