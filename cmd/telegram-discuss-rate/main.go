// Command telegram-discuss-rate lists and updates per-bot sampling rates for
// passive Telegram messages in discuss sessions.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/team"
)

const (
	defaultPostgresContainer = "memoh-postgres"
	defaultPostgresUser      = "memoh"
	defaultPostgresDatabase  = "memoh"
)

var safeIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type config struct {
	useSudo           bool
	postgresContainer string
	postgresUser      string
	postgresDatabase  string
	timeout           time.Duration
	action            string
	selector          string
	rate              float64
	keyword           string
}

type botRate struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	DisplayName        string   `json:"display_name"`
	ConfiguredRate     *float64 `json:"configured_rate"`
	ForceReplyKeywords []string `json:"force_reply_keywords"`
}

func (b botRate) effectiveRate() float64 {
	if b.ConfiguredRate != nil && *b.ConfiguredRate >= 0 && *b.ConfiguredRate <= 1 {
		return *b.ConfiguredRate
	}
	return channel.DefaultTelegramDiscussPassiveSampleRate
}

func (b botRate) source() string {
	if b.ConfiguredRate != nil && *b.ConfiguredRate >= 0 && *b.ConfiguredRate <= 1 {
		return "自定义"
	}
	return "默认"
}

type rateStore interface {
	List(context.Context) ([]botRate, error)
	Set(context.Context, string, float64) (botRate, error)
	Reset(context.Context, string) (botRate, error)
	AddKeyword(context.Context, string, string) (botRate, bool, error)
	RemoveKeyword(context.Context, string, string) (botRate, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := parseConfig(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "错误：%v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	if cfg.useSudo {
		cmd := exec.CommandContext(ctx, "sudo", "-v")
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			_, _ = fmt.Fprintf(stderr, "错误：sudo 授权失败：%v\n", err)
			return 1
		}
	}

	store := &dockerRateStore{cfg: cfg}
	return execute(ctx, cfg, store, stdout, stderr)
}

func execute(ctx context.Context, cfg config, store rateStore, stdout, stderr io.Writer) int {
	switch cfg.action {
	case "list":
		bots, err := store.List(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "错误：读取 Bot 比例失败：%v\n", err)
			return 1
		}
		printBots(stdout, bots)
	case "set":
		bot, err := store.Set(ctx, cfg.selector, cfg.rate)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "错误：设置比例失败：%v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(
			stdout,
			"已更新 %s (%s)：Telegram discuss 被动消息触发比例为 %s，立即生效。\n",
			bot.Name,
			bot.ID,
			formatPercent(bot.effectiveRate()),
		)
	case "reset":
		bot, err := store.Reset(ctx, cfg.selector)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "错误：重置比例失败：%v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(
			stdout,
			"已重置 %s (%s)：当前使用默认比例 %s，立即生效。\n",
			bot.Name,
			bot.ID,
			formatPercent(bot.effectiveRate()),
		)
	case "keyword-add":
		bot, changed, err := store.AddKeyword(ctx, cfg.selector, cfg.keyword)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "错误：添加关键词失败：%v\n", err)
			return 1
		}
		if changed {
			_, _ = fmt.Fprintf(stdout, "已为 %s (%s) 添加强制回复关键词 %q，立即生效。\n", bot.Name, bot.ID, cfg.keyword)
		} else {
			_, _ = fmt.Fprintf(stdout, "%s (%s) 已存在强制回复关键词 %q，未做变更。\n", bot.Name, bot.ID, cfg.keyword)
		}
	case "keyword-remove":
		bot, err := store.RemoveKeyword(ctx, cfg.selector, cfg.keyword)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "错误：删除关键词失败：%v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(
			stdout,
			"已从 %s (%s) 删除强制回复关键词 %q，立即生效。\n",
			bot.Name,
			bot.ID,
			cfg.keyword,
		)
	}
	return 0
}

func parseConfig(args []string, stderr io.Writer) (config, error) {
	cfg := config{
		useSudo:           true,
		postgresContainer: defaultPostgresContainer,
		postgresUser:      defaultPostgresUser,
		postgresDatabase:  defaultPostgresDatabase,
		timeout:           30 * time.Second,
	}

	fs := flag.NewFlagSet("telegram-discuss-rate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, `用法：
  go run ./cmd/telegram-discuss-rate list
  go run ./cmd/telegram-discuss-rate set <bot名称或ID> <0-100百分比>
  go run ./cmd/telegram-discuss-rate reset <bot名称或ID>
  go run ./cmd/telegram-discuss-rate keyword-add <bot名称或ID> <关键词>
  go run ./cmd/telegram-discuss-rate keyword-remove <bot名称或ID> <关键词>

示例：
  go run ./cmd/telegram-discuss-rate set alice 40
  go run ./cmd/telegram-discuss-rate set alice 12.5%
  go run ./cmd/telegram-discuss-rate reset alice
  go run ./cmd/telegram-discuss-rate keyword-add alice "小雪"
  go run ./cmd/telegram-discuss-rate keyword-remove alice "小雪"

关键词采用不区分英文大小写的子串匹配。命中后跳过随机比例并强制回复。
修改后立即生效，不需要重启服务。

选项：`)
		fs.PrintDefaults()
	}
	fs.BoolVar(&cfg.useSudo, "sudo", cfg.useSudo, "通过 sudo 调用 docker")
	fs.StringVar(&cfg.postgresContainer, "postgres-container", cfg.postgresContainer, "PostgreSQL 容器名")
	fs.StringVar(&cfg.postgresUser, "postgres-user", cfg.postgresUser, "PostgreSQL 用户")
	fs.StringVar(&cfg.postgresDatabase, "postgres-database", cfg.postgresDatabase, "PostgreSQL 数据库")
	fs.DurationVar(&cfg.timeout, "timeout", cfg.timeout, "操作超时")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if cfg.timeout <= 0 {
		return config{}, errors.New("--timeout 必须大于 0")
	}
	for name, value := range map[string]string{
		"--postgres-container": cfg.postgresContainer,
		"--postgres-user":      cfg.postgresUser,
		"--postgres-database":  cfg.postgresDatabase,
	} {
		if !safeIdentifierPattern.MatchString(value) {
			return config{}, fmt.Errorf("%s 包含不支持的字符", name)
		}
	}

	positional := fs.Args()
	if len(positional) == 0 {
		cfg.action = "list"
		return cfg, nil
	}
	cfg.action = strings.ToLower(strings.TrimSpace(positional[0]))
	switch cfg.action {
	case "list":
		if len(positional) != 1 {
			return config{}, errors.New("list 不接受额外参数")
		}
	case "set":
		if len(positional) != 3 {
			return config{}, errors.New("set 需要 Bot 名称或 ID，以及 0-100 的百分比")
		}
		cfg.selector = strings.TrimSpace(positional[1])
		if cfg.selector == "" {
			return config{}, errors.New("bot 名称或 ID 不能为空")
		}
		rate, err := parsePercent(positional[2])
		if err != nil {
			return config{}, err
		}
		cfg.rate = rate
	case "reset":
		if len(positional) != 2 {
			return config{}, errors.New("reset 需要 Bot 名称或 ID")
		}
		cfg.selector = strings.TrimSpace(positional[1])
		if cfg.selector == "" {
			return config{}, errors.New("bot 名称或 ID 不能为空")
		}
	case "keyword-add", "keyword-remove":
		if len(positional) != 3 {
			return config{}, fmt.Errorf("%s 需要 Bot 名称或 ID，以及一个关键词", cfg.action)
		}
		cfg.selector = strings.TrimSpace(positional[1])
		cfg.keyword = strings.TrimSpace(positional[2])
		if cfg.selector == "" {
			return config{}, errors.New("bot 名称或 ID 不能为空")
		}
		if cfg.keyword == "" {
			return config{}, errors.New("关键词不能为空")
		}
	default:
		return config{}, fmt.Errorf(
			"未知命令 %q；可用命令：list、set、reset、keyword-add、keyword-remove",
			cfg.action,
		)
	}
	return cfg, nil
}

func parsePercent(raw string) (float64, error) {
	value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%"))
	percent, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 || percent > 100 {
		return 0, fmt.Errorf("比例 %q 无效；请输入 0 到 100 的百分比", raw)
	}
	return percent / 100, nil
}

func formatPercent(rate float64) string {
	return strconv.FormatFloat(rate*100, 'f', -1, 64) + "%"
}

func printBots(w io.Writer, bots []botRate) {
	if len(bots) == 0 {
		_, _ = fmt.Fprintln(w, "没有找到 Bot。")
		return
	}
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "NAME\tDISPLAY NAME\tRATE\tSOURCE\tFORCE KEYWORDS\tID")
	for _, bot := range bots {
		keywords := "-"
		if len(bot.ForceReplyKeywords) > 0 {
			keywords = strings.Join(bot.ForceReplyKeywords, ", ")
		}
		_, _ = fmt.Fprintf(
			table,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			bot.Name,
			bot.DisplayName,
			formatPercent(bot.effectiveRate()),
			bot.source(),
			keywords,
			bot.ID,
		)
	}
	_ = table.Flush()
}

type dockerRateStore struct {
	cfg config
}

func (s *dockerRateStore) List(ctx context.Context) ([]botRate, error) {
	output, err := s.psql(ctx, listBotsSQL)
	if err != nil {
		return nil, err
	}
	var bots []botRate
	if err := json.Unmarshal(bytes.TrimSpace(output), &bots); err != nil {
		return nil, fmt.Errorf("解析 Bot 列表：%w", err)
	}
	return bots, nil
}

func (s *dockerRateStore) Set(ctx context.Context, selector string, rate float64) (botRate, error) {
	bot, err := s.resolve(ctx, selector)
	if err != nil {
		return botRate{}, err
	}
	if _, err := s.psql(
		ctx,
		setRateSQL,
		"bot_id="+bot.ID,
		"rate="+strconv.FormatFloat(rate, 'f', -1, 64),
	); err != nil {
		return botRate{}, err
	}
	return s.resolve(ctx, bot.ID)
}

func (s *dockerRateStore) Reset(ctx context.Context, selector string) (botRate, error) {
	bot, err := s.resolve(ctx, selector)
	if err != nil {
		return botRate{}, err
	}
	if _, err := s.psql(ctx, resetRateSQL, "bot_id="+bot.ID); err != nil {
		return botRate{}, err
	}
	return s.resolve(ctx, bot.ID)
}

func (s *dockerRateStore) AddKeyword(ctx context.Context, selector, keyword string) (botRate, bool, error) {
	bot, err := s.resolve(ctx, selector)
	if err != nil {
		return botRate{}, false, err
	}
	for _, existing := range bot.ForceReplyKeywords {
		if strings.EqualFold(existing, keyword) {
			return bot, false, nil
		}
	}
	keywords := append(append([]string(nil), bot.ForceReplyKeywords...), keyword)
	updated, err := s.updateKeywords(ctx, bot.ID, keywords)
	return updated, err == nil, err
}

func (s *dockerRateStore) RemoveKeyword(ctx context.Context, selector, keyword string) (botRate, error) {
	bot, err := s.resolve(ctx, selector)
	if err != nil {
		return botRate{}, err
	}
	keywords := make([]string, 0, len(bot.ForceReplyKeywords))
	found := false
	for _, existing := range bot.ForceReplyKeywords {
		if strings.EqualFold(existing, keyword) {
			found = true
			continue
		}
		keywords = append(keywords, existing)
	}
	if !found {
		return botRate{}, fmt.Errorf("bot %q 没有关键词 %q", bot.Name, keyword)
	}
	return s.updateKeywords(ctx, bot.ID, keywords)
}

func (s *dockerRateStore) updateKeywords(ctx context.Context, botID string, keywords []string) (botRate, error) {
	raw, err := json.Marshal(keywords)
	if err != nil {
		return botRate{}, fmt.Errorf("编码关键词：%w", err)
	}
	if _, err := s.psql(
		ctx,
		updateKeywordsSQL,
		"bot_id="+botID,
		"keywords="+string(raw),
	); err != nil {
		return botRate{}, err
	}
	return s.resolve(ctx, botID)
}

func (s *dockerRateStore) resolve(ctx context.Context, selector string) (botRate, error) {
	bots, err := s.List(ctx)
	if err != nil {
		return botRate{}, err
	}
	var matches []botRate
	for _, bot := range bots {
		if bot.ID == selector || bot.Name == selector {
			matches = append(matches, bot)
		}
	}
	switch len(matches) {
	case 0:
		return botRate{}, fmt.Errorf("找不到 Bot %q", selector)
	case 1:
		return matches[0], nil
	default:
		return botRate{}, fmt.Errorf("bot 选择器 %q 同时匹配名称和 ID，请改用明确的 ID", selector)
	}
}

func (s *dockerRateStore) psql(ctx context.Context, query string, variables ...string) ([]byte, error) {
	args := []string{
		"exec",
		"-i",
		s.cfg.postgresContainer,
		"psql",
		"-X",
		"-q",
		"-t",
		"-A",
		"-v",
		"ON_ERROR_STOP=1",
	}
	for _, variable := range variables {
		args = append(args, "-v", variable)
	}
	args = append(args, "-U", s.cfg.postgresUser, "-d", s.cfg.postgresDatabase, "-f", "-")

	var cmd *exec.Cmd
	if s.cfg.useSudo {
		cmdArgs := append([]string{"-n", "docker"}, args...)
		cmd = exec.CommandContext(ctx, "sudo", cmdArgs...) //nolint:gosec // arguments are passed directly without a shell
	} else {
		cmd = exec.CommandContext(ctx, "docker", args...) //nolint:gosec // arguments are passed directly without a shell
	}
	cmd.Stdin = strings.NewReader(query)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("操作超时")
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("在 %s 中执行 psql：%s", s.cfg.postgresContainer, detail)
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}

const listBotsSQL = `
SELECT COALESCE(
  json_agg(
    json_build_object(
      'id', id::text,
      'name', name,
      'display_name', COALESCE(display_name, ''),
      'configured_rate',
        CASE
          WHEN jsonb_typeof(metadata -> '` + channel.TelegramDiscussPassiveSampleRateMetadataKey + `') = 'number'
          THEN (metadata ->> '` + channel.TelegramDiscussPassiveSampleRateMetadataKey + `')::double precision
          ELSE NULL
        END,
      'force_reply_keywords',
        CASE
          WHEN jsonb_typeof(metadata -> '` + channel.TelegramDiscussForceReplyKeywordsMetadataKey + `') = 'array'
          THEN metadata -> '` + channel.TelegramDiscussForceReplyKeywordsMetadataKey + `'
          ELSE '[]'::jsonb
        END
    )
    ORDER BY name
  ),
  '[]'::json
)::text
FROM bots
WHERE team_id = '` + team.DefaultTeamID + `'::uuid;`

const setRateSQL = `
UPDATE bots
SET metadata = jsonb_set(
      COALESCE(metadata, '{}'::jsonb),
      '{` + channel.TelegramDiscussPassiveSampleRateMetadataKey + `}',
      to_jsonb((:rate)::double precision),
      true
    ),
    updated_at = now()
WHERE team_id = '` + team.DefaultTeamID + `'::uuid
  AND id = (:'bot_id')::uuid;`

const resetRateSQL = `
UPDATE bots
SET metadata = COALESCE(metadata, '{}'::jsonb) - '` + channel.TelegramDiscussPassiveSampleRateMetadataKey + `',
    updated_at = now()
WHERE team_id = '` + team.DefaultTeamID + `'::uuid
  AND id = (:'bot_id')::uuid;`

const updateKeywordsSQL = `
UPDATE bots
SET metadata = jsonb_set(
      COALESCE(metadata, '{}'::jsonb),
      '{` + channel.TelegramDiscussForceReplyKeywordsMetadataKey + `}',
      (:'keywords')::jsonb,
      true
    ),
    updated_at = now()
WHERE team_id = '` + team.DefaultTeamID + `'::uuid
  AND id = (:'bot_id')::uuid;`
