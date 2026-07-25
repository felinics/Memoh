package weixin

import "testing"

func TestEncodeAESKeyForSend(t *testing.T) {
	key := []byte("0123456789abcdef")
	want := "MzAzMTMyMzMzNDM1MzYzNzM4Mzk2MTYyNjM2NDY1NjY="

	if got := encodeAESKeyForSend(key); got != want {
		t.Errorf("encodeAESKeyForSend() = %q, want %q", got, want)
	}
}
