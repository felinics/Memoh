package feishu

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/partsfixture"
)

// TestCanonicalPartsRendering pins the Feishu adapter's lark_md output for
// the shared canonical fixture.
func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()
	msg := channel.Message{Parts: partsfixture.Canonical()}
	got := renderFeishuMessagePartsLarkMD(msg)
	if got != partsfixture.CanonicalFeishuLarkMD {
		t.Errorf("renderFeishuMessagePartsLarkMD(Canonical)\n  got:  %q\n  want: %q", got, partsfixture.CanonicalFeishuLarkMD)
	}
}
