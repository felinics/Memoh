package email

// OAuthClient is the narrow OAuth client descriptor used by Gmail OAuth flows.
type OAuthClient struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// OAuthClientResolver looks up built-in OAuth client credentials by ref.
type OAuthClientResolver interface {
	Get(ref string) (OAuthClient, bool)
	HasUsableClient(ref string) bool
}
