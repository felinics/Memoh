package input

import (
	"slices"

	"github.com/felinics/memoh/internal/i18n"
)

// Localized returns a display copy. Stored payloads and response identities
// remain independent of the channel's current UI language.
func (p UIPayload) Localized(loc *i18n.Localizer) UIPayload {
	p.Questions = slices.Clone(p.Questions)
	for i := range p.Questions {
		q := &p.Questions[i]
		q.Options = slices.Clone(q.Options)
		for j := range q.Options {
			if key := q.Options[j].LabelKey; key != "" {
				q.Options[j].Label = loc.T(key)
			}
		}
	}
	return p
}
