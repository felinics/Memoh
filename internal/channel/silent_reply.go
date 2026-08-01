package channel

import (
	"strings"
	"unicode"
)

const silentReplyToken = "NO_REPLY"

// IsSilentReplyText recognizes the standalone suppression token at either end
// of a model response. Punctuation and explanatory whitespace are tolerated;
// embedding the token inside a word is not.
func IsSilentReplyText(text string) bool {
	value := []rune(strings.ToUpper(strings.TrimSpace(text)))
	token := []rune(silentReplyToken)
	if len(value) < len(token) {
		return false
	}
	return hasStandaloneTokenPrefix(value, token) || hasStandaloneTokenSuffix(value, token)
}

func hasStandaloneTokenPrefix(value, token []rune) bool {
	if len(value) < len(token) || string(value[:len(token)]) != string(token) {
		return false
	}
	return len(value) == len(token) || !isSilentReplyWordChar(value[len(token)])
}

func hasStandaloneTokenSuffix(value, token []rune) bool {
	if len(value) < len(token) {
		return false
	}
	start := len(value) - len(token)
	if string(value[start:]) != string(token) {
		return false
	}
	return start == 0 || !isSilentReplyWordChar(value[start-1])
}

func isSilentReplyWordChar(value rune) bool {
	return value == '_' || unicode.IsLetter(value) || unicode.IsDigit(value)
}
