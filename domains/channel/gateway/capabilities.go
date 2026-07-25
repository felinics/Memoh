package gateway

// ChannelCapabilities describes the feature matrix of a channel type.
// It is used by the outbound layer to validate message content before delivery.
type ChannelCapabilities struct {
	Text            bool     `json:"text"`
	Markdown        bool     `json:"markdown"`
	RichText        bool     `json:"rich_text"`
	Attachments     bool     `json:"attachments"`
	Media           bool     `json:"media"`
	Reactions       bool     `json:"reactions"`
	Buttons         bool     `json:"buttons"`
	URLButtons      bool     `json:"url_buttons"`
	Reply           bool     `json:"reply"`
	Threads         bool     `json:"threads"`
	Streaming       bool     `json:"streaming"`
	Polls           bool     `json:"polls"`
	Edit            bool     `json:"edit"`
	Unsend          bool     `json:"unsend"`
	NativeCommands  bool     `json:"native_commands"`
	NativeUserInput bool     `json:"native_user_input"`
	BlockStreaming  bool     `json:"block_streaming"`
	ChatTypes       []string `json:"chat_types,omitempty"`
}
