package markdownmedia

import "testing"

func TestStreamHoldOnlyPausesPossibleMedia(t *testing.T) {
	for _, tc := range []struct {
		text   string
		offset int
		media  bool
	}{
		{"Hello! this is streamed", 23, false},
		{"Hello!", 5, false},
		{"Array [1, 2] and [x] done", 25, false},
		{"- [ ] task\n- [x] done", 21, false},
		{"see [site](https://example.com) now", 35, false},
		{"see [notes](/data/notes) and", 4, true},
		{"chart: ![c](/data/c.png) more", 7, true},
		{"remote ![c](https://example.com/c.png)", 7, true},
		{"partial ![c](/data/c", 8, false},
		{"partial [lab", 8, false},
		{"label done]", 11, false},
		{"pending [x]", 8, false},
		{"route [api](/api/v1/users) and [dir](/data/project/)", 52, false},
		{"`![c](/data/c.png)` is syntax", 29, false},
		{"```md\n![c](/data/c.png)\n```\nafter", 33, false},
		{"```md\n![c](/data/c.png)", 23, false},
		{"\\![c](/data/c.png) escaped", 2, true},
		{"[outer ![c](/data/c.png)](https://example.com) x", 0, true},
		{"[note ![c](/data/c.png)] x", 6, true},
		{"[a\n\nb](/data/x)", 15, false},
		{"Use the ` key. Chart: ![c](/data/c.png) next.", 22, true},
		{"see ![chart][c] later", 4, true},
		{"see [report][r] later", 4, true},
		{"see [note] later", 16, false},
		{"line one\nsee ![c](/data/c", 13, false},
	} {
		offset, media, resume := StreamHold(tc.text)
		if resume > offset || (resume > 0 && tc.text[resume-1] != '\n') {
			t.Errorf("StreamHold(%q) resume=%d is not a line start at or before %d", tc.text, resume, offset)
		}
		if offset != tc.offset || media != tc.media {
			t.Errorf("StreamHold(%q) = %d,%v want %d,%v", tc.text, offset, media, tc.offset, tc.media)
		}
	}
}
