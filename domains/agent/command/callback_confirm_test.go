package command

import "testing"

func TestConfirmNewCallbackRoundTrip(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"chat", "discuss"} {
		data := EncodeConfirmNewCallback(mode)
		if len(data) > telegramCallbackLimit {
			t.Fatalf("callback %q exceeds limit", data)
		}
		parsed, ok := DecodeCallback(data)
		if !ok || parsed.Kind != callbackKindConfirmNew {
			t.Fatalf("decode %q -> %+v ok=%v", data, parsed, ok)
		}
		if got, want := parsed.SyntheticCommand(), "/new "+mode+" --confirm"; got != want {
			t.Errorf("synthetic = %q, want %q", got, want)
		}
		reparsed, err := Parse(parsed.SyntheticCommand())
		if err != nil {
			t.Fatalf("Parse(%q): %v", parsed.SyntheticCommand(), err)
		}
		if reparsed.Resource != "new" || reparsed.Action != mode {
			t.Errorf("reparse = %+v, want new/%s", reparsed, mode)
		}
		// The --confirm marker survives into Args (it is not a parser flag).
		found := false
		for _, a := range reparsed.Args {
			if a == "--confirm" {
				found = true
			}
		}
		if !found {
			t.Errorf("--confirm marker lost: %+v", reparsed.Args)
		}
	}
}
