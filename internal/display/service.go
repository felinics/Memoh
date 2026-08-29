package display

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	sdpv3 "github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

const (
	TransportWebRTC      = "webrtc"
	EncoderGStreamer     = "gstreamer"
	CodecH264            = webrtc.MimeTypeH264
	CodecVP8             = webrtc.MimeTypeVP8
	gstLaunchEnv         = "MEMOH_GSTREAMER_LAUNCH"
	rtcUDPPortEnv        = "MEMOH_DISPLAY_WEBRTC_UDP_PORT"
	rtcUDPPortMinEnv     = "MEMOH_DISPLAY_WEBRTC_UDP_PORT_MIN"
	rtcUDPPortMaxEnv     = "MEMOH_DISPLAY_WEBRTC_UDP_PORT_MAX"
	rtcNATIPsEnv         = "MEMOH_DISPLAY_WEBRTC_NAT_IPS"
	forceVP8Env          = "MEMOH_DISPLAY_FORCE_VP8"
	videoPayloadTypeH264 = 102
	videoPayloadTypeVP8  = 96
	videoClockRate       = 90000
	videoFrameRate       = 15
	h264FmtpLine         = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"
	displayProbePeriod   = 5 * time.Second
	// socketProbeTimeout gates the RFB reachability probe used by Status /
	// Answer / session.start. 300ms was too tight for a busy container: a
	// momentary stall flipped running=false and forced a full prepare
	// (re-installing/starting Xvnc + desktop + Chrome) on every reconnect.
	// 1.5s still fails fast when the port is genuinely closed, but tolerates
	// the brief scheduling jitter that was causing spurious prepare runs.
	socketProbeTimeout = 1500 * time.Millisecond
	stalePeerTTL       = 2 * time.Minute
	encoderIdleHold    = 90 * time.Second
	// firstPacketTimeout gates how long session.start waits for GStreamer to
	// emit its first RTP packet before declaring the encoder dead. 10s was too
	// tight for cold starts (Xvnc + GStreamer + desktop spin-up on a freshly
	// prepared container), surfacing as "produced no frames within 10s" right
	// when the pipeline was about to produce them — under Orbstack the cold
	// start regularly exceeded 10s, so the encoder was killed and the WebRTC
	// offer got 503, looping the frontend in "connecting to desktop" for
	// minutes. 30s gives cold starts real room while still failing fast when
	// the pipeline is genuinely broken.
	firstPacketTimeout = 30 * time.Second
	screenshotTimeout  = 15 * time.Second
	screenshotWidth    = 1280
	screenshotQuality  = 82
	screenshotMaxBytes = 512 * 1024
	screenshotMIME     = "image/jpeg"
	rfbTCPAddress      = "127.0.0.1:5999"
)

type screenshotJPEGCandidate struct {
	width   int
	quality int
}

var (
	ErrManagerUnavailable = errors.New("manager not configured")
	ErrDisplayDisabled    = errors.New("display disabled")
	ErrDisplayUnavailable = errors.New("display server not reachable")
	ErrEncoderUnavailable = errors.New("gstreamer unavailable")
	ErrCodecUnsupported   = errors.New("no compatible video codec offered")
)

var screenshotJPEGCandidates = []screenshotJPEGCandidate{
	{quality: screenshotQuality},
	{quality: 72},
	{width: 1024, quality: 68},
	{width: 800, quality: 60},
	{width: 640, quality: 52},
	{width: 480, quality: 42},
	{width: 320, quality: 30},
}

type Workspace interface {
	BotDisplayEnabled(ctx context.Context, botID string) bool
	DisplaySocketPath(botID string) string
}

type workspaceDisplayDialer interface {
	DisplayDialContext(ctx context.Context, botID, network, address string) (net.Conn, error)
}

type Status struct {
	Enabled           bool
	Available         bool
	Running           bool
	Transport         string
	Encoder           string
	EncoderAvailable  bool
	UnavailableReason string
}

type OfferRequest struct {
	Type      string   `json:"type"`
	SDP       string   `json:"sdp"`
	SessionID string   `json:"session_id,omitempty"`
	NATIPs    []string `json:"-"`
}

type OfferResponse struct {
	Type      string `json:"type"`
	SDP       string `json:"sdp"`
	SessionID string `json:"session_id"`
}

type SessionInfo struct {
	ID        string    `json:"id"`
	Codec     string    `json:"codec"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

type ControlInput struct {
	Type       string
	X          int
	Y          int
	ButtonMask uint8
	Keysym     uint32
	Down       bool
}

type rtcSettings struct {
	UDPPort    uint16
	UDPPortMin uint16
	UDPPortMax uint16
	NATIPs     []string
}

type Service struct {
	logger    *slog.Logger
	workspace Workspace

	mu       sync.Mutex
	sessions map[string]*session
	starting map[string]*sessionStart

	rtcMu      sync.Mutex
	rtcMux     ice.UDPMux
	rtcMuxPort uint16
	rtcClosed  bool
}

type sessionStart struct {
	done chan struct{}
	sess *session
	err  error
}

func NewService(logger *slog.Logger, workspace Workspace) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		logger:    logger.With(slog.String("component", "display")),
		workspace: workspace,
		sessions:  make(map[string]*session),
		starting:  make(map[string]*sessionStart),
	}
}

// Start reserves the configured shared ICE UDP port before the server begins
// accepting display requests, so an invalid or unavailable port fails startup.
func (s *Service) Start() error {
	cfg, err := readRTCSettings(nil)
	if err != nil {
		return err
	}
	if cfg.UDPPort == 0 {
		return nil
	}
	_, err = s.sharedICEUDPMux(cfg.UDPPort)
	return err
}

// Close releases the shared ICE UDP listener owned by this display service.
// It is called after the HTTP server stops accepting display requests.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.rtcMu.Lock()
	s.rtcClosed = true
	mux := s.rtcMux
	s.rtcMux = nil
	s.rtcMu.Unlock()
	if mux == nil {
		return nil
	}
	return mux.Close()
}

func (s *Service) displayTarget(botID string) string {
	if _, ok := s.workspace.(workspaceDisplayDialer); ok {
		return "bridge-tcp://" + rfbTCPAddress
	}
	return s.workspace.DisplaySocketPath(botID)
}

func (s *Service) displayReachable(ctx context.Context, botID string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, socketProbeTimeout)
	defer cancel()
	conn, err := s.dialRFB(probeCtx, botID)
	if err != nil {
		return false
	}
	return probeRFBNoneSecurity(conn) == nil
}

func (s *Service) dialRFB(ctx context.Context, botID string) (net.Conn, error) {
	if dialer, ok := s.workspace.(workspaceDisplayDialer); ok {
		conn, err := dialer.DisplayDialContext(ctx, botID, "tcp", rfbTCPAddress)
		if err == nil {
			return conn, nil
		}
		if socketPath := strings.TrimSpace(s.workspace.DisplaySocketPath(botID)); socketPath != "" {
			if fallback, fallbackErr := dialRFBSocket(ctx, socketPath); fallbackErr == nil {
				return fallback, nil
			}
		}
		return nil, fmt.Errorf("dial workspace display %s: %w", rfbTCPAddress, err)
	}
	return dialRFBSocket(ctx, s.workspace.DisplaySocketPath(botID))
}

func dialRFBSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return nil, ErrDisplayUnavailable
	}
	dialer := net.Dialer{Timeout: displayProbePeriod}
	return dialer.DialContext(ctx, "unix", filepath.Clean(socketPath))
}

func (s *Service) Status(ctx context.Context, botID string) Status {
	status := Status{
		Transport: TransportWebRTC,
		Encoder:   EncoderGStreamer,
	}
	if s == nil || s.workspace == nil {
		status.UnavailableReason = "manager not configured"
		return status
	}

	status.Enabled = s.workspace.BotDisplayEnabled(ctx, botID)
	gstLaunch, gstErr := resolveGSTLaunch()
	status.EncoderAvailable = gstErr == nil && strings.TrimSpace(gstLaunch) != ""

	if status.Enabled {
		status.Running = s.displayReachable(ctx, botID)
	}
	status.Available = status.Enabled && status.Running && status.EncoderAvailable
	switch {
	case !status.Enabled:
	case !status.Running:
		status.UnavailableReason = "display server not reachable"
	case !status.EncoderAvailable:
		status.UnavailableReason = "gstreamer unavailable"
	}
	return status
}

func (s *Service) Answer(ctx context.Context, botID string, req OfferRequest) (OfferResponse, error) {
	if s == nil || s.workspace == nil {
		return OfferResponse{}, ErrManagerUnavailable
	}
	if !s.workspace.BotDisplayEnabled(ctx, botID) {
		return OfferResponse{}, ErrDisplayDisabled
	}
	if strings.TrimSpace(req.SDP) == "" {
		return OfferResponse{}, errors.New("offer sdp is required")
	}
	if req.Type != "" && req.Type != "offer" {
		return OfferResponse{}, fmt.Errorf("unsupported session description type %q", req.Type)
	}

	if !s.displayReachable(ctx, botID) {
		return OfferResponse{}, fmt.Errorf("%w: %s", ErrDisplayUnavailable, s.displayTarget(botID))
	}
	gstLaunch, err := resolveGSTLaunch()
	if err != nil {
		return OfferResponse{}, errors.Join(ErrEncoderUnavailable, err)
	}

	codec, err := negotiateCodec(req.SDP, forceVP8FromEnv())
	if err != nil {
		return OfferResponse{}, err
	}

	sess, err := s.session(ctx, botID, gstLaunch, codec)
	if err != nil {
		return OfferResponse{}, err
	}
	return sess.answer(ctx, req)
}

func (s *Service) ListSessions(botID string) []SessionInfo {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	sess := s.sessions[botID]
	s.mu.Unlock()
	if sess == nil || sess.closed() {
		return nil
	}
	sess.closeStalePeers(time.Now())
	return sess.peerInfos()
}

func (s *Service) CloseSession(botID, sessionID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	sess := s.sessions[botID]
	s.mu.Unlock()
	if sess == nil || sess.closed() {
		return false
	}
	return sess.closePeer(sessionID)
}

func (s *Service) Screenshot(ctx context.Context, botID string) ([]byte, string, error) {
	if s == nil || s.workspace == nil {
		return nil, "", ErrManagerUnavailable
	}
	if !s.workspace.BotDisplayEnabled(ctx, botID) {
		return nil, "", ErrDisplayDisabled
	}
	if !s.displayReachable(ctx, botID) {
		return nil, "", fmt.Errorf("%w: %s", ErrDisplayUnavailable, s.displayTarget(botID))
	}
	gstLaunch, err := resolveGSTLaunch()
	if err != nil {
		return nil, "", errors.Join(ErrEncoderUnavailable, err)
	}

	output, err := os.CreateTemp("", "memoh-display-*.jpg")
	if err != nil {
		return nil, "", err
	}
	outputPath := output.Name()
	_ = output.Close()
	defer func() { _ = os.Remove(outputPath) }()

	runCtx, cancel := context.WithTimeout(ctx, screenshotTimeout)

	listenConfig := net.ListenConfig{}
	proxy, err := listenConfig.Listen(runCtx, "tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		return nil, "", fmt.Errorf("start RFB screenshot shim: %w", err)
	}
	defer func() { _ = proxy.Close() }()
	defer cancel()
	go proxyRFBListener(runCtx, proxy, func(ctx context.Context) (net.Conn, error) {
		return s.dialRFB(ctx, botID)
	}, s.logger, botID)

	proxyPort := proxy.Addr().(*net.TCPAddr).Port
	cmd := exec.CommandContext(runCtx, gstLaunch, gstreamerScreenshotArgs(proxyPort, outputPath)...) //nolint:gosec // executable is resolved from PATH or explicit admin env.
	hideCommandWindow(cmd)
	cmd.Stdout = processLogWriter{logger: s.logger, botID: botID}
	cmd.Stderr = processLogWriter{logger: s.logger, botID: botID}
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("capture display screenshot: %w", err)
	}
	data, err := os.ReadFile(outputPath) //nolint:gosec // outputPath is a freshly-created temp file.
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", errors.New("display screenshot is empty")
	}
	data, err = limitJPEGSize(data, screenshotMaxBytes)
	if err != nil {
		return nil, "", err
	}
	return data, screenshotMIME, nil
}

func (s *Service) ControlInput(ctx context.Context, botID string, event ControlInput) error {
	return s.ControlInputs(ctx, botID, []ControlInput{event})
}

func (s *Service) ControlInputs(ctx context.Context, botID string, events []ControlInput) error {
	if s == nil || s.workspace == nil {
		return ErrManagerUnavailable
	}
	if !s.workspace.BotDisplayEnabled(ctx, botID) {
		return ErrDisplayDisabled
	}
	if !s.displayReachable(ctx, botID) {
		return fmt.Errorf("%w: %s", ErrDisplayUnavailable, s.displayTarget(botID))
	}
	conn, err := s.dialRFB(ctx, botID)
	if err != nil {
		return fmt.Errorf("connect display input: %w", err)
	}
	input, err := newRFBInputClient(conn)
	if err != nil {
		return fmt.Errorf("connect display input: %w", err)
	}
	defer func() { _ = input.Close() }()
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch event.Type {
		case "pointer":
			if err := input.Pointer(event.X, event.Y, event.ButtonMask); err != nil {
				return err
			}
		case "key":
			if event.Keysym == 0 {
				return errors.New("keysym is required")
			}
			if err := input.Key(event.Keysym, event.Down); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported input event type %q", event.Type)
		}
	}
	return nil
}

func (s *Service) session(ctx context.Context, botID, gstLaunch, codec string) (*session, error) {
	s.mu.Lock()
	if sess := s.sessions[botID]; sess != nil && !sess.closed() {
		s.mu.Unlock()
		// Display sessions are shared across viewers via RTP fan-out. If a new
		// viewer needs a different codec, we refuse rather than tearing down
		// the existing pipeline — that would black out anyone already watching.
		if sess.codec != codec {
			return nil, fmt.Errorf("%w: another viewer is already using %s", ErrCodecUnsupported, sess.codec)
		}
		return sess, nil
	}
	if start := s.starting[botID]; start != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-start.done:
		}
		if start.err != nil {
			return nil, start.err
		}
		if start.sess == nil || start.sess.closed() {
			return nil, fmt.Errorf("%w: display pipeline is not running", ErrEncoderUnavailable)
		}
		if start.sess.codec != codec {
			return nil, fmt.Errorf("%w: another viewer is already using %s", ErrCodecUnsupported, start.sess.codec)
		}
		return start.sess, nil
	}
	start := &sessionStart{done: make(chan struct{})}
	s.starting[botID] = start
	s.mu.Unlock()

	sess := newSession(s, botID, gstLaunch, codec)
	if err := sess.start(ctx); err != nil {
		sess.stop()
		s.finishSessionStart(botID, start, nil, err)
		return nil, err
	}

	s.mu.Lock()
	current := s.sessions[botID]
	if current == nil || current.closed() {
		s.sessions[botID] = sess
		s.mu.Unlock()
		s.finishSessionStart(botID, start, sess, nil)
		return sess, nil
	}
	s.mu.Unlock()

	sess.stop()
	if current.codec != codec {
		err := fmt.Errorf("%w: another viewer is already using %s", ErrCodecUnsupported, current.codec)
		s.finishSessionStart(botID, start, nil, err)
		return nil, err
	}
	s.finishSessionStart(botID, start, current, nil)
	return current, nil
}

func (s *Service) finishSessionStart(botID string, start *sessionStart, sess *session, err error) {
	start.sess = sess
	start.err = err
	s.mu.Lock()
	if s.starting[botID] == start {
		delete(s.starting, botID)
	}
	s.mu.Unlock()
	close(start.done)
}

func (s *Service) removeSession(botID string, target *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.sessions[botID]; current == target {
		delete(s.sessions, botID)
	}
}

type session struct {
	service   *Service
	botID     string
	gstLaunch string
	codec     string

	ctx          context.Context
	cancel       context.CancelFunc
	runCtxCancel context.CancelFunc

	proxy net.Listener
	udp   *net.UDPConn
	cmd   *exec.Cmd

	// firstPacket is closed by forwardRTP once the GStreamer pipeline emits
	// its first RTP packet, so session.start can wait for real output rather
	// than a fixed 150ms guess (which returned success before the encoder had
	// produced any frames — the peer connected but the track stayed empty).
	firstPacket     chan struct{}
	firstPacketOnce sync.Once

	tracksMu sync.RWMutex
	tracks   map[string]*webrtc.TrackLocalStaticRTP
	input    *rfbInputClient

	peersMu sync.RWMutex
	peers   map[string]*peerSession

	idleStopMu    sync.Mutex
	idleStopTimer *time.Timer

	stopOnce sync.Once
}

type peerSession struct {
	id        string
	codec     string
	createdAt time.Time
	trackID   string

	mu    sync.RWMutex
	state string

	closeOnce sync.Once
	close     func()
}

func newSession(service *Service, botID, gstLaunch, codec string) *session {
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored on the session and called when the display session stops.
	return &session{
		service:     service,
		botID:       botID,
		gstLaunch:   gstLaunch,
		codec:       codec,
		ctx:         ctx,
		cancel:      cancel,
		tracks:      make(map[string]*webrtc.TrackLocalStaticRTP),
		peers:       make(map[string]*peerSession),
		firstPacket: make(chan struct{}),
	}
}

func (s *session) closed() bool {
	select {
	case <-s.ctx.Done():
		return true
	default:
		return false
	}
}

func (s *session) start(ctx context.Context) error {
	if !s.service.displayReachable(ctx, s.botID) {
		return fmt.Errorf("%w: %s", ErrDisplayUnavailable, s.service.displayTarget(s.botID))
	}

	runCtx, runCtxCancel := context.WithCancel(context.WithoutCancel(ctx))
	cancelRunCtx := true
	defer func() {
		if cancelRunCtx {
			runCtxCancel()
		}
	}()
	go func() {
		<-s.ctx.Done()
		runCtxCancel()
	}()

	listenConfig := net.ListenConfig{}
	proxy, err := listenConfig.Listen(runCtx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start RFB tcp shim: %w", err)
	}
	s.proxy = proxy

	udpAddr, err := net.ResolveUDPAddr("udp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("resolve RTP udp address: %w", err)
	}
	udp, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return fmt.Errorf("listen RTP udp: %w", err)
	}
	s.udp = udp

	proxyPort := proxy.Addr().(*net.TCPAddr).Port
	rtpPort := udp.LocalAddr().(*net.UDPAddr).Port
	args := gstreamerArgs(s.codec, proxyPort, rtpPort)
	cmd := exec.CommandContext(runCtx, s.gstLaunch, args...) //nolint:gosec // executable is resolved from PATH or explicit admin env.
	hideCommandWindow(cmd)
	cmd.Stdout = processLogWriter{logger: s.service.logger, botID: s.botID}
	cmd.Stderr = processLogWriter{logger: s.service.logger, botID: s.botID}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start gstreamer display pipeline: %w", err)
	}
	s.cmd = cmd

	if conn, err := s.service.dialRFB(runCtx, s.botID); err == nil {
		input, inputErr := newRFBInputClient(conn)
		if inputErr != nil {
			_ = conn.Close()
			s.service.logger.Warn("display input channel unavailable", slog.String("bot_id", s.botID), slog.Any("error", inputErr))
		} else {
			s.input = input
		}
	} else {
		s.service.logger.Warn("display input channel unavailable", slog.String("bot_id", s.botID), slog.Any("error", err))
	}
	s.runCtxCancel = runCtxCancel
	cancelRunCtx = false

	s.service.logger.Info("display encoder started",
		slog.String("bot_id", s.botID),
		slog.String("rfb_target", s.service.displayTarget(s.botID)),
		slog.String("gst_launch", s.gstLaunch),
		slog.String("codec", s.codec),
		slog.Int("proxy_port", proxyPort),
		slog.Int("rtp_port", rtpPort),
		slog.Int("pid", cmd.Process.Pid),
	)

	go s.acceptProxy()
	go s.forwardRTP()
	gstreamerDone := make(chan error, 1)
	go s.waitGStreamer(gstreamerDone)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-gstreamerDone:
		if err != nil {
			return fmt.Errorf("%w: display pipeline exited during startup: %w", ErrEncoderUnavailable, err)
		}
		return fmt.Errorf("%w: display pipeline exited during startup", ErrEncoderUnavailable)
	case <-s.firstPacket:
		// GStreamer emitted its first RTP packet — the pipeline is actually
		// producing frames, not just running. Returning here (instead of after
		// a fixed 150ms) means the peer only connects once frames are flowing,
		// so the <video> element receives data immediately on connect.
		if s.closed() {
			return fmt.Errorf("%w: display pipeline exited during startup", ErrEncoderUnavailable)
		}
		return nil
	case <-time.After(firstPacketTimeout):
		return fmt.Errorf("%w: display pipeline produced no frames within %s", ErrEncoderUnavailable, firstPacketTimeout)
	}
}

func (s *session) answer(ctx context.Context, req OfferRequest) (OfferResponse, error) {
	if s.closed() {
		return OfferResponse{}, fmt.Errorf("%w: display pipeline is not running", ErrEncoderUnavailable)
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	s.closeStalePeers(time.Now())
	previousPeer := s.peer(sessionID)

	mediaEngine := &webrtc.MediaEngine{}
	if err := registerVideoCodec(mediaEngine, s.codec); err != nil {
		return OfferResponse{}, err
	}

	api, rtcCfg, err := s.service.newWebRTCAPI(mediaEngine, req.NATIPs)
	if err != nil {
		return OfferResponse{}, err
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return OfferResponse{}, err
	}
	if rtcCfg.UDPPort != 0 || rtcCfg.UDPPortMin != 0 || len(rtcCfg.NATIPs) > 0 {
		s.service.logger.Info("display webrtc configured",
			slog.String("bot_id", s.botID),
			slog.Int("udp_port", int(rtcCfg.UDPPort)),
			slog.Int("udp_port_min", int(rtcCfg.UDPPortMin)),
			slog.Int("udp_port_max", int(rtcCfg.UDPPortMax)),
			slog.Any("nat_ips", rtcCfg.NATIPs),
		)
	}

	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:  s.codec,
		ClockRate: videoClockRate,
	}, "video", "display-"+s.botID)
	if err != nil {
		_ = pc.Close()
		return OfferResponse{}, err
	}

	sender, err := pc.AddTrack(track)
	if err != nil {
		_ = pc.Close()
		return OfferResponse{}, err
	}
	go drainRTCP(sender)

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != "display-input" {
			return
		}
		dc.OnMessage(func(msg webrtc.DataChannelMessage) {
			if err := s.handleInput(msg.Data); err != nil {
				s.service.logger.Debug("display input event dropped", slog.String("bot_id", s.botID), slog.Any("error", err))
			}
		})
	})

	trackID := uuid.NewString()
	s.addTrack(trackID, track)
	peer := &peerSession{
		id:        sessionID,
		codec:     s.codec,
		createdAt: time.Now(),
		state:     "new",
		trackID:   trackID,
	}

	var cleanupOnce sync.Once
	cleanup := func(closePeer bool) {
		cleanupOnce.Do(func() {
			s.removePeer(peer)
			s.removeTrack(trackID)
			if closePeer {
				_ = pc.Close()
			}
		})
	}
	peer.close = func() { cleanup(true) }
	s.addPeer(peer)
	if previousPeer != nil {
		previousPeer.closeNow()
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		peer.setState(state.String())
		s.service.logger.Info("display webrtc connection state",
			slog.String("bot_id", s.botID),
			slog.String("session_id", sessionID),
			slog.String("state", state.String()),
		)
		switch state {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateDisconnected:
			cleanup(true)
		case webrtc.PeerConnectionStateClosed:
			cleanup(false)
		default:
		}
	})
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		s.service.logger.Info("display webrtc ice state",
			slog.String("bot_id", s.botID),
			slog.String("session_id", sessionID),
			slog.String("state", state.String()),
		)
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  req.SDP,
	}); err != nil {
		cleanup(true)
		return OfferResponse{}, err
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		cleanup(true)
		return OfferResponse{}, err
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		cleanup(true)
		return OfferResponse{}, err
	}

	select {
	case <-ctx.Done():
		cleanup(true)
		return OfferResponse{}, ctx.Err()
	case <-gatherDone:
	}

	local := pc.LocalDescription()
	if local == nil {
		cleanup(true)
		return OfferResponse{}, errors.New("local session description unavailable")
	}

	return OfferResponse{Type: "answer", SDP: local.SDP, SessionID: sessionID}, nil
}

type inputEvent struct {
	Type       string `json:"type"`
	X          int    `json:"x,omitempty"`
	Y          int    `json:"y,omitempty"`
	ButtonMask uint8  `json:"button_mask,omitempty"`
	Keysym     uint32 `json:"keysym,omitempty"`
	Down       bool   `json:"down,omitempty"`
}

func (s *session) handleInput(data []byte) error {
	if s.input == nil {
		return errors.New("display input is unavailable")
	}
	var event inputEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	switch event.Type {
	case "pointer":
		return s.input.Pointer(event.X, event.Y, event.ButtonMask)
	case "key":
		if event.Keysym == 0 {
			return errors.New("keysym is required")
		}
		return s.input.Key(event.Keysym, event.Down)
	default:
		return fmt.Errorf("unsupported input event type %q", event.Type)
	}
}

func (s *session) addTrack(id string, track *webrtc.TrackLocalStaticRTP) {
	s.tracksMu.Lock()
	s.tracks[id] = track
	// Cancel the idle-stop timer while still holding tracksMu so the timer
	// callback cannot observe an empty track set and stop() the encoder in the
	// gap between adding the track and cancelling the timer — which would bind
	// a new viewer to a stopped pipeline (black video, no error).
	s.cancelIdleStop()
	s.tracksMu.Unlock()
}

func (s *session) removeTrack(id string) {
	s.tracksMu.Lock()
	delete(s.tracks, id)
	empty := len(s.tracks) == 0
	s.tracksMu.Unlock()
	if empty {
		s.scheduleIdleStop()
	}
}

// scheduleIdleStop keeps the encoder alive for encoderIdleHold after the last
// viewer leaves, so a quick tab-switch reconnect reuses the running pipeline
// instead of cold-starting.
//
// TODO(display): this only covers the IDLE case (no viewers). It does NOT
// cover the GStreamer pipeline exiting on its own — which is what actually
// happens on session close: the RFB (VNC) connection to the bot is torn down,
// rfb_src emits "Connection was closed", the pipeline exits with status 1,
// and the encoder is gone. The next reconnect then pays a full cold start
// (firstPacket wait, slow/flaky under Orbstack), which is the "close tab →
// reconnect takes forever" symptom. Fix: catch the pipeline-error exit in
// waitGStreamer and either re-establish the RFB connection + restart the
// pipeline in place, or surface it as a recoverable state so the next offer
// reuses the session shell instead of rebuilding it. encoderIdleHold cannot
// help here because the pipeline is gone, not idle.
func (s *session) scheduleIdleStop() {
	s.idleStopMu.Lock()
	defer s.idleStopMu.Unlock()
	if s.idleStopTimer != nil {
		s.idleStopTimer.Stop()
	}
	s.idleStopTimer = time.AfterFunc(encoderIdleHold, func() {
		// Hold tracksMu for the empty-check AND the stop() so a concurrent
		// addTrack cannot insert a track between "still empty" and "stop the
		// encoder" — that race bound a new viewer to a pipeline being torn
		// down. stop() is idempotent (stopOnce) and lock-free w.r.t. tracksMu.
		s.tracksMu.Lock()
		empty := len(s.tracks) == 0
		if empty && !s.closed() {
			s.stop()
		}
		s.tracksMu.Unlock()
	})
}

func (s *session) cancelIdleStop() {
	s.idleStopMu.Lock()
	defer s.idleStopMu.Unlock()
	if s.idleStopTimer != nil {
		s.idleStopTimer.Stop()
		s.idleStopTimer = nil
	}
}

func (s *session) addPeer(peer *peerSession) {
	s.peersMu.Lock()
	s.peers[peer.id] = peer
	s.peersMu.Unlock()
}

func (s *session) removePeer(peer *peerSession) {
	if peer == nil {
		return
	}
	s.peersMu.Lock()
	if current := s.peers[peer.id]; current == peer {
		delete(s.peers, peer.id)
	}
	s.peersMu.Unlock()
}

func (s *session) peer(id string) *peerSession {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	return s.peers[strings.TrimSpace(id)]
}

func (s *session) peerInfos() []SessionInfo {
	s.peersMu.RLock()
	defer s.peersMu.RUnlock()
	infos := make([]SessionInfo, 0, len(s.peers))
	for _, peer := range s.peers {
		infos = append(infos, peer.info())
	}
	return infos
}

func (s *session) closePeer(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	s.peersMu.RLock()
	peer := s.peers[id]
	s.peersMu.RUnlock()
	if peer == nil {
		return false
	}
	peer.closeNow()
	return true
}

func (s *session) closeStalePeers(now time.Time) {
	s.peersMu.RLock()
	stale := make([]*peerSession, 0)
	for _, peer := range s.peers {
		if peer.stale(now) {
			stale = append(stale, peer)
		}
	}
	s.peersMu.RUnlock()
	for _, peer := range stale {
		peer.closeNow()
	}
}

func (p *peerSession) setState(state string) {
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
}

func (p *peerSession) info() SessionInfo {
	p.mu.RLock()
	state := p.state
	p.mu.RUnlock()
	return SessionInfo{
		ID:        p.id,
		Codec:     p.codec,
		State:     state,
		CreatedAt: p.createdAt,
	}
}

func (p *peerSession) closeNow() {
	p.closeOnce.Do(func() {
		if p.close != nil {
			p.close()
		}
	})
}

func (p *peerSession) stale(now time.Time) bool {
	p.mu.RLock()
	state := p.state
	p.mu.RUnlock()
	switch state {
	case webrtc.PeerConnectionStateClosed.String(),
		webrtc.PeerConnectionStateDisconnected.String(),
		webrtc.PeerConnectionStateFailed.String():
		return true
	case webrtc.PeerConnectionStateNew.String(),
		webrtc.PeerConnectionStateConnecting.String():
		return now.Sub(p.createdAt) > stalePeerTTL
	default:
		return false
	}
}

func (s *session) stop() {
	s.stopOnce.Do(func() {
		s.cancelIdleStop()
		s.cancel()
		if s.runCtxCancel != nil {
			s.runCtxCancel()
		}
		if s.proxy != nil {
			_ = s.proxy.Close()
		}
		if s.udp != nil {
			_ = s.udp.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		if s.input != nil {
			_ = s.input.Close()
		}
		s.service.removeSession(s.botID, s)
		s.service.logger.Info("display encoder stopped", slog.String("bot_id", s.botID))
	})
}

func (s *session) acceptProxy() {
	for {
		conn, err := s.proxy.Accept()
		if err != nil {
			if s.ctx.Err() == nil {
				s.service.logger.Warn("display RFB shim stopped", slog.String("bot_id", s.botID), slog.Any("error", err))
			}
			return
		}
		go s.proxyRFB(conn)
	}
}

func (s *session) proxyRFB(conn net.Conn) {
	proxyRFBConnection(s.ctx, conn, func(ctx context.Context) (net.Conn, error) {
		return s.service.dialRFB(ctx, s.botID)
	}, s.service.logger, s.botID)
}

func proxyRFBListener(ctx context.Context, listener net.Listener, dialRFB func(context.Context) (net.Conn, error), logger *slog.Logger, botID string) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				logger.Warn("display RFB screenshot shim stopped", slog.String("bot_id", botID), slog.Any("error", err))
			}
			return
		}
		go proxyRFBConnection(ctx, conn, dialRFB, logger, botID)
	}
}

func proxyRFBConnection(ctx context.Context, conn net.Conn, dialRFB func(context.Context) (net.Conn, error), logger *slog.Logger, botID string) {
	defer func() { _ = conn.Close() }()

	rfbConn, err := dialRFB(ctx)
	if err != nil {
		logger.Warn("display RFB dial failed", slog.String("bot_id", botID), slog.Any("error", err))
		return
	}
	defer func() { _ = rfbConn.Close() }()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(conn, rfbConn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(rfbConn, conn)
		done <- struct{}{}
	}()
	<-done
}

func (s *session) forwardRTP() {
	buf := make([]byte, 4096)
	for {
		n, _, err := s.udp.ReadFromUDP(buf)
		if err != nil {
			if s.ctx.Err() == nil {
				s.service.logger.Warn("display RTP reader stopped", slog.String("bot_id", s.botID), slog.Any("error", err))
				s.stop()
			}
			return
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			s.service.logger.Debug("display RTP packet dropped", slog.String("bot_id", s.botID), slog.Any("error", err))
			continue
		}

		// Signal that the GStreamer pipeline has produced its first frame so
		// session.start can return success based on real output, not a timer.
		s.firstPacketOnce.Do(func() { close(s.firstPacket) })

		s.tracksMu.RLock()
		for _, track := range s.tracks {
			pktCopy := pkt
			if err := track.WriteRTP(&pktCopy); err != nil {
				s.service.logger.Debug("display RTP write failed", slog.String("bot_id", s.botID), slog.Any("error", err))
			}
		}
		s.tracksMu.RUnlock()
	}
}

func (s *session) waitGStreamer(done chan<- error) {
	err := s.cmd.Wait()
	if done != nil {
		select {
		case done <- err:
		default:
		}
	}
	if s.ctx.Err() == nil {
		s.service.logger.Warn("display gstreamer pipeline exited", slog.String("bot_id", s.botID), slog.Any("error", err))
		s.stop()
	}
}

func drainRTCP(sender *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buf); err != nil {
			return
		}
	}
}

func (s *Service) newWebRTCAPI(mediaEngine *webrtc.MediaEngine, inferredNATIPs []string) (*webrtc.API, rtcSettings, error) {
	cfg, err := readRTCSettings(inferredNATIPs)
	if err != nil {
		return nil, rtcSettings{}, err
	}

	settingEngine := webrtc.SettingEngine{}
	if cfg.UDPPort != 0 {
		mux, err := s.sharedICEUDPMux(cfg.UDPPort)
		if err != nil {
			return nil, rtcSettings{}, err
		}
		settingEngine.SetICEUDPMux(mux)
	} else if cfg.UDPPortMin != 0 || cfg.UDPPortMax != 0 {
		if err := settingEngine.SetEphemeralUDPPortRange(cfg.UDPPortMin, cfg.UDPPortMax); err != nil {
			return nil, rtcSettings{}, fmt.Errorf("configure display WebRTC UDP port range: %w", err)
		}
	}
	if len(cfg.NATIPs) > 0 {
		if err := settingEngine.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        cfg.NATIPs,
			AsCandidateType: webrtc.ICECandidateTypeHost,
			Mode:            webrtc.ICEAddressRewriteReplace,
		}); err != nil {
			return nil, rtcSettings{}, fmt.Errorf("configure display WebRTC NAT rewrite: %w", err)
		}
	}

	return webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithSettingEngine(settingEngine)), cfg, nil
}

func (s *Service) sharedICEUDPMux(port uint16) (ice.UDPMux, error) {
	s.rtcMu.Lock()
	defer s.rtcMu.Unlock()
	if s.rtcClosed {
		return nil, errors.New("display WebRTC service is closed")
	}
	if s.rtcMux != nil {
		if s.rtcMuxPort != port {
			return nil, fmt.Errorf("display WebRTC UDP mux already listens on port %d, cannot switch to %d", s.rtcMuxPort, port)
		}
		return s.rtcMux, nil
	}
	mux, err := ice.NewMultiUDPMuxFromPort(int(port))
	if err != nil {
		return nil, fmt.Errorf("listen for display WebRTC on UDP port %d: %w", port, err)
	}
	s.rtcMux = mux
	s.rtcMuxPort = port
	return mux, nil
}

func readRTCSettings(inferredNATIPs []string) (rtcSettings, error) {
	var cfg rtcSettings
	portRaw := strings.TrimSpace(os.Getenv(rtcUDPPortEnv))
	minRaw := strings.TrimSpace(os.Getenv(rtcUDPPortMinEnv))
	maxRaw := strings.TrimSpace(os.Getenv(rtcUDPPortMaxEnv))
	if portRaw != "" && (minRaw != "" || maxRaw != "") {
		return cfg, fmt.Errorf("%s cannot be combined with %s or %s", rtcUDPPortEnv, rtcUDPPortMinEnv, rtcUDPPortMaxEnv)
	}
	if portRaw != "" {
		port, err := parseRTCUDPPort(rtcUDPPortEnv, portRaw)
		if err != nil {
			return cfg, err
		}
		cfg.UDPPort = port
	}
	if minRaw != "" || maxRaw != "" {
		if minRaw == "" || maxRaw == "" {
			return cfg, fmt.Errorf("%s and %s must be configured together", rtcUDPPortMinEnv, rtcUDPPortMaxEnv)
		}
		minPort, err := parseRTCUDPPort(rtcUDPPortMinEnv, minRaw)
		if err != nil {
			return cfg, err
		}
		maxPort, err := parseRTCUDPPort(rtcUDPPortMaxEnv, maxRaw)
		if err != nil {
			return cfg, err
		}
		cfg.UDPPortMin = minPort
		cfg.UDPPortMax = maxPort
	}

	for _, part := range strings.Split(os.Getenv(rtcNATIPsEnv), ",") {
		ip := strings.TrimSpace(part)
		if ip == "" {
			continue
		}
		if net.ParseIP(ip) == nil {
			return cfg, fmt.Errorf("%s contains invalid IP %q", rtcNATIPsEnv, ip)
		}
		cfg.NATIPs = append(cfg.NATIPs, ip)
	}
	if len(cfg.NATIPs) == 0 {
		for _, ip := range inferredNATIPs {
			ip = strings.TrimSpace(ip)
			if ip == "" {
				continue
			}
			if net.ParseIP(ip) == nil {
				return cfg, fmt.Errorf("inferred display WebRTC NAT IP %q is invalid", ip)
			}
			cfg.NATIPs = append(cfg.NATIPs, ip)
		}
	}
	return cfg, nil
}

func parseRTCUDPPort(name, raw string) (uint16, error) {
	port, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("%s must be a UDP port between 1 and 65535", name)
	}
	return uint16(port), nil
}

// negotiateCodec inspects the remote SDP offer's video m-section and returns
// the codec the encoder should produce. H264 is preferred whenever the offer
// advertises it; VP8 is used as a fallback. forceVP8 short-circuits the
// preference to VP8 (useful for environments without an x264 plugin) and
// errors out if the peer did not offer VP8 — silently encoding H264 in that
// situation would defeat the purpose of the override.
func negotiateCodec(offerSDP string, forceVP8 bool) (string, error) {
	offered := offeredVideoCodecs(offerSDP)
	if forceVP8 {
		if offered.vp8 {
			return CodecVP8, nil
		}
		return "", fmt.Errorf("%w: peer did not offer VP8 (force-VP8 enabled)", ErrCodecUnsupported)
	}
	if offered.h264 {
		return CodecH264, nil
	}
	if offered.vp8 {
		return CodecVP8, nil
	}
	return "", fmt.Errorf("%w: peer offered neither H264 nor VP8", ErrCodecUnsupported)
}

type offeredCodecs struct {
	h264 bool
	vp8  bool
}

func offeredVideoCodecs(rawSDP string) offeredCodecs {
	var result offeredCodecs
	parsed := &sdpv3.SessionDescription{}
	if err := parsed.Unmarshal([]byte(rawSDP)); err != nil {
		return result
	}
	for _, media := range parsed.MediaDescriptions {
		if media == nil || media.MediaName.Media != "video" {
			continue
		}
		for _, attr := range media.Attributes {
			if attr.Key != "rtpmap" {
				continue
			}
			value := strings.ToUpper(attr.Value)
			switch {
			case strings.Contains(value, "H264"):
				result.h264 = true
			case strings.Contains(value, "VP8"):
				result.vp8 = true
			}
		}
	}
	return result
}

func registerVideoCodec(engine *webrtc.MediaEngine, codec string) error {
	switch codec {
	case CodecH264:
		return engine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeH264,
				ClockRate:   videoClockRate,
				SDPFmtpLine: h264FmtpLine,
			},
			PayloadType: videoPayloadTypeH264,
		}, webrtc.RTPCodecTypeVideo)
	case CodecVP8:
		return engine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeVP8,
				ClockRate: videoClockRate,
			},
			PayloadType: videoPayloadTypeVP8,
		}, webrtc.RTPCodecTypeVideo)
	default:
		return fmt.Errorf("%w: %q", ErrCodecUnsupported, codec)
	}
}

func forceVP8FromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(forceVP8Env))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func gstreamerArgs(codec string, rfbPort, rtpPort int) []string {
	base := []string{
		"-q",
		"rfbsrc", "host=127.0.0.1", fmt.Sprintf("port=%d", rfbPort), "shared=true", "incremental=false", "use-copyrect=true", "do-timestamp=true",
		"!", "videoconvert",
		"!", "videorate",
		"!", fmt.Sprintf("video/x-raw,framerate=%d/1", videoFrameRate),
		"!", "queue", "leaky=downstream", "max-size-buffers=2",
	}
	switch codec {
	case CodecH264:
		return append(base,
			"!", "x264enc", "tune=zerolatency", "speed-preset=ultrafast",
			"bframes=0", "key-int-max=30", "byte-stream=true",
			"!", "video/x-h264,profile=baseline,stream-format=byte-stream,alignment=au",
			"!", "h264parse", "config-interval=-1",
			"!", "rtph264pay", "aggregate-mode=zero-latency", "config-interval=-1",
			fmt.Sprintf("pt=%d", videoPayloadTypeH264),
			"!", "udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", rtpPort), "sync=false", "async=false",
		)
	case CodecVP8:
		fallthrough
	default:
		return append(base,
			"!", "vp8enc", "deadline=1", "cpu-used=8", "keyframe-max-dist=30",
			"!", "rtpvp8pay", fmt.Sprintf("pt=%d", videoPayloadTypeVP8),
			"!", "udpsink", "host=127.0.0.1", fmt.Sprintf("port=%d", rtpPort), "sync=false", "async=false",
		)
	}
}

func gstreamerScreenshotArgs(rfbPort int, outputPath string) []string {
	return []string{
		"-q",
		"rfbsrc", "host=127.0.0.1", fmt.Sprintf("port=%d", rfbPort), "shared=true", "incremental=false", "do-timestamp=true", "num-buffers=1",
		"!", "videoconvert",
		"!", "videoscale",
		"!", fmt.Sprintf("video/x-raw,width=%d,pixel-aspect-ratio=1/1", screenshotWidth),
		"!", "jpegenc", fmt.Sprintf("quality=%d", screenshotQuality),
		"!", "filesink", "location=" + outputPath,
	}
}

func limitJPEGSize(data []byte, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 || len(data) <= maxBytes {
		return data, nil
	}
	source, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode oversized display screenshot: %w", err)
	}

	var last []byte
	sourceWidth := source.Bounds().Dx()
	for _, candidate := range screenshotJPEGCandidates {
		img := source
		if candidate.width > 0 && candidate.width < sourceWidth {
			img = resizeNearest(source, candidate.width)
		}
		encoded, err := encodeJPEG(img, candidate.quality)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= maxBytes {
			return encoded, nil
		}
		last = encoded
	}

	return nil, fmt.Errorf("display screenshot exceeds size limit after compression: %d > %d bytes", len(last), maxBytes)
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func resizeNearest(src image.Image, width int) image.Image {
	bounds := src.Bounds()
	srcWidth := bounds.Dx()
	srcHeight := bounds.Dy()
	if width <= 0 || srcWidth <= 0 || srcHeight <= 0 || width >= srcWidth {
		return src
	}
	height := width * srcHeight / srcWidth
	if height < 1 {
		height = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		sourceY := bounds.Min.Y + y*srcHeight/height
		for x := 0; x < width; x++ {
			sourceX := bounds.Min.X + x*srcWidth/width
			dst.Set(x, y, src.At(sourceX, sourceY))
		}
	}
	return dst
}

func resolveGSTLaunch() (string, error) {
	if path := strings.TrimSpace(os.Getenv(gstLaunchEnv)); path != "" {
		return resolveExecutable(path)
	}

	candidates := []string{"gst-launch-1.0"}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/opt/homebrew/bin/gst-launch-1.0",
			"/usr/local/bin/gst-launch-1.0",
		)
	}
	var errs []error
	for _, candidate := range candidates {
		path, err := resolveExecutable(candidate)
		if err == nil {
			return path, nil
		}
		errs = append(errs, err)
	}
	return "", errors.Join(errs...)
}

func resolveExecutable(candidate string) (string, error) {
	if strings.Contains(candidate, string(os.PathSeparator)) {
		cleanPath := filepath.Clean(candidate)
		info, err := os.Stat(cleanPath) //nolint:gosec // operator-controlled binary path from config/env.
		if err != nil {
			return "", err
		}
		if !isUsableExecutable(info, runtime.GOOS) {
			return "", fmt.Errorf("%s is not executable", cleanPath)
		}
		return cleanPath, nil
	}
	return exec.LookPath(candidate)
}

func isUsableExecutable(info os.FileInfo, goos string) bool {
	if info.IsDir() {
		return false
	}
	if goos == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

func isSocketReady(ctx context.Context, path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	dialCtx, cancel := context.WithTimeout(ctx, socketProbeTimeout)
	defer cancel()
	dialer := net.Dialer{Timeout: socketProbeTimeout}
	conn, err := dialer.DialContext(dialCtx, "unix", filepath.Clean(path))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

type processLogWriter struct {
	logger *slog.Logger
	botID  string
}

func (w processLogWriter) Write(p []byte) (int, error) {
	text := strings.TrimSpace(string(p))
	if text != "" {
		w.logger.Warn("display gstreamer output", slog.String("bot_id", w.botID), slog.String("message", text))
	}
	return len(p), nil
}

type rfbInputClient struct {
	mu   sync.Mutex
	conn net.Conn
}

func newRFBInputClient(conn net.Conn) (*rfbInputClient, error) {
	client := &rfbInputClient{conn: conn}
	if err := client.handshake(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

func (c *rfbInputClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *rfbInputClient) Pointer(x, y int, buttonMask uint8) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return net.ErrClosed
	}
	msg := []byte{5, buttonMask, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(msg[2:4], clampUint16(x))
	binary.BigEndian.PutUint16(msg[4:6], clampUint16(y))
	_, err := c.conn.Write(msg)
	return err
}

func (c *rfbInputClient) Key(keysym uint32, down bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return net.ErrClosed
	}
	msg := []byte{4, 0, 0, 0, 0, 0, 0, 0}
	if down {
		msg[1] = 1
	}
	binary.BigEndian.PutUint32(msg[4:8], keysym)
	_, err := c.conn.Write(msg)
	return err
}

func (c *rfbInputClient) handshake() error {
	return handshakeRFBNoneSecurity(c.conn, true)
}

func probeRFBNoneSecurity(conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	return handshakeRFBNoneSecurity(conn, true)
}

func handshakeRFBNoneSecurity(conn net.Conn, clientInit bool) error {
	if err := conn.SetDeadline(time.Now().Add(displayProbePeriod)); err != nil {
		return err
	}
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	version := make([]byte, 12)
	if _, err := io.ReadFull(conn, version); err != nil {
		return fmt.Errorf("read RFB version: %w", err)
	}
	if _, err := conn.Write(version); err != nil {
		return fmt.Errorf("write RFB version: %w", err)
	}

	count := []byte{0}
	if _, err := io.ReadFull(conn, count); err != nil {
		return fmt.Errorf("read RFB security types: %w", err)
	}
	if count[0] == 0 {
		reason, err := readRFBString(conn)
		if err != nil {
			return err
		}
		return fmt.Errorf("RFB security negotiation failed: %s", reason)
	}
	types := make([]byte, int(count[0]))
	if _, err := io.ReadFull(conn, types); err != nil {
		return fmt.Errorf("read RFB security type list: %w", err)
	}
	if !containsByte(types, 1) {
		return errors.New("RFB server does not allow None security")
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		return fmt.Errorf("write RFB security type: %w", err)
	}
	result := make([]byte, 4)
	if _, err := io.ReadFull(conn, result); err != nil {
		return fmt.Errorf("read RFB security result: %w", err)
	}
	if binary.BigEndian.Uint32(result) != 0 {
		reason, err := readRFBString(conn)
		if err != nil {
			return err
		}
		return fmt.Errorf("RFB security rejected: %s", reason)
	}
	if !clientInit {
		return nil
	}

	if _, err := conn.Write([]byte{1}); err != nil {
		return fmt.Errorf("write RFB client init: %w", err)
	}
	header := make([]byte, 24)
	if _, err := io.ReadFull(conn, header); err != nil {
		return fmt.Errorf("read RFB server init: %w", err)
	}
	nameLen := binary.BigEndian.Uint32(header[20:24])
	if nameLen > 0 {
		if _, err := io.CopyN(io.Discard, conn, int64(nameLen)); err != nil {
			return fmt.Errorf("read RFB server name: %w", err)
		}
	}
	return nil
}

func readRFBString(r io.Reader) (string, error) {
	sizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, sizeBuf); err != nil {
		return "", err
	}
	size := binary.BigEndian.Uint32(sizeBuf)
	if size == 0 {
		return "", nil
	}
	if size > 4096 {
		return "", fmt.Errorf("RFB string too large: %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func containsByte(values []byte, target byte) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func clampUint16(value int) uint16 {
	switch {
	case value < 0:
		return 0
	case value > 0xffff:
		return 0xffff
	default:
		return uint16(value)
	}
}
