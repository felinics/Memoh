package channel

import (
	neturl "net/url"
	"regexp"
	"strings"
)

const publicMediaPathRoot = "/channels/"

var publicMediaHashPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// IsPublicMediaPath reports whether path is a valid public Channel media path.
func IsPublicMediaPath(path string) bool {
	if !strings.HasPrefix(path, publicMediaPathRoot) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, publicMediaPathRoot), "/")
	if len(parts) == 6 && parts[1] == "public" && parts[2] == "media" && parts[5] == "preview.jpg" {
		return validPublicMediaPathIDs(parts[0], parts[3], parts[4])
	}
	if len(parts) == 7 && parts[1] == "public" && parts[2] == "media" && parts[5] == "original" && parts[6] != "" {
		return validPublicMediaPathIDs(parts[0], parts[3], parts[4])
	}
	return false
}

func validPublicMediaPathIDs(escapedChannelType, escapedBotID, escapedHash string) bool {
	channelType, err := neturl.PathUnescape(escapedChannelType)
	if err != nil || !validPublicMediaPathSegment(channelType) {
		return false
	}
	botID, err := neturl.PathUnescape(escapedBotID)
	if err != nil || !validPublicMediaPathSegment(botID) {
		return false
	}
	contentHash, err := neturl.PathUnescape(escapedHash)
	return err == nil && publicMediaHashPattern.MatchString(strings.TrimSpace(contentHash))
}

func validPublicMediaPathSegment(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\`)
}
