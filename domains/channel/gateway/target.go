package gateway

// TargetHint provides a display label and example for a target format.
type TargetHint struct {
	Example string `json:"example,omitempty"`
	Label   string `json:"label,omitempty"`
}

// TargetSpec describes the expected format of a delivery target for a channel type.
type TargetSpec struct {
	Format string       `json:"format"`
	Hints  []TargetHint `json:"hints,omitempty"`
}
