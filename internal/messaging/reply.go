package messaging

import (
	"errors"
	"fmt"
	"strings"
)

// ReplyMessageIDFromArgs returns the effective reply message ID accepted by
// the send executor. A non-empty top-level reply_to takes precedence over the
// legacy message.reply.message_id form; an empty reply is equivalent to no
// reply. The function does not mutate the caller's arguments.
func ReplyMessageIDFromArgs(args map[string]any) (string, error) {
	if raw, present := args["reply_to"]; present && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return "", errors.New("reply_to must be string")
		}
		if messageID := strings.TrimSpace(value); messageID != "" {
			return messageID, nil
		}
	}

	rawMessage, present := args["message"]
	if !present || rawMessage == nil {
		return "", nil
	}
	message, ok := rawMessage.(map[string]any)
	if !ok {
		// A plain string is a valid message input and cannot contain a reply.
		if _, isText := rawMessage.(string); isText {
			return "", nil
		}
		return "", errors.New("message must be object or string")
	}
	rawReply, present := message["reply"]
	if !present || rawReply == nil {
		return "", nil
	}
	return replyMessageIDFromValue(rawReply)
}

func replyMessageIDFromValue(raw any) (string, error) {
	reply, ok := raw.(map[string]any)
	if !ok {
		return "", errors.New("message reply must be object")
	}
	for key := range reply {
		if key != "message_id" {
			return "", fmt.Errorf("unknown message reply field %q", key)
		}
	}
	messageID, _ := reply["message_id"].(string)
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return "", errors.New("message reply message_id is required")
	}
	return messageID, nil
}
