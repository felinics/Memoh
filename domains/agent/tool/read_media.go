package tool

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
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
}

// ReadMediaToolResult is the public result returned to the model.
type ReadMediaToolResult struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path,omitempty"`
	Mime  string `json:"mime,omitempty"`
	Size  int    `json:"size,omitempty"`
	Error string `json:"error,omitempty"`
}

// ReadMediaToolOutput is the internal execution result used by the agent to
// inject the image into the next Twilight AI step while keeping the visible
// tool result lightweight.
type ReadMediaToolOutput struct {
	Public         ReadMediaToolResult
	ImageBase64    string
	ImageMediaType string
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
	return ReadMediaToolOutput{
		Public: ReadMediaToolResult{
			OK:   true,
			Path: path,
			Mime: mimeType,
			Size: len(data),
		},
		ImageBase64:    encoded,
		ImageMediaType: mimeType,
	}
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
		return "", errors.New("only supports PNG, JPEG, GIF, or WebP image bytes")
	case isSupportedReadMediaMime(sniffedMime):
		return sniffedMime, nil
	default:
		return "", errors.New("only supports PNG, JPEG, GIF, or WebP image bytes")
	}
}

func isSupportedReadMediaMime(mimeType string) bool {
	_, ok := readMediaSupportedMimeTypes[strings.ToLower(strings.TrimSpace(mimeType))]
	return ok
}
