package markdownmedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"

	"github.com/felinics/memoh/internal/media"
)

type memoryStore struct {
	files  map[string][]byte
	assets map[string][]byte
	reads  int
}

func (s *memoryStore) IngestWorkspaceFile(ctx context.Context, botID, path string) (media.Asset, error) {
	s.reads++
	data, ok := s.files[botID+path]
	if !ok {
		return media.Asset{}, errors.New("SECRET missing")
	}
	return s.Ingest(ctx, media.IngestInput{BotID: botID, Reader: bytes.NewReader(data)})
}

func (s *memoryStore) Ingest(_ context.Context, input media.IngestInput) (media.Asset, error) {
	data, err := io.ReadAll(input.Reader)
	if err != nil {
		return media.Asset{}, err
	}
	hash := sha256.Sum256(data)
	key := hex.EncodeToString(hash[:])
	if s.assets == nil {
		s.assets = map[string][]byte{}
	}
	s.assets[input.BotID+key] = data
	return media.Asset{BotID: input.BotID, ContentHash: key, StorageKey: key, Mime: http.DetectContentType(data), SizeBytes: int64(len(data))}, nil
}

func (s *memoryStore) Open(_ context.Context, botID, hash string) (io.ReadCloser, media.Asset, error) {
	data, ok := s.assets[botID+hash]
	if !ok {
		return nil, media.Asset{}, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(data)), media.Asset{}, nil
}

func TestSnapshotSurvivesOverwriteAndDeletion(t *testing.T) {
	ctx := context.Background()
	s := &memoryStore{files: map[string][]byte{"bot/data/report.txt": []byte("original")}}
	bindings := Resolve(ctx, s, "bot", "[report](/data/report.txt) and [again](/data/report.txt)")
	if len(bindings) != 2 || s.reads != 1 {
		t.Fatalf("bindings=%+v reads=%d", bindings, s.reads)
	}
	s.files["bot/data/report.txt"] = []byte("revised")
	newBinding := Resolve(ctx, s, "bot", "[report](/data/report.txt)")[0]
	if newBinding.Asset.ContentHash == bindings[0].Asset.ContentHash {
		t.Fatal("new message reused stale bytes")
	}
	delete(s.files, "bot/data/report.txt")
	r, _, err := s.Open(ctx, "bot", bindings[0].Asset.ContentHash)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	data, _ := io.ReadAll(r)
	if string(data) != "original" {
		t.Fatalf("snapshot=%q", data)
	}
}

func TestFailuresAreIsolatedAndDoNotExposeStorageErrors(t *testing.T) {
	s := &memoryStore{files: map[string][]byte{"bot/data/good.txt": []byte("hello")}}
	bindings := Resolve(context.Background(), s, "bot", "[good](/data/good.txt) ![bad](/data/good.txt) [missing](/data/missing) [outside](/etc/passwd)")
	if len(bindings) != 4 || bindings[0].ErrorCode != "" {
		t.Fatalf("bindings=%+v", bindings)
	}
	for _, b := range bindings[1:] {
		if b.ErrorCode != "media.reference_unavailable" || b.Asset.ContentHash != "" {
			t.Fatalf("failure leaked an asset: %+v", b)
		}
	}
	if s.reads != 3 {
		t.Fatalf("outside path reached storage: reads=%d", s.reads)
	}
	other := Resolve(context.Background(), s, "other", "[good](/data/good.txt)")
	if other[0].ErrorCode == "" {
		t.Fatal("cross-bot read succeeded")
	}
}

func TestRemoteImageRejectsLoopback(t *testing.T) {
	bindings := Resolve(context.Background(), &memoryStore{}, "bot", "![internal](http://127.0.0.1:9/secret)")
	if len(bindings) != 1 || bindings[0].ErrorCode == "" {
		t.Fatalf("bindings=%+v", bindings)
	}
}

func TestMediaDownloadRejectsNonPublicDestinations(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "169.254.169.254", "10.0.0.1", "100.64.0.1", "198.18.0.1", "64:ff9b::a00:1", "fc00::1"} {
		if publicMediaIP(net.ParseIP(address)) {
			t.Errorf("accepted restricted destination %s", address)
		}
	}
	if !publicMediaIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public destination rejected")
	}
}
