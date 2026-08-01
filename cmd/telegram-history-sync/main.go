// Command telegram-history-sync removes Telegram messages that were deleted
// upstream from Memoh's persisted timeline and visible bot history.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultPostgresContainer = "memoh-postgres"
	defaultPostgresUser      = "memoh"
	defaultPostgresDatabase  = "memoh"
)

var safeIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type stringListFlag []string

func (v *stringListFlag) String() string {
	return strings.Join(*v, ",")
}

func (v *stringListFlag) Set(raw string) error {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*v = append(*v, part)
		}
	}
	return nil
}

type config struct {
	chatID            string
	messageIDs        []string
	apply             bool
	allowReferences   bool
	restart           bool
	useSudo           bool
	composeDir        string
	postgresContainer string
	postgresUser      string
	postgresDatabase  string
	timeout           time.Duration
}

type preview struct {
	RouteCount int            `json:"route_count"`
	Routes     []routePreview `json:"routes"`
	Matches    []matchPreview `json:"matches"`
	Missing    []string       `json:"missing"`
}

type routePreview struct {
	BotName string `json:"bot_name"`
	RouteID string `json:"route_id"`
}

type matchPreview struct {
	BotName         string `json:"bot_name"`
	RouteID         string `json:"route_id"`
	SessionID       string `json:"session_id"`
	MessageID       string `json:"message_id"`
	SenderName      string `json:"sender_name"`
	ReceivedAtMS    int64  `json:"received_at_ms"`
	HistoryCount    int    `json:"history_count"`
	EventCount      int    `json:"event_count"`
	HistoryReplies  int    `json:"history_reply_count"`
	EventReplies    int    `json:"event_reply_count"`
	CompactionCount int    `json:"compaction_count"`
	MemoryNodeCount int    `json:"memory_node_count"`
}

type applyResult struct {
	SessionCount    int `json:"session_count"`
	HistoryCount    int `json:"history_count"`
	EventCount      int `json:"event_count"`
	CompactionCount int `json:"compaction_count"`
}

type commandRunner struct {
	useSudo bool
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// CLI output is best effort: a broken output stream leaves no useful recovery
// action for this short-lived operator command.
//
//nolint:errcheck
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := parseConfig(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "错误：%v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := &commandRunner{
		useSudo: cfg.useSudo,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
	}
	if cfg.useSudo {
		if err := runner.authorizeSudo(ctx); err != nil {
			fmt.Fprintf(stderr, "错误：sudo 授权失败：%v\n", err)
			return 1
		}
	}

	currentPreview, err := loadPreview(ctx, runner, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "错误：读取清理预览失败：%v\n", err)
		return 1
	}
	printPreview(stdout, cfg, currentPreview)

	if !cfg.apply {
		fmt.Fprintln(stdout, "\n当前为只读预览；确认无误后加 --apply 执行永久删除。")
		return 0
	}
	if err := validateApplyPreview(cfg, currentPreview); err != nil {
		fmt.Fprintf(stderr, "\n拒绝执行：%v\n", err)
		return 1
	}

	var servicesStopped bool
	if cfg.restart {
		fmt.Fprintln(stdout, "\n正在停止 Memoh channel/server，以冻结写入并清除旧时间线缓存……")
		if err := runner.compose(ctx, cfg, "stop", "channel", "server"); err != nil {
			fmt.Fprintf(stderr, "错误：停止服务失败：%v\n", err)
			return 1
		}
		servicesStopped = true
	}

	startServices := func() error {
		if !servicesStopped {
			return nil
		}
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		fmt.Fprintln(stdout, "正在启动 Memoh server/channel 并等待健康检查……")
		err := runner.compose(recoveryCtx, cfg, "up", "-d", "--wait", "server", "channel")
		if err == nil {
			servicesStopped = false
		}
		return err
	}

	// Re-read after stopping the writers. This closes the preview/apply race for
	// normal operation and catches derived references created by an in-flight turn.
	if cfg.restart {
		currentPreview, err = loadPreview(ctx, runner, cfg)
		if err != nil {
			_ = startServices()
			fmt.Fprintf(stderr, "错误：停止服务后的二次预览失败：%v\n", err)
			return 1
		}
		if err := validateApplyPreview(cfg, currentPreview); err != nil {
			_ = startServices()
			fmt.Fprintf(stderr, "拒绝执行：二次检查发现：%v\n", err)
			return 1
		}
	}

	fmt.Fprintln(stdout, "正在事务内删除目标历史与时间线记录……")
	result, applyErr := applyDeletion(ctx, runner, cfg)
	startErr := startServices()
	if applyErr != nil {
		if startErr != nil {
			fmt.Fprintf(stderr, "错误：删除失败：%v；服务恢复也失败：%v\n", applyErr, startErr)
		} else {
			fmt.Fprintf(stderr, "错误：删除失败，事务已回滚：%v\n", applyErr)
		}
		return 1
	}
	if startErr != nil {
		fmt.Fprintf(stderr, "错误：删除已提交，但服务恢复失败：%v\n", startErr)
		return 1
	}

	verification, err := loadPreview(ctx, runner, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "错误：删除已提交，但最终核验失败：%v\n", err)
		return 1
	}
	if len(verification.Matches) != 0 {
		fmt.Fprintf(stderr, "错误：最终核验仍发现 %d 个匹配项\n", len(verification.Matches))
		return 1
	}

	fmt.Fprintf(
		stdout,
		"\n完成：删除 %d 条历史、%d 条时间线事件；失效并移除 %d 个压缩产物，涉及 %d 个会话。\n",
		result.HistoryCount,
		result.EventCount,
		result.CompactionCount,
		result.SessionCount,
	)
	fmt.Fprintln(stdout, "目标记录已永久删除，Memoh 数据库内未保留恢复副本。")
	return 0
}

// Usage output is best effort for the same reason as the command's status output.
//
//nolint:errcheck
func parseConfig(args []string, stderr io.Writer) (config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return config{}, fmt.Errorf("resolve home directory: %w", err)
	}

	var cfg config
	var messageIDs stringListFlag
	fs := flag.NewFlagSet("memoh-telegram-sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `用法：
  memoh-telegram-sync --chat-id -1004402809405 --message-id 4688
  memoh-telegram-sync --chat-id -1004402809405 --message-id 4688,4689 --apply

默认只显示预览。--apply 会永久删除目标群中所有 Memoh bot 对应的
历史消息和时间线事件，并重启 server/channel 以清除进程内缓存。

选项：`)
		fs.PrintDefaults()
	}

	fs.StringVar(&cfg.chatID, "chat-id", "", "Telegram chat_id（必填）")
	fs.Var(&messageIDs, "message-id", "Telegram message_id；可重复或用逗号分隔（必填）")
	fs.BoolVar(&cfg.apply, "apply", false, "执行永久删除；省略时只预览")
	fs.BoolVar(
		&cfg.allowReferences,
		"allow-references",
		false,
		"即使仍有消息回复/引用目标也继续（可能保留引用预览）",
	)
	fs.BoolVar(&cfg.restart, "restart", true, "执行时重启 Memoh server/channel 以刷新缓存")
	fs.BoolVar(&cfg.useSudo, "sudo", true, "通过 sudo 调用 docker")
	fs.StringVar(
		&cfg.composeDir,
		"compose-dir",
		filepath.Join(homeDir, "Memoh"),
		"Memoh Docker Compose 项目目录",
	)
	fs.StringVar(
		&cfg.postgresContainer,
		"postgres-container",
		defaultPostgresContainer,
		"PostgreSQL 容器名",
	)
	fs.StringVar(&cfg.postgresUser, "postgres-user", defaultPostgresUser, "PostgreSQL 用户")
	fs.StringVar(
		&cfg.postgresDatabase,
		"postgres-database",
		defaultPostgresDatabase,
		"PostgreSQL 数据库",
	)
	fs.DurationVar(&cfg.timeout, "timeout", 2*time.Minute, "单个数据库/容器操作超时")

	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	messageIDs = append(messageIDs, fs.Args()...)

	cfg.chatID, err = normalizeChatID(cfg.chatID)
	if err != nil {
		return config{}, err
	}
	cfg.messageIDs, err = normalizeMessageIDs(messageIDs)
	if err != nil {
		return config{}, err
	}
	cfg.composeDir, err = filepath.Abs(strings.TrimSpace(cfg.composeDir))
	if err != nil {
		return config{}, fmt.Errorf("invalid compose directory: %w", err)
	}
	if cfg.timeout <= 0 {
		return config{}, errors.New("--timeout must be positive")
	}
	for name, value := range map[string]string{
		"--postgres-container": cfg.postgresContainer,
		"--postgres-user":      cfg.postgresUser,
		"--postgres-database":  cfg.postgresDatabase,
	} {
		if !safeIdentifierPattern.MatchString(value) {
			return config{}, fmt.Errorf("%s contains unsupported characters", name)
		}
	}
	return cfg, nil
}

func normalizeChatID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("--chat-id is required")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value == 0 {
		return "", errors.New("--chat-id must be a non-zero Telegram numeric chat ID")
	}
	return strconv.FormatInt(value, 10), nil
}

func normalizeMessageIDs(raw []string) ([]string, error) {
	unique := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		for _, part := range strings.Split(item, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			value, err := strconv.ParseInt(part, 10, 64)
			if err != nil || value <= 0 {
				return nil, fmt.Errorf("invalid Telegram message_id %q", part)
			}
			unique[strconv.FormatInt(value, 10)] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return nil, errors.New("at least one --message-id is required")
	}
	result := make([]string, 0, len(unique))
	for id := range unique {
		result = append(result, id)
	}
	slices.SortFunc(result, func(a, b string) int {
		ai, _ := strconv.ParseInt(a, 10, 64)
		bi, _ := strconv.ParseInt(b, 10, 64)
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		default:
			return 0
		}
	})
	return result, nil
}

func loadPreview(parent context.Context, runner *commandRunner, cfg config) (preview, error) {
	ctx, cancel := context.WithTimeout(parent, cfg.timeout)
	defer cancel()
	output, err := runner.psql(ctx, cfg, buildPreviewSQL(cfg.chatID, cfg.messageIDs))
	if err != nil {
		return preview{}, err
	}
	var result preview
	if err := json.Unmarshal(bytes.TrimSpace(output), &result); err != nil {
		return preview{}, fmt.Errorf("decode preview JSON: %w (output: %q)", err, truncate(output, 400))
	}
	if result.Routes == nil {
		result.Routes = []routePreview{}
	}
	if result.Matches == nil {
		result.Matches = []matchPreview{}
	}
	if result.Missing == nil {
		result.Missing = []string{}
	}
	return result, nil
}

func applyDeletion(parent context.Context, runner *commandRunner, cfg config) (applyResult, error) {
	ctx, cancel := context.WithTimeout(parent, cfg.timeout)
	defer cancel()
	output, err := runner.psql(ctx, cfg, buildApplySQL(cfg.chatID, cfg.messageIDs))
	if err != nil {
		return applyResult{}, err
	}
	var result applyResult
	if err := json.Unmarshal(bytes.TrimSpace(output), &result); err != nil {
		return applyResult{}, fmt.Errorf("decode apply JSON: %w (output: %q)", err, truncate(output, 400))
	}
	return result, nil
}

func validateApplyPreview(cfg config, p preview) error {
	if p.RouteCount == 0 {
		return fmt.Errorf("chat_id %s 没有 Telegram 路由", cfg.chatID)
	}
	if len(p.Matches) == 0 {
		return fmt.Errorf("消息 %s 在 Memoh 中没有匹配记录", strings.Join(cfg.messageIDs, ","))
	}
	var memoryNodes, references int
	for _, match := range p.Matches {
		memoryNodes += match.MemoryNodeCount
		references += match.HistoryReplies + match.EventReplies
	}
	if memoryNodes > 0 {
		return fmt.Errorf(
			"发现 %d 个长期记忆节点引用目标；工具不会绕过记忆服务直接删除这些节点，请先在 Memoh 中清理对应记忆",
			memoryNodes,
		)
	}
	if references > 0 && !cfg.allowReferences {
		return fmt.Errorf(
			"发现 %d 条仍存在的回复/引用；请检查这些消息，或明确加 --allow-references 接受可能保留的引用预览",
			references,
		)
	}
	return nil
}

// Preview rendering cannot recover from a broken caller-provided output stream.
//
//nolint:errcheck
func printPreview(w io.Writer, cfg config, p preview) {
	fmt.Fprintln(w, "Telegram → Memoh 删除同步预览")
	fmt.Fprintf(w, "chat_id: %s\n", cfg.chatID)
	fmt.Fprintf(w, "message_id: %s\n", strings.Join(cfg.messageIDs, ", "))
	fmt.Fprintf(w, "Telegram 路由: %d\n", p.RouteCount)
	for _, route := range p.Routes {
		fmt.Fprintf(w, "  - %s (route %s)\n", emptyAs(route.BotName, "<unnamed>"), route.RouteID)
	}
	if len(p.Matches) == 0 {
		fmt.Fprintln(w, "匹配记录: 0")
	} else {
		fmt.Fprintf(w, "匹配记录组: %d\n", len(p.Matches))
		for _, match := range p.Matches {
			received := "-"
			if match.ReceivedAtMS > 0 {
				received = time.UnixMilli(match.ReceivedAtMS).Local().Format(time.RFC3339)
			}
			fmt.Fprintf(
				w,
				"  - bot=%s message=%s sender=%s time=%s history=%d events=%d replies=%d compactions=%d memory=%d\n",
				emptyAs(match.BotName, "<unnamed>"),
				match.MessageID,
				emptyAs(match.SenderName, "-"),
				received,
				match.HistoryCount,
				match.EventCount,
				match.HistoryReplies+match.EventReplies,
				match.CompactionCount,
				match.MemoryNodeCount,
			)
		}
	}
	if len(p.Missing) > 0 {
		fmt.Fprintf(w, "未找到的 message_id: %s\n", strings.Join(p.Missing, ", "))
	}
}

func (r *commandRunner) authorizeSudo(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "sudo", "-v")
	cmd.Stdin = r.stdin
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo -v: %w", err)
	}
	return nil
}

func (r *commandRunner) psql(ctx context.Context, cfg config, sql string) ([]byte, error) {
	args := []string{
		"exec",
		cfg.postgresContainer,
		"psql",
		"-X",
		"-q",
		"-t",
		"-A",
		"-v",
		"ON_ERROR_STOP=1",
		"-U",
		cfg.postgresUser,
		"-d",
		cfg.postgresDatabase,
		"--single-transaction",
		"-c",
		sql,
	}
	output, err := r.dockerOutput(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("psql in %s: %w", cfg.postgresContainer, err)
	}
	return bytes.TrimSpace(output), nil
}

func (r *commandRunner) compose(ctx context.Context, cfg config, args ...string) error {
	dockerArgs := []string{"compose", "--project-directory", cfg.composeDir}
	dockerArgs = append(dockerArgs, args...)
	cmd := r.dockerCommand(ctx, dockerArgs...)
	cmd.Stdin = r.stdin
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(dockerArgs, " "), err)
	}
	return nil
}

func (r *commandRunner) dockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := r.dockerCommand(ctx, args...)
	cmd.Stdin = r.stdin
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, truncate(output.Bytes(), 1000))
	}
	return output.Bytes(), nil
}

func (r *commandRunner) dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	if r.useSudo {
		sudoArgs := []string{"-n", "docker"}
		sudoArgs = append(sudoArgs, args...)
		return exec.CommandContext(ctx, "sudo", sudoArgs...)
	}
	return exec.CommandContext(ctx, "docker", args...) //nolint:gosec // Arguments are validated or assembled by this trusted local operator CLI.
}

func buildPreviewSQL(chatID string, messageIDs []string) string {
	return fmt.Sprintf(`
WITH requested(message_id) AS (
  VALUES %s
),
routes AS MATERIALIZED (
  SELECT
    route.team_id,
    route.id AS route_id,
    route.bot_id,
    bot.name AS bot_name
  FROM bot_channel_routes route
  JOIN bots bot
    ON bot.team_id = route.team_id
   AND bot.id = route.bot_id
  WHERE route.channel_type = 'telegram'
    AND route.external_conversation_id = %s
),
matched AS MATERIALIZED (
  SELECT DISTINCT
    route.team_id,
    route.route_id,
    route.bot_id,
    route.bot_name,
    session.id AS session_id,
    requested.message_id
  FROM routes route
  JOIN bot_sessions session
    ON session.team_id = route.team_id
   AND session.route_id = route.route_id
  CROSS JOIN requested
  WHERE EXISTS (
    SELECT 1
    FROM bot_history_messages history
    WHERE history.team_id = session.team_id
      AND history.session_id = session.id
      AND history.source_message_id = requested.message_id
  )
  OR EXISTS (
    SELECT 1
    FROM bot_session_events event
    WHERE event.team_id = session.team_id
      AND event.session_id = session.id
      AND event.external_message_id = requested.message_id
  )
),
details AS (
  SELECT
    matched.*,
    COALESCE((
      SELECT event.event_data->'sender'->>'display_name'
      FROM bot_session_events event
      WHERE event.team_id = matched.team_id
        AND event.session_id = matched.session_id
        AND event.external_message_id = matched.message_id
        AND event.event_kind = 'message'
      ORDER BY event.received_at_ms DESC, event.id DESC
      LIMIT 1
    ), '') AS sender_name,
    COALESCE((
      SELECT MAX(event.received_at_ms)
      FROM bot_session_events event
      WHERE event.team_id = matched.team_id
        AND event.session_id = matched.session_id
        AND event.external_message_id = matched.message_id
    ), 0)::bigint AS received_at_ms,
    (
      SELECT COUNT(*)
      FROM bot_history_messages history
      WHERE history.team_id = matched.team_id
        AND history.session_id = matched.session_id
        AND (
          history.source_message_id = matched.message_id
          OR history.event_id IN (
            SELECT event.id
            FROM bot_session_events event
            WHERE event.team_id = matched.team_id
              AND event.session_id = matched.session_id
              AND event.external_message_id = matched.message_id
          )
        )
    )::int AS history_count,
    (
      SELECT COUNT(*)
      FROM bot_session_events event
      WHERE event.team_id = matched.team_id
        AND event.session_id = matched.session_id
        AND event.external_message_id = matched.message_id
    )::int AS event_count,
    (
      SELECT COUNT(*)
      FROM bot_history_messages reply
      JOIN bot_sessions reply_session
        ON reply_session.team_id = reply.team_id
       AND reply_session.id = reply.session_id
      WHERE reply.team_id = matched.team_id
        AND reply_session.route_id = matched.route_id
        AND reply.source_reply_to_message_id = matched.message_id
    )::int AS history_reply_count,
    (
      SELECT COUNT(*)
      FROM bot_session_events reply
      JOIN bot_sessions reply_session
        ON reply_session.team_id = reply.team_id
       AND reply_session.id = reply.session_id
      WHERE reply.team_id = matched.team_id
        AND reply_session.route_id = matched.route_id
        AND (
          reply.event_data->>'reply_to_message_id' = matched.message_id
          OR reply.event_data->'reply'->>'message_id' = matched.message_id
        )
    )::int AS event_reply_count,
    (
      SELECT COUNT(*)
      FROM bot_history_message_compacts compact
      WHERE compact.team_id = matched.team_id
        AND compact.session_id = matched.session_id
        AND EXISTS (
          SELECT 1
          FROM bot_history_message_compacts contaminated
          WHERE contaminated.team_id = compact.team_id
            AND contaminated.session_id = compact.session_id
            AND (
              contaminated.id IN (
                SELECT history.compact_id
                FROM bot_history_messages history
                WHERE history.team_id = matched.team_id
                  AND history.session_id = matched.session_id
                  AND history.source_message_id = matched.message_id
                  AND history.compact_id IS NOT NULL
              )
              OR EXISTS (
                SELECT 1
                FROM jsonb_array_elements(
                  CASE
                    WHEN jsonb_typeof(contaminated.coverage) = 'array' THEN contaminated.coverage
                    ELSE '[]'::jsonb
                  END
                ) item
                WHERE item->>'external_message_id' = matched.message_id
                   OR item->'ref'->>'id' IN (
                     SELECT history.id::text
                     FROM bot_history_messages history
                     WHERE history.team_id = matched.team_id
                       AND history.session_id = matched.session_id
                       AND history.source_message_id = matched.message_id
                   )
              )
            )
        )
    )::int AS compaction_count,
    (
      SELECT COUNT(*)
      FROM memory_nodes memory
      WHERE memory.team_id = matched.team_id
        AND memory.bot_id = matched.bot_id
        AND (
          memory.source_message_ids ? matched.message_id
          OR EXISTS (
            SELECT 1
            FROM bot_history_messages history
            WHERE history.team_id = matched.team_id
              AND history.session_id = matched.session_id
              AND history.source_message_id = matched.message_id
              AND memory.source_message_ids ? history.id::text
          )
        )
    )::int AS memory_node_count
  FROM matched
)
SELECT jsonb_build_object(
  'route_count', (SELECT COUNT(*) FROM routes),
  'routes', COALESCE((
    SELECT jsonb_agg(
      jsonb_build_object(
        'bot_name', route.bot_name,
        'route_id', route.route_id
      )
      ORDER BY route.bot_name, route.route_id
    )
    FROM routes route
  ), '[]'::jsonb),
  'matches', COALESCE((
    SELECT jsonb_agg(
      jsonb_build_object(
        'bot_name', detail.bot_name,
        'route_id', detail.route_id,
        'session_id', detail.session_id,
        'message_id', detail.message_id,
        'sender_name', detail.sender_name,
        'received_at_ms', detail.received_at_ms,
        'history_count', detail.history_count,
        'event_count', detail.event_count,
        'history_reply_count', detail.history_reply_count,
        'event_reply_count', detail.event_reply_count,
        'compaction_count', detail.compaction_count,
        'memory_node_count', detail.memory_node_count
      )
      ORDER BY detail.message_id::bigint, detail.bot_name, detail.session_id
    )
    FROM details detail
  ), '[]'::jsonb),
  'missing', COALESCE((
    SELECT jsonb_agg(requested.message_id ORDER BY requested.message_id::bigint)
    FROM requested
    WHERE NOT EXISTS (
      SELECT 1
      FROM matched
      WHERE matched.message_id = requested.message_id
    )
  ), '[]'::jsonb)
);
`, requestedValues(messageIDs), sqlText(chatID))
}

func buildApplySQL(chatID string, messageIDs []string) string {
	return fmt.Sprintf(`
CREATE TEMP TABLE memoh_tg_requested (
  message_id text PRIMARY KEY
) ON COMMIT DROP;
INSERT INTO memoh_tg_requested(message_id)
VALUES %s;

CREATE TEMP TABLE memoh_tg_target_sessions ON COMMIT DROP AS
SELECT DISTINCT
  session.team_id,
  session.id AS session_id,
  session.bot_id
FROM bot_channel_routes route
JOIN bot_sessions session
  ON session.team_id = route.team_id
 AND session.route_id = route.id
CROSS JOIN memoh_tg_requested requested
WHERE route.channel_type = 'telegram'
  AND route.external_conversation_id = %s
  AND (
    EXISTS (
      SELECT 1
      FROM bot_history_messages history
      WHERE history.team_id = session.team_id
        AND history.session_id = session.id
        AND history.source_message_id = requested.message_id
    )
    OR EXISTS (
      SELECT 1
      FROM bot_session_events event
      WHERE event.team_id = session.team_id
        AND event.session_id = session.id
        AND event.external_message_id = requested.message_id
    )
  );

CREATE TEMP TABLE memoh_tg_target_events ON COMMIT DROP AS
SELECT event.team_id, event.id
FROM bot_session_events event
JOIN memoh_tg_target_sessions target
  ON target.team_id = event.team_id
 AND target.session_id = event.session_id
JOIN memoh_tg_requested requested
  ON requested.message_id = event.external_message_id;

CREATE TEMP TABLE memoh_tg_target_history ON COMMIT DROP AS
SELECT history.team_id, history.id
FROM bot_history_messages history
JOIN memoh_tg_target_sessions target
  ON target.team_id = history.team_id
 AND target.session_id = history.session_id
WHERE history.source_message_id IN (
  SELECT message_id FROM memoh_tg_requested
)
OR history.event_id IN (
  SELECT id FROM memoh_tg_target_events
);

CREATE TEMP TABLE memoh_tg_target_compactions ON COMMIT DROP AS
SELECT compact.team_id, compact.id
FROM bot_history_message_compacts compact
JOIN memoh_tg_target_sessions target
  ON target.team_id = compact.team_id
 AND target.session_id = compact.session_id
WHERE EXISTS (
  SELECT 1
  FROM bot_history_message_compacts contaminated
  WHERE contaminated.team_id = compact.team_id
    AND contaminated.session_id = compact.session_id
    AND (
      contaminated.id IN (
        SELECT history.compact_id
        FROM bot_history_messages history
        JOIN memoh_tg_target_history target_history
          ON target_history.team_id = history.team_id
         AND target_history.id = history.id
        WHERE history.compact_id IS NOT NULL
      )
      OR EXISTS (
        SELECT 1
        FROM jsonb_array_elements(
          CASE
            WHEN jsonb_typeof(contaminated.coverage) = 'array' THEN contaminated.coverage
            ELSE '[]'::jsonb
          END
        ) item
        WHERE item->>'external_message_id' IN (
          SELECT message_id FROM memoh_tg_requested
        )
        OR item->'ref'->>'id' IN (
          SELECT id::text FROM memoh_tg_target_history
        )
      )
    )
);

CREATE TEMP TABLE memoh_tg_locked_sessions ON COMMIT DROP AS
SELECT session.team_id, session.id AS session_id
FROM bot_sessions session
JOIN memoh_tg_target_sessions target
  ON target.team_id = session.team_id
 AND target.session_id = session.id
ORDER BY session.id
FOR UPDATE;

UPDATE bot_sessions session
SET compaction_epoch = session.compaction_epoch + 1
FROM memoh_tg_locked_sessions target
WHERE target.team_id = session.team_id
  AND target.session_id = session.id;

DELETE FROM bot_history_message_compacts compact
USING memoh_tg_target_compactions target
WHERE target.team_id = compact.team_id
  AND target.id = compact.id;

DELETE FROM bot_history_messages history
USING memoh_tg_target_history target
WHERE target.team_id = history.team_id
  AND target.id = history.id;

DELETE FROM bot_session_events event
USING memoh_tg_target_events target
WHERE target.team_id = event.team_id
  AND target.id = event.id;

SELECT jsonb_build_object(
  'session_count', (SELECT COUNT(*) FROM memoh_tg_target_sessions),
  'history_count', (SELECT COUNT(*) FROM memoh_tg_target_history),
  'event_count', (SELECT COUNT(*) FROM memoh_tg_target_events),
  'compaction_count', (SELECT COUNT(*) FROM memoh_tg_target_compactions)
);
`, requestedValues(messageIDs), sqlText(chatID))
}

func requestedValues(messageIDs []string) string {
	values := make([]string, 0, len(messageIDs))
	for _, id := range messageIDs {
		values = append(values, "("+sqlText(id)+")")
	}
	return strings.Join(values, ", ")
}

func sqlText(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func truncate(value []byte, limit int) string {
	text := strings.TrimSpace(string(value))
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func emptyAs(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
