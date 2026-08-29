package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	displaypkg "github.com/felinics/memoh/internal/display"
	"github.com/felinics/memoh/internal/httpx"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type displayInfoResponse struct {
	Enabled           bool   `json:"enabled"`
	Available         bool   `json:"available"`
	Running           bool   `json:"running"`
	Transport         string `json:"transport"`
	Encoder           string `json:"encoder"`
	EncoderAvailable  bool   `json:"encoder_available"`
	DesktopAvailable  bool   `json:"desktop_available"`
	BrowserAvailable  bool   `json:"browser_available"`
	ToolkitAvailable  bool   `json:"toolkit_available"`
	A11yAvailable     bool   `json:"a11y_available"`
	PrepareSupported  bool   `json:"prepare_supported"`
	PrepareSystem     string `json:"prepare_system,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type displayWebRTCOfferRequest struct {
	Type          string `json:"type"`
	SDP           string `json:"sdp"`
	SessionID     string `json:"session_id,omitempty"`
	CandidateHost string `json:"candidate_host,omitempty"`
}

type displayWebRTCOfferResponse struct {
	Type      string `json:"type"`
	SDP       string `json:"sdp"`
	SessionID string `json:"session_id"`
}

type displaySessionListResponse struct {
	Items []displaypkg.SessionInfo `json:"items"`
}

type displayRuntimeProbe struct {
	ToolkitAvailable bool   `json:"toolkit_available"`
	PrepareSupported bool   `json:"prepare_supported"`
	PrepareSystem    string `json:"prepare_system"`
	DesktopAvailable bool   `json:"desktop_available"`
	BrowserAvailable bool   `json:"browser_available"`
	VNCAvailable     bool   `json:"vnc_available"`
	A11yAvailable    bool   `json:"a11y_available"`
}

// GetDisplayInfo godoc
// @Summary Check workspace display availability for bot
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} displayInfoResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/container/display [get].
func (h *ContainerdHandler) GetDisplayInfo(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	resp := displayInfoResponse{
		Transport: displaypkg.TransportWebRTC,
		Encoder:   displaypkg.EncoderGStreamer,
	}
	if h.manager == nil || h.displayService == nil {
		resp.UnavailableReason = "manager not configured"
		return c.JSON(http.StatusOK, resp)
	}

	resp.Enabled = h.manager.BotDisplayEnabled(ctx, botID)
	if _, err := h.manager.NativeMCPClient(ctx, botID); err != nil {
		// unavailable_reason is a wire-level enum consumed by older Desktop clients.
		resp.UnavailableReason = "container not reachable"
		return c.JSON(http.StatusOK, resp)
	}

	status := h.displayService.Status(ctx, botID)
	resp.Available = status.Available
	resp.Running = status.Running
	resp.Transport = status.Transport
	resp.Encoder = status.Encoder
	resp.EncoderAvailable = status.EncoderAvailable
	resp.UnavailableReason = status.UnavailableReason

	if resp.Enabled {
		client, err := h.manager.NativeMCPClient(ctx, botID)
		if err == nil && client != nil {
			if probe, ok := probeDisplayRuntime(ctx, client); ok {
				resp.ToolkitAvailable = probe.ToolkitAvailable
				resp.PrepareSupported = probe.PrepareSupported
				resp.PrepareSystem = probe.PrepareSystem
				resp.DesktopAvailable = probe.DesktopAvailable
				resp.BrowserAvailable = probe.BrowserAvailable
				resp.A11yAvailable = probe.A11yAvailable
				if !resp.Running && !probe.VNCAvailable {
					resp.UnavailableReason = "display bundle unavailable"
				}
			} else if resp.Available && resp.Running {
				resp.DesktopAvailable = true
				resp.BrowserAvailable = true
			}
		}
	}

	return c.JSON(http.StatusOK, resp)
}

// HandleDisplayWebRTCOffer godoc
// @Summary Create a WebRTC answer for bot workspace display
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param payload body displayWebRTCOfferRequest true "WebRTC offer payload"
// @Success 200 {object} displayWebRTCOfferResponse
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /bots/{bot_id}/container/display/webrtc/offer [post].
func (h *ContainerdHandler) HandleDisplayWebRTCOffer(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.displayService == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "display service not configured")
	}

	var req displayWebRTCOfferRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid display offer payload")
	}

	answer, err := h.displayService.Answer(c.Request().Context(), botID, displaypkg.OfferRequest{
		Type:      req.Type,
		SDP:       req.SDP,
		SessionID: req.SessionID,
		NATIPs:    h.displayNATIPs(c, req.CandidateHost),
	})
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, displaypkg.ErrDisplayDisabled) {
			status = http.StatusBadRequest
		}
		if !errors.Is(err, displaypkg.ErrEncoderUnavailable) &&
			!errors.Is(err, displaypkg.ErrDisplayUnavailable) &&
			!errors.Is(err, displaypkg.ErrDisplayDisabled) {
			status = http.StatusBadRequest
		}
		return echo.NewHTTPError(status, err.Error())
	}

	h.applyDisplayStyleAsync(c.Request().Context(), botID)

	return c.JSON(http.StatusOK, displayWebRTCOfferResponse{
		Type:      answer.Type,
		SDP:       answer.SDP,
		SessionID: answer.SessionID,
	})
}

// ListDisplaySessions godoc
// @Summary List active workspace display WebRTC sessions
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Success 200 {object} displaySessionListResponse
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/container/display/sessions [get].
func (h *ContainerdHandler) ListDisplaySessions(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	if h.displayService == nil {
		return c.JSON(http.StatusOK, displaySessionListResponse{Items: nil})
	}
	return c.JSON(http.StatusOK, displaySessionListResponse{
		Items: h.displayService.ListSessions(botID),
	})
}

// CloseDisplaySession godoc
// @Summary Close a workspace display WebRTC session
// @Tags containerd
// @Param bot_id path string true "Bot ID"
// @Param session_id path string true "Display session ID"
// @Success 204
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/container/display/sessions/{session_id} [delete].
func (h *ContainerdHandler) CloseDisplaySession(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "display session id is required")
	}
	if h.displayService == nil || !h.displayService.CloseSession(botID, sessionID) {
		return echo.NewHTTPError(http.StatusNotFound, "display session not found")
	}
	return c.NoContent(http.StatusNoContent)
}

const (
	displayPrepareProgressPrefix = "__MEMOH_DISPLAY_PROGRESS__"
	displayPrepareScriptPath     = "/opt/memoh/scripts/display-prepare.sh"
	displayApplyStyleScriptPath  = "/opt/memoh/scripts/display-apply-style.sh"
)

type displayPrepareStreamEvent struct {
	Type      string            `json:"type"`
	Step      string            `json:"step,omitempty"`
	Code      string            `json:"code,omitempty"`
	I18nKey   string            `json:"i18n_key,omitempty"`
	Args      map[string]string `json:"args,omitempty"`
	Detail    string            `json:"detail,omitempty"`
	Message   string            `json:"message,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	Percent   int               `json:"percent,omitempty"`
}

func newDisplayPrepareAppError(step string, err error, requestID string) displayPrepareStreamEvent {
	public, ok := apperror.PublicFrom(err, requestID)
	if !ok {
		return displayPrepareStreamEvent{
			Type: "error", Step: step, Message: "Display preparation failed.",
		}
	}
	return displayPrepareStreamEvent{
		Type:      "error",
		Step:      step,
		Code:      string(public.Code),
		Args:      public.Args,
		Detail:    public.Detail,
		Message:   public.Detail,
		RequestID: public.RequestID,
	}
}

// PrepareDisplay godoc
// @Summary Prepare workspace display dependencies
// @Description Validates the image-provided desktop/VNC/browser runtime, starts the display server, and launches the browser.
// @Tags containerd
// @Produce text/event-stream
// @Param bot_id path string true "Bot ID"
// @Success 200 {string} string "SSE stream of display preparation events"
// @Failure 404 {object} ErrorResponse
// @Router /bots/{bot_id}/container/display/prepare [post].
func (h *ContainerdHandler) PrepareDisplay(c echo.Context) error {
	botID, err := h.requireBotAccess(c)
	if err != nil {
		return err
	}

	flusher, ok := c.Response().Writer.(http.Flusher)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "streaming not supported")
	}

	setSSEHeaders(c)
	c.Response().WriteHeader(http.StatusOK)
	writer := c.Response().Writer
	send := func(payload displayPrepareStreamEvent) {
		_ = writeSSEJSON(writer, flusher, payload)
	}
	sendError := func(step, code, i18nKey, message string) {
		send(displayPrepareStreamEvent{
			Type:      "error",
			Step:      step,
			Code:      code,
			I18nKey:   i18nKey,
			Args:      map[string]string{},
			Message:   message,
			RequestID: httpx.RequestID(c),
		})
	}
	streamRequestID := httpx.RequestID(c)
	sendAppError := func(step string, code apperror.Code, cause error) {
		if cause != nil {
			h.logger.Error("display preparation failed",
				slog.String("code", string(code)),
				slog.String("request_id", streamRequestID),
				slog.Any("error", cause),
			)
		}
		send(newDisplayPrepareAppError(step, apperror.Wrap(code, cause, nil), streamRequestID))
	}

	ctx := c.Request().Context()
	if h.manager == nil {
		sendError("checking", "workspace_manager_unavailable", "chat.display.unavailable.manager", "manager not configured")
		return nil
	}
	if !h.manager.BotDisplayEnabled(ctx, botID) {
		sendError("checking", "workspace_display_disabled", "chat.display.unavailable.disabled", "workspace display is not enabled")
		return nil
	}

	client, err := h.manager.NativeMCPClient(ctx, botID)
	if err != nil || client == nil {
		if err == nil {
			err = errors.New("workspace bridge client is unavailable")
		}
		sendAppError("checking", apperror.CodeWorkspaceUnreachable, err)
		return nil
	}

	send(displayPrepareStreamEvent{
		Type:    "progress",
		Step:    "checking",
		Message: "Checking display runtime",
		Percent: 5,
	})

	stream, err := client.ExecStream(ctx, displayPrepareCommand(), "/", 1200)
	if err != nil {
		sendAppError("checking", apperror.CodeWorkspaceUnreachable, err)
		return nil
	}
	defer func() { _ = stream.Close() }()

	var stdout, stderr lineAccumulator
	var stderrText strings.Builder
	completed := false
	exitCode := int32(0)
	lastStep := "checking"
	for {
		msg, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			// The bridge was reachable and preparation already started, so a
			// broken stream is a prepare failure, not "workspace unreachable".
			sendAppError(lastStep, apperror.CodeWorkspaceDisplayPrepareFailed, recvErr)
			return nil
		}
		switch msg.GetStream() {
		case pb.ExecOutput_STDOUT:
			for _, line := range stdout.Append(msg.GetData()) {
				if event, ok := parseDisplayPrepareEvent(line); ok {
					if event.Step != "" {
						lastStep = event.Step
					}
					send(event)
					if event.Type == "complete" {
						completed = true
					}
				}
			}
		case pb.ExecOutput_STDERR:
			for _, line := range stderr.Append(msg.GetData()) {
				appendLimitedLine(&stderrText, line)
			}
		case pb.ExecOutput_EXIT:
			exitCode = msg.GetExitCode()
		}
	}
	for _, line := range stdout.Flush() {
		if event, ok := parseDisplayPrepareEvent(line); ok {
			if event.Step != "" {
				lastStep = event.Step
			}
			send(event)
			if event.Type == "complete" {
				completed = true
			}
		}
	}
	for _, line := range stderr.Flush() {
		appendLimitedLine(&stderrText, line)
	}
	if exitCode != 0 && !completed {
		diagnostic := strings.TrimSpace(stderrText.String())
		if diagnostic == "" {
			diagnostic = "no stderr output"
		}
		// A non-zero exit without a `complete` marker means the prepare script
		// itself failed (unsupported base image, package install, browser
		// launch, network). Route it through the shared app-error envelope so
		// the SSE `error` event carries the stable
		// `workspace.display_prepare_failed` code — identical to the mid-stream
		// recvErr path above — instead of the legacy underscore string that the
		// frontend's parseMemohError/isApiErrorCode branches can't match.
		// sendAppError logs the private cause (exit code + stderr) with the
		// request id, and apperror.PublicFrom renders only the catalog Detail,
		// so the diagnostic never leaks into the user-facing response.
		cause := fmt.Errorf("display preparation exited with status %d: %s", exitCode, diagnostic)
		sendAppError(lastStep, apperror.CodeWorkspaceDisplayPrepareFailed, cause)
		return nil
	}
	if !completed {
		send(displayPrepareStreamEvent{
			Type:    "complete",
			Step:    "complete",
			Message: "Display is ready",
			Percent: 100,
		})
	}
	return nil
}

type lineAccumulator struct {
	partial string
}

func (b *lineAccumulator) Append(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	text := b.partial + string(data)
	parts := strings.Split(text, "\n")
	b.partial = parts[len(parts)-1]
	lines := parts[:len(parts)-1]
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	return lines
}

func (b *lineAccumulator) Flush() []string {
	if b.partial == "" {
		return nil
	}
	line := strings.TrimRight(b.partial, "\r")
	b.partial = ""
	return []string{line}
}

func parseDisplayPrepareEvent(line string) (displayPrepareStreamEvent, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, displayPrepareProgressPrefix) {
		return displayPrepareStreamEvent{}, false
	}
	var event displayPrepareStreamEvent
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, displayPrepareProgressPrefix)), &event); err != nil {
		return displayPrepareStreamEvent{}, false
	}
	return event, true
}

func appendLimitedLine(builder *strings.Builder, line string) {
	line = strings.TrimSpace(line)
	if line == "" || builder.Len() > 6000 {
		return
	}
	if builder.Len() > 0 {
		builder.WriteByte('\n')
	}
	builder.WriteString(line)
}

func displayPrepareCommand() string {
	return "/bin/sh " + displayPrepareScriptPath
}

func displayApplyStyleCommand() string {
	return "/bin/sh " + displayApplyStyleScriptPath + " --if-needed"
}

func displayStyleStatusCommand() string {
	return "/bin/sh " + displayApplyStyleScriptPath + " --check"
}

func displayStyleLogTailCommand() string {
	return "tail -n 80 /tmp/memoh-desktop-style.log 2>/dev/null || true"
}

func trimDisplayLog(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 6000 {
		return text
	}
	return text[len(text)-6000:]
}

func displayStyleApplyNeedsRun(ctx context.Context, client *bridge.Client) bool {
	if client == nil {
		return false
	}
	result, err := client.Exec(ctx, displayStyleStatusCommand(), "/", 15)
	return err != nil || result == nil || result.ExitCode != 0
}

func displayStyleLogTail(ctx context.Context, client *bridge.Client) string {
	if client == nil {
		return ""
	}
	result, err := client.Exec(ctx, displayStyleLogTailCommand(), "/", 15)
	if err != nil || result == nil {
		return ""
	}
	return result.Stdout + result.Stderr
}

func displayStyleLogArgs(ctx context.Context, client *bridge.Client, botID string, result *bridge.ExecResult) []any {
	exitCode := -1
	stdout := ""
	stderr := ""
	if result != nil {
		exitCode = int(result.ExitCode)
		stdout = trimDisplayLog(result.Stdout)
		stderr = trimDisplayLog(result.Stderr)
	}
	args := []any{
		slog.String("bot_id", botID),
		slog.Int("exit_code", exitCode),
	}
	if stderr != "" {
		args = append(args, slog.String("stderr", stderr))
	}
	if stdout != "" {
		args = append(args, slog.String("stdout", stdout))
	}
	if stdout == "" && stderr == "" {
		if tail := trimDisplayLog(displayStyleLogTail(ctx, client)); tail != "" {
			args = append(args, slog.String("style_log_tail", tail))
		}
	}
	return args
}

func (h *ContainerdHandler) applyDisplayStyleAsync(ctx context.Context, botID string) {
	if h == nil || h.manager == nil {
		return
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		client, err := h.manager.NativeMCPClient(runCtx, botID)
		if err != nil || client == nil {
			if err != nil && h.logger != nil {
				h.logger.Warn("display desktop style skipped", slog.String("bot_id", botID), slog.Any("error", err))
			}
			return
		}
		if !displayStyleApplyNeedsRun(runCtx, client) {
			return
		}
		result, err := client.Exec(runCtx, displayApplyStyleCommand(), "/", 540)
		if err != nil {
			if h.logger != nil {
				h.logger.Warn("display desktop style failed", slog.String("bot_id", botID), slog.Any("error", err))
			}
			return
		}
		if (result == nil || result.ExitCode != 0) && h.logger != nil {
			h.logger.Warn("display desktop style exited non-zero", displayStyleLogArgs(runCtx, client, botID, result)...)
		}
	}()
}

const displayRuntimeProbeCommand = `has_cmd() { command -v "$1" >/dev/null 2>&1; }
has_exec() { [ -x "$1" ]; }
has_process() { ps -ef 2>/dev/null | grep -E "$1" | grep -v grep >/dev/null 2>&1; }
json_bool() { if "$@"; then printf true; else printf false; fi; }
os_id=unknown
os_like=
if [ -r /etc/os-release ]; then
  . /etc/os-release
  os_id="${ID:-unknown}"
  os_like="${ID:-} ${ID_LIKE:-}"
fi
has_toolkit() {
  has_exec /opt/memoh/toolkit/display/bin/Xvnc ||
    has_exec /opt/memoh/toolkit/display/bin/twm ||
    has_exec /opt/memoh/toolkit/display/root/usr/bin/Xvnc ||
    has_exec /opt/memoh/toolkit/display/root/usr/bin/twm
}
has_prepare() {
  case " $os_like " in
    *" debian "*|*" ubuntu "*|*" alpine "*) return 0 ;;
    *) return 1 ;;
  esac
}
has_vnc() {
  has_cmd Xvnc ||
    has_exec /opt/memoh/toolkit/display/bin/Xvnc ||
    has_exec /opt/memoh/toolkit/display/root/usr/bin/Xvnc ||
    has_exec /usr/bin/Xvnc ||
    has_exec /usr/local/bin/Xvnc
}
has_desktop() {
  has_cmd startxfce4 ||
    has_cmd xfce4-session ||
    has_cmd xfwm4 ||
    has_exec /opt/memoh/toolkit/display/bin/twm ||
    has_exec /opt/memoh/toolkit/display/root/usr/bin/twm ||
    has_process 'xfce4-session|xfwm4|twm'
}
has_browser() {
  has_cmd google-chrome-stable ||
    has_cmd google-chrome ||
    has_cmd chromium ||
    has_cmd chromium-browser ||
    has_process 'google-chrome|chromium'
}
has_a11y() {
  a11y=/opt/memoh/toolkit/display/bin/a11y-cli
  [ -x "$a11y" ] || return 1
  DISPLAY=:99 "$a11y" probe 2>/dev/null | grep -q '"ok":true'
}
printf '{"toolkit_available":%s,"prepare_supported":%s,"prepare_system":"%s","desktop_available":%s,"browser_available":%s,"vnc_available":%s,"a11y_available":%s}\n' \
  "$(json_bool has_toolkit)" \
  "$(json_bool has_prepare)" \
  "$os_id" \
  "$(json_bool has_desktop)" \
  "$(json_bool has_browser)" \
  "$(json_bool has_vnc)" \
  "$(json_bool has_a11y)"`

func probeDisplayRuntime(ctx context.Context, client *bridge.Client) (displayRuntimeProbe, bool) {
	var probe displayRuntimeProbe
	if client == nil {
		return probe, false
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := client.Exec(ctx, displayRuntimeProbeCommand, "/", 10)
		if err == nil && result != nil && result.ExitCode == 0 {
			if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &probe); err == nil {
				return probe, true
			}
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return probe, false
		case <-timer.C:
		}
	}
	return probe, false
}

func (*ContainerdHandler) displayNATIPs(c echo.Context, candidateHost string) []string {
	ctx := c.Request().Context()
	hosts := []string{
		candidateHost,
		firstHeaderValue(c.Request().Header.Get("X-Forwarded-Host")),
		c.Request().Host,
	}
	seen := make(map[string]struct{})
	var ips []string
	for _, host := range hosts {
		for _, ip := range resolveDisplayHostIPs(ctx, host) {
			if _, ok := seen[ip]; ok {
				continue
			}
			seen[ip] = struct{}{}
			ips = append(ips, ip)
		}
	}
	return ips
}

func resolveDisplayHostIPs(ctx context.Context, value string) []string {
	host := strings.TrimSpace(value)
	if host == "" {
		return nil
	}
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end >= 0 {
			host = host[1:end]
		}
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if strings.Count(host, ":") == 0 {
		if idx := strings.LastIndexByte(host, ':'); idx >= 0 {
			host = host[:idx]
		}
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}
	}
	resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(resolved))
	for _, ip := range resolved {
		if ip.IP == nil {
			continue
		}
		out = append(out, ip.IP.String())
	}
	return out
}
