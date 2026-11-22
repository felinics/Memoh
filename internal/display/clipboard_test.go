package display

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type clipboardTestConn struct{ bytes.Buffer }

func (*clipboardTestConn) Close() error                     { return nil }
func (*clipboardTestConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*clipboardTestConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*clipboardTestConn) SetDeadline(time.Time) error      { return nil }
func (*clipboardTestConn) SetReadDeadline(time.Time) error  { return nil }
func (*clipboardTestConn) SetWriteDeadline(time.Time) error { return nil }

func clipboardPayload(flags uint32, data []byte) []byte {
	msg := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(msg, flags)
	copy(msg[4:], data)
	return msg
}

func providedClipboard(t *testing.T, text string) []byte {
	t.Helper()
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	if err := binary.Write(w, binary.BigEndian, uint32(len(text)+1)); err != nil { //nolint:gosec // Test inputs are bounded to four times maxClipboardBytes.
		t.Fatal(err)
	}
	if _, err := w.Write(append([]byte(text), 0)); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	// Real TigerVNC sends the Z_SYNC_FLUSH bytes before finishing the stream.
	data := append([]byte(nil), compressed.Bytes()...)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return clipboardPayload(clipboardProvide|clipboardText, data)
}

func TestExtendedClipboardNegotiationAndRemoteCopy(t *testing.T) {
	conn := &clipboardTestConn{}
	c := &rfbInputClient{conn: conn, clipboard: &rfbClipboard{}}
	_, _, err := c.handleExtendedClipboard(clipboardPayload(clipboardCaps|clipboardText|clipboardNotify|clipboardProvide|clipboardRequest, []byte{0, 0, 0, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.clipboard.extended {
		t.Fatal("UTF-8 clipboard not negotiated")
	}
	if got := binary.BigEndian.Uint32(conn.Bytes()[8:12]); got != clipboardCaps|clipboardText|clipboardRequest|clipboardNotify|clipboardProvide {
		t.Fatalf("caps: %x", got)
	}
	conn.Reset()
	_, _, err = c.handleExtendedClipboard(clipboardPayload(clipboardNotify|clipboardText, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint32(conn.Bytes()[8:12]); got != clipboardRequest|clipboardText {
		t.Fatalf("request: %x", got)
	}
	text, present, err := c.handleExtendedClipboard(providedClipboard(t, "你好 👋\r\nsecond line"))
	if err != nil || !present || text != "你好 👋\nsecond line" {
		t.Fatalf("copy: %q %v %v", text, present, err)
	}
}

func TestClipboardPasteAnnouncesBeforeShortcutAndProvidesUTF8(t *testing.T) {
	for _, shift := range []bool{false, true} {
		conn := &clipboardTestConn{}
		c := &rfbInputClient{conn: conn, clipboard: &rfbClipboard{extended: true}}
		if err := c.Paste("中文\nsecond line", shift); err != nil {
			t.Fatal(err)
		}
		wire := conn.Bytes()
		if wire[0] != 6 || binary.BigEndian.Uint32(wire[4:8]) != 0xfffffffc || binary.BigEndian.Uint32(wire[8:12]) != clipboardNotify|clipboardText {
			t.Fatalf("paste did not announce clipboard first: %x", wire)
		}
		keys := wire[12:]
		if len(keys)%8 != 0 {
			t.Fatalf("invalid key events: %x", keys)
		}
		var down []uint32
		for len(keys) > 0 {
			if keys[0] != 4 {
				t.Fatalf("unexpected message: %x", keys)
			}
			if keys[1] == 1 {
				down = append(down, binary.BigEndian.Uint32(keys[4:8]))
			}
			keys = keys[8:]
		}
		if shift && (len(down) != 3 || down[0] != 0xffe1) {
			t.Fatalf("missing Shift: %x", down)
		}
		if got := down[len(down)-2:]; got[0] != 0xffe3 || got[1] != 'v' {
			t.Fatalf("missing Ctrl+V: %x", down)
		}
		conn.Reset()
		_, _, err := c.handleExtendedClipboard(clipboardPayload(clipboardRequest|clipboardText, nil))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := zlib.NewReader(bytes.NewReader(conn.Bytes()[12:]))
		if err != nil {
			t.Fatal(err)
		}
		plain, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(plain[4:]) != "中文\r\nsecond line\x00" || int(binary.BigEndian.Uint32(plain)) != len(plain)-4 {
			t.Fatalf("bad UTF-8 provide: %q", plain)
		}
	}
}

func TestClipboardOwnerChangePublishesOnlyNewText(t *testing.T) {
	conn := &clipboardTestConn{}
	c := &rfbInputClient{conn: conn, clipboard: &rfbClipboard{extended: true}}
	// TigerVNC first invalidates the old owner, then announces and provides
	// the new clipboard. The invalidation must not complete a browser copy.
	var copied []string
	for _, data := range [][]byte{
		clipboardPayload(clipboardNotify, nil),
		clipboardPayload(clipboardNotify|clipboardText, nil),
		providedClipboard(t, "最新复制 中文"),
	} {
		text, present, err := c.handleExtendedClipboard(data)
		if err != nil {
			t.Fatal(err)
		}
		if present {
			copied = append(copied, text)
		}
	}
	if len(copied) != 1 || copied[0] != "最新复制 中文" {
		t.Fatalf("ownership notifications were treated as text: %q", copied)
	}
	// A real zero-length text transfer remains a valid clipboard value.
	text, present, err := c.handleExtendedClipboard(providedClipboard(t, ""))
	if err != nil || !present || text != "" {
		t.Fatalf("empty provided text: %q %v %v", text, present, err)
	}
}

func TestClipboardSyncSupportsApplicationPasteWithoutInjectingKeys(t *testing.T) {
	conn := &clipboardTestConn{}
	c := &rfbInputClient{conn: conn, clipboard: &rfbClipboard{extended: true}}
	s := &session{input: c}
	if err := s.handleInput([]byte(`{"type":"clipboard_sync","text":"本机文本"}`)); err != nil {
		t.Fatal(err)
	}
	// Synchronizing must not type or paste into whichever application happens
	// to have focus. Only advertise clipboard availability to the VNC server.
	if conn.Len() != 12 || conn.Bytes()[0] != 6 || binary.BigEndian.Uint32(conn.Bytes()[8:12]) != clipboardNotify|clipboardText {
		t.Fatalf("sync injected input: %x", conn.Bytes())
	}
	conn.Reset()
	// A remote menu Paste reads the advertised text through this request;
	// it does not need a simulated Control+V from Memoh.
	if _, _, err := c.handleExtendedClipboard(clipboardPayload(clipboardRequest|clipboardText, nil)); err != nil {
		t.Fatal(err)
	}
	reader, err := zlib.NewReader(bytes.NewReader(conn.Bytes()[12:]))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(plain[4:]) != "本机文本\x00" {
		t.Fatalf("remote application received %q: %v", plain, err)
	}
}

func TestClipboardBoundsAndValidation(t *testing.T) {
	c := &rfbInputClient{conn: &clipboardTestConn{}, clipboard: &rfbClipboard{extended: true}}
	// Newline expansion on the RFB wire must not reject a valid selection.
	text, present, err := c.handleExtendedClipboard(providedClipboard(t, strings.Repeat("\r\n", maxClipboardBytes)))
	if err != nil || !present || text != strings.Repeat("\n", maxClipboardBytes) {
		t.Fatalf("newline boundary: %v", err)
	}
	for _, invalid := range []string{strings.Repeat("x", maxClipboardBytes+1), "contains\x00NUL", string([]byte{0xff})} {
		if err := c.Paste(invalid, false); err == nil {
			t.Fatal("accepted invalid local text")
		}
		if _, _, err := c.handleExtendedClipboard(providedClipboard(t, invalid)); err == nil {
			t.Fatal("accepted invalid remote text")
		}
	}
	if _, _, err := c.handleExtendedClipboard(providedClipboard(t, strings.Repeat("x", 4*maxClipboardBytes))); err == nil {
		t.Fatal("accepted compressed oversized text")
	}
	for _, size := range []int32{maxClipboardMessageBytes + 1, -maxClipboardMessageBytes - 1, -2147483648} {
		var header [8]byte
		header[0] = 3
		binary.BigEndian.PutUint32(header[4:], uint32(size)) //nolint:gosec // G115: RFB encodes extended lengths as signed two's-complement uint32 values.
		if err := c.readClipboard(bytes.NewReader(header[:]), func(string) { t.Fatal("unexpected text") }, func(error) {}); !errors.Is(err, io.EOF) {
			t.Fatalf("oversized frame was not discarded without allocating: %v", err)
		}
	}
}

func TestRejectedClipboardDoesNotCloseInput(t *testing.T) {
	conn := &clipboardTestConn{}
	c := &rfbInputClient{conn: conn, clipboard: &rfbClipboard{extended: true}}
	var frames bytes.Buffer
	for _, payload := range [][]byte{providedClipboard(t, strings.Repeat("x", maxClipboardBytes+1)), providedClipboard(t, "valid 中文")} {
		frames.Write([]byte{3, 0, 0, 0})
		if err := binary.Write(&frames, binary.BigEndian, -int32(len(payload))); err != nil { //nolint:gosec // Payloads are constructed from bounded test strings.
			t.Fatal(err)
		}
		frames.Write(payload)
	}
	var copied []string
	rejected := 0
	err := c.readClipboard(&frames, func(text string) { copied = append(copied, text) }, func(error) { rejected++ })
	if !errors.Is(err, io.EOF) || rejected != 1 || len(copied) != 1 || copied[0] != "valid 中文" {
		t.Fatalf("reader did not recover: %v, %d, %q", err, rejected, copied)
	}
	if err := c.Key('a', true); err != nil {
		t.Fatalf("clipboard rejection disabled input: %v", err)
	}
}

func TestClipboardTransportFailureDisconnectsInsteadOfReportingCopyError(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	go func() {
		var encodings [8]byte
		_, _ = io.ReadFull(server, encodings[:])
		_ = server.Close()
	}()
	c := &rfbInputClient{conn: client}
	disconnected := make(chan error, 1)
	if err := c.startClipboard(func(string) { t.Error("unexpected clipboard text") }, func(error) { t.Error("transport closure reported as clipboard content error") }, func(err error) { disconnected <- err }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-disconnected:
		if err == nil {
			t.Fatal("viewer disconnected without the transport error")
		}
	case <-time.After(time.Second):
		t.Fatal("transport closure did not disconnect the viewer")
	}
	if err := c.Key('a', true); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("dead input connection remained usable: %v", err)
	}
}

func TestLegacyClipboardAndPublicError(t *testing.T) {
	conn := &clipboardTestConn{}
	c := &rfbInputClient{conn: conn, clipboard: &rfbClipboard{}}
	if err := c.Paste("café", false); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(conn.Bytes()[:12], []byte{6, 0, 0, 0, 0, 0, 0, 4, 'c', 'a', 'f', 0xe9}) {
		t.Fatal("legacy text is not Latin-1")
	}
	conn.Reset()
	if err := c.Paste("中文", false); err == nil || conn.Len() != 0 {
		t.Fatal("legacy encoding silently corrupted Unicode")
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(clipboardErrorMessage(t.Context(), nil)), &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "clipboard_error" || event["code"] != "workspace.display_clipboard_failed" {
		t.Fatalf("invalid public error: %v", event)
	}
}

type blockedInputTestConn struct {
	net.Conn
	started chan struct{}
}

func (c *blockedInputTestConn) Write(data []byte) (int, error) {
	close(c.started)
	return c.Conn.Write(data)
}

func TestSessionStopUnblocksInputWrite(t *testing.T) {
	client, target := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = target.Close() }()
	conn := &blockedInputTestConn{Conn: client, started: make(chan struct{})}
	sess := newSession(NewService(nil, nil), "bot", "gst", CodecVP8)
	sess.input = &rfbInputClient{conn: conn}
	writeDone := make(chan error, 1)
	go func() { writeDone <- sess.input.Key('a', true) }()
	// The unread pipe keeps Key inside Write while it holds the input mutex.
	<-conn.started
	stopDone := make(chan struct{})
	go func() { sess.stop(); close(stopDone) }()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("session stop waited for blocked input instead of closing it")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("blocked input write succeeded after session stop")
		}
	case <-time.After(time.Second):
		t.Fatal("session stop left an input write blocked")
	}
	if err := sess.input.Key('a', true); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("input accepted a key after stop: %v", err)
	}
	if err := sess.input.Close(); err != nil {
		t.Fatalf("repeated input close: %v", err)
	}
}

func TestClipboardTooLargePublicErrorAndRecovery(t *testing.T) {
	c := &rfbInputClient{conn: &clipboardTestConn{}, clipboard: &rfbClipboard{extended: true}}
	_, _, cause := c.handleExtendedClipboard(providedClipboard(t, strings.Repeat("x", maxClipboardBytes+1)))
	if !errors.Is(cause, errClipboardTooLarge) {
		t.Fatalf("size rejection: %v", cause)
	}
	var frame struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	wire := clipboardErrorMessage(t.Context(), cause)
	if err := json.Unmarshal([]byte(wire), &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Code != "workspace.display_clipboard_too_large" || frame.Type != "clipboard_error" {
		t.Fatalf("wrong public error: %s", wire)
	}
	text, present, err := c.handleExtendedClipboard(providedClipboard(t, "small copy"))
	if err != nil || !present || text != "small copy" {
		t.Fatalf("copy did not recover: %q %v %v", text, present, err)
	}
	wire = clipboardErrorMessage(t.Context(), errors.New("PRIVATE diagnostic"))
	if strings.Contains(wire, "PRIVATE") || strings.Contains(wire, "too_large") {
		t.Fatalf("invalid error classification: %s", wire)
	}
}

func TestLegacyClipboardUTF8SizeRejectionKeepsFollowingCopy(t *testing.T) {
	c := &rfbInputClient{conn: &clipboardTestConn{}, clipboard: &rfbClipboard{}}
	var wire bytes.Buffer
	// Latin-1 bytes expand to two bytes each when represented as UTF-8.
	for _, text := range [][]byte{bytes.Repeat([]byte{0xe9}, maxClipboardBytes/2+1), []byte("next copy")} {
		wire.Write([]byte{3, 0, 0, 0})
		if err := binary.Write(&wire, binary.BigEndian, uint32(len(text))); err != nil { //nolint:gosec // Bounded test frames.
			t.Fatal(err)
		}
		wire.Write(text)
	}
	var copied []string
	var rejected []error
	err := c.readClipboard(&wire, func(text string) { copied = append(copied, text) }, func(err error) { rejected = append(rejected, err) })
	if !errors.Is(err, io.EOF) || len(rejected) != 1 || !errors.Is(rejected[0], errClipboardTooLarge) || len(copied) != 1 || copied[0] != "next copy" {
		t.Fatalf("legacy reader: %v, rejected=%v, copied=%q", err, rejected, copied)
	}
}
