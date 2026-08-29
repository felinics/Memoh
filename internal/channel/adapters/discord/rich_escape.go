package discord

import "github.com/felinics/memoh/internal/channel"

// discordEscapeLinkURL strips characters that would prematurely terminate a
// markdown link URL, then percent-encodes the few that Discord still parses
// inside `[label](url)`.
func discordEscapeLinkURL(url string) string {
	return channel.EscapeMessagePartLinkURL(url)
}
