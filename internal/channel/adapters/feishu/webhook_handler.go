package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/felinics/memoh/internal/channel"
)

const webhookMaxBodyBytes int64 = 1 << 20 // 1 MiB

// HandleWebhook processes Feishu/Lark event-subscription callbacks.
func (a *FeishuAdapter) HandleWebhook(ctx context.Context, cfg channel.ChannelConfig, handler channel.InboundHandler, r *http.Request, w http.ResponseWriter) error {
	if a == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "feishu adapter is nil")
	}
	if handler == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "feishu inbound handler is nil")
	}
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return nil
	}
	if r.Method != http.MethodPost {
		return echo.NewHTTPError(http.StatusMethodNotAllowed, "method not allowed")
	}

	feishuCfg, err := parseConfig(cfg.Credentials)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if feishuCfg.InboundMode != inboundModeWebhook {
		return echo.NewHTTPError(http.StatusBadRequest, "feishu inbound_mode is not webhook")
	}

	payload, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBodyBytes+1))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("read body: %v", err))
	}
	if int64(len(payload)) > webhookMaxBodyBytes {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, fmt.Sprintf("payload too large: max %d bytes", webhookMaxBodyBytes))
	}

	botOpenID := a.resolveBotOpenID(context.WithoutCancel(ctx), cfg)
	eventDispatcher := dispatcher.NewEventDispatcher(feishuCfg.VerificationToken, feishuCfg.EncryptKey)
	webhookReq, err := inspectWebhookRequest(ctx, eventDispatcher, r, payload)
	if err != nil {
		return err
	}
	if err := validateWebhookCallbackAuth(webhookReq, feishuCfg); err != nil {
		return err
	}
	if challengeResp := buildWebhookChallengeResponse(webhookReq); challengeResp != nil {
		return writeEventResponse(w, challengeResp)
	}
	eventDispatcher.OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
		msg := extractFeishuInbound(event, botOpenID, a.logger)
		if strings.TrimSpace(msg.Message.PlainText()) == "" && len(msg.Message.Attachments) == 0 {
			return nil
		}
		a.enrichSenderProfile(ctx, cfg, event, &msg)
		a.enrichQuotedMessage(ctx, cfg, &msg, botOpenID)
		msg.BotID = cfg.BotID
		return handler(ctx, cfg, msg)
	})

	resp := eventDispatcher.Handle(ctx, &larkevent.EventReq{
		Header:     r.Header,
		Body:       payload,
		RequestURI: r.RequestURI,
	})
	if resp == nil {
		w.WriteHeader(http.StatusOK)
		return nil
	}
	return writeEventResponse(w, resp)
}

func inspectWebhookRequest(ctx context.Context, eventDispatcher *dispatcher.EventDispatcher, req *http.Request, payload []byte) (larkevent.EventFuzzy, error) {
	plainPayload, err := parseWebhookPayload(ctx, eventDispatcher, req, payload)
	if err != nil {
		return larkevent.EventFuzzy{}, echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid feishu webhook payload: %v", err))
	}

	var fuzzy larkevent.EventFuzzy
	if err := json.Unmarshal([]byte(plainPayload), &fuzzy); err != nil {
		return larkevent.EventFuzzy{}, echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid feishu webhook payload: %v", err))
	}
	return fuzzy, nil
}

func validateWebhookCallbackAuth(fuzzy larkevent.EventFuzzy, cfg Config) error {
	expectedToken := strings.TrimSpace(cfg.VerificationToken)
	encryptKey := strings.TrimSpace(cfg.EncryptKey)
	if expectedToken == "" && encryptKey == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "feishu webhook requires encrypt_key or verification_token")
	}

	requestToken := webhookRequestToken(fuzzy)
	if expectedToken == "" {
		return nil
	}
	if requestToken == "" || requestToken != expectedToken {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid feishu webhook token")
	}
	return nil
}

func buildWebhookChallengeResponse(fuzzy larkevent.EventFuzzy) *larkevent.EventResp {
	if webhookRequestType(fuzzy) != larkevent.ReqTypeChallenge {
		return nil
	}
	return &larkevent.EventResp{
		Header:     http.Header{larkevent.ContentTypeHeader: []string{larkevent.DefaultContentType}},
		Body:       []byte(fmt.Sprintf(larkevent.ChallengeResponseFormat, fuzzy.Challenge)),
		StatusCode: http.StatusOK,
	}
}

func webhookRequestToken(fuzzy larkevent.EventFuzzy) string {
	requestToken := strings.TrimSpace(fuzzy.Token)
	if fuzzy.Header != nil && strings.TrimSpace(fuzzy.Header.Token) != "" {
		requestToken = strings.TrimSpace(fuzzy.Header.Token)
	}
	return requestToken
}

func webhookRequestType(fuzzy larkevent.EventFuzzy) larkevent.ReqType {
	return larkevent.ReqType(strings.TrimSpace(fuzzy.Type))
}

func parseWebhookPayload(ctx context.Context, eventDispatcher *dispatcher.EventDispatcher, req *http.Request, payload []byte) (string, error) {
	cipherPayload, err := eventDispatcher.ParseReq(ctx, &larkevent.EventReq{
		Header:     req.Header,
		Body:       payload,
		RequestURI: req.RequestURI,
	})
	if err != nil {
		return "", err
	}
	return eventDispatcher.DecryptEvent(ctx, cipherPayload)
}

func writeEventResponse(w http.ResponseWriter, resp *larkevent.EventResp) error {
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if len(resp.Body) == 0 {
		return nil
	}
	_, err := w.Write(resp.Body) //nolint:gosec // Response body is generated by the verified Feishu SDK event response.
	return err
}
