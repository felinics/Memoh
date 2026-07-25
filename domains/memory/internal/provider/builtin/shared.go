package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	memorydomain "github.com/memohai/memoh/domains/memory"
	memreg "github.com/memohai/memoh/domains/memory/registry"
)

// memoryIDSeq is a process-wide monotonic counter appended to memory IDs so
// that two Add calls landing in the same wall-clock nanosecond still produce
// distinct IDs. The wall-clock nanosecond remains the dominant component, so
// IDs stay human-readable and roughly time-ordered; the sequence only breaks
// ties when the clock has not advanced.
var memoryIDSeq uint64

// memoryStore is the markdown file store consumed by the builtin runtimes.
type memoryStore interface {
	PersistMemories(ctx context.Context, botID string, items []memorydomain.Item, filters map[string]any) error
	ReadAllMemoryFiles(ctx context.Context, botID string) ([]memorydomain.Item, error)
	ReadAllMemoryFilesForIngest(ctx context.Context, botID string) ([]memorydomain.Item, error)
	RemoveMemories(ctx context.Context, botID string, ids []string) error
	RemoveAllMemories(ctx context.Context, botID string) error
	RebuildFiles(ctx context.Context, botID string, items []memorydomain.Item, filters map[string]any) error
	SyncOverview(ctx context.Context, botID string) error
	CountMemoryFiles(ctx context.Context, botID string) (int, error)
}

func canonicalStoreItem(item memorydomain.Item) memorydomain.Item {
	item.ID = strings.TrimSpace(item.ID)
	item.Memory = strings.TrimSpace(item.Memory)
	if item.Memory != "" && strings.TrimSpace(item.Hash) == "" {
		item.Hash = runtimeHash(item.Memory)
	}
	return item
}

func storeItemFromMemoryItem(item memorydomain.Item) memorydomain.Item {
	return canonicalStoreItem(item)
}

func memoryItemFromStore(item memorydomain.Item) memorydomain.Item {
	return canonicalStoreItem(item)
}

func memoryItemsFromStore(items []memorydomain.Item) []memorydomain.Item {
	if len(items) == 0 {
		return []memorydomain.Item{}
	}
	out := make([]memorydomain.Item, 0, len(items))
	for _, item := range items {
		out = append(out, memoryItemFromStore(item))
	}
	return out
}

func runtimeBotID(botID string, filters map[string]any) (string, error) {
	botID = strings.TrimSpace(botID)
	if botID == "" {
		botID = strings.TrimSpace(runtimeFilterString(filters, "bot_id"))
	}
	if botID == "" {
		botID = strings.TrimSpace(runtimeFilterString(filters, "scopeId"))
	}
	if botID == "" {
		return "", errors.New("bot_id is required")
	}
	return botID, nil
}

func runtimeBotIDFromMemoryID(memoryID string) string {
	parts := strings.SplitN(strings.TrimSpace(memoryID), ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func runtimeLocalMemoryID(memoryID string) string {
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return ""
	}
	if idx := strings.Index(memoryID, ":"); idx >= 0 && idx+1 < len(memoryID) {
		return strings.TrimSpace(memoryID[idx+1:])
	}
	return memoryID
}

func runtimeText(message string, messages []memreg.Message) string {
	text := strings.TrimSpace(message)
	if text == "" && len(messages) > 0 {
		parts := make([]string, 0, len(messages))
		for _, m := range messages {
			content := strings.TrimSpace(m.Content)
			if content == "" {
				continue
			}
			role := strings.ToUpper(strings.TrimSpace(m.Role))
			if role == "" {
				role = "MESSAGE"
			}
			parts = append(parts, "["+role+"] "+content)
		}
		text = strings.Join(parts, "\n")
	}
	return strings.TrimSpace(text)
}

func runtimeMemoryID(botID string, now time.Time) string {
	seq := atomic.AddUint64(&memoryIDSeq, 1)
	return botID + ":" + "mem_" + strconv.FormatInt(now.UnixNano(), 10) + "_" + strconv.FormatUint(seq, 36)
}

// runtimeHash returns a stable SHA-256 hex digest of a trimmed memory body.
// It is shared by the dense and file runtimes (and the upcoming graph runtime)
// for content-addressing memory items.
func runtimeHash(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

func runtimeFilterString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
