// Package delivery contains transport-neutral helpers for interpreting
// messaging tool results.
package delivery

import "strings"

// IsSameConversation resolves omitted routing fields to the current route and
// compares both platform and target.
func IsSameConversation(currentPlatform, currentTarget, platform, target string) bool {
	currentPlatform = strings.TrimSpace(currentPlatform)
	currentTarget = strings.TrimSpace(currentTarget)
	platform = strings.TrimSpace(platform)
	target = strings.TrimSpace(target)
	if platform == "" {
		platform = currentPlatform
	}
	if target == "" {
		target = currentTarget
	}
	return strings.EqualFold(platform, currentPlatform) && target == currentTarget
}

// IsSuccessfulCurrentDelivery accepts ordinary successful send envelopes and
// partial sticker envelopes where text was already committed. The latter must
// terminate the local tool loop so a retry cannot duplicate public text.
func IsSuccessfulCurrentDelivery(result map[string]any, currentPlatform, currentTarget string) bool {
	if delivered, present := result["ok"].(bool); present && !delivered {
		textDelivered, _ := result["text_delivered"].(bool)
		if !textDelivered {
			return false
		}
	}
	platform, _ := result["platform"].(string)
	target, _ := result["target"].(string)
	return IsSameConversation(currentPlatform, currentTarget, platform, target)
}
