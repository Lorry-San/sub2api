package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func normalizeKiroTestModel(modelID string) string {
	model := strings.TrimSpace(defaultKiroMappedModel(modelID))
	switch model {
	case DefaultKiroModelSonnet, DefaultKiroModelHaiku, DefaultKiroModelOpus:
		return model
	default:
		return DefaultKiroModelSonnet
	}
}

// testKiroAccountConnection tests a Kiro/Amazon Q account using the native
// generateAssistantResponse request shape instead of the Anthropic API.
func (s *AccountTestService) testKiroAccountConnection(c *gin.Context, account *Account, modelID string) error {
	ctx := c.Request.Context()

	requestedModel := strings.TrimSpace(modelID)
	if requestedModel == "" {
		requestedModel = DefaultKiroModelSonnet
	}
	testModelID := normalizeKiroTestModel(account.GetMappedModel(requestedModel))

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})

	if s.kiroGatewayService == nil || s.httpUpstream == nil {
		return s.sendErrorAndEnd(c, "Kiro gateway service not configured")
	}

	creds := NewKiroCredentialsFromMap(account.Credentials)
	accessToken, updatedCreds, err := s.kiroGatewayService.resolveAccessToken(ctx, account, creds)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to get Kiro access token: %s", err.Error()))
	}
	if updatedCreds != nil {
		account.Credentials = MergeCredentials(account.Credentials, updatedCreds)
		creds = NewKiroCredentialsFromMap(account.Credentials)
	}

	payload, err := buildKiroRequestFromAnthropic(kiroAnthropicRequest{
		Model:     testModelID,
		Messages:  []kiroAnthropicMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 256,
	}, testModelID, creds)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to build Kiro request: %s", err.Error()))
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to encode Kiro request: %s", err.Error()))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kiroAssistantURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Kiro request")
	}
	setKiroHeaders(req.Header, accessToken, creds)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Kiro request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to read Kiro response: %s", err.Error()))
	}

	if resp.StatusCode >= 400 {
		errMsg := fmt.Sprintf("Kiro API returned %d: %s", resp.StatusCode, string(raw))
		if resp.StatusCode == http.StatusUnauthorized && s.accountRepo != nil {
			_ = s.accountRepo.SetError(ctx, account.ID, errMsg)
		}
		return s.sendErrorAndEnd(c, errMsg)
	}

	parsed := parseKiroEventStream(raw)
	text := strings.Join(parsed.Content, "")
	if strings.TrimSpace(text) == "" {
		text = "(empty response)"
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: text})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
