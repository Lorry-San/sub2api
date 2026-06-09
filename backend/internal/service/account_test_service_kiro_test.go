//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestService_KiroUsesNativeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newTestContext()

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"assistantResponseEvent":{"content":"ok"}}`)),
		},
	}
	repo := &mockAccountRepoForGemini{}
	kiroGateway := NewKiroGatewayService(repo, upstream, nil)
	svc := &AccountTestService{
		accountRepo:        repo,
		kiroGatewayService: kiroGateway,
		httpUpstream:       upstream,
	}
	account := &Account{
		ID:          91,
		Name:        "kiro-oauth",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "kiro-token",
			"expires_at":   strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		},
	}

	err := svc.testKiroAccountConnection(c, account, "claude-sonnet-4-6")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, kiroAssistantURL, upstream.lastReq.URL.String())
	require.Equal(t, "Bearer kiro-token", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, upstream.lastReq.Header.Get("User-Agent"), "KiroIDE-")
	require.Contains(t, upstream.lastReq.Header.Get("x-amz-user-agent"), "KiroIDE ")
	require.Contains(t, upstream.lastReq.Header.Get("User-Agent"), "api/codewhispererstreaming#")
	require.Contains(t, recorder.Body.String(), `"model":"claude-sonnet-4"`)
	require.Contains(t, recorder.Body.String(), `"text":"ok"`)
	require.Contains(t, recorder.Body.String(), `"success":true`)

	var sent map[string]any
	require.NoError(t, json.Unmarshal(upstream.lastBody, &sent))
	conversationState := sent["conversationState"].(map[string]any)
	currentMessage := conversationState["currentMessage"].(map[string]any)
	userInput := currentMessage["userInputMessage"].(map[string]any)
	require.Equal(t, "claude-sonnet-4", userInput["modelId"])
	require.Equal(t, kiroBuilderIDProfileARN, sent["profileArn"])
	require.Equal(t, kiroBuilderIDProfileARN, conversationState["profileArn"])
	require.Equal(t, "attempt=1; max=3", upstream.lastReq.Header.Get("amz-sdk-request"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("amz-sdk-invocation-id"))
}

func TestAccountTestService_KiroPreservesNewerOpusModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newTestContext()

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"assistantResponseEvent":{"content":"ok"}}`)),
		},
	}
	repo := &mockAccountRepoForGemini{}
	kiroGateway := NewKiroGatewayService(repo, upstream, nil)
	svc := &AccountTestService{
		accountRepo:        repo,
		kiroGatewayService: kiroGateway,
		httpUpstream:       upstream,
	}
	account := &Account{
		ID:          93,
		Name:        "kiro-oauth-opus",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "kiro-token",
			"expires_at":   strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		},
	}

	err := svc.testKiroAccountConnection(c, account, DefaultKiroModelOpus48)
	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), `"model":"claude-opus-4-8"`)

	var sent map[string]any
	require.NoError(t, json.Unmarshal(upstream.lastBody, &sent))
	conversationState := sent["conversationState"].(map[string]any)
	currentMessage := conversationState["currentMessage"].(map[string]any)
	userInput := currentMessage["userInputMessage"].(map[string]any)
	require.Equal(t, "claude-opus-4.8", userInput["modelId"])
}

func TestKiroGatewayService_EstimatesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello world from kiro usage estimation"}],"tools":[{"name":"read_file","description":"read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"max_tokens":256}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"assistantResponseEvent":{"content":"estimated output tokens from kiro"}}`)),
		},
	}
	svc := NewKiroGatewayService(&mockAccountRepoForGemini{}, upstream, nil)
	account := &Account{
		ID:          94,
		Name:        "kiro-usage",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "kiro-token",
			"expires_at":   strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		},
	}
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: DefaultKiroModelOpus48}

	result, err := svc.ForwardAnthropic(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Greater(t, result.Usage.InputTokens, 0)
	require.Greater(t, result.Usage.OutputTokens, 0)
	require.Equal(t, result.Usage.InputTokens, int(gjson.Get(rec.Body.String(), "usage.input_tokens").Int()))
	require.Equal(t, result.Usage.OutputTokens, int(gjson.Get(rec.Body.String(), "usage.output_tokens").Int()))
}

func TestKiroGatewayService_BuildsKiroProxyCompatiblePayload(t *testing.T) {
	req := kiroAnthropicRequest{
		Model:     DefaultKiroModelOpus48,
		Messages:  []kiroAnthropicMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 2048,
		Thinking:  map[string]any{"type": "enabled", "budget_tokens": 8000},
		Metadata:  map[string]any{"session_id": "session-one"},
	}
	temperature := 0.7
	topP := 0.9
	req.Temperature = &temperature
	req.TopP = &topP
	creds := &KiroCredentials{
		AuthMethod: KiroAuthMethodExternal,
		Region:     "us-east-1",
		StartURL:   "https://example.awsapps.com/start",
	}

	payload, err := buildKiroRequestFromAnthropic(req, DefaultKiroModelOpus48, creds)
	require.NoError(t, err)
	require.Equal(t, kiroEnterpriseFallbackProfileARN("us-east-1"), payload["profileArn"])
	require.Equal(t, float64(2048), gjson.GetBytes(mustJSONBytes(t, payload), "inferenceConfig.maxTokens").Float())
	require.Equal(t, 0.7, gjson.GetBytes(mustJSONBytes(t, payload), "inferenceConfig.temperature").Float())
	require.Equal(t, 0.9, gjson.GetBytes(mustJSONBytes(t, payload), "inferenceConfig.topP").Float())
	require.Equal(t, "adaptive", gjson.GetBytes(mustJSONBytes(t, payload), "additionalModelRequestFields.thinking.type").String())
	require.Equal(t, "medium", gjson.GetBytes(mustJSONBytes(t, payload), "additionalModelRequestFields.output_config.effort").String())

	payloadAgain, err := buildKiroRequestFromAnthropic(req, DefaultKiroModelOpus48, creds)
	require.NoError(t, err)
	firstID := gjson.GetBytes(mustJSONBytes(t, payload), "conversationState.conversationId").String()
	secondID := gjson.GetBytes(mustJSONBytes(t, payloadAgain), "conversationState.conversationId").String()
	require.NotEmpty(t, firstID)
	require.Equal(t, firstID, secondID)
}

func TestKiroGatewayService_EnterpriseHeadersUseExternalIDPTokenType(t *testing.T) {
	header := http.Header{}
	setKiroHeaders(header, "token", &KiroCredentials{
		AuthMethod: KiroAuthMethodExternal,
		StartURL:   "https://example.awsapps.com/start",
	})

	require.Equal(t, "Bearer token", header.Get("Authorization"))
	require.Equal(t, "EXTERNAL_IDP", header.Get("TokenType"))
	require.Equal(t, "vibe", header.Get("x-amzn-kiro-agent-mode"))
	require.Equal(t, "attempt=1; max=3", header.Get("amz-sdk-request"))
}

func TestKiroUsageEndpointUsesEffectiveProfileARN(t *testing.T) {
	socialEndpoint := buildKiroUsageEndpoint(&KiroCredentials{AuthMethod: KiroAuthMethodSocial})
	require.Contains(t, socialEndpoint, "profileArn=arn%3Aaws%3Acodewhisperer%3Aus-east-1%3A699475941385%3Aprofile%2FEHGA3GRVQMUK")

	enterpriseEndpoint := buildKiroUsageEndpoint(&KiroCredentials{AuthMethod: KiroAuthMethodExternal, Region: "eu-west-1"})
	require.Contains(t, enterpriseEndpoint, "https://q.eu-central-1.amazonaws.com/getUsageLimits")
	require.Contains(t, enterpriseEndpoint, "profileArn=arn%3Aaws%3Acodewhisperer%3Aeu-central-1%3A610548660232%3Aprofile%2FVNECVYCYYAWN")
}

func TestKiroCredentials_ExternalIDPUsesIDCRefresh(t *testing.T) {
	creds := NewKiroCredentialsFromMap(map[string]any{
		"auth_method":   "external_idp",
		"client_id":     "client",
		"client_secret": "secret",
		"start_url":     "https://example.awsapps.com/start",
	})

	require.True(t, creds.UsesIDCRefresh())
	require.True(t, creds.RequiresExternalIDPTokenType())
	require.Equal(t, KiroAuthMethodExternal, creds.AuthMethod)
}

func TestKiroGatewayService_UsesDotMinorVersionForUpstreamModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"assistantResponseEvent":{"content":"ok"}}`)),
		},
	}
	svc := NewKiroGatewayService(&mockAccountRepoForGemini{}, upstream, nil)
	account := &Account{
		ID:          97,
		Name:        "kiro-opus-47",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "kiro-token",
			"expires_at":   strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		},
	}

	result, err := svc.ForwardAnthropic(context.Background(), c, account, &ParsedRequest{Body: NewRequestBodyRef(body), Model: DefaultKiroModelOpus47})
	require.NoError(t, err)
	require.Equal(t, DefaultKiroModelOpus47, result.UpstreamModel)
	require.Equal(t, DefaultKiroModelOpus47, gjson.Get(rec.Body.String(), "model").String())
	require.Equal(t, "claude-opus-4.7", gjson.GetBytes(upstream.lastBody, "conversationState.currentMessage.userInputMessage.modelId").String())
}

func TestKiroGatewayService_FetchesEnterpriseProfileARNBeforeRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	realARN := "arn:aws:codewhisperer:us-east-1:111122223333:profile/REALPROFILE"
	upstream := &httpUpstreamRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"profiles":[{"arn":"` + realARN + `"}]}`)),
			},
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"assistantResponseEvent":{"content":"ok"}}`)),
			},
		},
	}
	account := &Account{
		ID:          98,
		Name:        "kiro-enterprise",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "enterprise-token",
			"refresh_token": "enterprise-refresh",
			"expires_at":    strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
			"auth_method":   KiroAuthMethodExternal,
			"client_id":     "client",
			"client_secret": "secret",
			"start_url":     "https://example.awsapps.com/start",
		},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	svc := NewKiroGatewayService(repo, upstream, nil)

	_, err := svc.ForwardAnthropic(context.Background(), c, account, &ParsedRequest{Body: NewRequestBodyRef(body), Model: DefaultKiroModelSonnet})
	require.NoError(t, err)
	require.Len(t, upstream.requests, 2)
	require.Contains(t, upstream.requests[0].URL.String(), "/ListAvailableProfiles")
	require.Equal(t, "EXTERNAL_IDP", upstream.requests[0].Header.Get("TokenType"))
	require.Equal(t, kiroAssistantURL, upstream.requests[1].URL.String())
	require.Equal(t, realARN, account.GetCredential("profile_arn"))
	require.Equal(t, realARN, gjson.GetBytes(upstream.lastBody, "profileArn").String())
	require.Equal(t, realARN, gjson.GetBytes(upstream.lastBody, "conversationState.profileArn").String())
}

func TestKiroGatewayService_UsesLatestCredentialsWhenSnapshotTokenDiffers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"assistantResponseEvent":{"content":"ok"}}`)),
		},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{
		95: {
			ID:          95,
			Name:        "kiro-db",
			Platform:    PlatformKiro,
			Type:        AccountTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":  "new-token",
				"refresh_token": "new-refresh",
				"expires_at":    strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
			},
		},
	}}
	svc := NewKiroGatewayService(repo, upstream, nil)
	account := &Account{
		ID:          95,
		Name:        "kiro-cache",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "old-token",
			"refresh_token": "old-refresh",
			"expires_at":    strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		},
	}
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: DefaultKiroModelSonnet}

	_, err := svc.ForwardAnthropic(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "Bearer new-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "new-token", account.GetCredential("access_token"))
}

func TestKiroGatewayService_UsesLatestCredentialsWhenKiroAuthConfigDiffers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"assistantResponseEvent":{"content":"ok"}}`)),
		},
	}
	expiresAt := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	realARN := "arn:aws:codewhisperer:us-east-1:111122223333:profile/REALPROFILE"
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{
		99: {
			ID:          99,
			Name:        "kiro-db-enterprise",
			Platform:    PlatformKiro,
			Type:        AccountTypeOAuth,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token":  "same-token",
				"refresh_token": "same-refresh",
				"expires_at":    expiresAt,
				"auth_method":   KiroAuthMethodExternal,
				"client_id":     "client",
				"client_secret": "secret",
				"start_url":     "https://example.awsapps.com/start",
				"profile_arn":   realARN,
				"region":        "us-east-1",
			},
		},
	}}
	cache := &snapshotHydrationCache{}
	snapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	svc := NewKiroGatewayService(repo, upstream, nil, snapshot)
	account := &Account{
		ID:          99,
		Name:        "kiro-cache-stale-auth",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "same-token",
			"refresh_token": "same-refresh",
			"expires_at":    expiresAt,
		},
	}
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: DefaultKiroModelSonnet}

	_, err := svc.ForwardAnthropic(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "Bearer same-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "EXTERNAL_IDP", upstream.lastReq.Header.Get("TokenType"))
	require.Equal(t, realARN, gjson.GetBytes(upstream.lastBody, "profileArn").String())
	require.Equal(t, KiroAuthMethodExternal, account.GetCredential("auth_method"))
	require.Equal(t, realARN, account.GetCredential("profile_arn"))

	cached, err := snapshot.GetAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, realARN, cached.GetCredential("profile_arn"))
}

func TestKiroGatewayService_RefreshSyncsSchedulerCache(t *testing.T) {
	account := &Account{
		ID:          96,
		Name:        "kiro-sync",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":   "fresh-token",
			"expires_at":     strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
			"_token_version": int64(300),
		},
	}
	cache := &snapshotHydrationCache{}
	snapshot := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)
	svc := NewKiroGatewayService(nil, &httpUpstreamRecorder{}, nil, snapshot)

	svc.syncSchedulerAccountCache(context.Background(), account)

	cached, err := snapshot.GetAccount(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, "fresh-token", cached.GetCredential("access_token"))
}

func TestEstimateKiroCountTokensFromBody(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","system":"你是一个代码助手","messages":[{"role":"user","content":"please explain this function"}]}`)

	require.Greater(t, EstimateKiroCountTokensFromBody(body), 0)
	require.Greater(t, EstimateKiroCountTokensFromBody([]byte(`not-json-but-still-countable`)), 0)
}

func TestAccountTestService_KiroAccountConnectionDoesNotFallbackToClaude(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/92/test", nil)

	account := &Account{
		ID:          92,
		Name:        "kiro-oauth",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "kiro-token",
			"expires_at":   strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"assistantResponseEvent":{"content":"ok"}}`)),
		},
	}
	kiroGateway := NewKiroGatewayService(repo, upstream, nil)
	svc := &AccountTestService{
		accountRepo:        repo,
		kiroGatewayService: kiroGateway,
		httpUpstream:       upstream,
	}

	err := svc.TestAccountConnection(c, account.ID, "claude-sonnet-4-6", "", "")
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, kiroAssistantURL, upstream.lastReq.URL.String())
	require.NotEqual(t, testClaudeAPIURL, upstream.lastReq.URL.String())
	require.Contains(t, rec.Body.String(), `"model":"claude-sonnet-4"`)
}

func mustJSONBytes(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}
