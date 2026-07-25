package agent

// ACPAgentProfile is the channel-safe view of one external agent profile.
// Runtime launch commands, compatibility quirks, and credential details stay
// behind ACPProfileResolver and never cross the Channel boundary.
type ACPAgentProfile struct {
	ID          string
	DisplayName string
	Known       bool
}

// ACPManagedField identifies one required managed setting that is missing
// during a Channel preflight. It deliberately carries only presentation-safe
// metadata; configured values and other runtime details remain private.
type ACPManagedField struct {
	ID    string
	Label string
}

// ACPSetupPreflight is the minimum setup state Channel needs before creating
// an ACP-backed thread.
type ACPSetupPreflight struct {
	Enabled             bool
	MissingManagedField *ACPManagedField
}

// ACPProfileResolver is the read-only Agent port used by Channel while
// interpreting /new commands. Implementations own profile normalization,
// registration, and setup validation; Channel supplies only stable inputs.
type ACPProfileResolver interface {
	ResolveACPProfile(agentID string) ACPAgentProfile
	ResolveACPSetupPreflight(agentID string, metadata map[string]any) ACPSetupPreflight
}
