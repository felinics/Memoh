package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/workspace/bridge"
)

const defaultReadMediaMaxBytes = 20 * 1024 * 1024

// ReadMediaToolName is the tool name that the agent decoration layer matches
// on to intercept image payloads. After the merge this is "read".
func ReadMediaToolName() ToolName {
	return ToolRead()
}

var readMediaSupportedMimeTypes = map[string]struct{}{
	"image/gif":  {},
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
	// PDFs ride the same pipeline but surface as FileBase64 for native
	// provider document input instead of ImageBase64.
	"application/pdf": {},
}

// readMediaFileMaxBytes caps document payloads below the per-attachment
// native-input budget (12MB binary; base64 inflates 4/3 on the wire).
const readMediaFileMaxBytes = 12 * 1024 * 1024

// ReadMediaToolResult is the public result returned to the model.
type ReadMediaToolResult struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path,omitempty"`
	Mime  string `json:"mime,omitempty"`
	Size  int    `json:"size,omitempty"`
	Error string `json:"error,omitempty"`
}

// ReadMediaToolOutput is the internal execution result used by the agent to
// inject the media into the next Twilight AI step while keeping the visible
// tool result lightweight. Exactly one of the Image/File pairs is populated:
// images go through ImageBase64, documents (PDF) through FileBase64.
type ReadMediaToolOutput struct {
	Public         ReadMediaToolResult
	ImageBase64    string
	ImageMediaType string
	FileBase64     string
	FileMediaType  string
	Filename       string
}

// readMediaOutputEnvelopeKey marks the JSON form of ReadMediaToolOutput. The
// executor encodes every non-text output, so the loop-side decorator and the
// MCP gateway recognise the internal result by this envelope rather than by
// its Go type, and only the read_media tool ever produces it.
const readMediaOutputEnvelopeKey = "memoh_read_media"

type readMediaToolOutputWire struct {
	Public         ReadMediaToolResult `json:"public"`
	ImageBase64    string              `json:"image_base64,omitempty"`
	ImageMediaType string              `json:"image_media_type,omitempty"`
	FileBase64     string              `json:"file_base64,omitempty"`
	FileMediaType  string              `json:"file_media_type,omitempty"`
	Filename       string              `json:"filename,omitempty"`
}

// MarshalJSON writes the envelope DecodeReadMediaToolOutput reads back.
func (o ReadMediaToolOutput) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]readMediaToolOutputWire{readMediaOutputEnvelopeKey: {
		Public:         o.Public,
		ImageBase64:    o.ImageBase64,
		ImageMediaType: o.ImageMediaType,
		FileBase64:     o.FileBase64,
		FileMediaType:  o.FileMediaType,
		Filename:       o.Filename,
	}})
}

// DecodeReadMediaToolOutput recovers the internal read-media result from a
// tool output; any other output reports false.
func DecodeReadMediaToolOutput(output sdk.ToolOutput) (ReadMediaToolOutput, bool) {
	if !output.IsJSON() {
		return ReadMediaToolOutput{}, false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(output.JSON, &envelope); err != nil || len(envelope) != 1 {
		return ReadMediaToolOutput{}, false
	}
	raw, ok := envelope[readMediaOutputEnvelopeKey]
	if !ok {
		return ReadMediaToolOutput{}, false
	}
	var wire readMediaToolOutputWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return ReadMediaToolOutput{}, false
	}
	return ReadMediaToolOutput(wire), true
}

// mimeSniffSize is the number of bytes http.DetectContentType needs.
const mimeSniffSize = 512

// ReadImageFromContainer reads a binary file through the bridge client,
// validates that it is a supported image format, and returns a
// ReadMediaToolOutput ready for the agent decoration pipeline.
//
// It reads only a small header first to sniff the MIME type, avoiding
// buffering large non-image binaries just to reject them.
func ReadImageFromContainer(ctx context.Context, client *bridge.Client, path string, maxBytes int64) ReadMediaToolOutput {
	if maxBytes <= 0 {
		maxBytes = defaultReadMediaMaxBytes
	}

	reader, err := client.ReadRaw(ctx, path)
	if err != nil {
		return readMediaErrorResult(err.Error())
	}
	defer func() { _ = reader.Close() }()

	// Read only the sniff header first so non-image binaries fail fast.
	header := make([]byte, mimeSniffSize)
	n, err := io.ReadAtLeast(reader, header, 1)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return readMediaErrorResult("failed to load image: " + err.Error())
	}
	header = header[:n]

	mimeType, err := detectReadMediaMime(header)
	if err != nil {
		return readMediaErrorResult(err.Error())
	}

	// MIME looks good — read the remainder up to the size limit.
	rest, err := io.ReadAll(io.LimitReader(reader, maxBytes-int64(n)+1))
	if err != nil {
		return readMediaErrorResult("failed to load image: " + err.Error())
	}
	data := make([]byte, 0, len(header)+len(rest))
	data = append(data, header...)
	data = append(data, rest...)
	if int64(len(data)) > maxBytes {
		return readMediaErrorResult(fmt.Sprintf("failed to load image: file exceeds %d bytes", maxBytes))
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	public := ReadMediaToolResult{
		OK:   true,
		Path: path,
		Mime: mimeType,
		Size: len(data),
	}
	if mimeType == "application/pdf" {
		if int64(len(data)) > readMediaFileMaxBytes {
			return readMediaErrorResult(fmt.Sprintf("failed to load document: file exceeds %d bytes", readMediaFileMaxBytes))
		}
		return ReadMediaToolOutput{
			Public:        public,
			FileBase64:    encoded,
			FileMediaType: mimeType,
			Filename:      filenameFromPath(path),
		}
	}
	return ReadMediaToolOutput{
		Public:         public,
		ImageBase64:    encoded,
		ImageMediaType: mimeType,
	}
}

func filenameFromPath(path string) string {
	path = strings.TrimSpace(path)
	if idx := strings.LastIndexAny(path, "/\\"); idx >= 0 {
		path = path[idx+1:]
	}
	return path
}

func readMediaErrorResult(message string) ReadMediaToolOutput {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "read failed"
	}
	return ReadMediaToolOutput{
		Public: ReadMediaToolResult{
			OK:    false,
			Error: msg,
		},
	}
}

func detectReadMediaMime(data []byte) (string, error) {
	sniffedMime := ""
	if len(data) > 0 {
		sniffedMime = strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	}

	switch {
	case sniffedMime == "":
		return "", errors.New("only supports PNG, JPEG, GIF, WebP image or PDF document bytes")
	case isSupportedReadMediaMime(sniffedMime):
		return sniffedMime, nil
	default:
		return "", errors.New("only supports PNG, JPEG, GIF, WebP image or PDF document bytes")
	}
}

func isSupportedReadMediaMime(mimeType string) bool {
	_, ok := readMediaSupportedMimeTypes[strings.ToLower(strings.TrimSpace(mimeType))]
	return ok
}
