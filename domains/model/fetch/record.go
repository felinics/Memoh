package fetch

import (
	"encoding/json"
	"time"

	fetchport "github.com/memohai/memoh/domains/model/internal/port/fetch"
)

// ProviderRecord is the runtime fetch-provider snapshot used by consumers
// (for example web_fetch). Persistence adapters use the owner-private port.
type ProviderRecord struct {
	ID        string
	Name      string
	Provider  string
	Config    json.RawMessage
	Enable    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func publicRecord(row fetchport.ProviderRecord) ProviderRecord {
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
