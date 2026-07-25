package search

import (
	"encoding/json"
	"time"

	searchport "github.com/memohai/memoh/domains/model/internal/port/search"
)

// Sentinel persistence conflicts re-exported for handlers and consumers.
var (
	ErrProviderTypeConflict = searchport.ErrProviderTypeConflict
	ErrProviderNameTaken    = searchport.ErrProviderNameTaken
)

// ProviderRecord is the runtime search-provider snapshot used by consumers
// (for example web_search). Persistence adapters use the owner-private port.
type ProviderRecord struct {
	ID        string
	Name      string
	Provider  string
	Config    json.RawMessage
	Enable    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func publicRecord(row searchport.ProviderRecord) ProviderRecord {
	return ProviderRecord{
		ID:        row.ID,
		Name:      row.Name,
		Provider:  row.Provider,
		Config:    append(json.RawMessage(nil), row.Config...),
		Enable:    row.Enable,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}
