package account

import (
	"time"
)

// Account represents a human account credential record.
type Account struct {
	ID                  string         `json:"id"`
	Username            string         `json:"username"`
	Email               string         `json:"email,omitempty"`
	Role                string         `json:"role"`
	DisplayName         string         `json:"display_name"`
	AvatarURL           string         `json:"avatar_url,omitempty"`
	Timezone            string         `json:"timezone,omitempty"`
	IsActive            bool           `json:"is_active"`
	PrincipalIsActive   bool           `json:"principal_is_active"`
	MembershipIsActive  bool           `json:"membership_is_active"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	JoinedAt            time.Time      `json:"joined_at"`
	MembershipUpdatedAt time.Time      `json:"membership_updated_at"`
	LastLoginAt         time.Time      `json:"last_login_at,omitempty"`
	TitleModelID        string         `json:"title_model_id,omitempty"`
}

// CreateAccountRequest is the input for creating an account.
type CreateAccountRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"` //nolint:gosec // intentional: JSON request field carrying a user-supplied credential
	Email       string `json:"email,omitempty"`
	Role        string `json:"role,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	IsActive    *bool  `json:"is_active,omitempty"`
}

// UpdateAccountRequest is the input for admin-level account updates.
type UpdateAccountRequest struct {
	Role     *string `json:"role,omitempty"`
	IsActive *bool   `json:"is_active,omitempty"`
}

// UpdateProfileRequest is the input for self-service profile updates.
type UpdateProfileRequest struct {
	DisplayName  *string                `json:"display_name,omitempty"`
	AvatarURL    *string                `json:"avatar_url,omitempty"`
	Timezone     *string                `json:"timezone,omitempty"`
	Metadata     *UpdateProfileMetadata `json:"metadata,omitempty"`
	TitleModelID *string                `json:"title_model_id,omitempty"`
}

// UpdateProfileMetadata enumerates the user-writable keys of user.metadata.
// Only fields listed here can be set via PUT /users/me. Never widen this to
// accept arbitrary JSON: user.metadata may later drive server-side decisions
// (feature flags, quotas, roles), so client-writable keys must stay an explicit
// allowlist.
type UpdateProfileMetadata struct {
	OnboardingCompleted *bool `json:"onboarding_completed,omitempty"`
}

// UpdatePasswordRequest is the input for password change.
type UpdatePasswordRequest struct {
	CurrentPassword string `json:"current_password,omitempty"`
	NewPassword     string `json:"new_password"`
}

// ListAccountsResponse wraps a list of accounts.
type ListAccountsResponse struct {
	Items []Account `json:"items"`
}
