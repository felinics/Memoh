package slack

import "strings"

// slackEscapeMrkdwn escapes characters that Slack interprets specially when
// emitting mrkdwn (`&`, `<`, `>`).
func slackEscapeMrkdwn(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

func slackEscapeMrkdwnLinkText(text string) string {
	text = slackEscapeMrkdwn(text)
	text = strings.ReplaceAll(text, "|", "&#124;")
	return text
}
