package registry

import (
	"fmt"
	"strings"
)

// MergeMetadata merges base and extra metadata maps.
func MergeMetadata(base map[string]any, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// BuildProfileMetadata builds profile metadata for memory writes.
func BuildProfileMetadata(userID, channelIdentityID, displayName string) map[string]any {
	userID = strings.TrimSpace(userID)
	channelIdentityID = strings.TrimSpace(channelIdentityID)
	displayName = strings.TrimSpace(displayName)
	if userID == "" && channelIdentityID == "" && displayName == "" {
		return nil
	}
	out := map[string]any{}
	if userID != "" {
		out["profile_user_id"] = userID
		out["profile_ref"] = fmt.Sprintf("user:%s", userID)
	} else if channelIdentityID != "" {
		out["profile_ref"] = fmt.Sprintf("channel_identity:%s", channelIdentityID)
	}
	if channelIdentityID != "" {
		out["profile_channel_identity_id"] = channelIdentityID
	}
	if displayName != "" {
		out["profile_display_name"] = displayName
	}
	return out
}
