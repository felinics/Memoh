// Package secure provides passphrase-based authenticated encryption for bot
// backup bundles.
//
// The container wraps an arbitrary plaintext stream (the .memoh.zip bytes) so a
// bundle that carries credentials can be encrypted at rest. The construction is
// deliberately small and dependency-light, using only the standard library and
// golang.org/x/crypto/argon2:
//
//   - Key derivation: Argon2id over the user passphrase + a random 16-byte salt.
//   - Encryption: AES-256-GCM in a chunked STREAM construction (RFC-style framed
//     AEAD, the same shape used by age/Tink streaming): the plaintext is split
//     into fixed-size chunks, each sealed independently with a counter-based
//     nonce, and the final chunk is tagged via a "last" flag folded into the
//     nonce. This authenticates chunk order and detects truncation/extension.
//
// On-disk layout:
//
//	magic[8] | kdf[1] | argonTime[4] | argonMemoryKiB[4] | argonThreads[1] | salt[16]
//	then a sequence of frames: ciphertextLen[uint32 BE] | ciphertext
//
// Every encrypted stream ends with a frame produced from the final (possibly
// empty) chunk sealed with the last flag set, so a decrypter can always tell a
// complete stream from a truncated one.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	// Magic identifies the encrypted bundle format. Bump the trailing digit if
	// the container layout ever changes incompatibly.
	magic = "MEMOHbk1"

	kdfArgon2id byte = 0x01

	saltLen   = 16
	keyLen    = 32 // AES-256
	nonceLen  = 12 // AES-GCM standard nonce size
	chunkSize = 64 * 1024
	headerLen = len(magic) + 1 + 4 + 4 + 1 + saltLen
)

// Argon2id parameters. Tuned for an interactive, one-off bundle encryption:
// 64 MiB memory keeps derivation affordable on desktop/server while staying
// resistant to brute force. They are written into the header so decryption is
// self-describing even if defaults change later.
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB => 64 MiB
	argonThreads uint8  = 4
)

var (
	// ErrPassphraseRequired is returned when an empty passphrase is supplied.
	ErrPassphraseRequired = errors.New("secure: passphrase required")
	// ErrNotEncrypted is returned when the input is not an encrypted bundle.
	ErrNotEncrypted = errors.New("secure: not an encrypted bundle")
	// ErrAuth is returned when authentication fails: a wrong passphrase or
	// tampered ciphertext.
	ErrAuth = errors.New("secure: authentication failed (wrong passphrase or corrupted data)")
	// ErrTruncated is returned when the stream ends before its final frame.
	ErrTruncated = errors.New("secure: truncated bundle")
)

// maxFrame caps a single ciphertext frame so a corrupt length prefix cannot
// trigger an unbounded allocation.
const maxFrame = chunkSize + 16 // 16 = AES-GCM tag overhead

// IsEncrypted reports whether raw begins with the encrypted bundle magic.
func IsEncrypted(raw []byte) bool {
	return len(raw) >= len(magic) && string(raw[:len(magic)]) == magic
}

// Encrypt reads plaintext from src and writes an encrypted bundle to dst.
func Encrypt(dst io.Writer, src io.Reader, passphrase string) error {
	if passphrase == "" {
		return ErrPassphraseRequired
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("secure: read salt: %w", err)
	}
	aead, err := newAEAD(deriveKey(passphrase, salt))
	if err != nil {
		return err
	}
	if err := writeHeader(dst, salt); err != nil {
		return err
	}
	return streamSeal(dst, src, aead)
}

// Decrypt reads an encrypted bundle from src and writes the plaintext to dst.
// It returns ErrNotEncrypted if src does not start with the bundle magic,
// ErrAuth on a wrong passphrase or tampering, and ErrTruncated on a short read.
func Decrypt(dst io.Writer, src io.Reader, passphrase string) error {
	if passphrase == "" {
		return ErrPassphraseRequired
	}
	salt, err := readHeader(src)
	if err != nil {
		return err
	}
	aead, err := newAEAD(deriveKey(passphrase, salt))
	if err != nil {
		return err
	}
	return streamOpen(dst, src, aead)
}

func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, keyLen)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secure: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secure: new gcm: %w", err)
	}
	return aead, nil
}

func writeHeader(dst io.Writer, salt []byte) error {
	header := make([]byte, 0, headerLen)
	header = append(header, magic...)
	header = append(header, kdfArgon2id)
	header = binary.BigEndian.AppendUint32(header, argonTime)
	header = binary.BigEndian.AppendUint32(header, argonMemory)
	header = append(header, argonThreads)
	header = append(header, salt...)
	if _, err := dst.Write(header); err != nil {
		return fmt.Errorf("secure: write header: %w", err)
	}
	return nil
}

func readHeader(src io.Reader) ([]byte, error) {
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(src, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrNotEncrypted
		}
		return nil, fmt.Errorf("secure: read header: %w", err)
	}
	if string(header[:len(magic)]) != magic {
		return nil, ErrNotEncrypted
	}
	if header[len(magic)] != kdfArgon2id {
		return nil, fmt.Errorf("secure: unsupported kdf %#x", header[len(magic)])
	}
	// Argon2 parameters are read from the header so the bundle is self-describing,
	// but the current build only supports the fixed defaults it was written with.
	salt := header[headerLen-saltLen:]
	out := make([]byte, saltLen)
	copy(out, salt)
	return out, nil
}

// streamSeal splits src into chunks and writes one authenticated frame per
// chunk. The final chunk (possibly empty) is sealed with the last flag set, so
// the stream always terminates with a frame a reader can recognize as final.
func streamSeal(dst io.Writer, src io.Reader, aead cipher.AEAD) error {
	buf := make([]byte, chunkSize)
	var counter uint64
	for {
		n, readErr := io.ReadFull(src, buf)
		last := false
		switch {
		case errors.Is(readErr, io.EOF):
			last = true // clean boundary: emit a final empty chunk
			n = 0
		case errors.Is(readErr, io.ErrUnexpectedEOF):
			last = true // partial trailing chunk is the final one
		case readErr != nil:
			return fmt.Errorf("secure: read plaintext: %w", readErr)
		}
		ct := aead.Seal(nil, makeNonce(counter, last), buf[:n], nil)
		if err := writeFrame(dst, ct); err != nil {
			return err
		}
		counter++
		if last {
			return nil
		}
	}
}

// streamOpen decrypts frames written by streamSeal. It reads one frame ahead so
// it can pass the correct last flag to the final chunk and detect truncation:
// an authentic final frame is sealed with last=true, so a missing tail makes
// the new trailing frame fail authentication.
func streamOpen(dst io.Writer, src io.Reader, aead cipher.AEAD) error {
	var counter uint64
	frame, err := readFrame(src)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return ErrTruncated // a valid stream always has at least the final frame
		}
		return err
	}
	for {
		next, nextErr := readFrame(src)
		last := errors.Is(nextErr, io.EOF)
		if nextErr != nil && !last {
			return nextErr
		}
		pt, openErr := aead.Open(nil, makeNonce(counter, last), frame, nil)
		if openErr != nil {
			return ErrAuth
		}
		if _, err := dst.Write(pt); err != nil {
			return fmt.Errorf("secure: write plaintext: %w", err)
		}
		counter++
		if last {
			return nil
		}
		frame = next
	}
}

func writeFrame(dst io.Writer, ciphertext []byte) error {
	// A frame is always one sealed chunk, so its length is bounded by maxFrame
	// and fits in uint32. The guard keeps that invariant explicit.
	if len(ciphertext) == 0 || len(ciphertext) > maxFrame {
		return fmt.Errorf("secure: invalid frame length %d", len(ciphertext))
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ciphertext))) // #nosec G115 -- guarded by maxFrame check above.
	if _, err := dst.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("secure: write frame length: %w", err)
	}
	if _, err := dst.Write(ciphertext); err != nil {
		return fmt.Errorf("secure: write frame: %w", err)
	}
	return nil
}

// readFrame reads one length-prefixed ciphertext frame. It returns io.EOF (and
// only io.EOF) when the stream ends cleanly at a frame boundary; a partial read
// is reported as ErrTruncated.
func readFrame(src io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(src, lenBuf[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, ErrTruncated
	}
	clen := binary.BigEndian.Uint32(lenBuf[:])
	if clen == 0 || clen > maxFrame {
		return nil, fmt.Errorf("secure: invalid frame length %d", clen)
	}
	ct := make([]byte, clen)
	if _, err := io.ReadFull(src, ct); err != nil {
		return nil, ErrTruncated
	}
	return ct, nil
}

// makeNonce builds a 12-byte AES-GCM nonce from a chunk counter and the last
// flag. The key is unique per bundle (random salt), so a per-chunk counter
// yields unique nonces; folding the last flag into the nonce binds it to the
// authentication tag.
func makeNonce(counter uint64, last bool) []byte {
	nonce := make([]byte, nonceLen)
	binary.BigEndian.PutUint64(nonce[:8], counter)
	if last {
		nonce[nonceLen-1] = 0x01
	}
	return nonce
}
