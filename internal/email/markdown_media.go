package email

import (
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/markdownmedia"
)

// OutboundEmail currently has no attachment transport. Preserve the body and
// report unsupported media explicitly, instead of mailing private workspace paths.
// HTML bodies remain HTML; only Markdown references in text bodies are parsed.
func prepareMarkdownBody(msg OutboundEmail) (OutboundEmail, bool) {
	if msg.HTML {
		return msg, false
	}
	refs := markdownmedia.Parse(msg.Body)
	if len(refs) == 0 {
		return msg, false
	}
	bindings := make([]markdownmedia.Binding, 0, len(refs))
	for _, ref := range refs {
		bindings = append(bindings, markdownmedia.Binding{Reference: ref})
	}
	msg.Body = markdownmedia.Render(msg.Body, bindings, func(b markdownmedia.Binding) string {
		return b.Label + " — " + i18n.New("en").T("media.reference_unavailable")
	})
	return msg, true
}
