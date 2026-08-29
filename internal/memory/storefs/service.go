package storefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/felinics/memoh/internal/config"
	memslug "github.com/felinics/memoh/internal/memory/slug"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	entryHeadingPrefix = "## Entry "
	yamlFence          = "```yaml"
	codeFence          = "```"
)

var ErrNotConfigured = errors.New("memory filesystem not configured")

// scanEntry maps a memory ID to the file that contains it and the parsed item
// from that file.
type scanEntry struct {
	FilePath string
	Item     MemoryItem
}

type Service struct {
	provider bridge.Provider
	logger   *slog.Logger
}

type MemoryItem struct {
	ID        string         `json:"id"`
	Memory    string         `json:"memory"`
	Hash      string         `json:"hash,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	Score     float64        `json:"score,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	BotID     string         `json:"bot_id,omitempty"`
	AgentID   string         `json:"agent_id,omitempty"`
	RunID     string         `json:"run_id,omitempty"`
}

type memoryEntryMeta struct {
	ID        string         `yaml:"id"`
	Hash      string         `yaml:"hash,omitempty"`
	CreatedAt string         `yaml:"created_at,omitempty"`
	UpdatedAt string         `yaml:"updated_at,omitempty"`
	Metadata  map[string]any `yaml:"metadata,omitempty"`
}

type conceptFrontmatter struct {
	Type       string         `yaml:"type"`
	Title      string         `yaml:"title,omitempty"`
	ID         string         `yaml:"id"`
	Layer      string         `yaml:"layer"`
	Tags       []string       `yaml:"tags,omitempty"`
	Subject    string         `yaml:"subject,omitempty"`
	Confidence *float64       `yaml:"confidence,omitempty"`
	ProfileRef string         `yaml:"profile_ref,omitempty"`
	Hash       string         `yaml:"hash,omitempty"`
	Timestamp  string         `yaml:"timestamp,omitempty"`
	UpdatedAt  string         `yaml:"updated_at,omitempty"`
	Metadata   map[string]any `yaml:"metadata,omitempty"`
}

type conceptFilePlan struct {
	Item     MemoryItem
	Layer    string
	Slug     string
	FilePath string
	RelPath  string
	Title    string
}

type conceptLinkTarget struct {
	Layer   string
	Slug    string
	RelPath string
}

type conceptPersistWrite struct {
	Plan        conceptFilePlan
	OldFilePath string
}

func New(log *slog.Logger, provider bridge.Provider) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{provider: provider, logger: log.With(slog.String("component", "storefs"))}
}

func (s *Service) client(ctx context.Context, botID string) (*bridge.Client, error) {
	if s.provider == nil {
		return nil, ErrNotConfigured
	}
	return s.provider.MCPClient(ctx, botID)
}

func (s *Service) readFile(ctx context.Context, botID, filePath string) (string, error) {
	c, err := s.client(ctx, botID)
	if err != nil {
		return "", err
	}
	reader, err := c.ReadRaw(ctx, filePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Service) writeFile(ctx context.Context, botID, filePath, content string) error {
	c, err := s.client(ctx, botID)
	if err != nil {
		return err
	}
	return c.WriteFile(ctx, filePath, []byte(content))
}

func (s *Service) deleteFile(ctx context.Context, botID, filePath string, recursive bool) error {
	c, err := s.client(ctx, botID)
	if err != nil {
		return err
	}
	return c.DeleteFile(ctx, filePath, recursive)
}

// buildScanIndex scans all memory markdown files and builds a map of id -> file path.
func (s *Service) buildScanIndex(ctx context.Context, botID string) (map[string]scanEntry, error) {
	c, err := s.client(ctx, botID)
	if err != nil {
		return nil, err
	}
	entries, err := c.ListDirAll(ctx, memoryDirPath(), true)
	if err != nil {
		if isNotFound(err) {
			return map[string]scanEntry{}, nil
		}
		return nil, err
	}
	index := make(map[string]scanEntry)
	for _, entry := range entries {
		if entry.GetIsDir() || !strings.HasSuffix(entry.GetPath(), ".md") {
			continue
		}
		entryPath := memoryEntryPath(entry.GetPath())
		content, readErr := s.readFile(ctx, botID, entryPath)
		if readErr != nil {
			s.logger.Warn("buildScanIndex: failed to read memory file",
				slog.String("bot_id", botID), slog.String("path", entryPath), slog.Any("error", readErr))
			continue
		}
		parsed, parseErr := parseMemoryFile(content)
		if parseErr != nil {
			s.logger.Warn("buildScanIndex: failed to parse memory file",
				slog.String("bot_id", botID), slog.String("path", entryPath), slog.Any("error", parseErr))
			continue
		}
		for _, item := range parsed {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				continue
			}
			item = normalizeMemoryItem(item)
			if _, ok := index[id]; !ok {
				index[id] = scanEntry{FilePath: entryPath, Item: item}
			}
		}
	}
	return index, nil
}

func (s *Service) PersistMemories(ctx context.Context, botID string, items []MemoryItem, _ map[string]any) error {
	if s.provider == nil {
		return ErrNotConfigured
	}
	if len(items) == 0 {
		return nil
	}
	index, err := s.buildScanIndex(ctx, botID)
	if err != nil {
		return err
	}
	writes, linkIndex, overviewItems, overviewPaths := planConceptPersistence(index, items)
	if len(writes) == 0 {
		return nil
	}
	newPaths := make(map[string]struct{}, len(writes))
	for _, write := range writes {
		newPaths[write.Plan.FilePath] = struct{}{}
	}
	removals := make(map[string]map[string]struct{})
	for _, write := range writes {
		oldPath := strings.TrimSpace(write.OldFilePath)
		if oldPath == "" || oldPath == write.Plan.FilePath {
			continue
		}
		if _, reusedByIncoming := newPaths[oldPath]; reusedByIncoming {
			continue
		}
		if removals[oldPath] == nil {
			removals[oldPath] = map[string]struct{}{}
		}
		removals[oldPath][write.Plan.Item.ID] = struct{}{}
	}
	if err := s.removeIDsFromFiles(ctx, botID, removals); err != nil {
		return err
	}
	for _, write := range writes {
		if err := s.writeConceptFile(ctx, botID, write.Plan, linkIndex); err != nil {
			return err
		}
	}
	return s.writeFile(ctx, botID, memoryOverviewPath(), formatMemoryOverviewMDWithPaths(overviewItems, overviewPaths))
}

func (s *Service) RebuildFiles(ctx context.Context, botID string, items []MemoryItem, _ map[string]any) error {
	if s.provider == nil {
		return ErrNotConfigured
	}
	// RebuildFiles regenerates the derived /data/memory projection from the
	// authoritative node set (`items`) without wiping the whole tree. Only
	// files that are recognizably DB projections — every parsed entry carries
	// the "<botID>:" id prefix minted by the wiki store — are removed before
	// re-rendering, so stale projections of deleted nodes go away while
	// agent-authored files that have not been ingested yet (unprefixed or
	// missing frontmatter ids, or free-form markdown that does not parse)
	// survive every sync until POST /bots/:bot_id/memory/ingest or /rebuild
	// imports them as DB nodes.
	if err := s.removeProjectionFiles(ctx, botID); err != nil {
		return err
	}
	plans := planConceptFiles(items)
	linkIndex := conceptLinkIndex(plans)
	for _, plan := range plans {
		if err := s.writeConceptFile(ctx, botID, plan, linkIndex); err != nil {
			return err
		}
	}
	return s.SyncOverview(ctx, botID)
}

// removeProjectionFiles deletes the markdown files under /data/memory that are
// projections of DB nodes (see isProjectionItems), leaving agent-authored
// un-ingested files untouched.
func (s *Service) removeProjectionFiles(ctx context.Context, botID string) error {
	entries, err := s.listMemoryMarkdownEntries(ctx, botID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		entryPath := memoryEntryPath(entry.GetPath())
		content, readErr := s.readFile(ctx, botID, entryPath)
		if readErr != nil {
			s.logger.Warn("rebuild: failed to read memory file; preserving it",
				slog.String("bot_id", botID), slog.String("path", entryPath), slog.Any("error", readErr))
			continue
		}
		parsed, parseErr := parseMemoryFile(content)
		if parseErr != nil || !isProjectionItems(botID, parsed) {
			continue
		}
		if err := s.deleteFile(ctx, botID, entryPath, false); err != nil && !isNotFound(err) {
			return err
		}
	}
	return nil
}

// isProjectionItems reports whether a parsed memory file is a projection of DB
// nodes: every entry id carries the canonical "<botID>:" prefix minted by the
// wiki store (runtimeMemoryID / qualifyIngestID). Agent-authored files use
// unprefixed ids (or none), so any entry without the prefix marks the file as
// not ours to delete.
func isProjectionItems(botID string, items []MemoryItem) bool {
	if len(items) == 0 {
		return false
	}
	prefix := botID + ":"
	for _, item := range items {
		if !strings.HasPrefix(strings.TrimSpace(item.ID), prefix) {
			return false
		}
	}
	return true
}

func (s *Service) RemoveMemories(ctx context.Context, botID string, ids []string) error {
	if s.provider == nil {
		return ErrNotConfigured
	}
	if len(ids) == 0 {
		return nil
	}
	index, err := s.buildScanIndex(ctx, botID)
	if err != nil {
		return err
	}
	removals := make(map[string]map[string]struct{})
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		targets := make([]string, 0, 1)
		if entry, ok := index[id]; ok {
			targets = append(targets, entry.FilePath)
		}
		for _, target := range targets {
			if removals[target] == nil {
				removals[target] = map[string]struct{}{}
			}
			removals[target][id] = struct{}{}
		}
	}
	if err := s.removeIDsFromFiles(ctx, botID, removals); err != nil {
		return err
	}
	return s.SyncOverview(ctx, botID)
}

func (s *Service) RemoveAllMemories(ctx context.Context, botID string) error {
	if s.provider == nil {
		return ErrNotConfigured
	}
	if err := s.deleteFile(ctx, botID, memoryDirPath(), true); err != nil && !isNotFound(err) {
		return err
	}
	return s.SyncOverview(ctx, botID)
}

func (s *Service) ReadAllMemoryFiles(ctx context.Context, botID string) ([]MemoryItem, error) {
	if s.provider == nil {
		return nil, ErrNotConfigured
	}
	c, err := s.client(ctx, botID)
	if err != nil {
		return nil, err
	}
	entries, err := c.ListDirAll(ctx, memoryDirPath(), true)
	if err != nil {
		if isNotFound(err) {
			return []MemoryItem{}, nil
		}
		return nil, err
	}
	items := make([]MemoryItem, 0, len(entries))
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.GetIsDir() || !strings.HasSuffix(entry.GetPath(), ".md") {
			continue
		}
		entryPath := memoryEntryPath(entry.GetPath())
		content, readErr := s.readFile(ctx, botID, entryPath)
		if readErr != nil {
			continue
		}
		parsed, parseErr := parseMemoryFile(content)
		if parseErr != nil {
			continue
		}
		for _, item := range parsed {
			if strings.TrimSpace(item.ID) == "" {
				continue
			}
			if _, ok := seen[item.ID]; ok {
				continue
			}
			seen[item.ID] = struct{}{}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return memoryTime(items[i]).Before(memoryTime(items[j]))
	})
	return items, nil
}

// ReadAllMemoryFilesForIngest is like ReadAllMemoryFiles but does not drop items
// whose YAML frontmatter lacks an `id`. Instead it synthesises a deterministic
// id from the file path + body so re-ingesting the same file is idempotent
// (collides on the same row via UpsertNode's ON CONFLICT(id)). Items that
// already declare an `id` keep it verbatim, preserving agent-authored control.
//
// This is the file→DB ingest entry point: the derived-markdown rebuild path
// (RebuildFiles) regenerates files from DB nodes and preserves un-ingested
// agent-authored files, but without this ingest the agent's directly-written
// /data/memory/*.md would stay invisible to search_memory.
func (s *Service) ReadAllMemoryFilesForIngest(ctx context.Context, botID string) ([]MemoryItem, error) {
	items, err := s.ReadAllMemoryFiles(ctx, botID)
	if err != nil {
		return nil, err
	}
	// ReadAllMemoryFiles already dropped empty-ID items; re-scan the raw files
	// to recover them with a synthesised deterministic id.
	entries, err := s.listMemoryMarkdownEntries(ctx, botID)
	if err != nil {
		return nil, err
	}
	existing := make(map[string]struct{}, len(items))
	for _, it := range items {
		existing[it.ID] = struct{}{}
	}
	for _, entry := range entries {
		content, readErr := s.readFile(ctx, botID, memoryEntryPath(entry.GetPath()))
		if readErr != nil {
			continue
		}
		parsed, parseErr := parseMemoryFile(content)
		if parseErr != nil {
			continue
		}
		for _, item := range parsed {
			if strings.TrimSpace(item.ID) != "" {
				continue // ReadAllMemoryFiles already returned it.
			}
			item.ID = synthIngestID(entry.GetPath(), item.Memory)
			if _, ok := existing[item.ID]; ok {
				continue
			}
			existing[item.ID] = struct{}{}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return memoryTime(items[i]).Before(memoryTime(items[j]))
	})
	return items, nil
}

// listMemoryMarkdownEntries lists .md files under /data/memory for a bot.
func (s *Service) listMemoryMarkdownEntries(ctx context.Context, botID string) ([]*pb.FileEntry, error) {
	if s.provider == nil {
		return nil, ErrNotConfigured
	}
	c, err := s.client(ctx, botID)
	if err != nil {
		return nil, err
	}
	entries, err := c.ListDirAll(ctx, memoryDirPath(), true)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]*pb.FileEntry, 0, len(entries))
	for _, e := range entries {
		if e.GetIsDir() || !strings.HasSuffix(e.GetPath(), ".md") {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// synthIngestID derives a deterministic node id for an agent-authored memory
// file that lacks a frontmatter id. It is bot-agnostic; callers prefix the
// bot id. The inputs are the file's relative path and body so editing the same
// file updates the same node, while a different file with the same body gets a
// distinct id.
func synthIngestID(relPath, body string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(relPath) + "\n" + strings.TrimSpace(body)))
	return hex.EncodeToString(h[:8])
}

func (s *Service) CountMemoryFiles(ctx context.Context, botID string) (int, error) {
	if s.provider == nil {
		return 0, ErrNotConfigured
	}
	c, err := s.client(ctx, botID)
	if err != nil {
		return 0, err
	}
	entries, err := c.ListDirAll(ctx, memoryDirPath(), true)
	if err != nil {
		if isNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.GetIsDir() || !strings.HasSuffix(entry.GetPath(), ".md") {
			continue
		}
		count++
	}
	return count, nil
}

func (s *Service) SyncOverview(ctx context.Context, botID string) error {
	if s.provider == nil {
		return ErrNotConfigured
	}
	items, err := s.ReadAllMemoryFiles(ctx, botID)
	if err != nil {
		return err
	}
	return s.writeFile(ctx, botID, memoryOverviewPath(), formatMemoryOverviewMD(items))
}

func (s *Service) readMemoryDay(ctx context.Context, botID, filePath string) ([]MemoryItem, error) {
	content, err := s.readFile(ctx, botID, filePath)
	if err != nil {
		if isNotFound(err) {
			return []MemoryItem{}, nil
		}
		return nil, err
	}
	items, parseErr := parseMemoryFile(content)
	if parseErr == nil {
		return items, nil
	}
	return []MemoryItem{}, nil
}

func (s *Service) writeMemoryDay(ctx context.Context, botID, filePath string, items []MemoryItem) error {
	date := strings.TrimSuffix(path.Base(filePath), ".md")
	return s.writeFile(ctx, botID, filePath, formatMemoryDayMD(date, items))
}

func (s *Service) writeConceptFile(ctx context.Context, botID string, plan conceptFilePlan, links map[string]conceptLinkTarget) error {
	return s.writeFile(ctx, botID, plan.FilePath, formatConceptMD(plan, links))
}

func (s *Service) removeIDsFromFiles(ctx context.Context, botID string, removals map[string]map[string]struct{}) error {
	for filePath, ids := range removals {
		if len(ids) == 0 {
			continue
		}
		items, err := s.readMemoryDay(ctx, botID, filePath)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			continue
		}
		filtered := make([]MemoryItem, 0, len(items))
		for _, item := range items {
			if _, remove := ids[item.ID]; remove {
				continue
			}
			filtered = append(filtered, item)
		}
		if len(filtered) == 0 {
			if err := s.deleteFile(ctx, botID, filePath, false); err != nil && !isNotFound(err) {
				return err
			}
			continue
		}
		if isConceptFilePath(filePath) && len(filtered) == 1 {
			plans := planConceptFiles(filtered)
			if len(plans) == 0 {
				continue
			}
			if err := s.writeConceptFile(ctx, botID, plans[0], conceptLinkIndex(plans)); err != nil {
				return err
			}
			continue
		}
		if err := s.writeMemoryDay(ctx, botID, filePath, filtered); err != nil {
			return err
		}
	}
	return nil
}

// --- path helpers ---

func memoryOverviewPath() string { return path.Join(config.DefaultDataMount, "MEMORY.md") }
func memoryDirPath() string      { return path.Join(config.DefaultDataMount, "memory") }

func memoryConceptPath(layer, slug string) string {
	layer = conceptLayerName(layer)
	slug = conceptSlugName(slug)
	return path.Join(memoryDirPath(), layer, slug+".md")
}

func memoryConceptRelPath(layer, slug string) string {
	layer = conceptLayerName(layer)
	slug = conceptSlugName(slug)
	return path.Join("memory", layer, slug+".md")
}

func memoryEntryPath(entryPath string) string {
	entryPath = strings.TrimSpace(entryPath)
	if entryPath == "" {
		return memoryDirPath()
	}
	clean := path.Clean(entryPath)
	if strings.HasPrefix(clean, config.DefaultDataMount+"/") || clean == config.DefaultDataMount {
		return clean
	}
	return path.Join(memoryDirPath(), clean)
}

func memoryRelPath(filePath string) string {
	clean := path.Clean(strings.TrimSpace(filePath))
	if clean == "." {
		return ""
	}
	dataRoot := path.Clean(config.DefaultDataMount)
	if clean == dataRoot {
		return ""
	}
	if strings.HasPrefix(clean, dataRoot+"/") {
		return strings.TrimPrefix(clean, dataRoot+"/")
	}
	return clean
}

func relativeMemoryLink(fromRelPath, toRelPath string) string {
	toRelPath = path.Clean(strings.TrimSpace(toRelPath))
	if toRelPath == "." {
		return ""
	}
	fromRelPath = path.Clean(strings.TrimSpace(fromRelPath))
	if fromRelPath == "." {
		return toRelPath
	}
	fromParts := splitPathParts(path.Dir(fromRelPath))
	toParts := splitPathParts(toRelPath)
	common := 0
	for common < len(fromParts) && common < len(toParts) && fromParts[common] == toParts[common] {
		common++
	}
	relParts := make([]string, 0, len(fromParts)-common+len(toParts)-common)
	for i := common; i < len(fromParts); i++ {
		relParts = append(relParts, "..")
	}
	relParts = append(relParts, toParts[common:]...)
	if len(relParts) == 0 {
		return toRelPath
	}
	return path.Join(relParts...)
}

func splitPathParts(filePath string) []string {
	clean := strings.Trim(path.Clean(strings.TrimSpace(filePath)), "/")
	if clean == "" || clean == "." {
		return nil
	}
	return strings.Split(clean, "/")
}

func isConceptFilePath(filePath string) bool {
	rel := strings.TrimPrefix(path.Clean(filePath), path.Clean(memoryDirPath())+"/")
	return rel != filePath && strings.Contains(rel, "/")
}

// --- format / parse helpers ---

func formatConceptMD(plan conceptFilePlan, links map[string]conceptLinkTarget) string {
	item := normalizeMemoryItem(plan.Item)
	body := strings.TrimSpace(item.Memory)
	if len(links) > 0 {
		body = memslug.RenderWikiLinks(body, func(_ string, normalized string) (string, bool) {
			target, ok := links[normalized]
			if !ok {
				return "", false
			}
			targetRelPath := firstNonEmpty(target.RelPath, memoryConceptRelPath(target.Layer, target.Slug))
			return relativeMemoryLink(plan.RelPath, targetRelPath), true
		})
	}

	fm := conceptFrontmatter{
		Type:       firstNonEmpty(metadataStringVal(item.Metadata, "fact_type"), metadataStringVal(item.Metadata, "type"), "memory"),
		Title:      firstNonEmpty(plan.Title, conceptTitle(item)),
		ID:         item.ID,
		Layer:      conceptLayer(item),
		Subject:    metadataStringVal(item.Metadata, "subject"),
		ProfileRef: metadataStringVal(item.Metadata, "profile_ref"),
		Hash:       strings.TrimSpace(item.Hash),
		Timestamp:  firstNonEmpty(strings.TrimSpace(item.CreatedAt), strings.TrimSpace(item.UpdatedAt)),
		UpdatedAt:  strings.TrimSpace(item.UpdatedAt),
		Metadata:   cleanMetadata(item.Metadata),
	}
	if topic := metadataStringVal(item.Metadata, "topic"); topic != "" {
		fm.Tags = []string{topic}
	}
	if confidence, ok := metadataFloat64(item.Metadata, "confidence"); ok {
		fm.Confidence = &confidence
	}
	rawMeta, _ := yaml.Marshal(fm)
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(strings.TrimSpace(string(rawMeta)))
	b.WriteString("\n---\n\n")
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}

func parseConceptMD(content string) (MemoryItem, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return MemoryItem{}, errors.New("empty concept file")
	}
	if !strings.HasPrefix(content, "---") {
		return MemoryItem{}, errors.New("missing frontmatter")
	}
	lines := strings.Split(content, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return MemoryItem{}, errors.New("missing frontmatter delimiter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end <= 1 {
		return MemoryItem{}, errors.New("missing frontmatter end")
	}
	var fm conceptFrontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fm); err != nil {
		return MemoryItem{}, err
	}
	if strings.TrimSpace(fm.Type) == "" {
		return MemoryItem{}, errors.New("frontmatter type is required")
	}
	body := strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	if body == "" {
		return MemoryItem{}, errors.New("empty concept body")
	}
	meta := cleanMetadata(fm.Metadata)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["fact_type"] = strings.TrimSpace(fm.Type)
	if fm.Layer != "" {
		meta["layer"] = conceptLayerName(fm.Layer)
	}
	if fm.Subject != "" {
		meta["subject"] = strings.TrimSpace(fm.Subject)
	} else if fm.Title != "" {
		meta["subject"] = strings.TrimSpace(fm.Title)
	}
	if fm.ProfileRef != "" {
		meta["profile_ref"] = strings.TrimSpace(fm.ProfileRef)
	}
	if fm.Confidence != nil {
		meta["confidence"] = *fm.Confidence
	}
	if len(fm.Tags) > 0 {
		meta["topic"] = strings.TrimSpace(fm.Tags[0])
	}
	return MemoryItem{
		ID:        strings.TrimSpace(fm.ID),
		Hash:      strings.TrimSpace(fm.Hash),
		CreatedAt: strings.TrimSpace(fm.Timestamp),
		UpdatedAt: strings.TrimSpace(fm.UpdatedAt),
		Metadata:  meta,
		Memory:    memslug.RestoreMarkdownFileLinks(body),
	}, nil
}

func parseMemoryFile(content string) ([]MemoryItem, error) {
	if item, err := parseConceptMD(content); err == nil {
		return []MemoryItem{item}, nil
	}
	if items, err := parseMemoryDayMD(content); err == nil {
		return items, nil
	}
	return parseJSONMemoryItems(content)
}

func formatMemoryDayMD(date string, items []MemoryItem) string {
	var b strings.Builder
	b.WriteString("# Memory ")
	b.WriteString(date)
	b.WriteString("\n\n")
	sort.Slice(items, func(i, j int) bool {
		ti, tj := memoryTime(items[i]), memoryTime(items[j])
		if ti.Equal(tj) {
			return items[i].ID < items[j].ID
		}
		return ti.Before(tj)
	})
	for _, item := range items {
		item.ID = strings.TrimSpace(item.ID)
		item.Memory = strings.TrimSpace(item.Memory)
		if item.ID == "" || item.Memory == "" {
			continue
		}
		meta := memoryEntryMeta{
			ID:        item.ID,
			Hash:      item.Hash,
			CreatedAt: item.CreatedAt,
			UpdatedAt: item.UpdatedAt,
			Metadata:  item.Metadata,
		}
		rawMeta, _ := yaml.Marshal(meta)
		b.WriteString(entryHeadingPrefix)
		b.WriteString(item.ID)
		b.WriteString("\n\n")
		b.WriteString(yamlFence)
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(string(rawMeta)))
		b.WriteString("\n")
		b.WriteString(codeFence)
		b.WriteString("\n\n")
		b.WriteString(item.Memory)
		b.WriteString("\n\n")
	}
	return b.String()
}

func parseMemoryDayMD(content string) ([]MemoryItem, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errors.New("empty memory file")
	}
	lines := strings.Split(content, "\n")
	items := make([]MemoryItem, 0, 8)
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, entryHeadingPrefix) {
			continue
		}
		entryID := strings.TrimSpace(strings.TrimPrefix(line, entryHeadingPrefix))
		j := i + 1
		for ; j < len(lines) && strings.TrimSpace(lines[j]) == ""; j++ {
		}
		if j >= len(lines) || strings.TrimSpace(lines[j]) != yamlFence {
			continue
		}
		metaStart := j + 1
		metaEnd := metaStart
		for ; metaEnd < len(lines); metaEnd++ {
			if strings.TrimSpace(lines[metaEnd]) == codeFence {
				break
			}
		}
		if metaEnd >= len(lines) {
			break
		}
		var meta memoryEntryMeta
		if err := yaml.Unmarshal([]byte(strings.Join(lines[metaStart:metaEnd], "\n")), &meta); err != nil {
			continue
		}
		bodyStart := metaEnd + 1
		if bodyStart < len(lines) && strings.TrimSpace(lines[bodyStart]) == "" {
			bodyStart++
		}
		bodyEnd := bodyStart
		for ; bodyEnd < len(lines); bodyEnd++ {
			if strings.HasPrefix(strings.TrimSpace(lines[bodyEnd]), entryHeadingPrefix) {
				break
			}
		}
		item := MemoryItem{
			ID:        firstNonEmpty(meta.ID, entryID),
			Hash:      strings.TrimSpace(meta.Hash),
			CreatedAt: strings.TrimSpace(meta.CreatedAt),
			UpdatedAt: strings.TrimSpace(meta.UpdatedAt),
			Metadata:  meta.Metadata,
			Memory:    strings.TrimSpace(strings.Join(lines[bodyStart:bodyEnd], "\n")),
		}
		if item.ID != "" && item.Memory != "" {
			items = append(items, item)
		}
		i = bodyEnd - 1
	}
	if len(items) == 0 {
		return nil, errors.New("no memory entries found")
	}
	return items, nil
}

type jsonMemoryRecord struct {
	Topic     string `json:"topic"`
	ID        string `json:"id"`
	Memory    string `json:"memory"`
	Text      string `json:"text"`
	Content   string `json:"content"`
	Hash      string `json:"hash"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func parseJSONMemoryItems(content string) ([]MemoryItem, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, errors.New("empty memory file")
	}
	var list []jsonMemoryRecord
	if err := json.Unmarshal([]byte(content), &list); err == nil {
		return normalizeJSONMemoryItems(list), nil
	}
	var obj jsonMemoryRecord
	if err := json.Unmarshal([]byte(content), &obj); err == nil {
		return normalizeJSONMemoryItems([]jsonMemoryRecord{obj}), nil
	}
	var wrapped struct {
		Items []jsonMemoryRecord `json:"items"`
	}
	if err := json.Unmarshal([]byte(content), &wrapped); err == nil {
		return normalizeJSONMemoryItems(wrapped.Items), nil
	}
	return nil, errors.New("not json memory format")
}

func normalizeJSONMemoryItems(records []jsonMemoryRecord) []MemoryItem {
	now := time.Now().UTC()
	items := make([]MemoryItem, 0, len(records))
	for _, record := range records {
		text := strings.TrimSpace(record.Memory)
		if text == "" {
			text = strings.TrimSpace(record.Content)
		}
		if text == "" {
			text = strings.TrimSpace(record.Text)
		}
		if text == "" {
			continue
		}
		item := MemoryItem{
			ID:        strings.TrimSpace(record.ID),
			Hash:      strings.TrimSpace(record.Hash),
			CreatedAt: strings.TrimSpace(record.CreatedAt),
			UpdatedAt: strings.TrimSpace(record.UpdatedAt),
			Memory:    text,
		}
		if item.ID == "" {
			item.ID = "mem_" + strconv.FormatInt(now.UnixNano(), 10)
		}
		if item.CreatedAt == "" {
			item.CreatedAt = now.Format(time.RFC3339)
		}
		if item.UpdatedAt == "" {
			item.UpdatedAt = item.CreatedAt
		}
		if item.Hash == "" {
			item.Hash = "json_" + strconv.FormatInt(now.UnixNano(), 10)
		}
		if topic := strings.TrimSpace(record.Topic); topic != "" {
			item.Metadata = map[string]any{"topic": topic}
		}
		items = append(items, item)
	}
	return items
}

func formatMemoryOverviewMD(items []MemoryItem) string {
	return formatMemoryOverviewMDWithPaths(items, nil)
}

func formatMemoryOverviewMDWithPaths(items []MemoryItem, relPaths map[string]string) string {
	var b strings.Builder
	b.WriteString("# MEMORY\n\n")
	if len(items) == 0 {
		b.WriteString("> No memory entries yet.\n")
		return b.String()
	}
	plans := planConceptFiles(items)
	sort.Slice(plans, func(i, j int) bool {
		ti, tj := memoryTime(plans[i].Item), memoryTime(plans[j].Item)
		if ti.Equal(tj) {
			return plans[i].Item.ID > plans[j].Item.ID
		}
		return ti.After(tj)
	})
	for i, plan := range plans {
		if i >= 500 {
			break
		}
		item := plan.Item
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = "unknown"
		}
		created := strings.TrimSpace(item.CreatedAt)
		if created == "" {
			created = "unknown"
		}
		body := strings.TrimSpace(item.Memory)
		if body == "" {
			continue
		}
		body = strings.Join(strings.Fields(body), " ")
		if len(body) > 400 {
			body = strings.TrimSpace(body[:400]) + "..."
		}
		b.WriteString(strconv.Itoa(i + 1))
		title := firstNonEmpty(plan.Title, created, id)
		b.WriteString(". [")
		b.WriteString(title)
		b.WriteString("](")
		b.WriteString(firstNonEmpty(relPaths[id], plan.RelPath))
		b.WriteString(") ")
		b.WriteString(body)
		b.WriteString("\n")
	}
	return b.String()
}

// --- utility helpers ---

func planConceptPersistence(index map[string]scanEntry, items []MemoryItem) ([]conceptPersistWrite, map[string]conceptLinkTarget, []MemoryItem, map[string]string) {
	existing := make(map[string]MemoryItem, len(index))
	relPaths := make(map[string]string, len(index)+len(items))
	reservedPaths := make(map[string]string, len(index)+len(items))
	for id, entry := range index {
		id = strings.TrimSpace(id)
		item := normalizeMemoryItem(entry.Item)
		if id == "" {
			id = item.ID
		}
		if id == "" || item.ID == "" || item.Memory == "" {
			continue
		}
		existing[id] = item
		if relPath := memoryRelPath(entry.FilePath); relPath != "" {
			relPaths[id] = relPath
		}
		if filePath := strings.TrimSpace(entry.FilePath); filePath != "" {
			if _, ok := reservedPaths[filePath]; !ok {
				reservedPaths[filePath] = id
			}
		}
	}

	incoming := make(map[string]MemoryItem, len(items))
	for _, item := range items {
		item = normalizeMemoryItem(item)
		if item.ID == "" || item.Memory == "" {
			continue
		}
		incoming[item.ID] = item
	}

	merged := make(map[string]MemoryItem, len(existing)+len(incoming))
	for id, item := range existing {
		merged[id] = item
	}
	for id, item := range incoming {
		merged[id] = item
	}

	ids := sortedMemoryIDs(incoming)
	writes := make([]conceptPersistWrite, 0, len(ids))
	for _, id := range ids {
		item := incoming[id]
		plan := planConceptFileWithReservedPath(item, reservedPaths)
		reservedPaths[plan.FilePath] = id
		relPaths[id] = plan.RelPath
		oldFilePath := ""
		if entry, ok := index[id]; ok {
			oldFilePath = entry.FilePath
		}
		writes = append(writes, conceptPersistWrite{Plan: plan, OldFilePath: oldFilePath})
	}

	overviewItems := mapToItems(merged)
	return writes, conceptLinkIndexForItems(overviewItems, relPaths), overviewItems, relPaths
}

func planConceptFileWithReservedPath(item MemoryItem, reservedPaths map[string]string) conceptFilePlan {
	item = normalizeMemoryItem(item)
	layer := conceptLayer(item)
	baseSlug := conceptSlugName(memslug.NodeSlug(item.ID, metadataStringVal(item.Metadata, "subject"), metadataStringVal(item.Metadata, "topic")))
	for suffix := 1; ; suffix++ {
		slug := baseSlug
		if suffix > 1 {
			slug = baseSlug + "-" + strconv.Itoa(suffix)
		}
		filePath := memoryConceptPath(layer, slug)
		if owner, ok := reservedPaths[filePath]; ok && owner != item.ID {
			continue
		}
		return conceptFilePlan{
			Item:     item,
			Layer:    layer,
			Slug:     slug,
			FilePath: filePath,
			RelPath:  memoryConceptRelPath(layer, slug),
			Title:    conceptTitle(item),
		}
	}
}

func planConceptFiles(items []MemoryItem) []conceptFilePlan {
	normalized := make([]MemoryItem, 0, len(items))
	for _, item := range items {
		item = normalizeMemoryItem(item)
		if item.ID == "" || item.Memory == "" {
			continue
		}
		normalized = append(normalized, item)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })

	used := map[string]int{}
	plans := make([]conceptFilePlan, 0, len(normalized))
	for _, item := range normalized {
		layer := conceptLayer(item)
		baseSlug := conceptSlugName(memslug.NodeSlug(item.ID, metadataStringVal(item.Metadata, "subject"), metadataStringVal(item.Metadata, "topic")))
		key := layer + "/" + baseSlug
		used[key]++
		finalSlug := baseSlug
		if used[key] > 1 {
			finalSlug = baseSlug + "-" + strconv.Itoa(used[key])
		}
		plans = append(plans, conceptFilePlan{
			Item:     item,
			Layer:    layer,
			Slug:     finalSlug,
			FilePath: memoryConceptPath(layer, finalSlug),
			RelPath:  memoryConceptRelPath(layer, finalSlug),
			Title:    conceptTitle(item),
		})
	}
	return plans
}

func conceptSlugName(slug string) string {
	slug = memslug.Slugify(slug)
	if slug == "" {
		return "memory"
	}
	switch slug {
	case "index", "log":
		return slug + "-memory"
	default:
		return slug
	}
}

func conceptLinkIndex(plans []conceptFilePlan) map[string]conceptLinkTarget {
	index := make(map[string]conceptLinkTarget, len(plans)*2)
	for _, plan := range plans {
		target := conceptLinkTarget{Layer: plan.Layer, Slug: plan.Slug, RelPath: plan.RelPath}
		index[plan.Slug] = target
		base := memslug.NodeSlug(plan.Item.ID, metadataStringVal(plan.Item.Metadata, "subject"), metadataStringVal(plan.Item.Metadata, "topic"))
		if base != "" {
			if _, exists := index[base]; !exists {
				index[base] = target
			}
		}
	}
	return index
}

func conceptLinkIndexForItems(items []MemoryItem, relPaths map[string]string) map[string]conceptLinkTarget {
	plans := planConceptFiles(items)
	index := make(map[string]conceptLinkTarget, len(plans)*2)
	for _, plan := range plans {
		relPath := firstNonEmpty(relPaths[plan.Item.ID], plan.RelPath)
		target := conceptLinkTarget{Layer: plan.Layer, Slug: plan.Slug, RelPath: relPath}
		if layer, slug, ok := conceptLayerSlugFromRelPath(relPath); ok {
			target.Layer = layer
			target.Slug = slug
		}
		index[target.Slug] = target
		base := memslug.NodeSlug(plan.Item.ID, metadataStringVal(plan.Item.Metadata, "subject"), metadataStringVal(plan.Item.Metadata, "topic"))
		if base != "" {
			if _, exists := index[base]; !exists {
				index[base] = target
			}
		}
	}
	return index
}

func conceptLayerSlugFromRelPath(relPath string) (string, string, bool) {
	clean := path.Clean(strings.TrimSpace(relPath))
	if clean == "." {
		return "", "", false
	}
	parts := strings.Split(clean, "/")
	if len(parts) != 3 || parts[0] != "memory" || !strings.HasSuffix(parts[2], ".md") {
		return "", "", false
	}
	slug := strings.TrimSuffix(parts[2], ".md")
	if strings.TrimSpace(parts[1]) == "" || slug == "" {
		return "", "", false
	}
	return conceptLayerName(parts[1]), slug, true
}

func sortedMemoryIDs(items map[string]MemoryItem) []string {
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func normalizeMemoryItem(item MemoryItem) MemoryItem {
	item.ID = strings.TrimSpace(item.ID)
	item.Memory = strings.TrimSpace(item.Memory)
	item.Hash = strings.TrimSpace(item.Hash)
	item.CreatedAt = strings.TrimSpace(item.CreatedAt)
	item.UpdatedAt = strings.TrimSpace(item.UpdatedAt)
	item.Metadata = cleanMetadata(item.Metadata)
	return item
}

func conceptLayer(item MemoryItem) string {
	return conceptLayerName(metadataStringVal(item.Metadata, "layer"))
}

func conceptLayerName(layer string) string {
	switch strings.ToLower(strings.TrimSpace(layer)) {
	case "preference":
		return "preference"
	case "identity":
		return "identity"
	case "context":
		return "context"
	case "experience":
		return "experience"
	case "activity":
		return "activity"
	case "persona":
		return "persona"
	default:
		return "note"
	}
}

func conceptTitle(item MemoryItem) string {
	if subject := metadataStringVal(item.Metadata, "subject"); subject != "" {
		return subject
	}
	body := strings.TrimSpace(item.Memory)
	if idx := strings.IndexByte(body, '\n'); idx >= 0 {
		body = strings.TrimSpace(body[:idx])
	}
	body = strings.Join(strings.Fields(body), " ")
	if len(body) > 80 {
		body = strings.TrimSpace(body[:80]) + "..."
	}
	return body
}

func cleanMetadata(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		key = strings.TrimSpace(key)
		if key == "" || value == nil {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func metadataStringVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(strings.Trim(fmt.Sprint(v), "\""))
}

func metadataFloat64(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func isNotFound(err error) bool {
	return errors.Is(err, bridge.ErrNotFound)
}

func mapToItems(m map[string]MemoryItem) []MemoryItem {
	items := make([]MemoryItem, 0, len(m))
	for _, item := range m {
		items = append(items, item)
	}
	return items
}

func memoryTime(item MemoryItem) time.Time {
	parse := func(v string) (time.Time, bool) {
		v = strings.TrimSpace(v)
		if v == "" {
			return time.Time{}, false
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC(), true
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC(), true
		}
		return time.Time{}, false
	}
	if t, ok := parse(item.CreatedAt); ok {
		return t
	}
	if t, ok := parse(item.UpdatedAt); ok {
		return t
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
