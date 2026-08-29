package feishu

import (
	"context"
	"errors"
	"fmt"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/felinics/memoh/internal/channel"
)

const (
	defaultDirectoryPageSize = 20
	maxDirectoryPageSize     = 200
)

func directoryLimit(n int) int {
	if n <= 0 {
		return defaultDirectoryPageSize
	}
	if n > maxDirectoryPageSize {
		return maxDirectoryPageSize
	}
	return n
}

// ListPeers lists users (peers) from Feishu contact, optionally filtered by query.
func (*FeishuAdapter) ListPeers(ctx context.Context, cfg channel.ChannelConfig, query channel.DirectoryQuery) ([]channel.DirectoryEntry, error) {
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	client := feishuCfg.newClient()
	pageSize := directoryLimit(query.Limit)
	req := larkcontact.NewListUserReqBuilder().
		UserIdType(larkcontact.UserIdTypeOpenId).
		DepartmentIdType(larkcontact.DepartmentIdTypeOpenDepartmentId).
		DepartmentId("0").
		PageSize(pageSize).
		Build()
	resp, err := client.Contact.User.List(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("feishu list users: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("feishu list users: code=%d msg=%s", resp.Code, resp.Msg)
	}
	entries := make([]channel.DirectoryEntry, 0, len(resp.Data.Items))
	for _, u := range resp.Data.Items {
		e := feishuUserToEntry(u)
		if query.Query != "" && !strings.Contains(strings.ToLower(e.Name+e.Handle), strings.ToLower(query.Query)) {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// ListGroups lists chat groups from Feishu IM, optionally filtered by query.
func (*FeishuAdapter) ListGroups(ctx context.Context, cfg channel.ChannelConfig, query channel.DirectoryQuery) ([]channel.DirectoryEntry, error) {
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	client := feishuCfg.newClient()
	pageSize := directoryLimit(query.Limit)
	var items []*larkim.ListChat
	if strings.TrimSpace(query.Query) != "" {
		req := larkim.NewSearchChatReqBuilder().
			UserIdType("open_id").
			Query(strings.TrimSpace(query.Query)).
			PageSize(pageSize).
			Build()
		resp, err := client.Im.Chat.Search(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("feishu search chats: %w", err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("feishu search chats: code=%d msg=%s", resp.Code, resp.Msg)
		}
		items = resp.Data.Items
	} else {
		req := larkim.NewListChatReqBuilder().
			UserIdType("open_id").
			PageSize(pageSize).
			Build()
		resp, err := client.Im.Chat.List(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("feishu list chats: %w", err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf("feishu list chats: code=%d msg=%s", resp.Code, resp.Msg)
		}
		items = resp.Data.Items
	}
	entries := make([]channel.DirectoryEntry, 0, len(items))
	for _, c := range items {
		entries = append(entries, feishuChatToEntry(c))
	}
	return entries, nil
}

// ListGroupMembers lists members of a Feishu chat group.
func (*FeishuAdapter) ListGroupMembers(ctx context.Context, cfg channel.ChannelConfig, groupID string, query channel.DirectoryQuery) ([]channel.DirectoryEntry, error) {
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	chatID := strings.TrimSpace(groupID)
	chatID = strings.TrimPrefix(chatID, "chat_id:")
	if chatID == "" {
		return nil, errors.New("feishu list group members: empty group id")
	}
	client := feishuCfg.newClient()
	pageSize := directoryLimit(query.Limit)
	req := larkim.NewGetChatMembersReqBuilder().
		ChatId(chatID).
		MemberIdType("open_id").
		PageSize(pageSize).
		Build()
	resp, err := client.Im.ChatMembers.Get(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("feishu get chat members: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("feishu get chat members: code=%d msg=%s", resp.Code, resp.Msg)
	}
	entries := make([]channel.DirectoryEntry, 0, len(resp.Data.Items))
	for _, m := range resp.Data.Items {
		e := feishuMemberToEntry(m)
		if query.Query != "" && !strings.Contains(strings.ToLower(e.Name+e.Handle), strings.ToLower(query.Query)) {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// ResolveEntry resolves an input string to a user or group DirectoryEntry.
func (a *FeishuAdapter) ResolveEntry(ctx context.Context, cfg channel.ChannelConfig, input string, kind channel.DirectoryEntryKind) (channel.DirectoryEntry, error) {
	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return channel.DirectoryEntry{}, err
	}
	client := feishuCfg.newClient()
	input = strings.TrimSpace(input)
	switch kind {
	case channel.DirectoryEntryUser:
		return a.resolveUser(ctx, client, input)
	case channel.DirectoryEntryGroup:
		return a.resolveGroup(ctx, client, input)
	default:
		return channel.DirectoryEntry{}, fmt.Errorf("feishu resolve entry: unsupported kind %q", kind)
	}
}

func (*FeishuAdapter) resolveUser(ctx context.Context, client *lark.Client, input string) (channel.DirectoryEntry, error) {
	userID, userIDType := parseFeishuUserInput(input)
	if userID == "" {
		return channel.DirectoryEntry{}, fmt.Errorf("feishu resolve entry user: invalid input %q", input)
	}
	req := larkcontact.NewGetUserReqBuilder().
		UserId(userID).
		UserIdType(userIDType).
		Build()
	resp, err := client.Contact.User.Get(ctx, req)
	if err != nil {
		return channel.DirectoryEntry{}, fmt.Errorf("feishu get user: %w", err)
	}
	if !resp.Success() {
		return channel.DirectoryEntry{}, fmt.Errorf("feishu get user: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.User == nil {
		return channel.DirectoryEntry{}, errors.New("feishu get user: empty response")
	}
	return feishuUserToEntry(resp.Data.User), nil
}

func (*FeishuAdapter) resolveGroup(ctx context.Context, client *lark.Client, input string) (channel.DirectoryEntry, error) {
	chatID := strings.TrimSpace(input)
	chatID = strings.TrimPrefix(chatID, "chat_id:")
	if chatID == "" {
		return channel.DirectoryEntry{}, fmt.Errorf("feishu resolve entry group: invalid input %q", input)
	}
	req := larkim.NewGetChatReqBuilder().
		ChatId(chatID).
		UserIdType("open_id").
		Build()
	resp, err := client.Im.Chat.Get(ctx, req)
	if err != nil {
		return channel.DirectoryEntry{}, fmt.Errorf("feishu get chat: %w", err)
	}
	if !resp.Success() {
		return channel.DirectoryEntry{}, fmt.Errorf("feishu get chat: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return channel.DirectoryEntry{
		Kind:      channel.DirectoryEntryGroup,
		ID:        "chat_id:" + chatID,
		Name:      ptrStr(resp.Data.Name),
		AvatarURL: ptrStr(resp.Data.Avatar),
		Metadata:  map[string]any{"chat_id": chatID},
	}, nil
}

func parseFeishuUserInput(raw string) (userID, userIDType string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if strings.HasPrefix(raw, "open_id:") {
		return strings.TrimSpace(strings.TrimPrefix(raw, "open_id:")), larkcontact.UserIdTypeOpenId
	}
	if strings.HasPrefix(raw, "user_id:") {
		return strings.TrimSpace(strings.TrimPrefix(raw, "user_id:")), larkcontact.UserIdTypeUserId
	}
	if strings.HasPrefix(raw, "ou_") {
		return raw, larkcontact.UserIdTypeOpenId
	}
	if strings.HasPrefix(raw, "u_") || strings.HasPrefix(raw, "u-") {
		return raw, larkcontact.UserIdTypeUserId
	}
	// For raw IDs without explicit prefix, default to user_id. In practice
	// open_id is usually "ou_*", while bare IDs are commonly user_id.
	return raw, larkcontact.UserIdTypeUserId
}

func feishuUserToEntry(u *larkcontact.User) channel.DirectoryEntry {
	openID := ptrStr(u.OpenId)
	userID := ptrStr(u.UserId)
	id := "open_id:" + openID
	if openID == "" && userID != "" {
		id = "user_id:" + userID
	}
	meta := make(map[string]any)
	if u.OpenId != nil {
		meta["open_id"] = *u.OpenId
	}
	if u.UserId != nil {
		meta["user_id"] = *u.UserId
	}
	return channel.DirectoryEntry{
		Kind:      channel.DirectoryEntryUser,
		ID:        id,
		Name:      ptrStr(u.Name),
		Handle:    ptrStr(u.Nickname),
		AvatarURL: feishuAvatarURL(u.Avatar),
		Metadata:  meta,
	}
}

func feishuChatToEntry(c *larkim.ListChat) channel.DirectoryEntry {
	chatID := ptrStr(c.ChatId)
	meta := map[string]any{"chat_id": chatID}
	return channel.DirectoryEntry{
		Kind:      channel.DirectoryEntryGroup,
		ID:        "chat_id:" + chatID,
		Name:      ptrStr(c.Name),
		AvatarURL: ptrStr(c.Avatar),
		Metadata:  meta,
	}
}

func feishuMemberToEntry(m *larkim.ListMember) channel.DirectoryEntry {
	id := ptrStr(m.MemberId)
	meta := make(map[string]any)
	if m.MemberIdType != nil {
		meta["member_id_type"] = *m.MemberIdType
	}
	prefix := "open_id:"
	if m.MemberIdType != nil && *m.MemberIdType == "user_id" {
		prefix = "user_id:"
	}
	return channel.DirectoryEntry{
		Kind:     channel.DirectoryEntryUser,
		ID:       prefix + id,
		Name:     ptrStr(m.Name),
		Metadata: meta,
	}
}

func feishuAvatarURL(avatar *larkcontact.AvatarInfo) string {
	if avatar == nil || avatar.Avatar72 == nil {
		return ""
	}
	return strings.TrimSpace(*avatar.Avatar72)
}
