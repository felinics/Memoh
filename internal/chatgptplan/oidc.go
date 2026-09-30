package chatgptplan

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/url"

	"github.com/golang-jwt/jwt/v5"
)

type discovery struct {
	Issuer             string `json:"issuer"`
	JWKSURI            string `json:"jwks_uri"`
	RevocationEndpoint string `json:"revocation_endpoint"`
}
type identityClaims struct {
	jwt.RegisteredClaims
	Nonce string `json:"nonce"`
	Email string `json:"email"`
}

func trustedAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "auth.openai.com" && u.User == nil && u.Fragment == ""
}

func (s *SessionService) fetchJSON(ctx context.Context, endpoint string, out any) error {
	if !trustedAuthURL(endpoint) {
		return ErrUpstream
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ErrUpstream
	}
	// #nosec G704 -- trustedAuthURL restricts endpoints to auth.openai.com; redirects are disabled.
	resp, err := s.client.Do(req)
	if err != nil {
		return ErrUpstream
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out) != nil {
		return ErrUpstream
	}
	return nil
}

func (s *SessionService) discover(ctx context.Context) (discovery, error) {
	var d discovery
	err := s.fetchJSON(ctx, authBaseURL+"/.well-known/openid-configuration", &d)
	if err != nil {
		return d, err
	}
	if d.Issuer != authBaseURL || !trustedAuthURL(d.JWKSURI) {
		return d, ErrUpstream
	}
	return d, nil
}

type accessClaims struct {
	jwt.RegisteredClaims
	ClientID string `json:"client_id"`
	Scope    string `json:"scope"`
}

func (s *SessionService) signingKeys(ctx context.Context) (jwt.Keyfunc, error) {
	d, err := s.discover(ctx)
	if err != nil {
		return nil, err
	}
	var keys struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err = s.fetchJSON(ctx, d.JWKSURI, &keys); err != nil {
		return nil, err
	}
	return func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		for _, key := range keys.Keys {
			if key.Kid != kid || (key.Use != "" && key.Use != "sig") || (key.Alg != "" && key.Alg != token.Method.Alg()) {
				continue
			}
			switch {
			case key.Kty == "RSA" && token.Method.Alg() == "RS256":
				n, err := base64.RawURLEncoding.DecodeString(key.N)
				if err != nil {
					return nil, ErrInvalidAuthorization
				}
				e, err := base64.RawURLEncoding.DecodeString(key.E)
				if err != nil || len(e) > 4 {
					return nil, ErrInvalidAuthorization
				}
				exponent := 0
				for _, b := range e {
					exponent = exponent*256 + int(b)
				}
				if exponent < 3 || len(n) < 256 {
					return nil, ErrInvalidAuthorization
				}
				return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}, nil
			case key.Kty == "EC" && key.Crv == "P-256" && token.Method.Alg() == "ES256":
				x, err := base64.RawURLEncoding.DecodeString(key.X)
				if err != nil {
					return nil, ErrInvalidAuthorization
				}
				y, err := base64.RawURLEncoding.DecodeString(key.Y)
				if err != nil {
					return nil, ErrInvalidAuthorization
				}
				curve := elliptic.P256()
				xx := new(big.Int).SetBytes(x)
				yy := new(big.Int).SetBytes(y)
				encoded := append([]byte{4}, x...)
				encoded = append(encoded, y...)
				if len(x) != 32 || len(y) != 32 {
					return nil, ErrInvalidAuthorization
				}
				if _, err := ecdh.P256().NewPublicKey(encoded); err != nil {
					return nil, ErrInvalidAuthorization
				}
				return &ecdsa.PublicKey{Curve: curve, X: xx, Y: yy}, nil
			}
		}
		return nil, ErrInvalidAuthorization
	}, nil
}

func verifyIDToken(raw, clientID, nonce string, key jwt.Keyfunc) (identityClaims, error) {
	claims := identityClaims{}
	_, err := jwt.ParseWithClaims(raw, &claims, key, jwt.WithValidMethods([]string{"RS256", "ES256"}), jwt.WithIssuer(authBaseURL), jwt.WithAudience(clientID), jwt.WithExpirationRequired())
	if err != nil || claims.Subject == "" || (nonce != "" && claims.Nonce != nonce) {
		return identityClaims{}, ErrInvalidAuthorization
	}
	return claims, nil
}

func verifyAccessToken(raw, clientID, subject string, key jwt.Keyfunc) (accessClaims, error) {
	claims := accessClaims{}
	_, err := jwt.ParseWithClaims(raw, &claims, key, jwt.WithValidMethods([]string{"RS256", "ES256"}), jwt.WithIssuer(authBaseURL), jwt.WithAudience(APIBaseURL), jwt.WithExpirationRequired())
	if err != nil || claims.Subject != subject || claims.ClientID != clientID {
		return accessClaims{}, ErrInvalidAuthorization
	}
	return claims, nil
}
