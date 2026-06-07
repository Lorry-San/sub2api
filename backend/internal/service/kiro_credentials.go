package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultKiroRegion      = "us-east-1"
	DefaultKiroVersion     = "0.1.25"
	DefaultKiroNodeVersion = "20.18.0"
	DefaultKiroModelSonnet = "claude-sonnet-4"
	DefaultKiroModelHaiku  = "claude-haiku-4.5"
	DefaultKiroModelOpus   = "claude-opus-4.5"
	KiroAuthMethodIDC      = "idc"
	KiroAuthMethodSocial   = "social"
	KiroTokenRefreshMargin = 5 * time.Minute
)

type KiroCredentials struct {
	AccessToken  string
	RefreshToken string
	ClientID     string
	ClientSecret string
	ProfileARN   string
	Region       string
	IDCRegion    string
	AuthMethod   string
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
		} else {
			c.AuthMethod = KiroAuthMethodSocial
		}
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
	default:
		return strings.ToLower(strings.TrimSpace(method))
	}
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
		return "windows#10.0"
	case "darwin":
		return "macos#14.0"
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

func defaultKiroMappedModel(requested string) string {
	lower := strings.ToLower(strings.TrimSpace(requested))
	switch {
	case lower == "":
		return DefaultKiroModelSonnet
	case strings.Contains(lower, "haiku"):
		return DefaultKiroModelHaiku
	case strings.Contains(lower, "opus"):
		return DefaultKiroModelOpus
	case strings.Contains(lower, "sonnet"), strings.Contains(lower, "claude-3-5"), strings.Contains(lower, "claude-4"):
		return DefaultKiroModelSonnet
	default:
		return strings.TrimSpace(requested)
	}
}
