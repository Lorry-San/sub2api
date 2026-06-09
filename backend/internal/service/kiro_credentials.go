package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

const (
	DefaultKiroRegion       = "us-east-1"
	DefaultKiroVersion      = "0.12.155"
	DefaultKiroNodeVersion  = "22.22.0"
	DefaultKiroAWSSDK       = "1.0.34"
	DefaultKiroStreamingAPI = "1.0.34"
	DefaultKiroModelSonnet  = "claude-sonnet-4"
	DefaultKiroModelHaiku   = "claude-haiku-4.5"
	DefaultKiroModelOpus    = "claude-opus-4.5"
	DefaultKiroModelOpus46  = "claude-opus-4-6"
	DefaultKiroModelOpus47  = "claude-opus-4-7"
	DefaultKiroModelOpus48  = "claude-opus-4-8"
	KiroAuthMethodIDC       = "idc"
	KiroAuthMethodSocial    = "social"
	KiroAuthMethodExternal  = "external_idp"
	KiroTokenRefreshMargin  = 5 * time.Minute
)

const (
	kiroBuilderIDProfileARN       = "arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX"
	kiroSocialProfileARN          = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"
	kiroEnterpriseFallbackAccount = "610548660232"
	kiroEnterpriseFallbackProfile = "VNECVYCYYAWN"
)

var kiroClaudeMinorVersionPattern = regexp.MustCompile(`(?i)^(claude-(?:sonnet|haiku|opus))-(\d+)-(\d{1,2})([^0-9].*)?$`)

var kiroDefaultModels = []claude.Model{
	{
		ID:          DefaultKiroModelSonnet,
		Type:        "model",
		DisplayName: "Claude Sonnet 4",
		CreatedAt:   "",
	},
	{
		ID:          DefaultKiroModelHaiku,
		Type:        "model",
		DisplayName: "Claude Haiku 4.5",
		CreatedAt:   "",
	},
	{
		ID:          DefaultKiroModelOpus,
		Type:        "model",
		DisplayName: "Claude Opus 4.5",
		CreatedAt:   "",
	},
	{
		ID:          DefaultKiroModelOpus46,
		Type:        "model",
		DisplayName: "Claude Opus 4.6",
		CreatedAt:   "2026-02-05T00:00:00Z",
	},
	{
		ID:          DefaultKiroModelOpus47,
		Type:        "model",
		DisplayName: "Claude Opus 4.7",
		CreatedAt:   "2026-04-17T00:00:00Z",
	},
	{
		ID:          DefaultKiroModelOpus48,
		Type:        "model",
		DisplayName: "Claude Opus 4.8",
		CreatedAt:   "2026-05-29T00:00:00Z",
	},
}

func KiroDefaultModels() []claude.Model {
	models := make([]claude.Model, len(kiroDefaultModels))
	copy(models, kiroDefaultModels)
	return models
}

func KiroDefaultModelIDs() []string {
	ids := make([]string, len(kiroDefaultModels))
	for i, model := range kiroDefaultModels {
		ids[i] = model.ID
	}
	return ids
}

func isKiroSupportedModel(model string) bool {
	model = strings.TrimSpace(model)
	for _, supported := range kiroDefaultModels {
		if model == supported.ID {
			return true
		}
	}
	return false
}

type KiroCredentials struct {
	AccessToken  string
	RefreshToken string
	ClientID     string
	ClientSecret string
	ProfileARN   string
	Region       string
	IDCRegion    string
	AuthMethod   string
	Provider     string
	UUID         string
	StartURL     string
	ExpiresAt    *time.Time
	LastRefresh  string
}

func NewKiroCredentialsFromMap(raw map[string]any) *KiroCredentials {
	if raw == nil {
		raw = map[string]any{}
	}
	c := &KiroCredentials{
		AccessToken:  firstKiroCredentialString(raw, "access_token", "accessToken"),
		RefreshToken: firstKiroCredentialString(raw, "refresh_token", "refreshToken"),
		ClientID:     firstKiroCredentialString(raw, "client_id", "clientId"),
		ClientSecret: firstKiroCredentialString(raw, "client_secret", "clientSecret"),
		ProfileARN:   firstKiroCredentialString(raw, "profile_arn", "profileArn"),
		Region:       firstKiroCredentialString(raw, "region"),
		IDCRegion:    firstKiroCredentialString(raw, "idc_region", "idcRegion"),
		AuthMethod:   normalizeKiroAuthMethod(firstKiroCredentialString(raw, "auth_method", "authMethod")),
		Provider:     firstKiroCredentialString(raw, "provider", "login_method", "loginMethod", "auth_provider", "authProvider"),
		UUID:         firstKiroCredentialString(raw, "uuid"),
		StartURL:     firstKiroCredentialString(raw, "start_url", "startUrl"),
		LastRefresh:  firstKiroCredentialString(raw, "last_refresh", "lastRefresh"),
	}
	if c.Region == "" {
		c.Region = DefaultKiroRegion
	}
	if c.IDCRegion == "" {
		c.IDCRegion = c.Region
	}
	if c.AuthMethod == "" {
		if c.ClientID != "" && c.ClientSecret != "" {
			c.AuthMethod = KiroAuthMethodIDC
		}
	}
	if c.AuthMethod == KiroAuthMethodIDC && c.ClientID != "" && c.ClientSecret != "" && isKiroEnterpriseStartURL(c.StartURL) {
		c.AuthMethod = KiroAuthMethodExternal
	}
	c.ExpiresAt = parseKiroCredentialTime(firstKiroCredentialString(raw, "expires_at", "expiresAt", "expire"))
	return c
}

func (c *KiroCredentials) IsExpired(now time.Time) bool {
	if c == nil || c.ExpiresAt == nil {
		return true
	}
	return !c.ExpiresAt.After(now.Add(KiroTokenRefreshMargin))
}

func (c *KiroCredentials) MachineID() string {
	source := ""
	for _, candidate := range []string{c.UUID, c.ProfileARN, c.ClientID, machineIDFromHost()} {
		if strings.TrimSpace(candidate) != "" {
			source = strings.TrimSpace(candidate)
			break
		}
	}
	if source == "" {
		source = "KIRO_DEFAULT_MACHINE"
	}
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func (c *KiroCredentials) ToCredentialMap() map[string]any {
	out := map[string]any{
		"access_token": c.AccessToken,
		"region":       firstNonEmpty(c.Region, DefaultKiroRegion),
		"idc_region":   firstNonEmpty(c.IDCRegion, firstNonEmpty(c.Region, DefaultKiroRegion)),
		"auth_method":  normalizeKiroAuthMethod(c.AuthMethod),
	}
	if c.RefreshToken != "" {
		out["refresh_token"] = c.RefreshToken
	}
	if c.ClientID != "" {
		out["client_id"] = c.ClientID
	}
	if c.ClientSecret != "" {
		out["client_secret"] = c.ClientSecret
	}
	if c.ProfileARN != "" {
		out["profile_arn"] = c.ProfileARN
	}
	if c.Provider != "" {
		out["provider"] = c.Provider
	}
	if c.UUID != "" {
		out["uuid"] = c.UUID
	}
	if c.StartURL != "" {
		out["start_url"] = c.StartURL
	}
	if c.ExpiresAt != nil {
		out["expires_at"] = strconv.FormatInt(c.ExpiresAt.Unix(), 10)
	}
	if c.LastRefresh != "" {
		out["last_refresh"] = c.LastRefresh
	}
	return out
}

func normalizeKiroAuthMethod(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "", "idc", "builder-id", "builder_id":
		if strings.TrimSpace(method) == "" {
			return ""
		}
		return KiroAuthMethodIDC
	case KiroAuthMethodSocial:
		return KiroAuthMethodSocial
	case KiroAuthMethodExternal, "external-idp", "externalidp", "enterprise", "iam", "iam-identity-center", "iam_identity_center":
		return KiroAuthMethodExternal
	default:
		return strings.ToLower(strings.TrimSpace(method))
	}
}

func (c *KiroCredentials) UsesIDCRefresh() bool {
	if c == nil {
		return false
	}
	method := normalizeKiroAuthMethod(c.AuthMethod)
	return method == KiroAuthMethodIDC || method == KiroAuthMethodExternal
}

func (c *KiroCredentials) RequiresExternalIDPTokenType() bool {
	if c == nil {
		return false
	}
	return c.IsEnterpriseExternalIDP()
}

func (c *KiroCredentials) IsEnterpriseExternalIDP() bool {
	if c == nil {
		return false
	}
	method := normalizeKiroAuthMethod(c.AuthMethod)
	provider := strings.ToLower(strings.TrimSpace(c.Provider))
	return method == KiroAuthMethodExternal ||
		provider == "enterprise" ||
		provider == "externalidp" ||
		provider == "external_idp" ||
		provider == "external-idp" ||
		(method == KiroAuthMethodIDC && c.ClientID != "" && c.ClientSecret != "" && isKiroEnterpriseStartURL(c.StartURL))
}

func (c *KiroCredentials) IsSocialLogin() bool {
	if c == nil {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(c.Provider))
	return normalizeKiroAuthMethod(c.AuthMethod) == KiroAuthMethodSocial ||
		provider == "github" ||
		provider == "google"
}

func (c *KiroCredentials) EffectiveProfileARN() string {
	if c == nil {
		return ""
	}
	if arn := strings.TrimSpace(c.ProfileARN); arn != "" && !isKiroPlaceholderProfileARN(arn) {
		return arn
	}
	if c.IsEnterpriseExternalIDP() {
		return kiroEnterpriseFallbackProfileARN(c.Region)
	}
	if c.IsSocialLogin() {
		return kiroSocialProfileARN
	}
	return kiroBuilderIDProfileARN
}

func isKiroPlaceholderProfileARN(arn string) bool {
	return strings.TrimSpace(arn) == kiroBuilderIDProfileARN
}

func isKiroEnterpriseStartURL(startURL string) bool {
	normalized := strings.TrimRight(strings.TrimSpace(startURL), "/")
	if normalized == "" {
		return false
	}
	return !strings.EqualFold(normalized, strings.TrimRight(kiroStartURL, "/"))
}

func kiroEnterpriseFallbackProfileARN(region string) string {
	r := firstNonEmpty(region, DefaultKiroRegion)
	if strings.HasPrefix(r, "eu-") {
		r = "eu-central-1"
	} else {
		r = DefaultKiroRegion
	}
	return fmt.Sprintf("arn:aws:codewhisperer:%s:%s:profile/%s", r, kiroEnterpriseFallbackAccount, kiroEnterpriseFallbackProfile)
}

func firstKiroCredentialString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := raw[key]; ok {
			if s := credentialValueToString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func credentialValueToString(v any) string {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case json.Number:
		return val.String()
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(val, 10)
	case int:
		return strconv.Itoa(val)
	default:
		return ""
	}
}

func parseKiroCredentialTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if unix > 1_000_000_000_000 {
			t := time.UnixMilli(unix)
			return &t
		}
		t := time.Unix(unix, 0)
		return &t
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t
		}
	}
	return nil
}

func machineIDFromHost() string {
	host, _ := os.Hostname()
	return host
}

func kiroOSString() string {
	switch runtime.GOOS {
	case "windows":
		return "win32#10.0.19043"
	case "darwin":
		return "macos#14.0.0"
	case "linux":
		if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			if release := strings.TrimSpace(string(data)); release != "" {
				return "linux#" + release
			}
		}
		return "linux#5.15.0"
	default:
		return fmt.Sprintf("%s#unknown", runtime.GOOS)
	}
}

func kiroUpstreamModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return model
	}
	return kiroClaudeMinorVersionPattern.ReplaceAllString(model, "$1-$2.$3$4")
}

func defaultKiroMappedModel(requested string) string {
	lower := strings.ToLower(strings.TrimSpace(requested))
	switch {
	case lower == "":
		return DefaultKiroModelSonnet
	case isKiroSupportedModel(lower):
		return lower
	case strings.Contains(lower, "haiku"):
		return DefaultKiroModelHaiku
	case strings.Contains(lower, "opus-4-8"), strings.Contains(lower, "opus-4.8"):
		return DefaultKiroModelOpus48
	case strings.Contains(lower, "opus-4-7"), strings.Contains(lower, "opus-4.7"):
		return DefaultKiroModelOpus47
	case strings.Contains(lower, "opus-4-6"), strings.Contains(lower, "opus-4.6"):
		return DefaultKiroModelOpus46
	case strings.Contains(lower, "opus"):
		return DefaultKiroModelOpus
	case strings.Contains(lower, "sonnet"), strings.Contains(lower, "claude-3-5"), strings.Contains(lower, "claude-4"):
		return DefaultKiroModelSonnet
	default:
		return strings.TrimSpace(requested)
	}
}
