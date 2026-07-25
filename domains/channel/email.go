package channel

type RefreshEmailProviderCommand struct {
	TeamID     string
	ProviderID string
}

type SendEmailCommand struct {
	TeamID     string
	BotID      string
	ProviderID string
	To         []string
	Subject    string
	Body       string
	HTML       bool
}

type SendEmailResult struct {
	MessageID string
}
