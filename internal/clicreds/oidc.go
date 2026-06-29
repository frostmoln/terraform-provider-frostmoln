package clicreds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// scope is the OAuth scope set the refresh grant requests. offline_access keeps
// the IdP returning a refresh token across rotations. It matches the fm CLI's
// oidc.Scope so a refresh issued by the provider is indistinguishable from one
// issued by `fm` (same client, same scopes).
const scope = "openid profile email offline_access"

// Token is a refreshed token set. RefreshToken is empty when the IdP did not
// rotate it (the caller keeps the previous one in that case).
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64 // access-token expiry as a Unix timestamp (seconds), 0 if unknown
}

// cliConfigResponse is the gateway's public bootstrap config
// (GET <apiEndpoint>/v1/auth/cli-config).
type cliConfigResponse struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientId"`
}

// discoveryResponse is the subset of the IdP's OpenID configuration we need.
type discoveryResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
}

// Refresh exchanges a refresh token for a fresh token set, reimplementing the
// fm CLI's oidc.RefreshViaGateway without importing it (that package is
// internal to a different module). It resolves the IdP from the gateway's
// cli-config, reads the token endpoint from OIDC discovery, and POSTs the
// refresh_token grant. apiEndpoint is the CLI's API base, which already
// includes the /api prefix.
func Refresh(ctx context.Context, httpClient *http.Client, apiEndpoint, refreshToken string) (*Token, error) {
	cc, err := fetchCLIConfig(ctx, httpClient, apiEndpoint)
	if err != nil {
		return nil, err
	}
	if cc.ClientID == "" {
		return nil, errors.New("token refresh is not available: the API gateway has no CLI client configured")
	}
	tokenEndpoint, err := discoverTokenEndpoint(ctx, httpClient, cc.Issuer)
	if err != nil {
		return nil, err
	}
	return refreshGrant(ctx, httpClient, tokenEndpoint, cc.ClientID, refreshToken)
}

// fetchCLIConfig reads the gateway's public cli-config endpoint. The response
// bootstraps the issuer/token endpoint the refresh token is later POSTed to, so
// a downgrade here could let an on-path attacker redirect the refresh to an
// https endpoint it controls; refuse a non-https (non-loopback) apiEndpoint.
func fetchCLIConfig(ctx context.Context, httpClient *http.Client, apiEndpoint string) (*cliConfigResponse, error) {
	if !SecureURL(apiEndpoint) {
		return nil, fmt.Errorf("refusing to fetch cli-config over an insecure (non-https) endpoint: %s", apiEndpoint)
	}
	endpoint := strings.TrimRight(apiEndpoint, "/") + "/v1/auth/cli-config"
	var cc cliConfigResponse
	if err := getJSON(ctx, httpClient, endpoint, &cc); err != nil {
		return nil, fmt.Errorf("fetch cli-config: %w", err)
	}
	if cc.Issuer == "" {
		return nil, errors.New("cli-config response is missing the issuer")
	}
	return &cc, nil
}

// discoverTokenEndpoint reads the IdP's OpenID configuration and returns the
// token endpoint. It refuses a non-https endpoint (the refresh token travels in
// the request body, so a downgrade would leak it); http is allowed only to
// loopback for local dev / tests.
func discoverTokenEndpoint(ctx context.Context, httpClient *http.Client, issuer string) (string, error) {
	if !SecureURL(issuer) {
		return "", fmt.Errorf("refusing OIDC discovery against an insecure (non-https) issuer: %s", issuer)
	}
	endpoint := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	var d discoveryResponse
	if err := getJSON(ctx, httpClient, endpoint, &d); err != nil {
		return "", fmt.Errorf("OIDC discovery: %w", err)
	}
	if d.TokenEndpoint == "" {
		return "", errors.New("issuer discovery is missing the token_endpoint")
	}
	if !SecureURL(d.TokenEndpoint) {
		return "", fmt.Errorf("refusing insecure (non-https) OIDC token endpoint: %s", d.TokenEndpoint)
	}
	return d.TokenEndpoint, nil
}

// refreshGrant POSTs the refresh_token grant and parses the token response.
func refreshGrant(ctx context.Context, httpClient *http.Client, tokenEndpoint, clientID, refreshToken string) (*Token, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"scope":         {scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// RFC 6749 §5.2 returns OAuth errors as a JSON body with an `error`
		// field. Surface only that code — never the response body, which can
		// echo the (secret) refresh token back.
		var oe struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &oe)
		if oe.Error != "" {
			return nil, fmt.Errorf("token refresh rejected: %s", oe.Error)
		}
		return nil, fmt.Errorf("token endpoint returned HTTP %d", resp.StatusCode)
	}

	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("token response is missing access_token")
	}
	var expiresAt int64
	if tr.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).Unix()
	}
	return &Token{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, ExpiresAt: expiresAt}, nil
}

// getJSON GETs endpoint and decodes a JSON 2xx body into out.
func getJSON(ctx context.Context, httpClient *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: HTTP %d", endpoint, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", endpoint, err)
	}
	return nil
}

// SecureURL reports whether raw is safe to send credentials to: https, or http
// only to a loopback host (local dev / tests).
func SecureURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	host := u.Hostname()
	return u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1")
}
