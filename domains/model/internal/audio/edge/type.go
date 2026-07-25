package edge

import (
	_ "embed"
	"encoding/json"
)

const (
	EDGE_SPEECH_URL        = "wss://speech.iam.bing.com/consumer/speech/synthesize/readaloud/edge/v1"
	EDGE_API_TOKEN         = "6A5AA1D4EAFF4E9FB37E23D68491D6F4" //nolint:gosec // Well-known public token shared by all Edge TTS clients, not a secret.
	CHROMIUM_FULL_VERSION  = "143.0.3650.75"
	CHROMIUM_MAJOR_VERSION = "143"

	// WSS handshake Origin (same as readest / extension).
	WSSOrigin = "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold"

	DEFAULT_VOICE = "en-US-EmmaMultilingualNeural"

	// Windows file time base (1601-01-01) for dynamic generation of Sec-MS-GEC.
	WIN_EPOCH_OFFSET = 11644473600
	S_TO_NS          = 1000000000
)

//go:embed voices.json
var voicesJSON []byte

// EdgeTTSVoices maps language tags to available voice IDs, loaded from voices.json.
var EdgeTTSVoices map[string][]string

var voiceToLang map[string]string

func init() {
	if err := json.Unmarshal(voicesJSON, &EdgeTTSVoices); err != nil {
		panic("edge: failed to parse voices.json: " + err.Error())
	}
	voiceToLang = make(map[string]string, 256)
	for lang, voices := range EdgeTTSVoices {
		for _, v := range voices {
			voiceToLang[v] = lang
		}
	}
}

// LookupVoiceLang returns the language for a known Edge TTS voice ID.
// Returns ("", false) if the voice is not recognized.
func LookupVoiceLang(voiceID string) (string, bool) {
	lang, ok := voiceToLang[voiceID]
	return lang, ok
}
