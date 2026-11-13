package fishaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"strconv"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/partmeta"
)

type transcription struct{ client *client }

func NewTranscription(apiKey, baseURL string) (sdk.TranscriptionProvider, error) {
	c, err := newClient(apiKey, baseURL)
	if err != nil {
		return nil, err
	}
	return &transcription{client: c}, nil
}

func (p *transcription) ListModels(ctx context.Context) ([]*sdk.TranscriptionModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []*sdk.TranscriptionModel{{ID: DefaultTranscriptionModel, Provider: p}, {ID: "transcribe-1", Provider: p}}, nil
}

func (p *transcription) DoTranscribe(ctx context.Context, params sdk.TranscriptionParams) (*sdk.TranscriptionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(params.Audio) == 0 {
		return nil, errors.New("fish audio requires a non-empty audio file")
	}
	model := DefaultTranscriptionModel
	if params.Model != nil && params.Model.ID != "" {
		model = params.Model.ID
	}
	if model != DefaultTranscriptionModel && model != "transcribe-1" {
		return nil, errors.New("unsupported fish audio transcription model")
	}
	fields, err := transcriptionFields(params.Config, model)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, &requestError{cause: err}
		}
	}
	filename := params.Filename
	if filename == "" {
		filename = "audio"
	}
	if _, err := writer.CreateFormFile("audio", filename); err != nil {
		return nil, &requestError{cause: err}
	}
	prefixLength := body.Len()
	if err := writer.Close(); err != nil {
		return nil, &requestError{cause: err}
	}
	// Insert the existing audio slice between the multipart headers and closing boundary.
	framing := body.Bytes()
	payload := io.MultiReader(bytes.NewReader(framing[:prefixLength]), bytes.NewReader(params.Audio), bytes.NewReader(framing[prefixLength:]))
	resp, err := p.client.post(ctx, "/asr", model, writer.FormDataContentType(), payload, int64(len(framing))+int64(len(params.Audio)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := readBounded(resp.Body, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	var result struct {
		Text         *string `json:"text"`
		Duration     float64 `json:"duration"`
		LanguageCode string  `json:"language_code"`
		Language     string  `json:"language"`
		Segments     []struct {
			Text  string  `json:"text"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"segments"`
		RequestID    string          `json:"request_id"`
		SpeakerTurns json.RawMessage `json:"speaker_turns"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errors.New("invalid fish audio transcription response JSON")
	}
	if result.Text == nil {
		return nil, errors.New("fish audio transcription response has no text")
	}
	language := result.LanguageCode
	if language == "" {
		language = result.Language
	}
	metadata := map[string]any{}
	if result.RequestID != "" {
		metadata["request_id"] = result.RequestID
	}
	if len(result.SpeakerTurns) > 0 {
		metadata["speaker_turns"] = result.SpeakerTurns
	}
	out := &sdk.TranscriptionResult{Text: *result.Text, Language: language, DurationSeconds: result.Duration, ProviderMetadata: partmeta.Fold(metadata)}
	for _, seg := range result.Segments {
		out.Words = append(out.Words, sdk.TranscriptionWord{Text: seg.Text, Start: seg.Start, End: seg.End})
	}
	return out, nil
}

func transcriptionFields(config map[string]any, model string) (map[string]string, error) {
	fields := map[string]string{}
	language, err := configString(config, "language")
	if err != nil {
		return nil, err
	}
	if language != "" {
		if len(language) != 2 || language[0] < 'a' || language[0] > 'z' || language[1] < 'a' || language[1] > 'z' {
			return nil, errors.New("language must be a lowercase two-letter ISO 639-1 code")
		}
		fields["language"] = language
	}
	for _, key := range []string{"ignore_timestamps", "tag_audio_events"} {
		value, ok := config[key]
		if !ok {
			continue
		}
		flag, err := configBool(value, key)
		if err != nil {
			return nil, err
		}
		fields[key] = strconv.FormatBool(flag)
	}
	if value, ok := config["diarize"]; ok {
		switch v := value.(type) {
		case bool:
			fields["diarize"] = strconv.FormatBool(v)
		case string:
			if v != "auto" && v != "true" && v != "false" {
				return nil, errors.New("diarize must be auto, true or false")
			}
			fields["diarize"] = v
		default:
			return nil, errors.New("diarize must be auto, true or false")
		}
	}
	counts := map[string]float64{}
	for _, key := range []string{"num_speakers", "min_speakers", "max_speakers"} {
		if _, ok := config[key]; !ok {
			continue
		}
		n, err := configNumber(config, key, 1, math.MaxInt32, true)
		if err != nil {
			return nil, err
		}
		fields[key] = strconv.FormatFloat(n, 'f', 0, 64)
		counts[key] = n
	}
	if model != DefaultTranscriptionModel {
		for _, key := range []string{"tag_audio_events", "diarize", "num_speakers", "min_speakers", "max_speakers"} {
			if _, ok := fields[key]; ok {
				return nil, fmt.Errorf("%s requires transcribe-1-pro", key)
			}
		}
	}
	if len(counts) > 0 && fields["diarize"] == "false" {
		return nil, errors.New("speaker counts cannot be used with diarize=false")
	}
	if _, ok := counts["num_speakers"]; ok && len(counts) > 1 {
		return nil, errors.New("num_speakers cannot be combined with min_speakers or max_speakers")
	}
	if minimum, ok := counts["min_speakers"]; ok {
		if maximum, ok := counts["max_speakers"]; ok && minimum > maximum {
			return nil, errors.New("min_speakers must not exceed max_speakers")
		}
	}
	return fields, nil
}
