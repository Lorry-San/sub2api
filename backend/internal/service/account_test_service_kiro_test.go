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
	require.Contains(t, upstream.lastReq.Header.Get("x-amz-user-agent"), "KiroIDE-")
	require.Contains(t, recorder.Body.String(), `"model":"claude-sonnet-4"`)
	require.Contains(t, recorder.Body.String(), `"text":"ok"`)
	require.Contains(t, recorder.Body.String(), `"success":true`)

	var sent map[string]any
	require.NoError(t, json.Unmarshal(upstream.lastBody, &sent))
	conversationState := sent["conversationState"].(map[string]any)
	currentMessage := conversationState["currentMessage"].(map[string]any)
	userInput := currentMessage["userInputMessage"].(map[string]any)
	require.Equal(t, "claude-sonnet-4", userInput["modelId"])
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
	require.Equal(t, DefaultKiroModelOpus48, userInput["modelId"])
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
