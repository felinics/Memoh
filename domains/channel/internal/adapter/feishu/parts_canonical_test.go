package feishu

import (
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/internal/test/fixture"
)

// TestCanonicalPartsRendering pins the Feishu adapter's lark_md output for
// the shared canonical fixture.
func TestCanonicalPartsRendering(t *testing.T) {
	t.Parallel()
	msg := gateway.Message{Parts: fixture.Canonical()}
	got := renderFeishuMessagePartsLarkMD(msg)
	if got != fixture.CanonicalFeishuLarkMD {
		t.Errorf("renderFeishuMessagePartsLarkMD(Canonical)\n  got:  %q\n  want: %q", got, fixture.CanonicalFeishuLarkMD)
	}
}
