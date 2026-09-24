package weixin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/channel"
)

// QRHandler handles WeChat QR code login for the management UI.
type QRHandler struct {
	logger    *slog.Logger
	client    *Client
	lifecycle *channel.Lifecycle
}

// NewQRHandler creates a QR handler.
func NewQRHandler(log *slog.Logger, lifecycle *channel.Lifecycle) *QRHandler {
	if log == nil {
		log = slog.Default()
	}
	return &QRHandler{
		logger:    log.With(slog.String("handler", "weixin_qr")),
		client:    NewClient(log),
		lifecycle: lifecycle,
	}
}

// NewQRServerHandler is a DI-friendly constructor for fx, returning the handler
// that implements server.Handler.
func NewQRServerHandler(log *slog.Logger, lifecycle *channel.Lifecycle) *QRHandler {
	return NewQRHandler(log, lifecycle)
}

// Register registers QR login routes on the Echo instance.
func (h *QRHandler) Register(e *echo.Echo) {
	e.POST("/bots/:id/channel/weixin/qr/start", h.Start)
	e.POST("/bots/:id/channel/weixin/qr/poll", h.Poll)
}

// QRStartResponse returns QR code data to the frontend.
type QRStartResponse struct {
	QRCodeURL string `json:"qr_code_url"`
	QRCode    string `json:"qr_code"`
	Message   string `json:"message"`
}

// Start godoc
// @Summary Start WeChat QR login
// @Description Fetch a QR code from WeChat for scanning.
// @Tags bots
// @Param id path string true "Bot ID"
// @Success 200 {object} QRStartResponse
// @Failure 500 {object} map[string]string
// @Router /bots/{id}/channel/weixin/qr/start [post].
func (h *QRHandler) Start(c echo.Context) error {
	ctx := c.Request().Context()
	qr, err := h.client.FetchQRCode(ctx, defaultBaseURL, h.localTokens(ctx, strings.TrimSpace(c.Param("id"))))
	if err != nil {
		h.logger.ErrorContext(ctx, "weixin qr start failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to fetch QR code: "+err.Error())
	}

	return c.JSON(http.StatusOK, QRStartResponse{
		QRCodeURL: strings.TrimSpace(qr.QRCodeImgContent),
		QRCode:    strings.TrimSpace(qr.QRCode),
		Message:   "Scan the QR code with WeChat",
	})
}

// localTokens returns this bot's current WeChat token, if any, for
// get_bot_qrcode's local_token_list: iLink then answers a re-scan of the same
// WeChat bot with binded_redirect instead of issuing new credentials. Only the
// bot being connected is included — upstream sends every locally stored
// account, but here other bots may belong to other tenants.
func (h *QRHandler) localTokens(ctx context.Context, botID string) []string {
	if h.lifecycle == nil || botID == "" {
		return nil
	}
	cfg, found, err := h.lifecycle.ResolveBotChannelConfig(ctx, botID, Type)
	if err != nil {
		h.logger.WarnContext(ctx, "weixin qr: read existing config failed", slog.String("bot_id", botID), slog.Any("error", err))
		return nil
	}
	if !found {
		return nil
	}
	if token := strings.TrimSpace(channel.ReadString(cfg.Credentials, "token")); token != "" {
		return []string{token}
	}
	return nil
}

// QRPollRequest is the request body for polling QR status. The endpoint is
// stateless (replicas don't share login sessions), so the client carries the
// state iLink hands out between polls.
type QRPollRequest struct {
	QRCode string `json:"qr_code"`
	// PollHost is the poll_host from a previous response; empty means the
	// default iLink host.
	PollHost string `json:"poll_host,omitempty"`
	// VerifyCode is the number shown on the phone after need_verify_code; the
	// client keeps sending it until the status moves past need_verify_code.
	VerifyCode string `json:"verify_code,omitempty"`
}

// QRPollResponse returns the poll result.
type QRPollResponse struct {
	// Status is one of wait, scanned, need_verify_code, verify_code_blocked,
	// already_bound, confirmed, expired.
	Status  string `json:"status"`
	Message string `json:"message"`
	// PollHost, when set, is where iLink moved this login; send it back as
	// poll_host on every later poll.
	PollHost string `json:"poll_host,omitempty"`
}

// Poll godoc
// @Summary Poll WeChat QR login status
// @Description Long-poll the QR code scan status. On confirmed, auto-saves credentials.
// @Tags bots
// @Param id path string true "Bot ID"
// @Param payload body QRPollRequest true "QR code to poll"
// @Success 200 {object} QRPollResponse
// @Failure 400 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /bots/{id}/channel/weixin/qr/poll [post].
func (h *QRHandler) Poll(c echo.Context) error {
	ctx := c.Request().Context()
	botID := strings.TrimSpace(c.Param("id"))
	if botID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "bot id is required")
	}

	var req QRPollRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	qrCode := strings.TrimSpace(req.QRCode)
	if qrCode == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "qr_code is required")
	}
	pollHost := strings.TrimSpace(req.PollHost)
	apiBaseURL := defaultBaseURL
	if pollHost != "" {
		if !isILinkHost(pollHost) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid poll_host")
		}
		apiBaseURL = "https://" + pollHost
	}

	status, err := h.client.PollQRStatus(ctx, apiBaseURL, qrCode, strings.TrimSpace(req.VerifyCode))
	if err != nil {
		h.logger.ErrorContext(ctx, "weixin qr poll failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "Poll failed: "+err.Error())
	}

	resp := QRPollResponse{Status: status.Status, PollHost: pollHost}
	switch status.Status {
	case "scaned_but_redirect":
		// The phone scanned on another iLink IDC; keep polling there. A host we
		// can't trust is dropped and polling stays put, as upstream does when
		// redirect_host is missing.
		resp.Status = "scanned"
		if host := strings.TrimSpace(status.RedirectHost); isILinkHost(host) {
			resp.PollHost = host
		} else {
			h.logger.WarnContext(ctx, "weixin qr: ignoring redirect host", slog.String("redirect_host", host))
		}
	case "need_verifycode":
		resp.Status = "need_verify_code"
	case "binded_redirect":
		resp.Status = "already_bound"
		if err := h.ensureEnabled(ctx, botID); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "WeChat is already connected but enabling the channel failed: "+err.Error())
		}
	case "confirmed":
		if err := h.saveCredentials(ctx, botID, status); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Login succeeded but failed to save credentials: "+err.Error())
		}
	}
	resp.Message = statusMessage(resp.Status)
	return c.JSON(http.StatusOK, resp)
}

func (h *QRHandler) saveCredentials(ctx context.Context, botID string, status *QRStatusResponse) error {
	if h.lifecycle == nil || strings.TrimSpace(status.BotToken) == "" {
		return nil
	}
	resolvedBaseURL := defaultBaseURL
	if strings.TrimSpace(status.BaseURL) != "" {
		resolvedBaseURL = strings.TrimSpace(status.BaseURL)
	}
	credentials := map[string]any{
		"token":   status.BotToken,
		"baseUrl": resolvedBaseURL,
	}
	_, err := h.lifecycle.UpsertBotChannelConfig(ctx, botID, Type, channel.UpsertConfigRequest{
		Credentials: credentials,
		Disabled:    boolPtr(false),
	})
	if err != nil {
		h.logger.ErrorContext(ctx, "weixin qr save credentials failed",
			slog.String("bot_id", botID),
			slog.Any("error", err),
		)
		return err
	}
	h.logger.InfoContext(ctx, "weixin qr login saved",
		slog.String("bot_id", botID),
		slog.String("account_id", status.ILinkBotID),
	)
	return nil
}

// ensureEnabled handles binded_redirect: iLink recognised the token we sent in
// local_token_list, so the stored credentials stay valid and none are issued.
// Scanning is still the user asking to connect, so a disabled channel is
// switched back on, matching what a confirmed login does.
func (h *QRHandler) ensureEnabled(ctx context.Context, botID string) error {
	if h.lifecycle == nil {
		return nil
	}
	cfg, found, err := h.lifecycle.ResolveBotChannelConfig(ctx, botID, Type)
	if err != nil {
		return err
	}
	if !found {
		// We only send this bot's own token, so iLink should not report a
		// binding the bot has no config for. Leave a trace if it does.
		h.logger.WarnContext(ctx, "weixin qr: binded_redirect for a bot without stored config", slog.String("bot_id", botID))
		return nil
	}
	if !cfg.Disabled {
		return nil
	}
	if _, err := h.lifecycle.SetBotChannelStatus(ctx, botID, Type, false); err != nil {
		h.logger.ErrorContext(ctx, "weixin qr enable bound channel failed", slog.String("bot_id", botID), slog.Any("error", err))
		return err
	}
	return nil
}

// isILinkHost accepts a bare hostname under qq.com. iLink redirect hosts come
// from the upstream response and poll_host from the browser; either way the
// server dials it, so anything else is refused rather than fetched.
func isILinkHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.ContainsAny(host, "/:@?#[] ") {
		return false
	}
	return strings.HasSuffix(host, ".qq.com")
}

func statusMessage(s string) string {
	switch s {
	case "wait":
		return "Waiting for scan..."
	case "scanned":
		return "Scanned — confirm on your phone"
	case "need_verify_code":
		return "Enter the number shown in WeChat on your phone"
	case "verify_code_blocked":
		return "Too many wrong numbers — refresh the QR code and try again later"
	case "already_bound":
		return "This WeChat is already connected"
	case "confirmed":
		return "Login successful"
	case "expired":
		return "QR code expired"
	default:
		return s
	}
}

func boolPtr(b bool) *bool { return &b }
