package alibabacloud

type asrRequest struct {
	Model    string       `json:"model"`
	Messages []asrMessage `json:"messages"`
	Stream   bool         `json:"stream"`
	Options  asrOptions   `json:"asr_options"`
}

type asrMessage struct {
	Role string `json:"role"`
	// Qwen expects a string for context and an audio-content array for the user.
	Content any `json:"content"`
}

type asrAudioContent struct {
	Type       string        `json:"type"`
	InputAudio asrInputAudio `json:"input_audio"`
}

type asrInputAudio struct {
	Data string `json:"data"`
}

type asrOptions struct {
	Language  string `json:"language,omitempty"`
	EnableITN *bool  `json:"enable_itn,omitempty"`
}
