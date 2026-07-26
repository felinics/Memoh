package access

// Manager is the effective Manage state for a channel identity on a bot, combining
// the local override (Channel Access) with the inherited grant (bound web member).
type Manager struct {
	ChannelIdentityID          string `json:"channel_identity_id"`
	ChannelType                string `json:"channel_type,omitempty"`
	ChannelSubjectID           string `json:"channel_subject_id,omitempty"`
	ChannelIdentityDisplayName string `json:"channel_identity_display_name,omitempty"`
	ChannelIdentityAvatarURL   string `json:"channel_identity_avatar_url,omitempty"`
	// Manage is the effective Manage capability (local override ?? inherited).
	Manage bool `json:"manage"`
	// Inherited reports whether this identity is bound to a web member that carries
	// the Manage capability (owner or workspace manage grant).
	Inherited bool `json:"inherited"`
	// HasOverride reports whether a local Channel Access override exists for it.
	HasOverride bool `json:"has_override"`
	// Bound reports whether this identity is linked to a workspace member of this
	// bot (any permission). It marks the identity as a "platform member" in the UI
	// regardless of whether it carries Manage.
	Bound bool `json:"bound"`
}

// ListManagersResponse wraps the effective managers of a bots.
type ListManagersResponse struct {
	Items []Manager `json:"items"`
}

// SetManagerRequest sets the local Manage override for a channel identity.
type SetManagerRequest struct {
	ChannelIdentityID string `json:"channel_identity_id"`
	// Granted forces Manage ON (true) or OFF (false) locally.
	Granted bool `json:"granted"`
}
