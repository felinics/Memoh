package telegram

import (
	"encoding/json"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

type telegramInputRichMessage struct {
	HTML                string `json:"html,omitempty"`
	Markdown            string `json:"markdown,omitempty"`
	SkipEntityDetection bool   `json:"skip_entity_detection,omitempty"`
}

func (rich telegramInputRichMessage) hasContent() bool {
	return strings.TrimSpace(rich.HTML) != "" || strings.TrimSpace(rich.Markdown) != ""
}

func (rich telegramInputRichMessage) content() string {
	if strings.TrimSpace(rich.HTML) != "" {
		return rich.HTML
	}
	return rich.Markdown
}

func writeTelegramRichParagraph(b *strings.Builder, html string) {
	html = strings.TrimSpace(html)
	if html == "" {
		return
	}
	b.WriteString("<p>")
	b.WriteString(html)
	b.WriteString("</p>")
}

func telegramEscapeAttr(value string) string {
	return strings.ReplaceAll(telegramEscapeHTML(value), `"`, "&quot;")
}

func isAllowedTelegramRichHref(href string) bool {
	href = strings.TrimSpace(href)
	return strings.HasPrefix(href, "https://") ||
		strings.HasPrefix(href, "http://") ||
		strings.HasPrefix(href, "mailto:") ||
		strings.HasPrefix(href, "tel:") ||
		strings.HasPrefix(href, "tg://user?id=") ||
		strings.HasPrefix(href, "#")
}

func sendTelegramRichMessageReturnMessage(
	bot *tele.Bot,
	target string,
	rich telegramInputRichMessage,
	replyTo int,
	actions []channel.Action,
) (chatID int64, messageID int, err error) {
	if !rich.hasContent() {
		return 0, 0, nil
	}
	if err := validateTelegramActions(actions); err != nil {
		return 0, 0, err
	}
	parsedChatID, channelUsername, parseErr := parseTelegramTarget(target)
	if parseErr != nil {
		return 0, 0, parseErr
	}
	payload := map[string]any{"rich_message": rich}
	if channelUsername != "" {
		payload["chat_id"] = channelUsername
	} else {
		payload["chat_id"] = strconv.FormatInt(parsedChatID, 10)
	}
	if replyTo > 0 {
		payload["reply_parameters"] = map[string]any{"message_id": replyTo}
	}
	markup := telegramInlineKeyboard(actions)
	if markup != nil && len(markup.InlineKeyboard) > 0 {
		payload["reply_markup"] = markup
	}
	data, err := bot.Raw("sendRichMessage", payload)
	if err != nil {
		return 0, 0, err
	}
	var resp struct {
		Result *tele.Message `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, 0, err
	}
	chatID = parsedChatID
	if resp.Result != nil && resp.Result.Chat != nil {
		chatID = resp.Result.Chat.ID
	}
	if resp.Result == nil {
		return chatID, 0, nil
	}
	return chatID, resp.Result.ID, nil
}

func rawEditTelegramRichMessage(
	bot *tele.Bot,
	chatID int64,
	messageID int,
	rich telegramInputRichMessage,
	actions []channel.Action,
) error {
	if !rich.hasContent() {
		return nil
	}
	if err := validateTelegramActions(actions); err != nil {
		return err
	}
	payload := map[string]any{
		"chat_id":      strconv.FormatInt(chatID, 10),
		"message_id":   messageID,
		"rich_message": rich,
	}
	markup := telegramInlineKeyboard(actions)
	if markup != nil && len(markup.InlineKeyboard) > 0 {
		payload["reply_markup"] = markup
	}
	_, err := bot.Raw("editMessageText", payload)
	return err
}

func editTelegramRichMessage(
	bot *tele.Bot,
	chatID int64,
	messageID int,
	rich telegramInputRichMessage,
	actions []channel.Action,
) error {
	err := rawEditTelegramRichMessage(bot, chatID, messageID, rich, actions)
	if err != nil && (isTelegramMessageNotModified(err) || isTelegramEditUnrecoverable(err)) {
		return nil
	}
	return err
}
