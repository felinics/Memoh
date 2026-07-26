package display

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	runtimedisplay "github.com/memohai/memoh/domains/runtime/display"
)

func TestServiceShutdownStopsPeersAndWaitsForWorkers(t *testing.T) {
	service := NewService(nil, nil)
	sess := newSession(service, "bot-1", "gst-launch-1.0", CodecVP8)
	workerExited := make(chan struct{})
	if !sess.goWorker(func() {
		<-sess.ctx.Done()
		close(workerExited)
	}) {
		t.Fatal("worker was not admitted")
	}
	peerClosed := make(chan struct{})
	peer := &peerSession{id: "viewer-1", state: "connected"}
	peer.close = func() {
		close(peerClosed)
		sess.removePeer(peer)
	}
	sess.addPeer(peer)
	service.mu.Lock()
	service.sessions[sess.botID] = sess
	service.workers[sess] = struct{}{}
	service.mu.Unlock()

	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-workerExited:
	default:
		t.Fatal("Shutdown returned before the session worker exited")
	}
	select {
	case <-peerClosed:
	default:
		t.Fatal("Shutdown returned before the peer closed")
	}
	if sess.goWorker(func() {}) {
		t.Fatal("session admitted a worker after shutdown")
	}
	if _, err := service.session(t.Context(), "bot-2", "gst-launch-1.0", CodecVP8); !errors.Is(err, runtimedisplay.ErrServiceStopped) {
		t.Fatalf("session() error = %v, want ErrServiceStopped", err)
	}
}

func TestServiceShutdownCanRetryAfterDeadline(t *testing.T) {
	service := NewService(nil, nil)
	sess := newSession(service, "bot-1", "gst-launch-1.0", CodecVP8)
	release := make(chan struct{})
	if !sess.goWorker(func() { <-release }) {
		t.Fatal("worker was not admitted")
	}
	service.mu.Lock()
	service.sessions[sess.botID] = sess
	service.workers[sess] = struct{}{}
	service.mu.Unlock()

	stopCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := service.Shutdown(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Shutdown() error = %v, want context.Canceled", err)
	}
	close(release)
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatalf("retry Shutdown() error = %v", err)
	}
	select {
	case <-sess.done:
	default:
		t.Fatal("retry Shutdown returned before the session finished")
	}
}

func TestServiceShutdownCancelsAndWaitsForAdmittedOperation(t *testing.T) {
	service := NewService(nil, nil)
	opCtx, finish, err := service.beginOperation(t.Context())
	if err != nil {
		t.Fatalf("beginOperation() error = %v", err)
	}
	canceled := make(chan struct{})
	release := make(chan struct{})
	go func() {
		<-opCtx.Done()
		close(canceled)
		<-release
		finish()
	}()

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- service.Shutdown(t.Context()) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Shutdown did not cancel the admitted operation")
	}
	select {
	case err := <-shutdownDone:
		close(release)
		t.Fatalf("Shutdown returned before the operation exited: %v", err)
	default:
	}
	close(release)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if _, _, err := service.beginOperation(t.Context()); !errors.Is(err, runtimedisplay.ErrServiceStopped) {
		t.Fatalf("beginOperation() after Shutdown error = %v, want ErrServiceStopped", err)
	}
}

func TestServiceShutdownDeadlineAndTerminalErrorSurviveRetry(t *testing.T) {
	service := NewService(nil, nil)
	sess := newSession(service, "bot-1", "gst-launch-1.0", CodecVP8)
	client, server := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })
	closeErr := errors.New("close input failed")
	sess.input = &rfbInputClient{conn: &closeErrorConn{Conn: client, err: closeErr}}
	service.mu.Lock()
	service.workers[sess] = struct{}{}
	service.mu.Unlock()

	// Hold the same lock used by startup to prove Shutdown observes its own
	// context instead of synchronously blocking behind session teardown.
	sess.lifecycleMu.Lock()
	stopCtx, cancel := context.WithCancel(t.Context())
	cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- service.Shutdown(stopCtx) }()
	select {
	case err := <-shutdownDone:
		if !errors.Is(err, context.Canceled) {
			sess.lifecycleMu.Unlock()
			t.Fatalf("first Shutdown() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		sess.lifecycleMu.Unlock()
		t.Fatal("Shutdown blocked behind session lifecycle lock past its context")
	}
	sess.lifecycleMu.Unlock()

	select {
	case <-sess.done:
	case <-time.After(time.Second):
		t.Fatal("session did not finish after releasing lifecycle lock")
	}
	if err := service.Shutdown(t.Context()); !errors.Is(err, closeErr) {
		t.Fatalf("retry Shutdown() error = %v, want retained close error", err)
	}
	if err := service.Shutdown(t.Context()); !errors.Is(err, closeErr) {
		t.Fatalf("idempotent Shutdown() error = %v, want retained close error", err)
	}
}

func TestSessionRequestStopCancelsStartupBeforeLifecycleUnlock(t *testing.T) {
	service := NewService(nil, nil)
	sess := newSession(service, "bot-1", "gst-launch-1.0", CodecVP8)
	runCtx, cancelRun := newSessionRunContext(t.Context(), sess.ctx)
	defer cancelRun()

	// Model startup holding lifecycleMu in a blocking dial. requestStop must
	// still cancel the run context before teardown can acquire this lock.
	sess.lifecycleMu.Lock()
	sess.requestStop()
	canceled := false
	select {
	case <-runCtx.Done():
		canceled = true
	case <-time.After(time.Second):
	}
	sess.lifecycleMu.Unlock()
	if !canceled {
		t.Fatal("requestStop did not cancel startup while lifecycleMu was held")
	}
	select {
	case <-sess.done:
	case <-time.After(time.Second):
		t.Fatal("session teardown did not finish after lifecycleMu was released")
	}
}

func TestReadRTCSettings(t *testing.T) {
	t.Setenv(rtcUDPPortMinEnv, "30000")
	t.Setenv(rtcUDPPortMaxEnv, "30100")
	t.Setenv(rtcNATIPsEnv, "127.0.0.1, 10.0.0.10")

	cfg, err := readRTCSettings(nil)
	if err != nil {
		t.Fatalf("readRTCSettings returned error: %v", err)
	}
	if cfg.UDPPortMin != 30000 || cfg.UDPPortMax != 30100 {
		t.Fatalf("unexpected UDP range: %d-%d", cfg.UDPPortMin, cfg.UDPPortMax)
	}
	if len(cfg.NATIPs) != 2 || cfg.NATIPs[0] != "127.0.0.1" || cfg.NATIPs[1] != "10.0.0.10" {
		t.Fatalf("unexpected NAT IPs: %#v", cfg.NATIPs)
	}
}

func TestIsSocketReadyRequiresListener(t *testing.T) {
	path := filepath.Join(os.TempDir(), "memoh-display-ready-test.sock")
	_ = os.Remove(path)
	t.Cleanup(func() { _ = os.Remove(path) })
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	if !isSocketReady(context.Background(), path) {
		t.Fatal("expected active unix socket to be ready")
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if isSocketReady(context.Background(), path) {
		t.Fatal("closed unix socket file must not be reported ready")
	}
}

func TestReadRTCSettingsRejectsPartialPortRange(t *testing.T) {
	t.Setenv(rtcUDPPortMinEnv, "30000")

	if _, err := readRTCSettings(nil); err == nil {
		t.Fatal("expected partial port range to fail")
	}
}

func TestReadRTCSettingsRejectsInvalidNATIP(t *testing.T) {
	t.Setenv(rtcNATIPsEnv, "localhost")

	if _, err := readRTCSettings(nil); err == nil {
		t.Fatal("expected invalid NAT IP to fail")
	}
}

func TestReadRTCSettingsUsesInferredNATIPs(t *testing.T) {
	cfg, err := readRTCSettings([]string{"100.123.2.67", "10.0.0.2"})
	if err != nil {
		t.Fatalf("readRTCSettings returned error: %v", err)
	}
	if len(cfg.NATIPs) != 2 || cfg.NATIPs[0] != "100.123.2.67" || cfg.NATIPs[1] != "10.0.0.2" {
		t.Fatalf("unexpected inferred NAT IPs: %#v", cfg.NATIPs)
	}
}

func TestSessionReplacingPeerKeepsNewPeer(t *testing.T) {
	sess := newSession(NewService(nil, nil), "bot-1", "gst-launch-1.0", CodecVP8)
	first := &peerSession{id: "viewer-1", trackID: "track-1", state: "connected"}
	second := &peerSession{id: "viewer-1", trackID: "track-2", state: "new"}

	sess.addPeer(first)
	sess.addPeer(second)
	sess.removePeer(first)

	if got := sess.peer("viewer-1"); got != second {
		t.Fatal("closing a replaced peer must not remove the newer peer for the same display session")
	}
}

func TestSessionConcurrentPeerReplacementDoesNotOrphanPeer(t *testing.T) {
	sess := newSession(NewService(nil, nil), "bot-1", "gst-launch-1.0", CodecVP8)
	initial := &peerSession{id: "viewer-1"}
	sess.addPeer(initial)

	const replacementCount = 32
	all := make([]*peerSession, 0, replacementCount+1)
	all = append(all, initial)
	replaced := make(chan *peerSession, replacementCount)
	var wg sync.WaitGroup
	for range replacementCount {
		peer := &peerSession{id: "viewer-1"}
		all = append(all, peer)
		wg.Go(func() { replaced <- sess.replacePeer(peer) })
	}
	wg.Wait()
	close(replaced)

	current := sess.peer("viewer-1")
	if current == nil {
		t.Fatal("concurrent replacement removed the current peer")
	}
	seen := make(map[*peerSession]struct{}, replacementCount)
	for peer := range replaced {
		if peer == nil {
			t.Fatal("atomic replacement returned a nil previous peer")
		}
		if _, duplicate := seen[peer]; duplicate {
			t.Fatal("the same previous peer was displaced more than once")
		}
		seen[peer] = struct{}{}
	}
	if len(seen) != replacementCount {
		t.Fatalf("displaced peer count = %d, want %d", len(seen), replacementCount)
	}
	for _, peer := range all {
		_, displaced := seen[peer]
		if peer != current && !displaced {
			t.Fatal("peer was neither retained nor returned for cleanup")
		}
	}
}

func TestSessionCloseStalePeers(t *testing.T) {
	sess := newSession(NewService(nil, nil), "bot-1", "gst-launch-1.0", CodecVP8)
	fresh := &peerSession{id: "fresh", createdAt: time.Now(), state: webrtc.PeerConnectionStateConnecting.String()}
	staleConnecting := &peerSession{id: "stale-connecting", createdAt: time.Now().Add(-stalePeerTTL - time.Second), state: webrtc.PeerConnectionStateConnecting.String()}
	staleDisconnected := &peerSession{id: "stale-disconnected", createdAt: time.Now(), state: webrtc.PeerConnectionStateDisconnected.String()}
	staleClosed := make(map[string]bool)
	staleConnecting.close = func() {
		staleClosed[staleConnecting.id] = true
		sess.removePeer(staleConnecting)
	}
	staleDisconnected.close = func() {
		staleClosed[staleDisconnected.id] = true
		sess.removePeer(staleDisconnected)
	}

	sess.addPeer(fresh)
	sess.addPeer(staleConnecting)
	sess.addPeer(staleDisconnected)
	sess.closeStalePeers(time.Now())

	if sess.peer("fresh") == nil {
		t.Fatal("fresh connecting peer should remain active")
	}
	if sess.peer("stale-connecting") != nil {
		t.Fatal("old connecting peer should be removed")
	}
	if sess.peer("stale-disconnected") != nil {
		t.Fatal("disconnected peer should be removed")
	}
	if !staleClosed["stale-connecting"] || !staleClosed["stale-disconnected"] {
		t.Fatal("stale peers should be closed through their close callbacks")
	}
}

func TestSessionWaitsForInFlightStart(t *testing.T) {
	service := NewService(nil, nil)
	start := &sessionStart{done: make(chan struct{})}
	service.starting["bot-1"] = start
	expected := newSession(service, "bot-1", "gst-launch-1.0", CodecVP8)

	done := make(chan struct {
		sess *session
		err  error
	}, 1)
	go func() {
		sess, err := service.session(context.Background(), "bot-1", "gst-launch-1.0", CodecVP8)
		done <- struct {
			sess *session
			err  error
		}{sess: sess, err: err}
	}()

	select {
	case <-done:
		t.Fatal("session should wait for the in-flight encoder start")
	case <-time.After(20 * time.Millisecond):
	}

	service.finishSessionStart("bot-1", start, expected, nil)

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("session returned error: %v", result.err)
		}
		if result.sess != expected {
			t.Fatal("session should reuse the encoder from the in-flight start")
		}
	case <-time.After(time.Second):
		t.Fatal("session did not return after in-flight start completed")
	}
}

func TestGStreamerArgsH264UsesX264AndH264Pay(t *testing.T) {
	args := gstreamerArgs(CodecH264, 5901, 5004)
	if !containsString(args, "incremental=false") {
		t.Fatal("live rfbsrc must keep requesting full frames so late viewers receive a first frame on a static desktop")
	}
	if !containsString(args, "use-copyrect=true") {
		t.Fatal("live rfbsrc must allow copyrect updates")
	}
	if !containsString(args, "do-timestamp=true") {
		t.Fatal("rfbsrc buffers must be timestamped for RTP encoding")
	}
	if !containsString(args, "x264enc") {
		t.Fatal("H264 pipeline must use x264enc")
	}
	if !containsString(args, "rtph264pay") {
		t.Fatal("H264 pipeline must use rtph264pay")
	}
}

func TestGStreamerArgsVP8FallbackUsesVP8Pay(t *testing.T) {
	args := gstreamerArgs(CodecVP8, 5901, 5004)
	if !containsString(args, "vp8enc") {
		t.Fatal("VP8 pipeline must use vp8enc")
	}
	if !containsString(args, "rtpvp8pay") {
		t.Fatal("VP8 pipeline must use rtpvp8pay")
	}
}

func TestGStreamerScreenshotArgsCapturesComputerUseJPEG(t *testing.T) {
	args := gstreamerScreenshotArgs(5901, "/tmp/display.jpg")
	if !containsString(args, "num-buffers=1") {
		t.Fatal("screenshot pipeline must stop after one frame")
	}
	if !containsString(args, "videoscale") {
		t.Fatal("screenshot pipeline must scale in GStreamer")
	}
	if !containsString(args, "video/x-raw,width=1280,pixel-aspect-ratio=1/1") {
		t.Fatal("screenshot pipeline must capture a computer-use friendly desktop width without distorting aspect ratio")
	}
	if !containsString(args, "jpegenc") || !containsString(args, "quality=82") {
		t.Fatal("screenshot pipeline must encode bounded-size JPEG directly")
	}
	if !containsString(args, "location=/tmp/display.jpg") {
		t.Fatal("screenshot pipeline must write to requested path")
	}
	if !containsString(args, "incremental=false") {
		t.Fatal("screenshot pipeline must request a full frame")
	}
}

func TestLimitJPEGSizeRecompressesOversizedImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 800))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.Set(x, y, color.RGBA{
				R: uint8((x * 17) % 256),
				G: uint8((y * 31) % 256),
				B: uint8((x*y + y) % 256),
				A: 255,
			})
		}
	}

	var original bytes.Buffer
	if err := jpeg.Encode(&original, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode original jpeg: %v", err)
	}

	const maxBytes = 32 * 1024
	bounded, err := limitJPEGSize(original.Bytes(), maxBytes)
	if err != nil {
		t.Fatalf("limitJPEGSize returned error: %v", err)
	}
	if len(bounded) > maxBytes {
		t.Fatalf("bounded image is too large: %d > %d", len(bounded), maxBytes)
	}
	if _, err := jpeg.Decode(bytes.NewReader(bounded)); err != nil {
		t.Fatalf("bounded image must remain decodable JPEG: %v", err)
	}
}

func TestNegotiateCodecPrefersH264(t *testing.T) {
	// SDP fragment offering both H264 (PT 102) and VP8 (PT 96).
	offer := "v=0\r\n" +
		"o=- 0 0 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 102 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=rtpmap:102 H264/90000\r\n" +
		"a=rtpmap:96 VP8/90000\r\n"
	codec, err := negotiateCodec(offer, false)
	if err != nil {
		t.Fatalf("negotiateCodec returned error: %v", err)
	}
	if codec != CodecH264 {
		t.Fatalf("expected H264, got %s", codec)
	}
}

func TestNegotiateCodecFallsBackToVP8(t *testing.T) {
	offer := "v=0\r\n" +
		"o=- 0 0 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=rtpmap:96 VP8/90000\r\n"
	codec, err := negotiateCodec(offer, false)
	if err != nil {
		t.Fatalf("negotiateCodec returned error: %v", err)
	}
	if codec != CodecVP8 {
		t.Fatalf("expected VP8, got %s", codec)
	}
}

func TestNegotiateCodecForceVP8(t *testing.T) {
	offer := "v=0\r\n" +
		"o=- 0 0 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 102 96\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=rtpmap:102 H264/90000\r\n" +
		"a=rtpmap:96 VP8/90000\r\n"
	codec, err := negotiateCodec(offer, true)
	if err != nil {
		t.Fatalf("negotiateCodec returned error: %v", err)
	}
	if codec != CodecVP8 {
		t.Fatalf("expected forced VP8, got %s", codec)
	}
}

func TestNegotiateCodecForceVP8RejectsH264Only(t *testing.T) {
	offer := "v=0\r\n" +
		"o=- 0 0 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 102\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=rtpmap:102 H264/90000\r\n"
	if _, err := negotiateCodec(offer, true); err == nil {
		t.Fatal("force-VP8 must not silently fall back to H264")
	}
}

func TestNegotiateCodecNoMatch(t *testing.T) {
	offer := "v=0\r\n" +
		"o=- 0 0 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=video 9 UDP/TLS/RTP/SAVPF 100\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=rtpmap:100 AV1/90000\r\n"
	if _, err := negotiateCodec(offer, false); err == nil {
		t.Fatal("expected codec negotiation to fail without H264/VP8")
	}
}

func TestIsUsableExecutableAllowsWindowsFilesWithoutExecuteBits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gst-launch-1.0.exe")
	if err := os.WriteFile(path, []byte("fake exe"), 0o600); err != nil {
		t.Fatalf("write test executable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat test executable: %v", err)
	}
	if !isUsableExecutable(info, "windows") {
		t.Fatal("Windows absolute executable paths must not require Unix execute bits")
	}
	if isUsableExecutable(info, "linux") {
		t.Fatal("non-Windows executable paths must still require Unix execute bits")
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	if isUsableExecutable(dirInfo, "windows") {
		t.Fatal("directories must not be treated as Windows executables")
	}
}

func TestSessionRemoveTrackDefersEncoderStopUntilIdleHold(t *testing.T) {
	sess := newSession(NewService(nil, nil), "bot-1", "gst-launch-1.0", CodecVP8)
	trackID := "track-1"
	sess.addTrack(trackID, nil)

	sess.removeTrack(trackID)

	select {
	case <-sess.ctx.Done():
		t.Fatal("encoder must stay warm briefly after the last viewer disconnects")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRFBInputCloseInterruptsBlockedWrite(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })
	writeStarted := make(chan struct{})
	conn := &writeSignalConn{Conn: client, started: writeStarted}
	input := &rfbInputClient{conn: conn}

	writeDone := make(chan error, 1)
	go func() { writeDone <- input.Pointer(10, 20, 1) }()
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("pointer write did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- input.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind the in-flight RFB write")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("blocked write returned nil after its connection was closed")
		}
	case <-time.After(time.Second):
		t.Fatal("closing the RFB input did not interrupt the blocked write")
	}
}

type closeErrorConn struct {
	net.Conn
	err error
}

func (c *closeErrorConn) Close() error {
	_ = c.Conn.Close()
	return c.err
}

type writeSignalConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *writeSignalConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
