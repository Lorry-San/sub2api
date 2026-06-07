package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	kiroStartURL        = "https://view.awsapps.com/start"
	kiroDeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
)

var kiroScopes = []string{
	"codewhisperer:completions",
	"codewhisperer:analysis",
	"codewhisperer:conversations",
	"codewhisperer:transformations",
	"codewhisperer:taskassist",
}

type KiroOAuthService struct {
	proxyRepo ProxyRepository

	mu       sync.Mutex
	sessions map[string]*KiroDeviceSession
}

func NewKiroOAuthService(proxyRepo ProxyRepository) *KiroOAuthService {
	return &KiroOAuthService{
		proxyRepo: proxyRepo,
		sessions:  make(map[string]*KiroDeviceSession),
	}
}

type KiroDeviceSession struct {
	ClientID        string
	ClientSecret    string
	DeviceCode      string
	UserCode        string
	VerificationURI string
	Interval        int
	ExpiresAt       time.Time
	Region          string
	ProxyURL        string
	StartedAt       time.Time
}

type KiroDeviceStartResult struct {
	SessionID       string `json:"session_id"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int    `json:"interval"`
	Region          string `json:"region"`
}

type KiroDevicePollResult struct {
	Completed bool           `json:"completed"`
	Status    string         `json:"status,omitempty"`
	TokenInfo *KiroTokenInfo `json:"token_info,omitempty"`
}

type KiroTokenInfo struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	ProfileARN   string `json:"profile_arn,omitempty"`
	Region       string `json:"region,omitempty"`
	IDCRegion    string `json:"idc_region,omitempty"`
	AuthMethod   string `json:"auth_method,omitempty"`
	StartURL     string `json:"start_url,omitempty"`
	LastRefresh  string `json:"last_refresh,omitempty"`
}

func (s *KiroOAuthService) StartDeviceFlow(ctx context.Context, region string, proxyID *int64) (*KiroDeviceStartResult, error) {
	region = firstNonEmpty(region, DefaultKiroRegion)
	proxyURL := s.resolveProxyURL(ctx, proxyID)
	client := newKiroHTTPClient(proxyURL)
	oidcBase := fmt.Sprintf("https://oidc.%s.amazonaws.com", region)

	regBody := map[string]any{
		"clientName": "Kiro Proxy",
		"clientType": "public",
		"scopes":     kiroScopes,
		"grantTypes": []string{kiroDeviceGrantType, "refresh_token"},
		"issuerUrl":  kiroStartURL,
	}
	regResp, err := s.postJSON(ctx, client, oidcBase+"/client/register", regBody, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}
	clientID := stringFromJSON(regResp, "clientId", "client_id")
	clientSecret := stringFromJSON(regResp, "clientSecret", "client_secret")
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("Kiro client registration response missing clientId/clientSecret")
	}

	authBody := map[string]any{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     kiroStartURL,
	}
	authResp, err := s.postJSON(ctx, client, oidcBase+"/device_authorization", authBody, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}
	deviceCode := stringFromJSON(authResp, "deviceCode", "device_code")
	userCode := stringFromJSON(authResp, "userCode", "user_code")
	verificationURI := firstNonEmpty(stringFromJSON(authResp, "verificationUriComplete", "verification_uri_complete"), stringFromJSON(authResp, "verificationUri", "verification_uri"))
	interval := intFromJSON(authResp, 5, "interval")
	expiresIn := int64(intFromJSON(authResp, 600, "expiresIn", "expires_in"))
	if deviceCode == "" || userCode == "" || verificationURI == "" {
		return nil, fmt.Errorf("Kiro device authorization response missing required fields")
	}

	sessionID, err := randomKiroHex(24)
	if err != nil {
		return nil, err
	}
	session := &KiroDeviceSession{
		ClientID:        clientID,
		ClientSecret:    clientSecret,
		DeviceCode:      deviceCode,
		UserCode:        userCode,
		VerificationURI: verificationURI,
		Interval:        interval,
		ExpiresAt:       time.Now().Add(time.Duration(expiresIn) * time.Second),
		Region:          region,
		ProxyURL:        proxyURL,
		StartedAt:       time.Now(),
	}
	s.mu.Lock()
	s.sessions[sessionID] = session
	s.mu.Unlock()

	return &KiroDeviceStartResult{
		SessionID:       sessionID,
		UserCode:        userCode,
		VerificationURI: verificationURI,
		ExpiresIn:       expiresIn,
		Interval:        interval,
		Region:          region,
	}, nil
}

func (s *KiroOAuthService) PollDeviceFlow(ctx context.Context, sessionID string, proxyID *int64) (*KiroDevicePollResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	session := s.getDeviceSession(sessionID)
	if session == nil {
		return nil, fmt.Errorf("Kiro device session not found or expired")
	}
	if time.Now().After(session.ExpiresAt) {
		s.CancelDeviceFlow(sessionID)
		return nil, fmt.Errorf("Kiro device authorization expired")
	}

	proxyURL := session.ProxyURL
	if proxyID != nil {
		proxyURL = s.resolveProxyURL(ctx, proxyID)
	}
	client := newKiroHTTPClient(proxyURL)
	oidcBase := fmt.Sprintf("https://oidc.%s.amazonaws.com", session.Region)
	body := map[string]any{
		"clientId":     session.ClientID,
		"clientSecret": session.ClientSecret,
		"grantType":    kiroDeviceGrantType,
		"deviceCode":   session.DeviceCode,
	}
	data, status, err := postJSONRaw(ctx, client, oidcBase+"/token", body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK {
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("parse Kiro token response: %w", err)
		}
		s.CancelDeviceFlow(sessionID)
		tokenInfo := kiroTokenInfoFromResponse(parsed, &KiroCredentials{
			ClientID:     session.ClientID,
			ClientSecret: session.ClientSecret,
			Region:       session.Region,
			IDCRegion:    session.Region,
			AuthMethod:   KiroAuthMethodIDC,
			StartURL:     kiroStartURL,
		})
		if strings.TrimSpace(tokenInfo.AccessToken) == "" {
			return nil, fmt.Errorf("Kiro device token response missing access_token")
		}
		return &KiroDevicePollResult{Completed: true, TokenInfo: tokenInfo}, nil
	}

	var errPayload map[string]any
	_ = json.Unmarshal(data, &errPayload)
	errorCode := stringFromJSON(errPayload, "error")
	switch errorCode {
	case "authorization_pending":
		return &KiroDevicePollResult{Completed: false, Status: "pending"}, nil
	case "slow_down":
		return &KiroDevicePollResult{Completed: false, Status: "slow_down"}, nil
	case "expired_token":
		s.CancelDeviceFlow(sessionID)
		return nil, fmt.Errorf("Kiro device authorization expired")
	case "access_denied":
		s.CancelDeviceFlow(sessionID)
		return nil, fmt.Errorf("Kiro device authorization denied")
	default:
		return nil, fmt.Errorf("Kiro token request failed: status=%d body=%s", status, truncateString(string(data), 300))
	}
}

func (s *KiroOAuthService) CancelDeviceFlow(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return false
	}
	delete(s.sessions, sessionID)
	return true
}

func (s *KiroOAuthService) ValidateRefreshToken(ctx context.Context, refreshToken string, proxyID *int64, credentials map[string]any) (*KiroTokenInfo, error) {
	creds := NewKiroCredentialsFromMap(credentials)
	creds.RefreshToken = strings.TrimSpace(refreshToken)
	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("missing refresh_token")
	}
	proxyURL := s.resolveProxyURL(ctx, proxyID)
	return s.RefreshToken(ctx, creds, proxyURL)
}

func (s *KiroOAuthService) RefreshAccountToken(ctx context.Context, account *Account) (*KiroTokenInfo, error) {
	if account == nil || account.Platform != PlatformKiro || account.Type != AccountTypeOAuth {
		return nil, fmt.Errorf("not a Kiro OAuth account")
	}
	creds := NewKiroCredentialsFromMap(account.Credentials)
	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("missing refresh_token")
	}
	proxyURL := ""
	if account.ProxyID != nil {
		proxyURL = s.resolveProxyURL(ctx, account.ProxyID)
	}
	return s.RefreshToken(ctx, creds, proxyURL)
}

func (s *KiroOAuthService) RefreshToken(ctx context.Context, creds *KiroCredentials, proxyURL string) (*KiroTokenInfo, error) {
	if creds == nil {
		return nil, fmt.Errorf("missing Kiro credentials")
	}
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return nil, fmt.Errorf("missing refresh_token")
	}
	method := normalizeKiroAuthMethod(creds.AuthMethod)
	if method == "" {
		if creds.ClientID != "" && creds.ClientSecret != "" {
			method = KiroAuthMethodIDC
		} else {
			method = KiroAuthMethodSocial
		}
	}
	creds.AuthMethod = method
	region := firstNonEmpty(creds.Region, DefaultKiroRegion)
	idcRegion := firstNonEmpty(creds.IDCRegion, region)
	machineID := creds.MachineID()
	version := DefaultKiroVersion

	var refreshURL string
	var body map[string]any
	headers := map[string]string{"Content-Type": "application/json"}
	if method == KiroAuthMethodIDC {
		if creds.ClientID == "" || creds.ClientSecret == "" {
			return nil, fmt.Errorf("Kiro IdC refresh missing client_id/client_secret")
		}
		refreshURL = fmt.Sprintf("https://oidc.%s.amazonaws.com/token", idcRegion)
		body = map[string]any{
			"refreshToken": creds.RefreshToken,
			"clientId":     creds.ClientID,
			"clientSecret": creds.ClientSecret,
			"grantType":    "refresh_token",
		}
		headers["x-amz-user-agent"] = fmt.Sprintf("aws-sdk-js/3.738.0 KiroIDE-%s-%s", version, machineID)
		headers["User-Agent"] = "node"
	} else {
		refreshURL = fmt.Sprintf("https://prod.%s.auth.desktop.kiro.dev/refreshToken", region)
		body = map[string]any{"refreshToken": creds.RefreshToken}
		headers["User-Agent"] = fmt.Sprintf("KiroIDE-%s-%s", version, machineID)
		headers["Accept"] = "application/json, text/plain, */*"
	}

	data, status, err := postJSONRaw(ctx, newKiroHTTPClient(proxyURL), refreshURL, body, headers)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("Kiro token refresh failed: status=%d body=%s", status, truncateString(string(data), 300))
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse Kiro refresh response: %w", err)
	}
	tokenInfo := kiroTokenInfoFromResponse(parsed, creds)
	if strings.TrimSpace(stringFromJSON(parsed, "accessToken", "access_token")) == "" || strings.TrimSpace(tokenInfo.AccessToken) == "" {
		return nil, fmt.Errorf("Kiro refresh response missing access_token")
	}
	return tokenInfo, nil
}

func (s *KiroOAuthService) BuildAccountCredentials(tokenInfo *KiroTokenInfo) map[string]any {
	if tokenInfo == nil {
		return nil
	}
	out := map[string]any{}
	if tokenInfo.AccessToken != "" {
		out["access_token"] = tokenInfo.AccessToken
	}
	if tokenInfo.RefreshToken != "" {
		out["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.ExpiresAt > 0 {
		out["expires_at"] = fmt.Sprintf("%d", tokenInfo.ExpiresAt)
	}
	if tokenInfo.TokenType != "" {
		out["token_type"] = tokenInfo.TokenType
	}
	if tokenInfo.ClientID != "" {
		out["client_id"] = tokenInfo.ClientID
	}
	if tokenInfo.ClientSecret != "" {
		out["client_secret"] = tokenInfo.ClientSecret
	}
	if tokenInfo.ProfileARN != "" {
		out["profile_arn"] = tokenInfo.ProfileARN
	}
	if tokenInfo.Region != "" {
		out["region"] = tokenInfo.Region
	}
	if tokenInfo.IDCRegion != "" {
		out["idc_region"] = tokenInfo.IDCRegion
	}
	if tokenInfo.AuthMethod != "" {
		out["auth_method"] = tokenInfo.AuthMethod
	}
	if tokenInfo.StartURL != "" {
		out["start_url"] = tokenInfo.StartURL
	}
	if tokenInfo.LastRefresh != "" {
		out["last_refresh"] = tokenInfo.LastRefresh
	}
	return out
}

func (s *KiroOAuthService) getDeviceSession(sessionID string) *KiroDeviceSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[sessionID]
	if session == nil {
		return nil
	}
	if time.Now().After(session.ExpiresAt) {
		delete(s.sessions, sessionID)
		return nil
	}
	return session
}

func (s *KiroOAuthService) resolveProxyURL(ctx context.Context, proxyID *int64) string {
	if s == nil || s.proxyRepo == nil || proxyID == nil {
		return ""
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil || proxy == nil {
		return ""
	}
	return proxy.URL()
}

func (s *KiroOAuthService) postJSON(ctx context.Context, client *http.Client, endpoint string, body map[string]any, headers map[string]string) (map[string]any, error) {
	data, status, err := postJSONRaw(ctx, client, endpoint, body, headers)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Kiro OAuth request failed: status=%d body=%s", status, truncateString(string(data), 300))
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse Kiro OAuth response: %w", err)
	}
	return parsed, nil
}

func newKiroHTTPClient(proxyURL string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(proxyURL) != "" {
		if parsed, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

func postJSONRaw(ctx context.Context, client *http.Client, endpoint string, body map[string]any, headers map[string]string) ([]byte, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func kiroTokenInfoFromResponse(data map[string]any, base *KiroCredentials) *KiroTokenInfo {
	expiresIn := int64(intFromJSON(data, 0, "expiresIn", "expires_in"))
	expiresAt := int64(0)
	if expiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second).Unix()
	}
	return &KiroTokenInfo{
		AccessToken:  firstNonEmpty(stringFromJSON(data, "accessToken", "access_token"), base.AccessToken),
		RefreshToken: firstNonEmpty(stringFromJSON(data, "refreshToken", "refresh_token"), base.RefreshToken),
		ExpiresAt:    expiresAt,
		ExpiresIn:    expiresIn,
		TokenType:    firstNonEmpty(stringFromJSON(data, "tokenType", "token_type"), "Bearer"),
		ClientID:     base.ClientID,
		ClientSecret: base.ClientSecret,
		ProfileARN:   firstNonEmpty(stringFromJSON(data, "profileArn", "profile_arn"), base.ProfileARN),
		Region:       firstNonEmpty(base.Region, DefaultKiroRegion),
		IDCRegion:    firstNonEmpty(base.IDCRegion, firstNonEmpty(base.Region, DefaultKiroRegion)),
		AuthMethod:   normalizeKiroAuthMethod(firstNonEmpty(base.AuthMethod, KiroAuthMethodIDC)),
		StartURL:     firstNonEmpty(base.StartURL, kiroStartURL),
		LastRefresh:  time.Now().UTC().Format(time.RFC3339),
	}
}

func stringFromJSON(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := credentialValueToString(data[key]); s != "" {
			return s
		}
	}
	return ""
}

func intFromJSON(data map[string]any, fallback int, keys ...string) int {
	for _, key := range keys {
		switch v := data[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case int64:
			return int(v)
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return int(i)
			}
		case string:
			var i int
			if _, err := fmt.Sscanf(v, "%d", &i); err == nil {
				return i
			}
		}
	}
	return fallback
}

func randomKiroHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
