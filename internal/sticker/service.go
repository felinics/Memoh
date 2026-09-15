package sticker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/config"
	memslug "github.com/felinics/memoh/internal/memory/slug"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

const (
	// defaultSearchLimit keeps a search result small enough to read in one
	// tool response; maxSearchLimit stops a library dump from evicting the
	// conversation it was supposed to help with.
	defaultSearchLimit = 10
	maxSearchLimit     = 50

	// entryDigestLength is how much of the identity digest goes into the file
	// name. 12 hex characters is 48 bits — collision-free at library scale,
	// and short enough that the name still reads as the pack it belongs to.
	entryDigestLength = 12

	// Library bounds. The workspace is writable by the agent and by the user,
	// so a "library" can be a directory of arbitrary junk; these keep one bot's
	// bad directory from spending the shared server's memory. A read that would
	// cross a bound fails loudly instead of returning a short list, because a
	// silently truncated library is indistinguishable from an empty one — the
	// exact failure this package already had once.
	maxLibraryFiles = 500
	maxEntryBytes   = 64 << 10
	maxLibraryBytes = 8 << 20

	overviewHeader = "# Sticker Library\n\n" +
		"<!-- Generated from stickers/*.md. Edit an entry file to change a description; this list is rewritten on every save. -->\n"
)

// errLibraryBudgetExhausted stops a listing that would read more of the
// workspace than the library is allowed to cost.
var errLibraryBudgetExhausted = errors.New("sticker library read budget exhausted")

// Service reads and writes the sticker library in a bot's workspace.
type Service struct {
	provider bridge.Provider
	logger   *slog.Logger
	now      func() time.Time
}

func New(log *slog.Logger, provider bridge.Provider) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		provider: provider,
		logger:   log.With(slog.String("component", "sticker")),
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func stickerDirPath() string { return path.Join(config.DefaultDataMount, "stickers") }
func stickerOverviewPath() string {
	return path.Join(config.DefaultDataMount, "STICKERS.md")
}

// entryFileName derives a stable, human-readable file name. The digest suffix
// is what makes it stable: the pack name can be renamed upstream or be missing
// entirely, and two stickers in one pack share everything else.
//
// The suffix is a digest rather than a prefix of the id itself. Platform ids
// are case-sensitive and full of symbols, so any lossy transcription of one —
// lowercasing, stripping punctuation, truncating — maps distinct stickers onto
// one name, and a save then overwrites a sticker it never looked at. The
// platform is folded in so two platforms cannot share a name by coincidence.
func entryFileName(entry Entry) string {
	base := memslug.Slugify(entry.Pack)
	if base == "" {
		base = "misc"
	}
	return base + "-" + entryDigest(entry) + ".md"
}

func entryDigest(entry Entry) string {
	platform := strings.TrimSpace(entry.Platform)
	if platform == "" {
		platform = PlatformTelegram
	}
	sum := sha256.Sum256([]byte(platform + "\x00" + entry.Identity()))
	return hex.EncodeToString(sum[:])[:entryDigestLength]
}

// uniqueEntryFileName resolves the astronomically unlikely digest collision,
// and the far likelier case of a hand-created file that already owns the name.
// Writing anyway would destroy a sticker the caller never asked about.
func uniqueEntryFileName(entry Entry, taken map[string]string) string {
	name := entryFileName(entry)
	identity := entry.Identity()
	for suffix := 2; ; suffix++ {
		owner, exists := taken[name]
		if !exists || owner == identity {
			return name
		}
		name = strings.TrimSuffix(entryFileName(entry), ".md") + "-" + strconv.Itoa(suffix) + ".md"
	}
}

// List returns every stored sticker, newest update first.
func (s *Service) List(ctx context.Context, botID string) ([]Entry, error) {
	if s == nil || s.provider == nil {
		return nil, ErrNotConfigured
	}
	client, err := s.provider.MCPClient(ctx, botID)
	if err != nil {
		return nil, err
	}
	// Bounded and non-recursive: the library is flat by construction, so a
	// recursive walk would only ever spend time on whatever else was dropped
	// under /data/stickers.
	files, err := client.ListDirBounded(ctx, stickerDirPath(), false, maxLibraryFiles)
	if err != nil {
		if errors.Is(err, bridge.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sticker library: %w", err)
	}
	entries := make([]Entry, 0, len(files))
	budget := int64(maxLibraryBytes)
	for _, file := range files {
		// ListDir reports names relative to the directory it listed. Handing
		// one straight to a read call would address /data/<name> — a path that
		// does not exist, whose read error this loop would then swallow, and
		// the library would read as empty however many stickers were saved.
		name := entryFileNameFromListing(file.GetPath())
		if file.GetIsDir() || name == "" || !strings.HasSuffix(name, ".md") {
			continue
		}
		content, err := readEntryFile(ctx, client, name, budget)
		if err != nil {
			if errors.Is(err, errLibraryBudgetExhausted) {
				return nil, fmt.Errorf("sticker library exceeds %d bytes; prune /data/stickers", maxLibraryBytes)
			}
			// One unreadable file must not hide the rest of the library: these
			// files are hand-editable, so a broken entry is a normal state.
			s.logger.Warn("read sticker entry failed", slog.String("name", name), slog.Any("error", err))
			continue
		}
		budget -= int64(len(content))
		entry, err := parseEntry(content)
		if err != nil {
			s.logger.Warn("parse sticker entry failed", slog.String("name", name), slog.Any("error", err))
			continue
		}
		if strings.TrimSpace(entry.Ref) == "" {
			continue
		}
		entry.Path = path.Join(stickerDirPath(), name)
		entries = append(entries, entry)
	}
	sortEntries(entries)
	return entries, nil
}

// entryFileNameFromListing normalizes what a listing reports into a name
// relative to the library directory, tolerating an absolute path in case a
// bridge implementation reports one.
func entryFileNameFromListing(listed string) string {
	trimmed := strings.TrimSpace(listed)
	if trimmed == "" {
		return ""
	}
	clean := path.Clean(trimmed)
	if rel, ok := strings.CutPrefix(clean, stickerDirPath()+"/"); ok {
		clean = rel
	}
	if path.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return ""
	}
	return clean
}

// readEntryFile reads one entry under the library root.
//
// The read is rooted and does not follow links, so a symlink dropped into the
// library cannot turn a sticker lookup into a read of anything else in the
// workspace. Both the per-file cap and the remaining library budget apply: an
// oversized single file is a broken entry and is skipped, while exhausting the
// budget stops the whole listing.
func readEntryFile(ctx context.Context, client *bridge.Client, name string, budget int64) (string, error) {
	if budget <= 0 {
		return "", errLibraryBudgetExhausted
	}
	reader, err := client.ReadRawNoFollow(ctx, stickerDirPath(), name)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()

	limit := int64(maxEntryBytes)
	if budget < limit {
		limit = budget
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limit {
		if limit < maxEntryBytes {
			return "", errLibraryBudgetExhausted
		}
		return "", fmt.Errorf("sticker entry exceeds %d bytes", maxEntryBytes)
	}
	return string(data), nil
}

// Search returns stored stickers matching every token in query. An empty
// query lists the library.
func (s *Service) Search(ctx context.Context, botID, query string, limit int) ([]Entry, error) {
	entries, err := s.List(ctx, botID)
	if err != nil {
		return nil, err
	}
	return filterEntries(entries, query, limit), nil
}

// FindByRef looks up a sticker by its platform reference or unique id.
func (s *Service) FindByRef(ctx context.Context, botID, ref string) (Entry, bool, error) {
	entries, err := s.List(ctx, botID)
	if err != nil {
		return Entry{}, false, err
	}
	entry, ok := findByRef(entries, ref)
	return entry, ok, nil
}

// Save writes a sticker and its description, merging with an existing entry
// for the same sticker, then rewrites the overview.
func (s *Service) Save(ctx context.Context, botID string, incoming Entry) (Entry, error) {
	if s == nil || s.provider == nil {
		return Entry{}, ErrNotConfigured
	}
	now := s.now()
	incoming = incoming.normalized(now)
	if incoming.Ref == "" {
		return Entry{}, ErrRefRequired
	}
	if incoming.Description == "" {
		return Entry{}, ErrDescRequired
	}
	entries, err := s.List(ctx, botID)
	if err != nil {
		return Entry{}, err
	}

	saved := incoming
	index := -1
	if existing, pos := findEntryIndex(entries, incoming); pos >= 0 {
		saved = existing.merge(incoming, now)
		saved.Path = existing.Path
		index = pos
	}
	if strings.TrimSpace(saved.Path) == "" {
		saved.Path = path.Join(stickerDirPath(), uniqueEntryFileName(saved, takenEntryFileNames(entries)))
	}

	content, err := formatEntry(saved)
	if err != nil {
		return Entry{}, err
	}
	client, err := s.provider.MCPClient(ctx, botID)
	if err != nil {
		return Entry{}, err
	}
	if err := client.WriteFile(ctx, saved.Path, []byte(content)); err != nil {
		return Entry{}, fmt.Errorf("write sticker entry: %w", err)
	}

	if index >= 0 {
		entries[index] = saved
	} else {
		entries = append(entries, saved)
	}
	sortEntries(entries)
	if err := client.WriteFile(ctx, stickerOverviewPath(), []byte(formatOverview(entries))); err != nil {
		// The entry is already durable and the overview is derived from it, so
		// a failed rewrite is stale-index territory, not lost work.
		s.logger.Warn("write sticker overview failed", slog.Any("error", err))
	}
	return saved, nil
}

// takenEntryFileNames maps each stored file name to the identity that owns it,
// so a new entry can tell "this name is mine" from "this name is someone
// else's".
func takenEntryFileNames(entries []Entry) map[string]string {
	taken := make(map[string]string, len(entries))
	for _, entry := range entries {
		if name := path.Base(strings.TrimSpace(entry.Path)); name != "" && name != "." && name != "/" {
			taken[name] = entry.Identity()
		}
	}
	return taken
}

func findEntryIndex(entries []Entry, target Entry) (Entry, int) {
	identity := target.Identity()
	for i, entry := range entries {
		if identity != "" && entry.Identity() == identity {
			return entry, i
		}
		if entry.Ref != "" && entry.Ref == target.Ref {
			return entry, i
		}
	}
	return Entry{}, -1
}

func findByRef(entries []Entry, ref string) (Entry, bool) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return Entry{}, false
	}
	for _, entry := range entries {
		if entry.Ref == trimmed || entry.UniqueID == trimmed {
			return entry, true
		}
	}
	return Entry{}, false
}

func filterEntries(entries []Entry, query string, limit int) []Entry {
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	tokens := queryTokens(query)
	matched := make([]Entry, 0, limit)
	for _, entry := range entries {
		if !matchTokens(entry, tokens) {
			continue
		}
		matched = append(matched, entry)
		if len(matched) == limit {
			break
		}
	}
	return matched
}

func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
		}
		return entries[i].Identity() < entries[j].Identity()
	})
}

// formatOverview renders the browsable index. It is grouped by pack because
// that is how a sticker library is actually used: the agent reaches for a
// voice ("the cat pack"), not for a timestamp.
func formatOverview(entries []Entry) string {
	byPack := map[string][]Entry{}
	for _, entry := range entries {
		pack := entry.Pack
		if pack == "" {
			pack = "Unpacked"
		}
		byPack[pack] = append(byPack[pack], entry)
	}
	packs := make([]string, 0, len(byPack))
	for pack := range byPack {
		packs = append(packs, pack)
	}
	sort.Strings(packs)

	var sb strings.Builder
	sb.WriteString(overviewHeader)
	if len(entries) == 0 {
		sb.WriteString("\nNo stickers saved yet.\n")
		return sb.String()
	}
	for _, pack := range packs {
		fmt.Fprintf(&sb, "\n## %s\n\n", pack)
		items := byPack[pack]
		sort.SliceStable(items, func(i, j int) bool { return items[i].Identity() < items[j].Identity() })
		for _, entry := range items {
			prefix := entry.Emoji
			if prefix == "" {
				prefix = "•"
			}
			fmt.Fprintf(&sb, "- %s [%s](%s)\n", prefix, overviewSummary(entry), overviewLink(entry))
		}
	}
	return sb.String()
}

// overviewSummary keeps one entry to one line. Markdown link text cannot hold
// a newline or an unescaped bracket without breaking the link.
func overviewSummary(entry Entry) string {
	summary := strings.TrimSpace(entry.Description)
	if idx := strings.IndexAny(summary, "\n\r"); idx >= 0 {
		summary = strings.TrimSpace(summary[:idx])
	}
	summary = strings.NewReplacer("[", "(", "]", ")").Replace(summary)
	if summary == "" {
		summary = "(no description)"
	}
	const maxSummaryRunes = 80
	runes := []rune(summary)
	if len(runes) > maxSummaryRunes {
		summary = string(runes[:maxSummaryRunes]) + "…"
	}
	return summary
}

func overviewLink(entry Entry) string {
	name := path.Base(strings.TrimSpace(entry.Path))
	if name == "" || name == "." || name == "/" {
		name = entryFileName(entry)
	}
	return "stickers/" + name
}
