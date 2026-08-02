// Command telegram-discuss-rate is retained only to explain where its settings moved.
//
// Deprecated: Configure Telegram discuss mode from the bot's channel settings in Web.
package main

import (
	"fmt"
	"io"
	"os"
)

const deprecationMessage = "telegram-discuss-rate 已弃用：请在 Web 的 Bot 渠道设置中配置 Telegram 的被动触发比例和强制回复关键词。"

func main() {
	os.Exit(run(os.Stderr))
}

func run(stderr io.Writer) int {
	_, _ = fmt.Fprintln(stderr, deprecationMessage)
	return 2
}
