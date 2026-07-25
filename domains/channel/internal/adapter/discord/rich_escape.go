package discord

import "github.com/memohai/memoh/domains/channel/gateway"

// discordEscapeLinkURL strips characters that would prematurely terminate a
// markdown link URL, then percent-encodes the few that Discord still parses
// inside `[label](url)`.
func discordEscapeLinkURL(url string) string {
	return gateway.EscapeMessagePartLinkURL(url)
}
