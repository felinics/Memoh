package contextview

import (
	"encoding/json"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

type SelectionTag string

const (
	TagMustKeep            SelectionTag = "must_keep"
	TagPreserveRecent      SelectionTag = "preserve_recent"
	TagPreserveToolClosure SelectionTag = "preserve_tool_closure"
	TagCanDrop             SelectionTag = "can_drop"
)

type TaggedFrag struct {
	Frag contextfrag.ContextFrag
	Tags []SelectionTag
	// Tokens is the cost budget arithmetic charges for Frag. A hard provider
	// budget prices it with the envelope estimator so selection and the
	// rendered-envelope check agree; the fragment's own estimate is left
	// untouched so selection decisions do not read as trims.
	Tokens int
}

func (t TaggedFrag) HasTag(tag SelectionTag) bool {
	return hasSelectionTag(t.Tags, tag)
}

func tagFragments(frags []contextfrag.ContextFrag, profile IntentProfile) []TaggedFrag {
	tagged := make([]TaggedFrag, 0, len(frags))
	for _, frag := range frags {
		next := TaggedFrag{Frag: frag, Tokens: contextfrag.ResolveFragTokens(frag)}
		if isMustKeepFrag(frag, profile) {
			next.Tags = appendSelectionTag(next.Tags, TagMustKeep)
		}
		if isToolExchangeFrag(frag) {
			next.Tags = appendSelectionTag(next.Tags, TagPreserveToolClosure)
		}
		if hasAskUserTool(frag) {
			next.Tags = appendSelectionTag(next.Tags, TagMustKeep)
			next.Tags = appendSelectionTag(next.Tags, TagPreserveToolClosure)
		}
		tagged = append(tagged, next)
	}
	markRecentAndDropTags(tagged)
	return tagged
}

func markRecentAndDropTags(tagged []TaggedFrag) {
	if len(tagged) == 0 {
		return
	}
	if latestUser := latestUserIndex(tagged); latestUser == 0 && len(tagged) > 1 {
		tailStart := recentTailProtectedStart(tagged, 1)
		for i := range tagged {
			if i == 0 || i >= tailStart || tagged[i].HasTag(TagMustKeep) {
				tagged[i].Tags = appendSelectionTag(tagged[i].Tags, TagPreserveRecent)
				continue
			}
			tagged[i].Tags = appendSelectionTag(tagged[i].Tags, TagCanDrop)
		}
		return
	}

	start := recentProtectedStart(tagged)
	for i := range tagged {
		if tagged[i].HasTag(TagMustKeep) {
			tagged[i].Tags = appendSelectionTag(tagged[i].Tags, TagPreserveRecent)
			continue
		}
		if i < start {
			tagged[i].Tags = appendSelectionTag(tagged[i].Tags, TagCanDrop)
			continue
		}
		tagged[i].Tags = appendSelectionTag(tagged[i].Tags, TagPreserveRecent)
	}
}

func isMustKeepFrag(frag contextfrag.ContextFrag, profile IntentProfile) bool {
	if frag.Budget.Overflow == contextfrag.OverflowKeep {
		return true
	}
	if profile.MustKeepFrag != nil && profile.MustKeepFrag(frag) {
		return true
	}
	for _, slot := range profile.MustKeepSlots {
		if frag.Slot == slot {
			return true
		}
	}
	return false
}

func latestUserIndex(tagged []TaggedFrag) int {
	for i := len(tagged) - 1; i >= 0; i-- {
		// Background summaries are per-step status notices, not conversation
		// turns: they must not anchor the recent-protection window.
		if tagged[i].Frag.Kind == contextfrag.KindBackgroundSummary {
			continue
		}
		if isRole(tagged[i].Frag.Role, sdk.MessageRoleUser) {
			return i
		}
		for _, part := range tagged[i].Frag.Parts {
			if msg := sdkMessagePart(part); msg != nil && isRole(msg.Role, sdk.MessageRoleUser) {
				return i
			}
		}
	}
	return -1
}

func recentProtectedStart(tagged []TaggedFrag) int {
	if len(tagged) == 0 {
		return 0
	}
	if latestUser := latestUserIndex(tagged); latestUser >= 0 {
		return latestUser
	}
	return recentTailProtectedStart(tagged, 0)
}

func recentTailProtectedStart(tagged []TaggedFrag, minStart int) int {
	start := len(tagged) - 1
	for start > minStart && isToolClosureResult(tagged[start]) {
		start--
	}
	return start
}

func isToolClosureResult(tagged TaggedFrag) bool {
	return tagged.HasTag(TagPreserveToolClosure) && isToolResultFrag(tagged.Frag)
}

func isToolResultFrag(frag contextfrag.ContextFrag) bool {
	if isRole(frag.Role, sdk.MessageRoleTool) {
		return true
	}
	for _, part := range frag.Parts {
		if msg := sdkMessagePart(part); msg != nil && isRole(msg.Role, sdk.MessageRoleTool) {
			return true
		}
	}
	return false
}

func hasAskUserTool(frag contextfrag.ContextFrag) bool {
	for _, part := range frag.Parts {
		if msg := sdkMessagePart(part); msg != nil && messageHasToolName(*msg, "ask_user") {
			return true
		}
	}
	return false
}

func messageHasToolName(msg sdk.Message, name string) bool {
	for _, part := range msg.Content {
		switch p := part.(type) {
		case sdk.ToolCallPart:
			if equalToolName(p.ToolName, name) {
				return true
			}
		case sdk.ToolResultPart:
			if equalToolName(p.ToolName, name) {
				return true
			}
		default:
			if equalToolName(rawToolName(part), name) {
				return true
			}
		}
	}
	return false
}

func equalToolName(got, want string) bool {
	return strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want))
}

func isRole(got, want sdk.MessageRole) bool {
	return strings.EqualFold(strings.TrimSpace(string(got)), string(want))
}

func rawToolName(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var envelope struct {
		ToolName string `json:"toolName"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.ToolName)
}

func appendSelectionTag(tags []SelectionTag, tag SelectionTag) []SelectionTag {
	if hasSelectionTag(tags, tag) {
		return tags
	}
	return append(tags, tag)
}

func hasSelectionTag(tags []SelectionTag, tag SelectionTag) bool {
	for _, existing := range tags {
		if existing == tag {
			return true
		}
	}
	return false
}

// selectionDropReason reports the mutually exclusive retention state first
// (must_keep / preserve_recent / can_drop) so a droppable tool-exchange
// fragment reads as can_drop instead of the misleading closure tag; the
// closure tag only surfaces when it is the sole marker.
func selectionDropReason(tagged TaggedFrag) string {
	for _, tag := range []SelectionTag{TagMustKeep, TagPreserveRecent, TagCanDrop, TagPreserveToolClosure} {
		if tagged.HasTag(tag) {
			return string(tag)
		}
	}
	if tagged.Frag.ID != "" {
		return "not_selected"
	}
	return string(TagCanDrop)
}
