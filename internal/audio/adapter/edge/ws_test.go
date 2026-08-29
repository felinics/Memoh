package edge

import (
	"bytes"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/audio"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// mockEdgeTTSHandler mock Edge TTS server: receive speech.config and ssml, replay turn.start, response, binary audio, turn.end.
func mockEdgeTTSHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("mock edge tts upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		// 1) expect the first message to be speech.config
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Logf("mock read 1: %v", err)
			return
		}
		if !bytes.Contains(data, []byte("Path: speech.config")) && !bytes.Contains(data, []byte("speech.config")) {
			t.Logf("mock expected speech.config, got: %s", string(data))
			return
		}

		// 2) expect the second message to be ssml
		_, data, err = conn.ReadMessage()
		if err != nil {
			t.Logf("mock read 2: %v", err)
			return
		}
		if !bytes.Contains(data, []byte("<speak")) && !bytes.Contains(data, []byte("Path: ssml")) {
			t.Logf("mock expected ssml, got: %s", string(data))
			return
		}

		// 3) send turn.start
		turnStart := "Path: turn.start\r\nContent-Type: application/json; charset=utf-8\r\n\r\n{\"context\":{\"serviceTag\":\"mock\"}}"
		if err := conn.WriteMessage(websocket.TextMessage, []byte(turnStart)); err != nil {
			t.Logf("mock write turn.start: %v", err)
			return
		}
		// 4) send response
		resp := "Path: response\r\nContent-Type: application/json; charset=utf-8\r\n\r\n{\"context\":{},\"audio\":{\"type\":\"inline\",\"streamId\":\"mock-stream\"}}"
		if err := conn.WriteMessage(websocket.TextMessage, []byte(resp)); err != nil {
			t.Logf("mock write response: %v", err)
			return
		}
		// 5) send binary audio frame: 2 bytes header length (Big Endian) + header + audio
		header := []byte("Path: audio\r\nX-RequestId: mock\r\nContent-Type: audio/webm; codec=opus\r\n\r\n")
		audioPayload := []byte("fake-webm-audio-data")
		buf := make([]byte, 2+len(header)+len(audioPayload))
		if len(header) > math.MaxUint16 {
			t.Logf("header too large: %d > %d", len(header), math.MaxUint16)
			return
		}
		binary.BigEndian.PutUint16(buf[:2], uint16(len(header))) //nolint:gosec // Bounded by MaxUint16 check above.
		copy(buf[2:], header)
		copy(buf[2+len(header):], audioPayload)
		if err := conn.WriteMessage(websocket.BinaryMessage, buf); err != nil {
			t.Logf("mock write audio: %v", err)
			return
		}
		// 6) send turn.end
		turnEnd := "Path: turn.end\r\nContent-Type: application/json; charset=utf-8\r\n\r\n{}"
		if err := conn.WriteMessage(websocket.TextMessage, []byte(turnEnd)); err != nil {
			t.Logf("mock write turn.end: %v", err)
			return
		}
	}
}

func TestEdgeWsClient_ConnectAndSynthesize(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(mockEdgeTTSHandler(t))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/edge/v1"
	client := NewEdgeWsClient()
	client.BaseURL = wsURL

	config := audio.AudioConfig{Voice: audio.VoiceConfig{ID: "en-US-JennyNeural", Lang: "en-US"}, Speed: 1.0}
	audio, err := client.Synthesize(t.Context(), "Hello world", config)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if !bytes.Equal(audio, []byte("fake-webm-audio-data")) {
		t.Errorf("audio = %q, want fake-webm-audio-data", string(audio))
	}
}

func TestEdgeWsClient_Stream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(mockEdgeTTSHandler(t))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/edge/v1"
	client := NewEdgeWsClient()
	client.BaseURL = wsURL

	config := audio.AudioConfig{Voice: audio.VoiceConfig{ID: "en-US-JennyNeural", Lang: "en-US"}}
	ch, errCh := client.Stream(t.Context(), "Hi", config)
	var chunks [][]byte
	for b := range ch {
		chunks = append(chunks, b)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Stream errCh: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !bytes.Equal(chunks[0], []byte("fake-webm-audio-data")) {
		t.Errorf("chunk = %q", chunks[0])
	}
}

func TestParsePath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"turn.start", []byte("Path: turn.start\r\nContent-Type: application/json\r\n\r\n{}"), "turn.start"},
		{"turn.end", []byte("Path: turn.end\r\n\r\n"), "turn.end"},
		{"response", []byte("X-RequestId: x\r\nPath: response\r\n\r\n"), "response"},
		{"no path", []byte("Content-Type: text/plain\r\n\r\n"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePath(tc.data)
			if got != tc.want {
				t.Errorf("parsePath(...) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseAudioChunk(t *testing.T) {
	t.Parallel()
	header := []byte("Path: audio\r\n\r\n")
	audio := []byte("xyz")
	buf := make([]byte, 2+len(header)+len(audio))
	if len(header) > math.MaxUint16 {
		t.Logf("header too large: %d > %d", len(header), math.MaxUint16)
		return
	}
	binary.BigEndian.PutUint16(buf[:2], uint16(len(header))) //nolint:gosec // Bounded by MaxUint16 check above.
	copy(buf[2:], header)
	copy(buf[2+len(header):], audio)

	got, err := parseAudioChunk(buf)
	if err != nil {
		t.Fatalf("parseAudioChunk: %v", err)
	}
	if !bytes.Equal(got, audio) {
		t.Errorf("got %q, want %q", got, audio)
	}
}

func TestParseAudioChunk_EmptyOrShort(t *testing.T) {
	t.Parallel()
	// no data
	got, err := parseAudioChunk(nil)
	if err != nil {
		t.Errorf("nil: want nil err, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("nil: got %d bytes", len(got))
	}
	// only 2 bytes and headerLen=0, no header and no audio
	got, err = parseAudioChunk([]byte{0x00, 0x00})
	if err != nil {
		t.Fatalf("short: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("short: got %d bytes", len(got))
	}
}

func TestBuildSSML(t *testing.T) {
	t.Parallel()
	ssml := buildSSML("Hello", audio.VoiceConfig{ID: "zh-CN-XiaoxiaoNeural", Lang: "zh-CN"}, 1.0, 0)
	if !strings.Contains(ssml, "zh-CN-XiaoxiaoNeural") {
		t.Errorf("ssml should contain voice: %s", ssml)
	}
	if !strings.Contains(ssml, "Hello") {
		t.Errorf("ssml should contain text: %s", ssml)
	}
	if !strings.Contains(ssml, "<speak") {
		t.Errorf("ssml should be valid SSML: %s", ssml)
	}
}

func TestEscapeSSML(t *testing.T) {
	t.Parallel()
	if got := escapeSSML("a & b"); got != "a &amp; b" {
		t.Errorf("escapeSSML(&) = %q", got)
	}
	if got := escapeSSML("<tag>"); got != "&lt;tag&gt;" {
		t.Errorf("escapeSSML(<>) = %q", got)
	}
}
