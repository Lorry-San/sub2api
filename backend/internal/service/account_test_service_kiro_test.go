//go:build unit

package service

import (
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
