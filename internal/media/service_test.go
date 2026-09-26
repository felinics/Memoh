package media

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // compatibility digest required by the Weixin upload protocol
	"encoding/hex"
	"testing"

	"github.com/felinics/memoh/internal/storage/providers/localfs"
)

func TestIngestCalculatesRawMD5AlongsideContentHash(t *testing.T) {
	data := []byte("hello attachment")
	service := NewService(nil, localfs.New(t.TempDir()))
	asset, err := service.Ingest(context.Background(), IngestInput{BotID: "bot-1", Mime: "text/plain", Reader: bytes.NewReader(data)})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	want := md5.Sum(data) //nolint:gosec
	if asset.RawMD5 != hex.EncodeToString(want[:]) {
		t.Fatalf("RawMD5 = %q, want %x", asset.RawMD5, want)
	}
}
