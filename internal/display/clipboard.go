package display

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/pion/webrtc/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/server"
)

const (
	maxClipboardBytes        = 48 * 1024
	maxClipboardMessageBytes = 64 * 1024
	clipboardText            = uint32(1)
	clipboardCaps            = uint32(1 << 24)
	clipboardRequest         = uint32(1 << 25)
	clipboardPeek            = uint32(1 << 26)
	clipboardNotify          = uint32(1 << 27)
	clipboardProvide         = uint32(1 << 28)
)

var errClipboardTooLarge = errors.New("display clipboard text too large")

// State is guarded by rfbInputClient.mu, along with writes to the RFB stream.
type rfbClipboard struct {
	extended bool
	text     string
	hasText  bool
}

func (c *rfbInputClient) startClipboard(onText func(string), onError func(error), onDisconnect func(error)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clipboard = &rfbClipboard{}
	// SetEncodings: request clipboard extensions, without requesting any
	// framebuffer updates on this input-only connection.
	if _, err := c.conn.Write([]byte{2, 0, 0, 1, 0xc0, 0xa1, 0xe5, 0xce}); err != nil {
		return err
	}
	conn := c.conn
	go func() {
		err := c.readClipboard(conn, onText, onError)
		// Close interrupts any concurrent input write before taking its lock.
		_ = c.Close()
		onDisconnect(err)
	}()
	return nil
}

// readClipboard reads server messages until the connection fails; the error
// it returns is never nil.
func (c *rfbInputClient) readClipboard(reader io.Reader, onText func(string), onRejected func(error)) error {
	for {
		var kind [1]byte
		if _, err := io.ReadFull(reader, kind[:]); err != nil {
			return err
		}
		switch kind[0] {
		case 2: // Bell
			continue
		case 1: // SetColourMapEntries
			var header [5]byte
			if _, err := io.ReadFull(reader, header[:]); err != nil {
				return err
			}
			if _, err := io.CopyN(io.Discard, reader, int64(binary.BigEndian.Uint16(header[3:]))*6); err != nil {
				return err
			}
		case 3: // ServerCutText
			var header [7]byte
			if _, err := io.ReadFull(reader, header[:]); err != nil {
				return err
			}
			signed := int32(binary.BigEndian.Uint32(header[3:])) //nolint:gosec // RFB encodes extended lengths as signed two's-complement uint32 values.
			size := int64(signed)
			if size < 0 {
				size = -size
			}
			if size > maxClipboardMessageBytes {
				if _, err := io.CopyN(io.Discard, reader, size); err != nil {
					return err
				}
				onRejected(errClipboardTooLarge)
				continue
			}
			data := make([]byte, int(size))
			if _, err := io.ReadFull(reader, data); err != nil {
				return err
			}
			if signed >= 0 {
				runes := make([]rune, len(data))
				for i, b := range data {
					runes[i] = rune(b)
				}
				text := string(runes)
				if len(text) > maxClipboardBytes {
					onRejected(errClipboardTooLarge)
					continue
				}
				c.mu.Lock()
				c.clipboard.hasText = false
				c.mu.Unlock()
				onText(text)
				continue
			}
			text, present, err := c.handleExtendedClipboard(data)
			if err != nil {
				// This cut-text frame is already consumed. Reject just this copy;
				// clipboard data must never disable pointer and keyboard input.
				onRejected(err)
				continue
			}
			if present {
				onText(text)
			}
		default:
			return fmt.Errorf("unexpected input-only RFB message %d", kind[0])
		}
	}
}

func (c *rfbInputClient) handleExtendedClipboard(data []byte) (string, bool, error) {
	if len(data) < 4 {
		return "", false, errors.New("invalid clipboard header")
	}
	flags := binary.BigEndian.Uint32(data)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.conn == nil {
		return "", false, io.ErrClosedPipe
	}
	if flags&clipboardCaps != 0 {
		c.clipboard.extended = flags&clipboardText != 0
		// Zero unsolicited size: ask explicitly for text after notifications.
		return "", false, c.writeExtendedClipboard(clipboardCaps|clipboardText|clipboardRequest|clipboardNotify|clipboardProvide, make([]byte, 4))
	}
	switch flags & 0xff000000 {
	case clipboardNotify:
		c.clipboard.hasText = false
		if flags&clipboardText != 0 {
			return "", false, c.writeExtendedClipboard(clipboardRequest|clipboardText, nil)
		}
		// Notify with no formats invalidates the previous selection. TigerVNC
		// sends it before announcing the new owner during an ordinary copy.
		// It is not an empty text value; only Provide can deliver that value.
		return "", false, nil
	case clipboardRequest:
		if flags&clipboardText != 0 && c.clipboard.hasText {
			return "", false, c.provideClipboard()
		}
	case clipboardPeek:
		available := uint32(0)
		if c.clipboard.hasText {
			available = clipboardText
		}
		return "", false, c.writeExtendedClipboard(clipboardNotify|available, nil)
	case clipboardProvide:
		if flags&0xffff != clipboardText {
			return "", false, errors.New("unsupported clipboard format")
		}
		reader, err := zlib.NewReader(bytes.NewReader(data[4:]))
		if err != nil {
			return "", false, err
		}
		defer func() { _ = reader.Close() }()
		// TigerVNC uses Z_SYNC_FLUSH, so the stream need not have a zlib EOF.
		// Read the declared format size rather than waiting for stream EOF.
		var header [4]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return "", false, err
		}
		size := binary.BigEndian.Uint32(header[:])
		// CRLF can use twice as many bytes as the normalized text. Bound the
		// allocation before decompressing to also reject compression bombs.
		if size > 2*maxClipboardBytes+1 {
			return "", false, errClipboardTooLarge
		}
		if size < 1 {
			return "", false, errors.New("invalid clipboard text size")
		}
		plain := make([]byte, int(size))
		if _, err := io.ReadFull(reader, plain); err != nil {
			return "", false, err
		}
		if plain[len(plain)-1] != 0 || !utf8.Valid(plain[:len(plain)-1]) {
			return "", false, errors.New("invalid UTF-8 clipboard text")
		}
		text := strings.ReplaceAll(string(plain[:len(plain)-1]), "\r\n", "\n")
		if len(text) > maxClipboardBytes {
			return "", false, errClipboardTooLarge
		}
		if strings.ContainsRune(text, 0) {
			return "", false, errors.New("invalid clipboard text")
		}
		c.clipboard.hasText = false
		return text, true, nil
	}
	return "", false, nil
}

// writeExtendedClipboard and provideClipboard require c.mu.
func (c *rfbInputClient) writeExtendedClipboard(flags uint32, data []byte) error {
	if len(data) > maxClipboardMessageBytes-4 {
		return errors.New("RFB clipboard message too large")
	}
	msg := make([]byte, 12+len(data))
	msg[0] = 6
	binary.BigEndian.PutUint32(msg[4:8], uint32(-int32(4+len(data)))) //nolint:gosec // Bounded above; negative length is required by the RFB extension.
	binary.BigEndian.PutUint32(msg[8:12], flags)
	copy(msg[12:], data)
	_, err := c.conn.Write(msg)
	return err
}

func (c *rfbInputClient) provideClipboard() error {
	text := strings.ReplaceAll(strings.ReplaceAll(c.clipboard.text, "\r\n", "\n"), "\n", "\r\n")
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(text)+1)) //nolint:gosec // text is bounded by maxClipboardBytes.
	_, _ = writer.Write(size[:])
	_, _ = writer.Write([]byte(text))
	_, _ = writer.Write([]byte{0})
	if err := writer.Close(); err != nil {
		return err
	}
	return c.writeExtendedClipboard(clipboardProvide|clipboardText, compressed.Bytes())
}

func validateClipboardText(text string) error {
	if len(text) > maxClipboardBytes || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return errors.New("invalid clipboard text size or encoding")
	}
	return nil
}

func (c *rfbInputClient) SetClipboardText(text string) error {
	if err := validateClipboardText(text); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setClipboardLocked(text)
}

func (c *rfbInputClient) setClipboardLocked(text string) error {
	if c.closed || c.conn == nil || c.clipboard == nil {
		return errors.New("clipboard unavailable")
	}
	if c.clipboard.extended {
		c.clipboard.text, c.clipboard.hasText = text, true
		// The server requests the UTF-8 text when the remote application reads
		// its selection. Notify before pressing Ctrl+V to avoid a stale paste.
		if err := c.writeExtendedClipboard(clipboardNotify|clipboardText, nil); err != nil {
			return err
		}
	} else {
		var latin1 []byte
		for _, r := range text {
			if r > 255 {
				return errors.New("VNC server does not support UTF-8 clipboard")
			}
			latin1 = append(latin1, byte(r)) //nolint:gosec // G115: at most 255 here, and a rune ranged from a string is never negative.
		}
		msg := make([]byte, 8+len(latin1))
		msg[0] = 6
		binary.BigEndian.PutUint32(msg[4:8], uint32(len(latin1))) //nolint:gosec // bounded above.
		copy(msg[8:], latin1)
		if _, err := c.conn.Write(msg); err != nil {
			return err
		}
	}
	return nil
}

func (c *rfbInputClient) Paste(text string, shift bool) error {
	if err := validateClipboardText(text); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.setClipboardLocked(text); err != nil {
		return err
	}
	// Release host modifiers (including macOS Command), then perform a Linux
	// paste atomically with respect to other input writes.
	for _, key := range []uint32{0xffeb, 0xffe9, 0xffe1, 0xffe3} {
		if err := c.keyLocked(key, false); err != nil {
			return err
		}
	}
	if shift {
		if err := c.keyLocked(0xffe1, true); err != nil {
			return err
		}
	}
	for _, event := range []struct {
		key  uint32
		down bool
	}{{0xffe3, true}, {'v', true}, {'v', false}, {0xffe3, false}} {
		if err := c.keyLocked(event.key, event.down); err != nil {
			return err
		}
	}
	if shift {
		return c.keyLocked(0xffe1, false)
	}
	return nil
}

func (s *session) publishClipboard(text string) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"clipboard", text}); err != nil {
		return
	}
	data := buffer.String()
	if len(data) > maxClipboardMessageBytes {
		data = clipboardErrorMessage(s.ctx, errClipboardTooLarge)
	}
	s.broadcastClipboard(data)
}

func (s *session) broadcastClipboard(data string) {
	s.peersMu.Lock()
	channels := make([]*webrtc.DataChannel, 0, len(s.clipboardChannels))
	for dc := range s.clipboardChannels {
		channels = append(channels, dc)
	}
	s.peersMu.Unlock()
	for _, dc := range channels {
		_ = dc.SendText(data)
	}
}

func (s *session) attachClipboardChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		s.peersMu.Lock()
		s.clipboardChannels[dc] = struct{}{}
		s.peersMu.Unlock()
	})
	dc.OnClose(func() {
		s.peersMu.Lock()
		delete(s.clipboardChannels, dc)
		s.peersMu.Unlock()
	})
}

func clipboardErrorMessage(ctx context.Context, cause error) string {
	code := apperror.CodeWorkspaceDisplayClipboardFailed
	if errors.Is(cause, errClipboardTooLarge) {
		code = apperror.CodeWorkspaceDisplayClipboardTooLarge
	}
	frame, _ := server.NewStreamError(ctx, apperror.New(code, nil), "")
	frame.Type = "clipboard_error"
	data, _ := json.Marshal(frame)
	return string(data)
}
