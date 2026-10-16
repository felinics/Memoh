package channel

import (
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/i18n"
)

func TestMissingChatModelHasActionableChannelCopy(t *testing.T) {
	for _, tc := range []struct{ locale, want string }{
		{"en", "No chat model is selected. Set a default chat model in Bot settings → General, then try again."},
		{"zh", "尚未选择对话模型。请在 Bot 设置 → 通用中设置默认对话模型，然后重试。"},
		{"ja", "チャットモデルが選択されていません。Bot 設定 →「一般的な」でデフォルトのチャットモデルを設定してから、もう一度お試しください。"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			const code apperror.Code = "agent.chat_model_not_configured"
			event := CodeEvent(i18n.New(tc.locale), code, nil)
			if event.ErrorCode != string(code) || event.Error != tc.want {
				t.Fatalf("channel guidance = %+v, want %q", event, tc.want)
			}
		})
	}
}
