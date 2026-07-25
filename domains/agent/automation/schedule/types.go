package schedule

import (
	"encoding/json"
	"time"
)

type Schedule struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Pattern      string    `json:"pattern"`
	MaxCalls     *int      `json:"max_calls,omitempty"`
	CurrentCalls int       `json:"current_calls"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Enabled      bool      `json:"enabled"`
	Command      string    `json:"command"`
	BotID        string    `json:"bot_id"`
}

type NullableInt struct {
	Value *int
	Set   bool
}

func (n NullableInt) IsZero() bool {
	return !n.Set
}

func (n NullableInt) MarshalJSON() ([]byte, error) {
	if !n.Set || n.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*n.Value)
}

func (n *NullableInt) UnmarshalJSON(data []byte) error {
	n.Set = true
	if string(data) == "null" {
		n.Value = nil
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	n.Value = &value
	return nil
}

type CreateRequest struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Pattern     string      `json:"pattern"`
	MaxCalls    NullableInt `json:"max_calls,omitempty"`
	Command     string      `json:"command"`
	Enabled     *bool       `json:"enabled,omitempty"`
}

type UpdateRequest struct {
	Name        *string     `json:"name,omitempty"`
	Description *string     `json:"description,omitempty"`
	Pattern     *string     `json:"pattern,omitempty"`
	MaxCalls    NullableInt `json:"max_calls,omitempty"`
	Command     *string     `json:"command,omitempty"`
	Enabled     *bool       `json:"enabled,omitempty"`
}

type ListResponse struct {
	Items []Schedule `json:"items"`
}

type Log struct {
	ID           string     `json:"id"`
	ScheduleID   string     `json:"schedule_id"`
	BotID        string     `json:"bot_id"`
	SessionID    string     `json:"session_id,omitempty"`
	Status       string     `json:"status"`
	ResultText   string     `json:"result_text"`
	ErrorMessage string     `json:"error_message"`
	Usage        any        `json:"usage,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

type ListLogsResponse struct {
	Items      []Log `json:"items"`
	TotalCount int64 `json:"total_count"`
}
