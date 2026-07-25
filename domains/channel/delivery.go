package channel

type ReactCommand struct {
	TeamID      string
	BotID       string
	ChannelType ChannelType
	Target      string
	MessageID   string
	Emoji       string
	Remove      bool
}
