package feishu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/felinics/memoh/internal/channel"
)

type feishuSenderProfile struct {
	displayName string
	username    string
	avatarURL   string
}

const feishuChatMembersPageSize = 100

// enrichSenderProfile fills sender display name / username for inbound messages.
// It first tries Contact.User.Get (open_id/user_id), then falls back to group member
// lookup when permissions are limited.
func (a *FeishuAdapter) enrichSenderProfile(ctx context.Context, cfg channel.ChannelConfig, event *larkim.P2MessageReceiveV1, msg *channel.InboundMessage) {
	if msg == nil {
		return
	}
	needDisplay := strings.TrimSpace(msg.Sender.DisplayName) == "" &&
		strings.TrimSpace(msg.Sender.Attribute("display_name")) == "" &&
		strings.TrimSpace(msg.Sender.Attribute("name")) == ""
	needUsername := strings.TrimSpace(msg.Sender.Attribute("username")) == ""
	if !needDisplay && !needUsername {
		return
	}

	openID := strings.TrimSpace(msg.Sender.Attribute("open_id"))
	userID := strings.TrimSpace(msg.Sender.Attribute("user_id"))
	if openID == "" && userID == "" {
		return
	}

	chatID := ""
	if event != nil && event.Event != nil && event.Event.Message != nil && event.Event.Message.ChatId != nil {
		chatID = strings.TrimSpace(*event.Event.Message.ChatId)
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	profile, err := a.lookupSenderProfile(lookupCtx, cfg, openID, userID, chatID)
	if err != nil {
		if a.logger != nil {
			a.logger.Debug("feishu sender profile lookup failed",
				slog.String("config_id", cfg.ID),
				slog.String("open_id", openID),
				slog.String("user_id", userID),
				slog.String("chat_id", chatID),
				slog.Any("error", err),
			)
		}
	}
	if strings.TrimSpace(profile.displayName) == "" && strings.TrimSpace(profile.username) == "" && strings.TrimSpace(profile.avatarURL) == "" {
		profile = fallbackSenderProfile(openID, userID)
	}
	applySenderProfile(msg, profile)
}

func (*FeishuAdapter) lookupSenderProfile(ctx context.Context, cfg channel.ChannelConfig, openID, userID, chatID string) (feishuSenderProfile, error) {
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return feishuSenderProfile{}, err
	}
	client := feishuCfg.newClient()

	var lastErr error
	chatID = strings.TrimSpace(chatID)
	chatID = strings.TrimPrefix(chatID, "chat_id:")

	// Group scene: chat members has the highest chance to return a human-readable name.
	if chatID != "" && openID != "" {
		if profile, err := lookupSenderProfileFromGroupMember(ctx, client, chatID, "open_id", openID); err == nil {
			if strings.TrimSpace(profile.displayName) != "" || strings.TrimSpace(profile.username) != "" {
				return profile, nil
			}
		} else {
			lastErr = err
		}
	}
	if chatID != "" && userID != "" {
		if profile, err := lookupSenderProfileFromGroupMember(ctx, client, chatID, "user_id", userID); err == nil {
			if strings.TrimSpace(profile.displayName) != "" || strings.TrimSpace(profile.username) != "" {
				return profile, nil
			}
		} else {
			lastErr = err
		}
	}

	if profile, err := lookupSenderProfileFromContact(ctx, client, openID, userID); err == nil {
		if strings.TrimSpace(profile.displayName) != "" || strings.TrimSpace(profile.username) != "" {
			return profile, nil
		}
	} else {
		lastErr = err
	}

	if lastErr != nil {
		return feishuSenderProfile{}, lastErr
	}
	return feishuSenderProfile{}, nil
}

func lookupSenderProfileFromContact(ctx context.Context, client *lark.Client, openID, userID string) (feishuSenderProfile, error) {
	lookupID := strings.TrimSpace(openID)
	idType := larkcontact.UserIdTypeOpenId
	if lookupID == "" {
		lookupID = strings.TrimSpace(userID)
		idType = larkcontact.UserIdTypeUserId
	}
	if lookupID == "" {
		return feishuSenderProfile{}, errors.New("empty sender id")
	}
	req := larkcontact.NewGetUserReqBuilder().
		UserIdType(idType).
		UserId(lookupID).
		Build()
	resp, err := client.Contact.User.Get(ctx, req)
	if err != nil {
		return feishuSenderProfile{}, err
	}
	if resp == nil || !resp.Success() {
		code := 0
		msg := ""
		if resp != nil {
			code = resp.Code
			msg = strings.TrimSpace(resp.Msg)
		}
		return feishuSenderProfile{}, fmt.Errorf("feishu get user failed: code=%d msg=%s", code, msg)
	}
	if resp.Data == nil || resp.Data.User == nil {
		return feishuSenderProfile{}, errors.New("feishu get user returned empty user")
	}
	displayName := ptrStr(resp.Data.User.Name)
	username := ptrStr(resp.Data.User.Nickname)
	if username == "" {
		username = displayName
	}
	return feishuSenderProfile{
		displayName: displayName,
		username:    username,
		avatarURL:   feishuAvatarURL(resp.Data.User.Avatar),
	}, nil
}

func lookupSenderProfileFromGroupMember(ctx context.Context, client *lark.Client, chatID, memberIDType, memberID string) (feishuSenderProfile, error) {
	memberIDType = strings.TrimSpace(memberIDType)
	memberID = strings.TrimSpace(memberID)
	if memberIDType == "" || memberID == "" {
		return feishuSenderProfile{}, errors.New("empty member lookup input")
	}
	pageToken := ""
	for page := 0; page < 5; page++ {
		builder := larkim.NewGetChatMembersReqBuilder().
			ChatId(chatID).
			MemberIdType(memberIDType).
			PageSize(feishuChatMembersPageSize)
		if pageToken != "" {
			builder = builder.PageToken(pageToken)
		}
		resp, err := client.Im.ChatMembers.Get(ctx, builder.Build())
		if err != nil {
			return feishuSenderProfile{}, err
		}
		if resp == nil || !resp.Success() {
			code := 0
			msg := ""
			if resp != nil {
				code = resp.Code
				msg = strings.TrimSpace(resp.Msg)
			}
			return feishuSenderProfile{}, fmt.Errorf("feishu get chat members failed: code=%d msg=%s", code, msg)
		}
		if resp.Data == nil {
			return feishuSenderProfile{}, nil
		}
		for _, item := range resp.Data.Items {
			if item == nil {
				continue
			}
			if strings.TrimSpace(ptrStr(item.MemberId)) != memberID {
				continue
			}
			name := ptrStr(item.Name)
			username := firstNameFallback(name)
			if username == "" {
				username = name
			}
			return feishuSenderProfile{
				displayName: name,
				username:    username,
			}, nil
		}
		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		if !hasMore || resp.Data.PageToken == nil {
			break
		}
		pageToken = strings.TrimSpace(*resp.Data.PageToken)
		if pageToken == "" {
			break
		}
	}
	return feishuSenderProfile{}, nil
}

func fallbackSenderProfile(openID, userID string) feishuSenderProfile {
	openID = strings.TrimSpace(openID)
	userID = strings.TrimSpace(userID)
	username := userID
	if username == "" {
		username = openID
	}
	if username == "" {
		return feishuSenderProfile{}
	}
	displayName := username
	return feishuSenderProfile{
		displayName: displayName,
		username:    username,
	}
}

func firstNameFallback(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func applySenderProfile(msg *channel.InboundMessage, profile feishuSenderProfile) {
	if msg == nil {
		return
	}
	displayName := strings.TrimSpace(profile.displayName)
	username := strings.TrimSpace(profile.username)
	if username == "" {
		username = displayName
	}
	if msg.Sender.Attributes == nil {
		msg.Sender.Attributes = map[string]string{}
	}
	if displayName != "" {
		if strings.TrimSpace(msg.Sender.DisplayName) == "" {
			msg.Sender.DisplayName = displayName
		}
		if strings.TrimSpace(msg.Sender.Attributes["display_name"]) == "" {
			msg.Sender.Attributes["display_name"] = displayName
		}
		if strings.TrimSpace(msg.Sender.Attributes["name"]) == "" {
			msg.Sender.Attributes["name"] = displayName
		}
	}
	if username != "" && strings.TrimSpace(msg.Sender.Attributes["username"]) == "" {
		msg.Sender.Attributes["username"] = username
	}
	if avatarURL := strings.TrimSpace(profile.avatarURL); avatarURL != "" {
		if strings.TrimSpace(msg.Sender.Attributes["avatar_url"]) == "" {
			msg.Sender.Attributes["avatar_url"] = avatarURL
		}
	}
}
