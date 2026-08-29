package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	ManagedDirPath            = config.DefaultDataMount + "/skills"
	LegacyDirPath             = config.DefaultDataMount + "/.skills"
	IndexDirPath              = config.DefaultDataMount + "/.memoh/skills"
	IndexFilePath             = IndexDirPath + "/index.json"
	SkillDiscoveryRootsEnvVar = "MEMOH_SKILL_DISCOVERY_ROOTS"

	SourceKindManaged  = "managed"
	SourceKindLegacy   = "legacy"
	SourceKindCompat   = "compat"
	SourceKindRegistry = "registry"

	StateEffective = "effective"
	StateShadowed  = "shadowed"
	StateDisabled  = "disabled"

	ActionAdopt   = "adopt"
	ActionDisable = "disable"
	ActionEnable  = "enable"
)

type Entry struct {
	Name                     string         `json:"name"`
	Description              string         `json:"description"`
	Content                  string         `json:"content"`
	Metadata                 map[string]any `json:"metadata,omitempty"`
	Raw                      string         `json:"raw"`
	SourcePath               string         `json:"source_path,omitempty"`
	SourceRoot               string         `json:"source_root,omitempty"`
	SourceKind               string         `json:"source_kind,omitempty"`
	Managed                  bool           `json:"managed,omitempty"`
	State                    string         `json:"state,omitempty"`
	ShadowedBy               string         `json:"shadowed_by,omitempty"`
	RuntimeUsable            bool           `json:"runtime_usable,omitempty"`
	RuntimeUnusableReason    string         `json:"runtime_unusable_reason,omitempty"`
	RuntimeUsabilityChecked  bool           `json:"runtime_usability_checked,omitempty"`
	runtimeMetadataMalformed bool
}

type ActionRequest struct {
	Action     string
	TargetPath string
}

type Parsed struct {
	Name                     string
	Description              string
	Content                  string
	Metadata                 map[string]any
	runtimeMetadataMalformed bool
}

type Root struct {
	Path    string
	Kind    string
	Managed bool
}

type fileClient interface {
	ListDirAll(ctx context.Context, path string, recursive bool) ([]*pb.FileEntry, error)
	ReadRaw(ctx context.Context, path string) (io.ReadCloser, error)
	WriteRaw(ctx context.Context, path string, r io.Reader) (int64, error)
	Mkdir(ctx context.Context, path string) error
}

type indexState struct {
	Version   int                      `json:"version"`
	UpdatedAt string                   `json:"updated_at,omitempty"`
	Overrides map[string]indexOverride `json:"overrides,omitempty"`
	Items     []indexedItem            `json:"items,omitempty"`
}

type indexOverride struct {
	Disabled bool `json:"disabled,omitempty"`
}

type indexedItem struct {
	Name        string `json:"name"`
	SourcePath  string `json:"source_path"`
	SourceKind  string `json:"source_kind"`
	Managed     bool   `json:"managed"`
	State       string `json:"state"`
	ShadowedBy  string `json:"shadowed_by,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`
}

func ManagedDir() string {
	return ManagedDirPath
}

func ContainerEnv(rawCompatRoots []string) []string {
	compatRoots := compatDiscoveryRoots(rawCompatRoots)
	env := []string{
		"HOME=" + config.DefaultDataMount,
		"XDG_CONFIG_HOME=" + path.Join(config.DefaultDataMount, ".config"),
		"XDG_DATA_HOME=" + path.Join(config.DefaultDataMount, ".local", "share"),
		"XDG_CACHE_HOME=" + path.Join(config.DefaultDataMount, ".cache"),
	}
	env = append(env, SkillDiscoveryRootsEnvVar+"="+strings.Join(compatRoots, ":"))
	return env
}

func DiscoveryRoots(rawCompatRoots []string) []Root {
	roots := []Root{
		{Path: IndexDirPath, Kind: SourceKindManaged, Managed: true},
		{Path: LegacyDirPath, Kind: SourceKindLegacy, Managed: false},
	}
	for _, compatRoot := range compatDiscoveryRoots(rawCompatRoots) {
		roots = append(roots, Root{Path: compatRoot, Kind: SourceKindCompat, Managed: false})
	}
	return roots
}

func List(ctx context.Context, client fileClient, rawCompatRoots []string) ([]Entry, error) {
	idx := readIndex(ctx, client)
	roots := orderedDiscoveryRoots(ctx, client, rawCompatRoots)
	items := scan(ctx, client, roots)
	resolved := resolve(items, idx.Overrides)
	writeIndex(ctx, client, idx.withItems(resolved))
	return resolved, nil
}

func LoadEffective(ctx context.Context, client fileClient, rawCompatRoots []string) ([]Entry, error) {
	items, err := List(ctx, client, rawCompatRoots)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(items))
	for _, item := range items {
		if item.State == StateEffective {
			out = append(out, item)
		}
	}
	return out, nil
}

func normalizeRuntimeUsabilityEntries(entries []Entry) []Entry {
	if len(entries) == 0 {
		return entries
	}
	out := make([]Entry, len(entries))
	for i, entry := range entries {
		out[i] = NormalizeRuntimeUsability(entry)
	}
	return out
}

// NormalizeRuntimeUsability stores the registry-authoritative runtime usability
// decision on an Entry so catalog and resolver code do not reinterpret raw metadata.
func NormalizeRuntimeUsability(entry Entry) Entry {
	usable, reason := computeRuntimeUsability(entry)
	entry.RuntimeUsable = usable
	entry.RuntimeUnusableReason = reason
	entry.RuntimeUsabilityChecked = true
	return entry
}

func computeRuntimeUsability(entry Entry) (bool, string) {
	if entry.State != StateEffective {
		return false, "state"
	}
	if !IsValidName(entry.Name) {
		return false, "name"
	}
	if strings.TrimSpace(entry.Content) == "" {
		return false, "content"
	}
	switch entry.SourceKind {
	case SourceKindManaged, SourceKindLegacy, SourceKindCompat, SourceKindRegistry:
	default:
		return false, "source_kind"
	}
	if entry.runtimeMetadataMalformed {
		return false, "metadata"
	}
	for _, key := range []string{"hidden", "internal", "internal_only", "subagent_only"} {
		blocked, ok := metadataBool(entry.Metadata, key)
		if !ok {
			return false, "metadata"
		}
		if blocked {
			return false, key
		}
	}
	return true, ""
}

func metadataBool(metadata map[string]any, key string) (bool, bool) {
	if metadata == nil {
		return false, true
	}
	value, ok := metadata[key]
	if !ok {
		return false, true
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1":
			return true, true
		case "false", "no", "0", "":
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}

func ApplyAction(ctx context.Context, client fileClient, rawCompatRoots []string, req ActionRequest) error {
	targetPath := strings.TrimSpace(req.TargetPath)
	if targetPath == "" {
		return bridge.ErrBadRequest
	}
	if _, _, _, ok := RegistrySkillIDs(targetPath); ok {
		return ErrRegistrySkillReadOnly
	}

	roots := orderedDiscoveryRoots(ctx, client, rawCompatRoots)
	switch strings.TrimSpace(req.Action) {
	case ActionDisable:
		idx := readIndex(ctx, client)
		items := scan(ctx, client, roots)
		if !containsSourcePath(items, targetPath) {
			return bridge.ErrNotFound
		}
		if idx.Overrides == nil {
			idx.Overrides = make(map[string]indexOverride)
		}
		idx.Overrides[targetPath] = indexOverride{Disabled: true}
		writeIndex(ctx, client, idx.withItems(resolve(items, idx.Overrides)))
		return nil
	case ActionEnable:
		idx := readIndex(ctx, client)
		items := scan(ctx, client, roots)
		if !containsSourcePath(items, targetPath) {
			return bridge.ErrNotFound
		}
		delete(idx.Overrides, targetPath)
		writeIndex(ctx, client, idx.withItems(resolve(items, idx.Overrides)))
		return nil
	case ActionAdopt:
		items := scan(ctx, client, roots)
		target, ok := findBySourcePath(items, targetPath)
		if !ok {
			return bridge.ErrNotFound
		}
		if target.Managed {
			return bridge.ErrBadRequest
		}
		for _, item := range items {
			if item.Name == target.Name && item.Managed {
				return bridge.ErrBadRequest
			}
		}
		dirPath, err := userSkillDirForName(target.Name)
		if err != nil {
			return err
		}
		if err := client.Mkdir(ctx, dirPath); err != nil {
			return err
		}
		if _, err := client.WriteRaw(ctx, path.Join(dirPath, "SKILL.md"), strings.NewReader(target.Raw)); err != nil {
			return err
		}
		idx := readIndex(ctx, client)
		writeIndex(ctx, client, idx.withItems(resolve(scan(ctx, client, roots), idx.Overrides)))
		return nil
	default:
		return bridge.ErrBadRequest
	}
}

func compatDiscoveryRoots(rawRoots []string) []string {
	if rawRoots == nil {
		rawRoots = defaultCompatDiscoveryRoots()
	}
	return normalizeCompatDiscoveryRoots(rawRoots)
}

func defaultCompatDiscoveryRoots() []string {
	return []string{
		path.Join(config.DefaultDataMount, ".agents", "skills"),
		path.Join("/root", ".agents", "skills"),
	}
}

func normalizeCompatDiscoveryRoots(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = path.Clean(p)
		if !strings.HasPrefix(p, "/") {
			continue
		}
		if p == ManagedDirPath || p == IndexDirPath || p == LegacyDirPath {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func ParseFile(raw string, fallbackName string) Parsed {
	trimmed := strings.TrimSpace(raw)
	result := Parsed{
		Name:    strings.TrimSpace(fallbackName),
		Content: trimmed,
	}
	if !strings.HasPrefix(trimmed, "---") {
		return normalizeParsed(result)
	}

	rest := trimmed[3:]
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}
	closingIdx := strings.Index(rest, "\n---")
	if closingIdx < 0 {
		result.runtimeMetadataMalformed = true
		return normalizeParsed(result)
	}

	frontmatterRaw := rest[:closingIdx]
	body := rest[closingIdx+4:]
	body = strings.TrimLeft(body, "\r\n")
	result.Content = body

	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Metadata    any    `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(frontmatterRaw), &fm); err != nil {
		result.runtimeMetadataMalformed = true
		return normalizeParsed(result)
	}
	if strings.TrimSpace(fm.Name) != "" {
		result.Name = strings.TrimSpace(fm.Name)
	}
	result.Description = strings.TrimSpace(fm.Description)
	switch metadata := fm.Metadata.(type) {
	case nil:
	case map[string]any:
		result.Metadata = metadata
	default:
		result.runtimeMetadataMalformed = true
	}
	return normalizeParsed(result)
}

func IsValidName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '+':
		default:
			return false
		}
	}
	return true
}

const maxPortableResourceIDBytes = 128

// IsValidRegistryComponent validates one Registry, Package, or Skill path component.
func IsValidRegistryComponent(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxPortableResourceIDBytes ||
		strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") ||
		isWindowsReservedName(value) {
		return false
	}
	if first := value[0]; (first < 'a' || first > 'z') && (first < '0' || first > '9') {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

// IsValidRegistryID also excludes the namespace reserved for user-authored Skills.
func IsValidRegistryID(value string) bool {
	return value != UserSkillNamespace && IsValidRegistryComponent(value)
}

func isWindowsReservedName(value string) bool {
	base := strings.ToLower(strings.SplitN(value, ".", 2)[0])
	if base == "con" || base == "prn" || base == "aux" || base == "nul" {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) &&
		base[3] >= '1' && base[3] <= '9'
}

func normalizeParsed(skill Parsed) Parsed {
	if strings.TrimSpace(skill.Name) == "" {
		skill.Name = "default"
	}
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	skill.Content = strings.TrimSpace(skill.Content)
	if skill.Description == "" {
		skill.Description = skill.Name
	}
	if skill.Content == "" {
		skill.Content = skill.Description
	}
	return skill
}

func scan(ctx context.Context, client fileClient, roots []Root) []Entry {
	items := make([]Entry, 0, 16)
	for _, root := range roots {
		entries, err := client.ListDirAll(ctx, root.Path, false)
		if err != nil {
			continue
		}
		slices.SortFunc(entries, func(a, b *pb.FileEntry) int {
			return strings.Compare(a.GetPath(), b.GetPath())
		})
		for _, entry := range entries {
			if !entry.GetIsDir() {
				if path.Base(entry.GetPath()) != "SKILL.md" {
					continue
				}
				filePath := path.Join(root.Path, "SKILL.md")
				raw, err := readRawFile(ctx, client, filePath)
				if err != nil {
					continue
				}
				parsed := ParseFile(raw, "default")
				items = append(items, entryFromParsed(parsed, raw, root, filePath))
				continue
			}

			name := path.Base(entry.GetPath())
			if name == "" || name == "." {
				continue
			}
			filePath := path.Join(root.Path, name, "SKILL.md")
			raw, err := readRawFile(ctx, client, filePath)
			if err != nil {
				continue
			}
			parsed := ParseFile(raw, name)
			items = append(items, entryFromParsed(parsed, raw, root, filePath))
		}
	}
	return items
}

func resolve(items []Entry, overrides map[string]indexOverride) []Entry {
	byName := make(map[string][]Entry, len(items))
	for _, item := range items {
		byName[item.Name] = append(byName[item.Name], item)
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	slices.Sort(names)

	out := make([]Entry, 0, len(items))
	for _, name := range names {
		group := byName[name]
		var effectivePath string
		for i := range group {
			if overrides[group[i].SourcePath].Disabled {
				group[i].State = StateDisabled
				out = append(out, NormalizeRuntimeUsability(group[i]))
				continue
			}
			if effectivePath == "" {
				group[i].State = StateEffective
				effectivePath = group[i].SourcePath
				out = append(out, NormalizeRuntimeUsability(group[i]))
				continue
			}
			group[i].State = StateShadowed
			group[i].ShadowedBy = effectivePath
			out = append(out, NormalizeRuntimeUsability(group[i]))
		}
	}

	slices.SortFunc(out, func(a, b Entry) int {
		if cmp := strings.Compare(a.Name, b.Name); cmp != 0 {
			return cmp
		}
		if cmp := stateRank(a.State) - stateRank(b.State); cmp != 0 {
			return cmp
		}
		if a.Managed != b.Managed {
			if a.Managed {
				return -1
			}
			return 1
		}
		return strings.Compare(a.SourcePath, b.SourcePath)
	})
	return out
}

func stateRank(state string) int {
	switch state {
	case StateEffective:
		return 0
	case StateShadowed:
		return 1
	case StateDisabled:
		return 2
	default:
		return 3
	}
}

func entryFromParsed(parsed Parsed, raw string, root Root, sourcePath string) Entry {
	return Entry{
		Name:                     parsed.Name,
		Description:              parsed.Description,
		Content:                  parsed.Content,
		Metadata:                 parsed.Metadata,
		Raw:                      raw,
		SourcePath:               sourcePath,
		SourceRoot:               root.Path,
		SourceKind:               root.Kind,
		Managed:                  root.Managed,
		runtimeMetadataMalformed: parsed.runtimeMetadataMalformed,
	}
}

func readRawFile(ctx context.Context, client fileClient, filePath string) (string, error) {
	rc, err := client.ReadRaw(ctx, filePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func readIndex(ctx context.Context, client fileClient) indexState {
	rc, err := client.ReadRaw(ctx, IndexFilePath)
	if err != nil {
		return indexState{Version: 1, Overrides: make(map[string]indexOverride)}
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil || len(data) == 0 {
		return indexState{Version: 1, Overrides: make(map[string]indexOverride)}
	}

	var idx indexState
	if err := json.Unmarshal(data, &idx); err != nil {
		return indexState{Version: 1, Overrides: make(map[string]indexOverride)}
	}
	if idx.Version == 0 {
		idx.Version = 1
	}
	if idx.Overrides == nil {
		idx.Overrides = make(map[string]indexOverride)
	}
	return idx
}

func writeIndex(ctx context.Context, client fileClient, idx indexState) {
	if err := client.Mkdir(ctx, IndexDirPath); err != nil {
		return
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return
	}
	_, _ = client.WriteRaw(ctx, IndexFilePath, strings.NewReader(string(data)))
}

func (i indexState) withItems(items []Entry) indexState {
	if i.Version == 0 {
		i.Version = 1
	}
	if i.Overrides == nil {
		i.Overrides = make(map[string]indexOverride)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	i.UpdatedAt = now
	i.Items = make([]indexedItem, 0, len(items))
	for _, item := range items {
		sum := sha256.Sum256([]byte(item.Raw))
		i.Items = append(i.Items, indexedItem{
			Name:        item.Name,
			SourcePath:  item.SourcePath,
			SourceKind:  item.SourceKind,
			Managed:     item.Managed,
			State:       item.State,
			ShadowedBy:  item.ShadowedBy,
			ContentHash: hex.EncodeToString(sum[:]),
			LastSeenAt:  now,
		})
	}
	return i
}

func containsSourcePath(items []Entry, target string) bool {
	_, ok := findBySourcePath(items, target)
	return ok
}

func findBySourcePath(items []Entry, target string) (Entry, bool) {
	for _, item := range items {
		if item.SourcePath == target {
			return item, true
		}
	}
	return Entry{}, false
}
