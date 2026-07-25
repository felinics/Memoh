package secure

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func roundTrip(t *testing.T, plaintext []byte, passphrase string) {
	t.Helper()
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(plaintext), passphrase); err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if !IsEncrypted(enc.Bytes()) {
		t.Fatal("IsEncrypted() = false for encrypted output")
	}
	var dec bytes.Buffer
	if err := Decrypt(&dec, bytes.NewReader(enc.Bytes()), passphrase); err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if !bytes.Equal(dec.Bytes(), plaintext) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", dec.Len(), len(plaintext))
	}
}

func TestRoundTripSizes(t *testing.T) {
	sizes := []int{0, 1, 100, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 123}
	for _, size := range sizes {
		data := make([]byte, size)
		if _, err := rand.Read(data); err != nil {
			t.Fatalf("rand.Read() error = %v", err)
		}
		roundTrip(t, data, "correct horse battery staple")
	}
}

func TestWrongPassphraseFails(t *testing.T) {
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader([]byte("top secret payload")), "right"); err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	var dec bytes.Buffer
	err := Decrypt(&dec, bytes.NewReader(enc.Bytes()), "wrong")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("Decrypt() with wrong passphrase error = %v, want ErrAuth", err)
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(bytes.Repeat([]byte("a"), 200)), "pw"); err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	corrupted := enc.Bytes()
	corrupted[headerLen+5] ^= 0xff // flip a bit inside the first frame
	var dec bytes.Buffer
	if err := Decrypt(&dec, bytes.NewReader(corrupted), "pw"); !errors.Is(err, ErrAuth) {
		t.Fatalf("Decrypt() of tampered data error = %v, want ErrAuth", err)
	}
}

func TestTruncatedStreamFails(t *testing.T) {
	var enc bytes.Buffer
	// Multi-chunk payload so dropping the tail removes the final frame.
	if err := Encrypt(&enc, bytes.NewReader(make([]byte, 3*chunkSize)), "pw"); err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	full := enc.Bytes()
	// Drop the final frame entirely (header + first two full frames remain).
	frameLen := 4 + chunkSize + 16
	truncated := full[:headerLen+2*frameLen]
	var dec bytes.Buffer
	err := Decrypt(&dec, bytes.NewReader(truncated), "pw")
	if !errors.Is(err, ErrAuth) && !errors.Is(err, ErrTruncated) {
		t.Fatalf("Decrypt() of truncated stream error = %v, want ErrAuth or ErrTruncated", err)
	}
}

func TestNotEncryptedDetection(t *testing.T) {
	if IsEncrypted([]byte("PK\x03\x04 a normal zip")) {
		t.Fatal("IsEncrypted() = true for a plain zip")
	}
	var dec bytes.Buffer
	if err := Decrypt(&dec, bytes.NewReader([]byte("PK\x03\x04 not encrypted")), "pw"); !errors.Is(err, ErrNotEncrypted) {
		t.Fatalf("Decrypt() of plain data error = %v, want ErrNotEncrypted", err)
	}
}

func TestEmptyPassphraseRejected(t *testing.T) {
	if err := Encrypt(&bytes.Buffer{}, bytes.NewReader(nil), ""); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("Encrypt() empty passphrase error = %v, want ErrPassphraseRequired", err)
	}
	if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(nil), ""); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("Decrypt() empty passphrase error = %v, want ErrPassphraseRequired", err)
	}
}
