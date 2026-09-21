package sticker

import (
	"strings"
	"testing"
	"time"
)

func TestFormatAndParseEntry_Roundtrip(t *testing.T) {
	t.Parallel()

	saved := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	entry := Entry{
		Platform:    PlatformTelegram,
		Ref:         "CAACAgUAAxkBAAEsticker",
		UniqueID:    "AgADkQ0AAjkjEEs",
		Pack:        "可莉表情包",
		Emoji:       "🎉",
		Kind:        KindAnimated,
		ContentHash: "abc123",
		Tags:        []string{"庆祝", "开心"},
		SavedAt:     saved,
		UpdatedAt:   saved,
		Description: "金发小女孩举着双手欢呼，头顶冒出爆炸特效。\n用来表达特别兴奋。",
	}

	content, err := formatEntry(entry)
	if err != nil {
		t.Fatalf("formatEntry: %v", err)
	}
	parsed, err := parseEntry(content)
	if err != nil {
		t.Fatalf("parseEntry: %v", err)
	}

	if parsed.Ref != entry.Ref || parsed.UniqueID != entry.UniqueID {
		t.Fatalf("identity lost: %+v", parsed)
	}
	if parsed.Pack != entry.Pack || parsed.Emoji != entry.Emoji || parsed.Kind != entry.Kind {
		t.Fatalf("metadata lost: %+v", parsed)
	}
	if parsed.ContentHash != entry.ContentHash {
		t.Fatalf("content hash lost: %q", parsed.ContentHash)
	}
	if strings.Join(parsed.Tags, ",") != strings.Join(entry.Tags, ",") {
		t.Fatalf("tags lost: %v", parsed.Tags)
	}
	// 多行描述是正文而不是 YAML 字段，换行必须原样保留。
	if parsed.Description != entry.Description {
		t.Fatalf("description = %q, want %q", parsed.Description, entry.Description)
	}
	if !parsed.SavedAt.Equal(saved) {
		t.Fatalf("saved_at = %v, want %v", parsed.SavedAt, saved)
	}
}

func TestParseEntry_RejectsFileWithoutFrontmatter(t *testing.T) {
	t.Parallel()

	if _, err := parseEntry("就是一段普通的 markdown\n"); err == nil {
		t.Fatal("parseEntry should reject a file without frontmatter")
	}
}

func TestParseEntry_AcceptsHandEditedBody(t *testing.T) {
	t.Parallel()

	// 条目文件就在工作区里，人和 agent 都会直接改正文；只要 frontmatter 还在，
	// 重写过的描述就必须被读回来。
	parsed, err := parseEntry("---\nplatform: telegram\nref: file-1\n---\n\n换了一句新的描述\n")
	if err != nil {
		t.Fatalf("parseEntry: %v", err)
	}
	if parsed.Ref != "file-1" || parsed.Description != "换了一句新的描述" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestEntryMerge_KeepsStoredProseWhenSightingHasNone(t *testing.T) {
	t.Parallel()

	first := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	stored := Entry{
		Platform:    PlatformTelegram,
		Ref:         "old-file-id",
		UniqueID:    "unique-1",
		Pack:        "猫猫",
		Description: "猫猫挥手",
		SavedAt:     first,
		UpdatedAt:   first,
	}

	// 同一张贴纸再次出现：file_id 会换，描述不该被清掉。
	refreshed := stored.merge(Entry{Ref: "new-file-id", UniqueID: "unique-1"}, later)
	if refreshed.Ref != "new-file-id" {
		t.Fatalf("ref = %q, want the fresh file id", refreshed.Ref)
	}
	if refreshed.Description != "猫猫挥手" {
		t.Fatalf("description = %q, want the stored prose", refreshed.Description)
	}
	if !refreshed.SavedAt.Equal(first) || !refreshed.UpdatedAt.Equal(later) {
		t.Fatalf("timestamps = %v/%v", refreshed.SavedAt, refreshed.UpdatedAt)
	}

	// 显式改写描述时，新描述取胜。
	rewritten := stored.merge(Entry{Ref: "old-file-id", UniqueID: "unique-1", Description: "猫猫在拍手"}, later)
	if rewritten.Description != "猫猫在拍手" {
		t.Fatalf("description = %q, want the new prose", rewritten.Description)
	}
}

func TestFilterEntries_AllTokensMustMatch(t *testing.T) {
	t.Parallel()

	entries := []Entry{
		{Ref: "a", Description: "猫猫开心地挥手", Emoji: "😺", Pack: "猫猫日常"},
		{Ref: "b", Description: "猫猫生气地转身", Emoji: "😾", Pack: "猫猫日常"},
		{Ref: "c", Description: "狗狗开心地摇尾巴", Emoji: "🐶", Pack: "狗狗日常", Tags: []string{"庆祝"}},
	}

	got := filterEntries(entries, "猫猫 开心", 0)
	if len(got) != 1 || got[0].Ref != "a" {
		t.Fatalf("query = %v, want only the happy cat", refs(got))
	}
	if got := filterEntries(entries, "庆祝", 0); len(got) != 1 || got[0].Ref != "c" {
		t.Fatalf("tag query = %v, want the tagged sticker", refs(got))
	}
	if got := filterEntries(entries, "", 0); len(got) != 3 {
		t.Fatalf("empty query = %v, want the whole library", refs(got))
	}
	if got := filterEntries(entries, "", 2); len(got) != 2 {
		t.Fatalf("limit ignored: %v", refs(got))
	}
}

func TestEntryFileName_StableAcrossPackRename(t *testing.T) {
	t.Parallel()

	entry := Entry{Ref: "file-1", UniqueID: "AgADkQ0AAjkjEEs", Pack: "Klee Pack"}
	name := entryFileName(entry)
	if !strings.HasPrefix(name, "klee-pack-") || !strings.HasSuffix(name, ".md") {
		t.Fatalf("file name = %q", name)
	}
	// 同一张贴纸即使换了包名，后缀仍然由稳定的 unique id 决定。
	renamed := entryFileName(Entry{Ref: "file-1", UniqueID: "AgADkQ0AAjkjEEs", Pack: "Klee"})
	if suffix(name) != suffix(renamed) {
		t.Fatalf("suffix changed with the pack name: %q vs %q", name, renamed)
	}
	// 没有 unique id 时退回 ref，仍然要产出一个可用的名字。
	if got := entryFileName(Entry{Ref: "file-1"}); !strings.HasPrefix(got, "misc-") {
		t.Fatalf("unpacked file name = %q", got)
	}
}

func TestFormatOverview_GroupsByPackAndFlattensProse(t *testing.T) {
	t.Parallel()

	overview := formatOverview([]Entry{
		{Ref: "a", UniqueID: "u-a", Pack: "猫猫日常", Emoji: "😺", Description: "猫猫挥手\n第二行不该出现在索引里", Path: "/data/stickers/maomao-ua.md"},
		{Ref: "b", UniqueID: "u-b", Emoji: "🎉", Description: "撒花", Path: "/data/stickers/misc-ub.md"},
	})

	if strings.Contains(overview, "第二行不该出现在索引里") {
		t.Fatalf("overview should keep one line per sticker:\n%s", overview)
	}
	if !strings.Contains(overview, "## 猫猫日常") || !strings.Contains(overview, "## Unpacked") {
		t.Fatalf("overview should group by pack:\n%s", overview)
	}
	if !strings.Contains(overview, "(stickers/maomao-ua.md)") {
		t.Fatalf("overview should link the entry file:\n%s", overview)
	}
}

func refs(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Ref)
	}
	return out
}

func suffix(fileName string) string {
	idx := strings.LastIndex(fileName, "-")
	if idx < 0 {
		return fileName
	}
	return fileName[idx:]
}
