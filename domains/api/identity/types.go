package identity

import "strings"

const (
	IdentityTypeHuman = "human"
	IdentityTypeBot   = "bot"
)

// IsBotIdentityType checks if the identity type is a bot.
func IsBotIdentityType(identityType string) bool {
	return strings.EqualFold(strings.TrimSpace(identityType), IdentityTypeBot)
}
