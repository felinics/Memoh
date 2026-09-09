package weixin

import (
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
	qr, err := h.client.FetchQRCode(c.Request().Context(), defaultBaseURL)
	if err != nil {
		h.logger.Error("weixin qr start failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to fetch QR code: "+err.Error())
	}

	return c.JSON(http.StatusOK, QRStartResponse{
		QRCodeURL: strings.TrimSpace(qr.QRCodeImgContent),
		QRCode:    strings.TrimSpace(qr.QRCode),
		Message:   "Scan the QR code with WeChat",
	})
}

// QRPollRequest is the request body for polling QR status.
type QRPollRequest struct {
	QRCode string `json:"qr_code"`
}

// QRPollResponse returns the poll result.
type QRPollResponse struct {
	Status  string `json:"status"` // wait, scanned, confirmed, expired
	Message string `json:"message"`
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

	status, err := h.client.PollQRStatus(c.Request().Context(), defaultBaseURL, qrCode)
	if err != nil {
		h.logger.Error("weixin qr poll failed", slog.Any("error", err))
		return echo.NewHTTPError(http.StatusInternalServerError, "Poll failed: "+err.Error())
	}

	resp := QRPollResponse{
		Status:  status.Status,
		Message: statusMessage(status.Status),
	}

	if status.Status == "confirmed" && strings.TrimSpace(status.BotToken) != "" {
		resolvedBaseURL := defaultBaseURL
		if strings.TrimSpace(status.BaseURL) != "" {
			resolvedBaseURL = strings.TrimSpace(status.BaseURL)
		}

		if h.lifecycle != nil {
			credentials := map[string]any{
				"token":   status.BotToken,
				"baseUrl": resolvedBaseURL,
			}

			_, saveErr := h.lifecycle.UpsertBotChannelConfig(
				c.Request().Context(),
				botID,
				Type,
				channel.UpsertConfigRequest{
					Credentials: credentials,
					Disabled:    boolPtr(false),
				},
			)
			if saveErr != nil {
				h.logger.Error("weixin qr save credentials failed",
					slog.String("bot_id", botID),
					slog.Any("error", saveErr),
				)
				return echo.NewHTTPError(http.StatusInternalServerError, "Login succeeded but failed to save credentials: "+saveErr.Error())
			}
			h.logger.Info("weixin qr login saved",
				slog.String("bot_id", botID),
				slog.String("account_id", status.ILinkBotID),
			)
		}
	}

	return c.JSON(http.StatusOK, resp)
}

func statusMessage(s string) string {
	switch s {
	case "wait":
		return "Waiting for scan..."
	case "scanned":
		return "Scanned — confirm on your phone"
	case "confirmed":
		return "Login successful"
	case "expired":
		return "QR code expired"
	default:
		return s
	}
}

func boolPtr(b bool) *bool { return &b }
