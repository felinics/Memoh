package command

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/memohai/memoh/internal/i18n"
)

// humanizeTime renders a timestamp relative to now ("just now", "5m ago",
// "2h ago", "3d ago"); timestamps older than a week fall back to an absolute
// date. A zero time renders as the empty string. This is the single timestamp
// formatter used across command output.
func humanizeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return relativeSince(t, time.Now())
}

func humanizeTimeT(cc CommandContext, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return relativeSinceT(cc, t, time.Now())
}

func relativeSince(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		// Future timestamps: show absolute to avoid "−5m ago" weirdness.
		return t.Format("2006-01-02 15:04")
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02 15:04")
	}
}

func relativeSinceT(cc CommandContext, t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return t.Format("2006-01-02 15:04")
	case d < 5*time.Second:
		return cc.T("cmd.time.justNow")
	case d < time.Minute:
		return cc.T("cmd.time.secondsAgo", map[string]any{"n": int(d.Seconds())})
	case d < time.Hour:
		return cc.T("cmd.time.minutesAgo", map[string]any{"n": int(d.Minutes())})
	case d < 24*time.Hour:
		return cc.T("cmd.time.hoursAgo", map[string]any{"n": int(d.Hours())})
	case d < 7*24*time.Hour:
		return cc.T("cmd.time.daysAgo", map[string]any{"n": int(d.Hours() / 24)})
	default:
		return t.Format("2006-01-02 15:04")
	}
}

// humanizeDuration renders a duration compactly: "820ms", "3.2s", "2m 5s",
// "1h 4m".
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		return fmt.Sprintf("%dh %dm", h, m)
	}
}

// humanizeBytes renders a byte count in base-1024 units (B/KB/MB/GB/TB).
func humanizeBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// formatTokens abbreviates large token counts (e.g. 1.2K, 3.4M).
func formatTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return strconv.FormatInt(n, 10)
}

// MdBold / MdCode author Markdown for capable channels. Telegram bold is
// **double-star** (single * is italic); backtick code spans are the safe path
// for dynamic values (escaped verbatim, never mis-parsed).
func MdBold(s string) string { return "**" + s + "**" }
func MdCode(s string) string { return "`" + s + "`" }

// CommandText renders a slash command and optionally addresses its first token
// to a bot username. Keeping the suffix next to the resource is required for
// Telegram's /command@bot grammar; any arguments remain after that token.
func CommandText(cmd, botUsername string) string {
	parts := strings.Fields(strings.TrimPrefix(strings.TrimSpace(cmd), "/"))
	if len(parts) == 0 {
		return "/"
	}
	username := strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if username != "" && !strings.Contains(parts[0], "@") {
		parts[0] += "@" + username
	}
	return "/" + strings.Join(parts, " ")
}

// TelegramGroupCommandTip explains the one transport-specific rule users need
// when typing commands manually. Command lists stay visually clean; the bot
// username appears once in this complete, copyable example.
func TelegramGroupCommandTip(t *i18n.Localizer, botUsername string) string {
	username := strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if username == "" {
		return ""
	}
	return t.T("groupCommand.telegramTip", map[string]any{
		"example": MdCode(CommandText("help", username)),
	})
}

// CmdRef renders a slash-command reference as a tap-to-copy code span: on
// Telegram a code span is tap-to-copy monospace, and on text-only channels the
// backticks strip cleanly. Accepts "schedule list" or "/schedule list".
func CmdRef(cmd string) string {
	return MdCode(CommandText(cmd, ""))
}

var cronWeekdays = [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// humanizeCron renders the common cron shapes as a readable phrase ("daily at
// 09:00", "every 15 minutes", "Mondays at 09:00"), falling back to the raw
// pattern for anything it does not model. The raw fallback stays a machine token
// (code span) via isMachineToken, so unusual patterns are still legible.
func humanizeCron(pattern string) string {
	f := strings.Fields(strings.TrimSpace(pattern))
	if len(f) != 5 {
		return pattern
	}
	minute, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4]

	allStar := hour == "*" && dom == "*" && mon == "*" && dow == "*"
	if minute == "*" && allStar {
		return "every minute"
	}
	if strings.HasPrefix(minute, "*/") && allStar {
		if n, err := strconv.Atoi(minute[2:]); err == nil && n > 0 {
			return fmt.Sprintf("every %d minutes", n)
		}
	}
	if minute == "0" && allStar {
		return "hourly"
	}

	clock, ok := cronClock(minute, hour)
	if !ok || mon != "*" {
		return pattern
	}
	switch {
	case dom == "*" && dow == "*":
		return "daily at " + clock
	case dom == "*" && dow != "*":
		if wd, ok := cronWeekday(dow); ok {
			return fmt.Sprintf("%ss at %s", wd, clock)
		}
	}
	return pattern
}

func humanizeCronT(cc CommandContext, pattern string) string {
	f := strings.Fields(strings.TrimSpace(pattern))
	if len(f) != 5 {
		return pattern
	}
	minute, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4]

	allStar := hour == "*" && dom == "*" && mon == "*" && dow == "*"
	if minute == "*" && allStar {
		return cc.T("cmd.cron.everyMinute")
	}
	if strings.HasPrefix(minute, "*/") && allStar {
		if n, err := strconv.Atoi(minute[2:]); err == nil && n > 0 {
			return cc.T("cmd.cron.everyNMinutes", map[string]any{"n": n})
		}
	}
	if minute == "0" && allStar {
		return cc.T("cmd.cron.hourly")
	}

	clock, ok := cronClock(minute, hour)
	if !ok || mon != "*" {
		return pattern
	}
	switch {
	case dom == "*" && dow == "*":
		return cc.T("cmd.cron.dailyAt", map[string]any{"time": clock})
	case dom == "*" && dow != "*":
		if wd, ok := cronWeekday(dow); ok {
			return cc.T("cmd.cron.weekdayAt", map[string]any{"weekday": cc.T("cmd.cron.weekday." + strings.ToLower(wd)), "time": clock})
		}
	}
	return pattern
}

func cronClock(minute, hour string) (string, bool) {
	m, err1 := strconv.Atoi(minute)
	h, err2 := strconv.Atoi(hour)
	if err1 != nil || err2 != nil || m < 0 || m > 59 || h < 0 || h > 23 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", h, m), true
}

func cronWeekday(dow string) (string, bool) {
	d, err := strconv.Atoi(dow)
	if err != nil {
		return "", false
	}
	if d == 7 { // cron allows both 0 and 7 for Sunday
		d = 0
	}
	if d < 0 || d > 6 {
		return "", false
	}
	return cronWeekdays[d], true
}

// isSuccessStatus reports whether a status string represents a successful run.
// Used to suppress a redundant "Success" flag on rows where success is the
// expected, common state (absence of a failure flag conveys success).
func isSuccessStatus(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ok", "success", "succeeded":
		return true
	}
	return false
}

// humanizeStatus renders a status/enum token as a friendly Title-cased label,
// mapping known machine values to clearer words. Unknown values are Title-cased
// as-is; the empty string passes through unchanged.
func humanizeStatus(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	switch strings.ToLower(t) {
	case "ok", "success", "succeeded":
		return "Success"
	case "failed", "fail":
		return "Failed"
	case "error", "errored":
		return "Error"
	case "unknown":
		return "Not checked"
	case "connected":
		return "Connected"
	case "disconnected":
		return "Disconnected"
	case "active":
		return "Active"
	case "inactive":
		return "Inactive"
	case "pending":
		return "Pending"
	case "running":
		return "Running"
	case "allow", "allowed":
		return "Allowed"
	case "deny", "denied":
		return "Denied"
	case "sent":
		return "Sent"
	case "queued":
		return "Queued"
	case "bounced":
		return "Bounced"
	}
	r := []rune(t)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func humanizeStatusT(cc CommandContext, s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	switch strings.ToLower(t) {
	case "ok", "success", "succeeded":
		return cc.T("cmd.statusValue.success")
	case "failed", "fail":
		return cc.T("cmd.statusValue.failed")
	case "error", "errored":
		return cc.T("cmd.common.error")
	case "unknown":
		return cc.T("cmd.statusValue.notChecked")
	case "connected":
		return cc.T("cmd.statusValue.connected")
	case "disconnected":
		return cc.T("cmd.statusValue.disconnected")
	case "active":
		return cc.T("cmd.common.active")
	case "inactive":
		return cc.T("cmd.statusValue.inactive")
	case "pending":
		return cc.T("cmd.statusValue.pending")
	case "running":
		return cc.T("cmd.statusValue.running")
	case "allow", "allowed":
		return cc.T("cmd.common.allowed")
	case "deny", "denied":
		return cc.T("cmd.common.denied")
	case "sent":
		return cc.T("cmd.statusValue.sent")
	case "queued":
		return cc.T("cmd.statusValue.queued")
	case "bounced":
		return cc.T("cmd.statusValue.bounced")
	}
	r := []rune(t)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
