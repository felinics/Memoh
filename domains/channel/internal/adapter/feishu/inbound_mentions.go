package feishu

import (
	"sort"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type feishuMention struct {
	Key       string
	Name      string
	OpenID    string
	UserID    string
	UnionID   string
	TenantKey string
}

func normalizeFeishuMentions(mentions []*larkim.MentionEvent) []feishuMention {
	result := make([]feishuMention, 0, len(mentions))
	for _, m := range mentions {
		if m == nil {
			continue
		}
		item := feishuMention{
			Key:  strings.TrimSpace(ptrStr(m.Key)),
			Name: strings.TrimSpace(ptrStr(m.Name)),
		}
		if m.Id != nil {
			item.OpenID = strings.TrimSpace(ptrStr(m.Id.OpenId))
			item.UserID = strings.TrimSpace(ptrStr(m.Id.UserId))
			item.UnionID = strings.TrimSpace(ptrStr(m.Id.UnionId))
		}
		item.TenantKey = strings.TrimSpace(ptrStr(m.TenantKey))
		result = append(result, item)
	}
	return result
}

func feishuMentionDisplayName(m feishuMention) string {
	name := strings.TrimSpace(m.Name)
	if name != "" {
		if strings.HasPrefix(name, "@") {
			return name
		}
		return "@" + name
	}
	if strings.TrimSpace(m.OpenID) != "" {
		return "@open_id:" + strings.TrimSpace(m.OpenID)
	}
	if strings.TrimSpace(m.UserID) != "" {
		return "@user_id:" + strings.TrimSpace(m.UserID)
	}
	return "@user"
}

// rewriteFeishuMentionKeys converts Feishu text placeholders (e.g. @_user_1)
// into stable mention labels so downstream logic can identify who is mentioned.
func rewriteFeishuMentionKeys(text string, mentions []feishuMention) string {
	if strings.TrimSpace(text) == "" || len(mentions) == 0 {
		return text
	}
	type kv struct {
		key   string
		value string
	}
	replacements := make([]kv, 0, len(mentions))
	for _, mention := range mentions {
		if mention.Key == "" {
			continue
		}
		replacements = append(replacements, kv{
			key:   mention.Key,
			value: feishuMentionDisplayName(mention),
		})
	}
	// Replace longer keys first to avoid partial replacement (@_user_1 vs @_user_10).
	sort.Slice(replacements, func(i, j int) bool {
		return len(replacements[i].key) > len(replacements[j].key)
	})
	rewritten := text
	for _, item := range replacements {
		rewritten = strings.ReplaceAll(rewritten, item.key, item.value)
	}
	return rewritten
}

func feishuMentionsMetadata(mentions []feishuMention) []map[string]any {
	if len(mentions) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(mentions))
	for _, mention := range mentions {
		entry := map[string]any{
			"key":        mention.Key,
			"name":       mention.Name,
			"open_id":    mention.OpenID,
			"user_id":    mention.UserID,
			"union_id":   mention.UnionID,
			"tenant_key": mention.TenantKey,
		}
		if target := feishuMentionTarget(mention); target != "" {
			entry["target"] = target
		}
		result = append(result, entry)
	}
	return result
}

func feishuMentionTarget(mention feishuMention) string {
	if strings.TrimSpace(mention.OpenID) != "" {
		return "open_id:" + strings.TrimSpace(mention.OpenID)
	}
	if strings.TrimSpace(mention.UserID) != "" {
		return "user_id:" + strings.TrimSpace(mention.UserID)
	}
	return ""
}

func feishuMentionTargets(mentions []feishuMention) []string {
	if len(mentions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(mentions))
	result := make([]string, 0, len(mentions))
	for _, mention := range mentions {
		target := feishuMentionTarget(mention)
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		result = append(result, target)
	}
	return result
}

func feishuBotMentionAlias(mentions []feishuMention, botOpenID string) string {
	botOpenID = strings.TrimSpace(botOpenID)
	if botOpenID == "" {
		return ""
	}
	for _, mention := range mentions {
		if strings.TrimSpace(mention.OpenID) == botOpenID {
			return feishuMentionDisplayName(mention)
		}
	}
	return ""
}

// isFeishuBotMentioned checks whether the bot itself is mentioned in the message.
// When botOpenID is provided, only mentions matching the bot's open_id count.
// When botOpenID is empty (fallback), any mention is treated as a bot mention.
func isFeishuBotMentioned(contentMap map[string]any, mentions []feishuMention, botOpenID string) bool {
	botOpenID = strings.TrimSpace(botOpenID)
	if botOpenID == "" {
		return hasAnyFeishuMention(contentMap, len(mentions))
	}
	for _, m := range mentions {
		if strings.TrimSpace(m.OpenID) == botOpenID {
			return true
		}
	}
	return matchFeishuContentMention(contentMap, botOpenID)
}

// hasAnyFeishuMention is the fallback when the bot's open_id is unknown.
func hasAnyFeishuMention(contentMap map[string]any, mentionCount int) bool {
	if mentionCount > 0 {
		return true
	}
	if len(contentMap) == 0 {
		return false
	}
	if raw, ok := contentMap["mentions"]; ok {
		switch values := raw.(type) {
		case []any:
			if len(values) > 0 {
				return true
			}
		case []map[string]any:
			if len(values) > 0 {
				return true
			}
		}
	}
	if text, ok := contentMap["text"].(string); ok {
		normalized := strings.ToLower(strings.TrimSpace(text))
		if strings.Contains(normalized, "@_user_") || strings.Contains(normalized, "<at ") || strings.Contains(normalized, "</at>") {
			return true
		}
	}
	return hasFeishuAtTag(contentMap)
}

// matchFeishuContentMention checks rich-text at tags for the bot's open_id.
func matchFeishuContentMention(raw any, botOpenID string) bool {
	switch value := raw.(type) {
	case map[string]any:
		if tag, ok := value["tag"].(string); ok && strings.EqualFold(strings.TrimSpace(tag), "at") {
			if uid, ok := value["user_id"].(string); ok && strings.TrimSpace(uid) == botOpenID {
				return true
			}
			if uid, ok := value["open_id"].(string); ok && strings.TrimSpace(uid) == botOpenID {
				return true
			}
		}
		for _, child := range value {
			if matchFeishuContentMention(child, botOpenID) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if matchFeishuContentMention(child, botOpenID) {
				return true
			}
		}
	}
	return false
}

func hasFeishuAtTag(raw any) bool {
	switch value := raw.(type) {
	case map[string]any:
		if tag, ok := value["tag"].(string); ok && strings.EqualFold(strings.TrimSpace(tag), "at") {
			return true
		}
		for _, child := range value {
			if hasFeishuAtTag(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if hasFeishuAtTag(child) {
				return true
			}
		}
	}
	return false
}
