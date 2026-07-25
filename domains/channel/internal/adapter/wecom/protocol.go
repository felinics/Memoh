package wecom

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	defaultWSURL = "wss://openws.work.weixin.qq.com"
)

const (
	WSCmdSubscribe      = "aibot_subscribe"
	WSCmdHeartbeat      = "ping"
	WSCmdRespond        = "aibot_respond_msg"
	WSCmdRespondWelcome = "aibot_respond_welcome_msg"
	WSCmdRespondUpdate  = "aibot_respond_update_msg"
	WSCmdSendMessage    = "aibot_send_msg"
	WSCmdMsgCallback    = "aibot_msg_callback"
	WSCmdEventCallback  = "aibot_event_callback"
)

type WSHeaders struct {
	ReqID string `json:"req_id"`
}

type WSFrame struct {
	Cmd     string          `json:"cmd,omitempty"`
	Headers WSHeaders       `json:"headers"`
	Body    json.RawMessage `json:"body,omitempty"`
	ErrCode int             `json:"errcode,omitempty"`
	ErrMsg  string          `json:"errmsg,omitempty"`
}

func (f WSFrame) DecodeBody(dst any) error {
	if len(f.Body) == 0 {
		return errors.New("wecom frame body is empty")
	}
	if dst == nil {
		return errors.New("decode target is nil")
	}
	return json.Unmarshal(f.Body, dst)
}

func BuildFrame(cmd, reqID string, body any) (WSFrame, error) {
	frame := WSFrame{
		Cmd: cmd,
		Headers: WSHeaders{
			ReqID: strings.TrimSpace(reqID),
		},
	}
	if frame.Headers.ReqID == "" {
		return WSFrame{}, errors.New("req_id is required")
	}
	if body == nil {
		return frame, nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return WSFrame{}, err
	}
	frame.Body = raw
	return frame, nil
}

func NewReqID(prefix string) string {
	p := strings.TrimSpace(prefix)
	if p == "" {
		return uuid.NewString()
	}
	return p + "_" + uuid.NewString()
}

type AuthCredentials struct {
	BotID      string
	Credential string
}

func (c AuthCredentials) Validate() error {
	if strings.TrimSpace(c.BotID) == "" {
		return errors.New("wecom bot_id is required")
	}
	if strings.TrimSpace(c.Credential) == "" {
		return errors.New("wecom secret is required")
	}
	return nil
}

type CallbackFrom struct {
	UserID string `json:"userid,omitempty"`
}

type MessageText struct {
	Content string `json:"content,omitempty"`
}

type MessageImage struct {
	URL    string `json:"url,omitempty"`
	AESKey string `json:"aeskey,omitempty"`
}

type MessageFile struct {
	URL      string `json:"url,omitempty"`
	AESKey   string `json:"aeskey,omitempty"`
	FileName string `json:"file_name,omitempty"`
}

type MessageVoice struct {
	Content string `json:"content,omitempty"`
}

type MessageVideo struct {
	URL    string `json:"url,omitempty"`
	AESKey string `json:"aeskey,omitempty"`
}

type MessageMixedItem struct {
	MsgType  string        `json:"msgtype,omitempty"`
	Text     *MessageText  `json:"text,omitempty"`
	Markdown *MessageText  `json:"markdown,omitempty"`
	Image    *MessageImage `json:"image,omitempty"`
	File     *MessageFile  `json:"file,omitempty"`
	Voice    *MessageVoice `json:"voice,omitempty"`
	Video    *MessageVideo `json:"video,omitempty"`
}

type MessageQuote struct {
	MsgID string `json:"msgid,omitempty"`
}

type MessageCallbackBody struct {
	MsgID       string             `json:"msgid,omitempty"`
	AIBotID     string             `json:"aibotid,omitempty"`
	ChatID      string             `json:"chatid,omitempty"`
	ChatType    string             `json:"chattype,omitempty"`
	From        CallbackFrom       `json:"from,omitempty"`
	CreateTime  int64              `json:"create_time,omitempty"`
	MsgType     string             `json:"msgtype,omitempty"`
	ResponseURL string             `json:"response_url,omitempty"`
	Text        *MessageText       `json:"text,omitempty"`
	Markdown    *MessageText       `json:"markdown,omitempty"`
	Image       *MessageImage      `json:"image,omitempty"`
	File        *MessageFile       `json:"file,omitempty"`
	Voice       *MessageVoice      `json:"voice,omitempty"`
	Video       *MessageVideo      `json:"video,omitempty"`
	Mixed       []MessageMixedItem `json:"mixed,omitempty"`
	Quote       *MessageQuote      `json:"quote,omitempty"`
}

type EventPayload struct {
	EventType  string `json:"event_type,omitempty"`
	EventType2 string `json:"eventtype,omitempty"`
	EventKey   string `json:"event_key,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	Code       string `json:"code,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type EventTask struct {
	TaskID     string `json:"task_id,omitempty"`
	TaskStatus string `json:"task_status,omitempty"`
}

type EventCallbackBody struct {
	MsgID       string       `json:"msgid,omitempty"`
	AIBotID     string       `json:"aibotid,omitempty"`
	ChatID      string       `json:"chatid,omitempty"`
	ChatType    string       `json:"chattype,omitempty"`
	From        CallbackFrom `json:"from,omitempty"`
	CreateTime  int64        `json:"create_time,omitempty"`
	MsgType     string       `json:"msgtype,omitempty"`
	ResponseURL string       `json:"response_url,omitempty"`
	Event       EventPayload `json:"event,omitempty"`
	Task        EventTask    `json:"task,omitempty"`
}

type StreamReplyBody struct {
	MsgType string           `json:"msgtype"`
	Stream  StreamReplyBlock `json:"stream"`
}

type StreamReplyBlock struct {
	ID       string               `json:"id"`
	Finish   bool                 `json:"finish,omitempty"`
	Content  string               `json:"content,omitempty"`
	MsgItems []StreamReplyItem    `json:"msg_item,omitempty"`
	Feedback *StreamReplyFeedback `json:"feedback,omitempty"`
}

type StreamReplyItem struct {
	MsgType string            `json:"msgtype"`
	Image   *StreamReplyImage `json:"image,omitempty"`
}

type StreamReplyImage struct {
	Base64 string `json:"base64"`
	MD5    string `json:"md5"`
}

type StreamReplyFeedback struct {
	ID string `json:"id"`
}

type SendMessageMarkdownBody struct {
	ChatID   string          `json:"chatid"`
	MsgType  string          `json:"msgtype"`
	Markdown markdownPayload `json:"markdown"`
}

type SendMessageTemplateCardBody struct {
	ChatID       string         `json:"chatid"`
	MsgType      string         `json:"msgtype"`
	TemplateCard map[string]any `json:"template_card"`
}

type StreamWithTemplateCardReplyBody struct {
	MsgType      string           `json:"msgtype"`
	Stream       StreamReplyBlock `json:"stream"`
	TemplateCard map[string]any   `json:"template_card"`
}

type WelcomeTextReplyBody struct {
	MsgType string          `json:"msgtype"`
	Text    welcomeTextBody `json:"text"`
}

type welcomeTextBody struct {
	Content string `json:"content"`
}

type WelcomeTemplateCardReplyBody struct {
	MsgType      string         `json:"msgtype"`
	TemplateCard map[string]any `json:"template_card"`
}

type UpdateTemplateCardBody struct {
	ResponseType string         `json:"response_type"`
	UserIDs      []string       `json:"userids,omitempty"`
	TemplateCard map[string]any `json:"template_card"`
}

func unixMilliseconds(ts int64) time.Time {
	if ts <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ts)
}
