package edge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/audio"
)

// Edge TTS WebSocket client.
// Reference: https://github.com/readest/readest/blob/main/apps/readest-app/src/libs/edgeTTS.ts
//
// Protocol flow:
//
//	Client  ── Establish Connection ──────────>  Edge TTS Server
//	Client  ── speech.config (JSON) ─────────>  Edge TTS Server
//	Client  ── ssml (XML) ───────────────────>  Edge TTS Server
//	Client  <─ turn.start / response (Text) ──  Edge TTS Server
//	Client  <─ Audio binary frames ───────────  Edge TTS Server
//	Client  <─ turn.end (Text) ───────────────  Edge TTS Server
//
// Important implementation notes:
//
//   - TLS must negotiate HTTP/1.1 (NextProtos: ["http/1.1"]); the server returns 404 on HTTP/2.
//   - Do NOT set Sec-WebSocket-Version in headers; gorilla/websocket adds it automatically
//     and a duplicate causes "duplicate header not allowed" errors.
//   - ConnectionId (URL param) and X-RequestId (SSML header) MUST be the same 32-hex value.
//   - Each synthesis uses a one-shot connection: connect → config → SSML → audio → turn.end → close.
//     The server closes the WebSocket after turn.end, so connections cannot be reused.
//   - Binary audio frames use big-endian for the 2-byte header-length prefix.
//     JavaScript's DataView.getInt16() defaults to big-endian; using little-endian silently
//     produces wrong offsets, causing all audio chunks to be discarded.
//   - speech.config message must include an X-Timestamp header.
//   - SSML must declare xmlns:mstts and set xml:lang from the voice name.

type EdgeWsClient struct {
	conn         *websocket.Conn
	connID       string
	mu           sync.Mutex
	outputFormat string // like audio-24khz-48kbitrate-mono-mp3
	BaseURL      string // for mock, empty will use EDGE_SPEECH_URL
}

func NewEdgeWsClient() *EdgeWsClient {
	return &EdgeWsClient{
		outputFormat: "audio-24khz-48kbitrate-mono-mp3",
	}
}

// Generate Sec-MS-GEC
// @see https://github.com/readest/readest/blob/main/apps/readest-app/src/libs/edgeTTS.ts#L208
func generateSecMSGec() string {
	ticks := time.Now().Unix() + WIN_EPOCH_OFFSET
	ticks -= ticks % 300
	ticks100ns := ticks * (S_TO_NS / 100)
	strToHash := fmt.Sprintf("%d%s", ticks100ns, EDGE_API_TOKEN)
	sum := sha256.Sum256([]byte(strToHash))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// generateMuid  MUID，for Cookie.
func generateMuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

func (c *EdgeWsClient) buildWsURL() string {
	base := EDGE_SPEECH_URL
	if c.BaseURL != "" {
		base = c.BaseURL
	}
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("TrustedClientToken", EDGE_API_TOKEN)
	q.Set("Sec-MS-GEC", generateSecMSGec())
	q.Set("Sec-MS-GEC-Version", "1-"+CHROMIUM_FULL_VERSION)
	q.Set("ConnectionId", c.connID)
	u.RawQuery = q.Encode()
	return u.String()
}

func buildWSSHeaders() http.Header {
	h := http.Header{}
	h.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"+
			" (KHTML, like Gecko) Chrome/"+CHROMIUM_MAJOR_VERSION+".0.0.0 Safari/537.36"+
			" Edg/"+CHROMIUM_MAJOR_VERSION+".0.0.0")
	h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Pragma", "no-cache")
	h.Set("Cache-Control", "no-cache")
	h.Set("Origin", WSSOrigin)
	h.Set("Cookie", "muid="+generateMuid()+";")
	return h
}

// Connect establishes a new WebSocket connection to Edge TTS.
// Each call generates a fresh connID (matching readest's one-connection-per-request model).
// If already connected, this is a no-op; call Close first to force a reconnect.
func (c *EdgeWsClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	c.connID = strings.ReplaceAll(uuid.New().String(), "-", "")

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
	d := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		TLSClientConfig:  tlsConfig,
		HandshakeTimeout: 15 * time.Second,
	}
	wsURL := c.buildWsURL()
	reqHeader := buildWSSHeaders()
	conn, resp, err := d.DialContext(ctx, wsURL, reqHeader)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return fmt.Errorf("edge tts ws dial: %w (status=%s body=%s)", err, resp.Status, string(bytes.TrimSpace(body)))
		}
		return fmt.Errorf("edge tts ws dial: %w", err)
	}
	c.conn = conn
	return nil
}

// Close closes the WebSocket connection.
func (c *EdgeWsClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resetLocked()
}

// resetLocked closes the current connection and clears state. Caller must hold c.mu.
func (c *EdgeWsClient) resetLocked() error {
	conn := c.conn
	c.conn = nil
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// sendFrame send a text frame with Path header (Edge protocol: header + empty line + body).
func (c *EdgeWsClient) sendFrame(path, contentType, body string, extraHeaders map[string]string) error {
	var b strings.Builder
	b.WriteString("Path: ")
	b.WriteString(path)
	b.WriteString("\r\n")
	b.WriteString("Content-Type: ")
	b.WriteString(contentType)
	b.WriteString("\r\n")
	for k, v := range extraHeaders {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(body)
	return c.conn.WriteMessage(websocket.TextMessage, []byte(b.String()))
}

// Configure sends the speech.config message (output format, etc.).
func (c *EdgeWsClient) Configure(ctx context.Context, config audio.AudioConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("edge tts: not connected")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(deadline)
		defer func() { _ = c.conn.SetWriteDeadline(time.Time{}) }()
	}
	format := c.outputFormat
	if config.Format != "" {
		format = config.Format
	}
	// like readest: outputFormat + boolean is JSON false
	body := fmt.Sprintf(`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":false,"wordBoundaryEnabled":true},"outputFormat":"%s"}}}}`, format)
	extra := map[string]string{
		"X-Timestamp": time.Now().String(),
	}
	return c.sendFrame("speech.config", "application/json; charset=utf-8", body, extra)
}

// buildSSML builds SSML with rate and pitch for Edge TTS prosody.
func buildSSML(text string, voice audio.VoiceConfig, speed, pitch float64) string {
	voiceID := voice.ID
	if voiceID == "" {
		voiceID = DEFAULT_VOICE
	}
	lang := voice.Lang
	if lang == "" {
		lang = "en-US"
	}

	rate := 0
	if speed > 0 {
		rate = int((speed - 1) * 100)
	}
	rateStr := fmt.Sprintf("%+d%%", rate)
	pitchStr := fmt.Sprintf("%+dHz", int(pitch))

	return fmt.Sprintf(
		`<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xmlns:mstts="https://www.w3.org/2001/mstts" xml:lang="%s">`+
			`<voice name="%s"><prosody rate="%s" pitch="%s">%s</prosody></voice></speak>`,
		lang, voiceID, rateStr, pitchStr, escapeSSML(text))
}

func escapeSSML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// Synthesize sends SSML and synchronously collects all audio data.
// It handles the full lifecycle: connect → configure → send → receive → close.
func (c *EdgeWsClient) Synthesize(ctx context.Context, text string, config audio.AudioConfig) ([]byte, error) {
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	if err := c.Configure(ctx, config); err != nil {
		return nil, err
	}

	c.mu.Lock()
	conn := c.conn
	connID := c.connID
	c.mu.Unlock()
	if conn == nil {
		return nil, errors.New("edge tts: not connected")
	}

	ssml := buildSSML(text, config.Voice, config.Speed, config.Pitch)
	extra := map[string]string{
		"X-RequestId": connID,
		"X-Timestamp": time.Now().String(),
	}
	if err := c.sendFrame("ssml", "application/ssml+xml", ssml, extra); err != nil {
		return nil, err
	}

	var out []byte
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		mt, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				if len(out) > 0 {
					c.mu.Lock()
					_ = c.resetLocked()
					c.mu.Unlock()
					return out, nil
				}
			}
			c.mu.Lock()
			_ = c.resetLocked()
			c.mu.Unlock()
			return nil, fmt.Errorf("edge tts read (format=%q voice=%q): %w", config.Format, config.Voice.ID, err)
		}
		switch mt {
		case websocket.TextMessage:
			if parsePath(data) == "turn.end" {
				c.mu.Lock()
				_ = c.resetLocked()
				c.mu.Unlock()
				return out, nil
			}
		case websocket.BinaryMessage:
			audio, err := parseAudioChunk(data)
			if err != nil {
				return nil, err
			}
			if len(audio) > 0 {
				out = append(out, audio...)
			}
		}
	}
}

// parsePath parse Path header from Edge text frame.
func parsePath(data []byte) string {
	idx := bytes.Index(data, []byte("Path:"))
	if idx < 0 {
		return ""
	}
	lineEnd := bytes.Index(data[idx:], []byte("\r\n"))
	if lineEnd < 0 {
		lineEnd = len(data) - idx
	}
	pathLine := data[idx+5 : idx+lineEnd]
	return strings.TrimSpace(string(pathLine))
}

// parseAudioChunk parse Edge binary audio frame: the first 2 bytes are the header length (big endian), followed by the header text, and then the audio data.
// if the data is too short or the header length is invalid, return nil, nil, not considered an error.
func parseAudioChunk(data []byte) ([]byte, error) {
	if len(data) < 2 {
		return nil, nil
	}
	headerLen := binary.BigEndian.Uint16(data[:2])
	audioStart := 2 + int(headerLen)
	if audioStart > len(data) {
		return nil, nil
	}
	return data[audioStart:], nil
}

// Stream sends SSML and returns audio chunks via channel.
// It handles the full lifecycle: connect → configure → send → stream → close.
func (c *EdgeWsClient) Stream(ctx context.Context, text string, config audio.AudioConfig) (ch chan []byte, errCh chan error) {
	ch = make(chan []byte, 8)
	errCh = make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)

		if err := c.Connect(ctx); err != nil {
			errCh <- err
			return
		}
		if err := c.Configure(ctx, config); err != nil {
			errCh <- err
			return
		}

		c.mu.Lock()
		conn := c.conn
		connID := c.connID
		c.mu.Unlock()
		if conn == nil {
			errCh <- errors.New("edge tts: not connected")
			return
		}

		ssml := buildSSML(text, config.Voice, config.Speed, config.Pitch)
		extra := map[string]string{
			"X-RequestId": connID,
			"X-Timestamp": time.Now().String(),
		}
		if err := c.sendFrame("ssml", "application/ssml+xml", ssml, extra); err != nil {
			errCh <- err
			return
		}

		for {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}
			mt, data, err := conn.ReadMessage()
			if err != nil {
				errCh <- fmt.Errorf("edge tts read: %w", err)
				return
			}
			switch mt {
			case websocket.TextMessage:
				if parsePath(data) == "turn.end" {
					c.mu.Lock()
					_ = c.resetLocked()
					c.mu.Unlock()
					return
				}
			case websocket.BinaryMessage:
				audio, err := parseAudioChunk(data)
				if err != nil {
					errCh <- err
					return
				}
				if len(audio) > 0 {
					select {
					case ch <- audio:
					case <-ctx.Done():
						errCh <- ctx.Err()
						return
					}
				}
			}
		}
	}()
	return ch, errCh
}
