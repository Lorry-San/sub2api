package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	kiroAssistantURL = "https://q.us-east-1.amazonaws.com/generateAssistantResponse"
	kiroModelsURL    = "https://q.us-east-1.amazonaws.com/ListAvailableModels"
)

type KiroGatewayService struct {
	accountRepo      AccountRepository
	httpUpstream     HTTPUpstream
	kiroOAuthService *KiroOAuthService
}

func NewKiroGatewayService(accountRepo AccountRepository, httpUpstream HTTPUpstream, kiroOAuthService *KiroOAuthService) *KiroGatewayService {
	return &KiroGatewayService{
		accountRepo:      accountRepo,
		httpUpstream:     httpUpstream,
		kiroOAuthService: kiroOAuthService,
	}
}

type kiroAnthropicRequest struct {
	Model     string                 `json:"model"`
	System    any                    `json:"system,omitempty"`
	Messages  []kiroAnthropicMessage `json:"messages"`
	Tools     []map[string]any       `json:"tools,omitempty"`
	Thinking  map[string]any         `json:"thinking,omitempty"`
	Stream    bool                   `json:"stream,omitempty"`
	MaxTokens int                    `json:"max_tokens,omitempty"`
}

type kiroAnthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type kiroParsedResponse struct {
	Content    []string
	ToolUses   []map[string]any
	StopReason string
}

type kiroEstimatedUsage struct {
	InputTokens  int
	OutputTokens int
}

func (s *KiroGatewayService) ForwardAnthropic(ctx context.Context, c *gin.Context, account *Account, parsed *ParsedRequest) (*ForwardResult, error) {
	if s == nil || s.httpUpstream == nil {
		return nil, fmt.Errorf("Kiro gateway service is not configured")
	}
	start := time.Now()
	body := parsed.Body.Bytes()
	var reqPayload kiroAnthropicRequest
	if err := json.Unmarshal(body, &reqPayload); err != nil {
		return nil, fmt.Errorf("parse Kiro Anthropic request: %w", err)
	}
	if strings.TrimSpace(reqPayload.Model) == "" {
		return nil, fmt.Errorf("missing model")
	}

	creds := NewKiroCredentialsFromMap(account.Credentials)
	accessToken, updatedCreds, err := s.resolveAccessToken(ctx, account, creds)
	if err != nil {
		return nil, err
	}
	if updatedCreds != nil {
		account.Credentials = MergeCredentials(account.Credentials, updatedCreds)
		creds = NewKiroCredentialsFromMap(account.Credentials)
	}

	requestedModel := reqPayload.Model
	mappedModel := defaultKiroMappedModel(account.GetMappedModel(requestedModel))
	kiroBody, err := buildKiroRequestFromAnthropic(reqPayload, mappedModel, creds)
	if err != nil {
		return nil, err
	}
	wireBody, err := json.Marshal(kiroBody)
	if err != nil {
		return nil, err
	}

	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, kiroAssistantURL, bytes.NewReader(wireBody))
	if err != nil {
		return nil, err
	}
	setKiroHeaders(upstreamReq.Header, accessToken, creds)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		s.writeKiroJSONError(c, http.StatusBadGateway, "upstream_error", "Kiro upstream request failed")
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("read Kiro upstream response: %w", err)
	}
	if resp.StatusCode >= 400 {
		if shouldKiroFailover(resp.StatusCode) {
			return nil, &UpstreamFailoverError{
				StatusCode:      resp.StatusCode,
				ResponseBody:    raw,
				ResponseHeaders: resp.Header.Clone(),
			}
		}
		c.Data(resp.StatusCode, firstNonEmpty(resp.Header.Get("Content-Type"), "application/json"), raw)
		return &ForwardResult{Model: requestedModel, UpstreamModel: mappedModel, Duration: time.Since(start)}, nil
	}

	parsedResp := parseKiroEventStream(raw)
	usage := estimateKiroUsage(reqPayload, parsedResp)
	if reqPayload.Stream {
		writeKiroAnthropicStream(c, requestedModel, parsedResp, usage)
	} else {
		c.JSON(http.StatusOK, kiroAnthropicResponse(requestedModel, parsedResp, usage))
	}

	return &ForwardResult{
		Model:         requestedModel,
		UpstreamModel: mappedModel,
		Stream:        reqPayload.Stream,
		Duration:      time.Since(start),
		Usage: ClaudeUsage{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
		},
	}, nil
}

func (s *KiroGatewayService) resolveAccessToken(ctx context.Context, account *Account, creds *KiroCredentials) (string, map[string]any, error) {
	if creds == nil {
		return "", nil, fmt.Errorf("missing Kiro credentials")
	}
	if !creds.IsExpired(time.Now()) && strings.TrimSpace(creds.AccessToken) != "" {
		return creds.AccessToken, nil, nil
	}
	if s.kiroOAuthService == nil {
		if strings.TrimSpace(creds.AccessToken) != "" {
			return creds.AccessToken, nil, nil
		}
		return "", nil, fmt.Errorf("missing Kiro access token")
	}
	tokenInfo, err := s.kiroOAuthService.RefreshAccountToken(ctx, account)
	if err != nil {
		if strings.TrimSpace(creds.AccessToken) != "" {
			return creds.AccessToken, nil, nil
		}
		return "", nil, err
	}
	newCredentials := s.kiroOAuthService.BuildAccountCredentials(tokenInfo)
	if s.accountRepo != nil {
		merged := MergeCredentials(account.Credentials, newCredentials)
		merged["_token_version"] = time.Now().UnixMilli()
		if err := persistAccountCredentials(ctx, s.accountRepo, account, merged); err != nil {
			return "", nil, fmt.Errorf("persist Kiro refreshed token: %w", err)
		}
		newCredentials = merged
	}
	return tokenInfo.AccessToken, newCredentials, nil
}

func buildKiroRequestFromAnthropic(req kiroAnthropicRequest, model string, creds *KiroCredentials) (map[string]any, error) {
	userContent, history, toolResults := convertAnthropicMessagesToKiro(req.Messages, req.System, req.Thinking)
	if strings.TrimSpace(userContent) == "" {
		userContent = "Continue"
	}
	userMsg := map[string]any{
		"content": userContent,
		"modelId": model,
		"origin":  "AI_EDITOR",
	}
	contextPayload := map[string]any{}
	if tools := convertAnthropicToolsToKiro(req.Tools); len(tools) > 0 {
		contextPayload["tools"] = tools
	}
	if len(toolResults) > 0 {
		contextPayload["toolResults"] = dedupeKiroToolResults(toolResults)
	}
	if len(contextPayload) > 0 {
		userMsg["userInputMessageContext"] = contextPayload
	}
	conversationState := map[string]any{
		"agentContinuationId": uuid.NewString(),
		"agentTaskType":       "vibe",
		"chatTriggerType":     "MANUAL",
		"conversationId":      uuid.NewString(),
		"currentMessage":      map[string]any{"userInputMessage": userMsg},
	}
	if len(history) > 0 {
		conversationState["history"] = normalizeKiroHistory(history, model)
	}
	payload := map[string]any{"conversationState": conversationState}
	if creds != nil && strings.TrimSpace(creds.ProfileARN) != "" {
		conversationState["profileArn"] = creds.ProfileARN
		payload["profileArn"] = creds.ProfileARN
	}
	return payload, nil
}

func setKiroHeaders(h http.Header, token string, creds *KiroCredentials) {
	machineID := "KIRO_DEFAULT_MACHINE"
	if creds != nil {
		machineID = creds.MachineID()
	}
	kiroVersion := DefaultKiroVersion
	nodeVersion := DefaultKiroNodeVersion
	h.Set("content-type", "application/json")
	h.Set("x-amzn-codewhisperer-optout", "true")
	h.Set("x-amzn-kiro-agent-mode", "vibe")
	h.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/1.0.0 KiroIDE-%s-%s", kiroVersion, machineID))
	h.Set("user-agent", fmt.Sprintf("aws-sdk-js/1.0.0 ua/2.1 os/%s lang/js md/nodejs#%s api/codewhispererruntime#1.0.0 m/E KiroIDE-%s-%s", kiroOSString(), nodeVersion, kiroVersion, machineID))
	h.Set("amz-sdk-invocation-id", uuid.NewString())
	h.Set("amz-sdk-request", "attempt=1; max=1")
	h.Set("authorization", "Bearer "+token)
	h.Set("connection", "close")
}

func convertAnthropicToolsToKiro(tools []map[string]any) []map[string]any {
	const maxTools = 50
	const maxDescription = 9216
	out := make([]map[string]any, 0, len(tools))
	count := 0
	for _, tool := range tools {
		name := strings.TrimSpace(credentialValueToString(tool["name"]))
		if name == "" {
			continue
		}
		if name == "web_search" || name == "web_search_20250305" {
			out = append(out, map[string]any{"webSearchTool": map[string]any{"type": "web_search"}})
			continue
		}
		if count >= maxTools {
			continue
		}
		count++
		description := credentialValueToString(tool["description"])
		if description == "" {
			description = "Tool: " + name
		}
		if len(description) > maxDescription {
			description = description[:maxDescription-3] + "..."
		}
		schema, ok := tool["input_schema"].(map[string]any)
		if !ok || schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"toolSpecification": map[string]any{
				"name":        name,
				"description": description,
				"inputSchema": map[string]any{"json": schema},
			},
		})
	}
	return out
}

func convertAnthropicMessagesToKiro(messages []kiroAnthropicMessage, system any, thinking map[string]any) (string, []map[string]any, []map[string]any) {
	systemText := injectKiroThinkingPrefix(systemToText(system), thinking)
	history := make([]map[string]any, 0, len(messages))
	var userContent string
	var currentToolResults []map[string]any
	systemAttached := false

	for i, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		isLast := i == len(messages)-1
		text, toolResults, toolUses := anthropicContentToKiroParts(msg.Content)
		if role == "user" {
			if systemText != "" && !systemAttached {
				if strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "x-anthropic-billing-header:") {
					text = text
				} else if text != "" {
					text = systemText + "\n\n" + text
				} else {
					text = systemText
				}
				systemAttached = true
			}
			if len(toolResults) > 0 {
				if isLast {
					currentToolResults = toolResults
					if strings.TrimSpace(text) == "" {
						text = "Tool results provided."
					}
					userContent = text
				} else {
					history = append(history, map[string]any{
						"userInputMessage": map[string]any{
							"content": textOrDefault(text, "Tool results provided."),
							"modelId": DefaultKiroModelSonnet,
							"origin":  "AI_EDITOR",
							"userInputMessageContext": map[string]any{
								"toolResults": dedupeKiroToolResults(toolResults),
							},
						},
					})
				}
				continue
			}
			if isLast {
				userContent = textOrDefault(text, "Continue")
			} else {
				history = append(history, map[string]any{
					"userInputMessage": map[string]any{
						"content": textOrDefault(text, "Continue"),
						"modelId": DefaultKiroModelSonnet,
						"origin":  "AI_EDITOR",
					},
				})
			}
		} else if role == "assistant" {
			msgPayload := map[string]any{"content": textOrDefault(text, "I understand.")}
			if len(toolUses) > 0 {
				msgPayload["toolUses"] = toolUses
			}
			history = append(history, map[string]any{"assistantResponseMessage": msgPayload})
		}
	}
	return userContent, fixKiroHistoryAlternation(history, DefaultKiroModelSonnet), currentToolResults
}

func anthropicContentToKiroParts(content any) (string, []map[string]any, []map[string]any) {
	switch v := content.(type) {
	case string:
		return v, nil, nil
	case []any:
		var textParts []string
		var toolResults []map[string]any
		var toolUses []map[string]any
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				if s, ok := item.(string); ok {
					textParts = append(textParts, s)
				}
				continue
			}
			switch credentialValueToString(block["type"]) {
			case "text":
				textParts = append(textParts, credentialValueToString(block["text"]))
			case "tool_result":
				contentText := toolResultContentToText(block["content"])
				status := "success"
				if b, _ := block["is_error"].(bool); b {
					status = "error"
				}
				toolResults = append(toolResults, map[string]any{
					"content":   []map[string]any{{"text": contentText}},
					"status":    status,
					"toolUseId": credentialValueToString(block["tool_use_id"]),
				})
			case "tool_use":
				toolUses = append(toolUses, map[string]any{
					"toolUseId": credentialValueToString(block["id"]),
					"name":      credentialValueToString(block["name"]),
					"input":     block["input"],
				})
			case "image":
				textParts = append(textParts, "[Image attached]")
			}
		}
		return strings.Join(nonEmptyStrings(textParts), "\n"), toolResults, toolUses
	default:
		if content == nil {
			return "", nil, nil
		}
		return fmt.Sprint(content), nil, nil
	}
}

func toolResultContentToText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				if credentialValueToString(block["type"]) == "text" {
					parts = append(parts, credentialValueToString(block["text"]))
				}
			} else if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(nonEmptyStrings(parts), "\n")
	default:
		if content == nil {
			return ""
		}
		return fmt.Sprint(content)
	}
}

func systemToText(system any) string {
	switch v := system.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				if credentialValueToString(block["type"]) == "text" {
					parts = append(parts, credentialValueToString(block["text"]))
				}
			} else if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(nonEmptyStrings(parts), "\n")
	default:
		return ""
	}
}

func injectKiroThinkingPrefix(systemText string, thinking map[string]any) string {
	if len(thinking) == 0 || strings.Contains(systemText, "<thinking_mode>") {
		return systemText
	}
	typ := strings.ToLower(credentialValueToString(thinking["type"]))
	prefix := ""
	switch typ {
	case "enabled":
		budget := intFromJSON(thinking, 20000, "budget_tokens")
		if budget < 1024 {
			budget = 1024
		}
		if budget > 24576 {
			budget = 24576
		}
		prefix = fmt.Sprintf("<thinking_mode>enabled</thinking_mode><max_thinking_length>%d</max_thinking_length>", budget)
	case "adaptive":
		effort := credentialValueToString(thinking["effort"])
		if effort == "" {
			effort = "high"
		}
		prefix = fmt.Sprintf("<thinking_mode>adaptive</thinking_mode><thinking_effort>%s</thinking_effort>", effort)
	}
	if prefix == "" {
		return systemText
	}
	if systemText == "" {
		return prefix
	}
	return prefix + "\n" + systemText
}

func normalizeKiroHistory(history []map[string]any, model string) []map[string]any {
	for _, item := range history {
		if user, ok := item["userInputMessage"].(map[string]any); ok {
			if credentialValueToString(user["content"]) == "" {
				user["content"] = "Continue"
			}
			user["modelId"] = model
			user["origin"] = "AI_EDITOR"
		}
	}
	return history
}

func fixKiroHistoryAlternation(history []map[string]any, model string) []map[string]any {
	fixed := make([]map[string]any, 0, len(history))
	for _, item := range history {
		if _, isUser := item["userInputMessage"]; isUser {
			if len(fixed) > 0 {
				if _, previousUser := fixed[len(fixed)-1]["userInputMessage"]; previousUser {
					fixed = append(fixed, map[string]any{"assistantResponseMessage": map[string]any{"content": "I understand."}})
				}
			}
			fixed = append(fixed, item)
			continue
		}
		if _, isAssistant := item["assistantResponseMessage"]; isAssistant {
			if len(fixed) == 0 {
				fixed = append(fixed, map[string]any{"userInputMessage": map[string]any{"content": "Continue", "modelId": model, "origin": "AI_EDITOR"}})
			} else if _, previousAssistant := fixed[len(fixed)-1]["assistantResponseMessage"]; previousAssistant {
				fixed = append(fixed, map[string]any{"userInputMessage": map[string]any{"content": "Continue", "modelId": model, "origin": "AI_EDITOR"}})
			}
			fixed = append(fixed, item)
		}
	}
	if len(fixed) > 0 {
		if _, isUser := fixed[len(fixed)-1]["userInputMessage"]; isUser {
			fixed = append(fixed, map[string]any{"assistantResponseMessage": map[string]any{"content": "I understand."}})
		}
	}
	return fixed
}

func dedupeKiroToolResults(results []map[string]any) []map[string]any {
	seen := make(map[string]bool)
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		id := credentialValueToString(result["toolUseId"])
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, result)
	}
	return out
}

func parseKiroEventStream(raw []byte) kiroParsedResponse {
	result := kiroParsedResponse{StopReason: "end_turn"}
	toolBuffers := map[string]struct {
		Name  string
		Parts []string
	}{}
	pos := 0
	for pos+12 <= len(raw) {
		totalLen := int(binary.BigEndian.Uint32(raw[pos : pos+4]))
		headersLen := int(binary.BigEndian.Uint32(raw[pos+4 : pos+8]))
		if totalLen <= 0 || totalLen > len(raw)-pos {
			break
		}
		headerStart := pos + 12
		headerEnd := headerStart + headersLen
		payloadStart := headerEnd
		payloadEnd := pos + totalLen - 4
		eventType := ""
		if headerEnd <= len(raw) {
			headerText := string(raw[headerStart:headerEnd])
			if strings.Contains(headerText, "toolUseEvent") {
				eventType = "toolUseEvent"
			}
			if strings.Contains(headerText, "assistantResponseEvent") {
				eventType = "assistantResponseEvent"
			}
		}
		if payloadStart < payloadEnd && payloadEnd <= len(raw) {
			parseKiroPayload(raw[payloadStart:payloadEnd], eventType, &result, toolBuffers)
		}
		pos += totalLen
	}
	if len(result.Content) == 0 && len(toolBuffers) == 0 {
		scanKiroJSONObjects(string(raw), &result, toolBuffers)
	}
	for id, buffer := range toolBuffers {
		inputRaw := strings.Join(buffer.Parts, "")
		var input any
		if err := json.Unmarshal([]byte(inputRaw), &input); err != nil || input == nil {
			input = map[string]any{"raw": inputRaw}
		}
		result.ToolUses = append(result.ToolUses, map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  buffer.Name,
			"input": input,
		})
	}
	if len(result.ToolUses) > 0 {
		result.StopReason = "tool_use"
	}
	return result
}

func parseKiroPayload(payload []byte, eventType string, result *kiroParsedResponse, toolBuffers map[string]struct {
	Name  string
	Parts []string
}) {
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return
	}
	if ev, ok := parsed["assistantResponseEvent"].(map[string]any); ok {
		if content := credentialValueToString(ev["content"]); content != "" {
			result.Content = append(result.Content, content)
		}
	} else if content := credentialValueToString(parsed["content"]); content != "" && eventType != "toolUseEvent" {
		result.Content = append(result.Content, content)
	}
	toolID := credentialValueToString(parsed["toolUseId"])
	if eventType == "toolUseEvent" || toolID != "" {
		if toolID == "" {
			return
		}
		buffer := toolBuffers[toolID]
		if name := credentialValueToString(parsed["name"]); name != "" {
			buffer.Name = name
		}
		if input := credentialValueToString(parsed["input"]); input != "" {
			buffer.Parts = append(buffer.Parts, input)
		}
		toolBuffers[toolID] = buffer
	}
}

func scanKiroJSONObjects(raw string, result *kiroParsedResponse, toolBuffers map[string]struct {
	Name  string
	Parts []string
}) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	for {
		var parsed map[string]any
		if err := decoder.Decode(&parsed); err != nil {
			break
		}
		payload, _ := json.Marshal(parsed)
		parseKiroPayload(payload, "", result, toolBuffers)
	}
}

func kiroAnthropicResponse(model string, parsed kiroParsedResponse, usage kiroEstimatedUsage) gin.H {
	content := make([]gin.H, 0, 1+len(parsed.ToolUses))
	text := strings.Join(parsed.Content, "")
	if text != "" {
		content = append(content, gin.H{"type": "text", "text": text})
	}
	for _, toolUse := range parsed.ToolUses {
		content = append(content, gin.H{
			"type":  "tool_use",
			"id":    toolUse["id"],
			"name":  toolUse["name"],
			"input": toolUse["input"],
		})
	}
	if len(content) == 0 {
		content = append(content, gin.H{"type": "text", "text": ""})
	}
	stopReason := firstNonEmpty(parsed.StopReason, "end_turn")
	return gin.H{
		"id":            "msg_kiro_" + uuid.NewString(),
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": gin.H{
			"input_tokens":  usage.InputTokens,
			"output_tokens": usage.OutputTokens,
		},
	}
}

func writeKiroAnthropicStream(c *gin.Context, model string, parsed kiroParsedResponse, usage kiroEstimatedUsage) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	msgID := "msg_kiro_" + uuid.NewString()
	writeSSE(c.Writer, "message_start", gin.H{
		"type": "message_start",
		"message": gin.H{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         gin.H{"input_tokens": usage.InputTokens, "output_tokens": 0},
		},
	})
	idx := 0
	text := strings.Join(parsed.Content, "")
	if text != "" {
		writeSSE(c.Writer, "content_block_start", gin.H{
			"type":          "content_block_start",
			"index":         idx,
			"content_block": gin.H{"type": "text", "text": ""},
		})
		writeSSE(c.Writer, "content_block_delta", gin.H{
			"type":  "content_block_delta",
			"index": idx,
			"delta": gin.H{"type": "text_delta", "text": text},
		})
		writeSSE(c.Writer, "content_block_stop", gin.H{"type": "content_block_stop", "index": idx})
		idx++
	}
	for _, toolUse := range parsed.ToolUses {
		writeSSE(c.Writer, "content_block_start", gin.H{
			"type":  "content_block_start",
			"index": idx,
			"content_block": gin.H{
				"type":  "tool_use",
				"id":    toolUse["id"],
				"name":  toolUse["name"],
				"input": map[string]any{},
			},
		})
		if input, err := json.Marshal(toolUse["input"]); err == nil && string(input) != "{}" {
			writeSSE(c.Writer, "content_block_delta", gin.H{
				"type":  "content_block_delta",
				"index": idx,
				"delta": gin.H{"type": "input_json_delta", "partial_json": string(input)},
			})
		}
		writeSSE(c.Writer, "content_block_stop", gin.H{"type": "content_block_stop", "index": idx})
		idx++
	}
	writeSSE(c.Writer, "message_delta", gin.H{
		"type":  "message_delta",
		"delta": gin.H{"stop_reason": firstNonEmpty(parsed.StopReason, "end_turn"), "stop_sequence": nil},
		"usage": gin.H{"output_tokens": usage.OutputTokens},
	})
	writeSSE(c.Writer, "message_stop", gin.H{"type": "message_stop"})
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func shouldKiroFailover(status int) bool {
	return status == http.StatusUnauthorized ||
		status == http.StatusTooManyRequests ||
		status == http.StatusServiceUnavailable ||
		status == 529
}

func (s *KiroGatewayService) writeKiroJSONError(c *gin.Context, status int, typ, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    typ,
			"message": message,
		},
	})
}

func EstimateKiroCountTokensFromBody(body []byte) int {
	var req kiroAnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return estimateKiroTokensForText(string(body))
	}
	return estimateKiroRequestTokens(req)
}

func estimateKiroUsage(req kiroAnthropicRequest, parsed kiroParsedResponse) kiroEstimatedUsage {
	return kiroEstimatedUsage{
		InputTokens:  estimateKiroRequestTokens(req),
		OutputTokens: estimateKiroResponseTokens(parsed),
	}
}

func estimateKiroRequestTokens(req kiroAnthropicRequest) int {
	total := 8
	total += estimateKiroValueTokens(req.System)
	total += estimateKiroValueTokens(req.Thinking)
	for _, msg := range req.Messages {
		total += 4 + estimateKiroTokensForText(msg.Role) + estimateKiroValueTokens(msg.Content)
	}
	for _, tool := range req.Tools {
		total += 12 + estimateKiroValueTokens(tool)
	}
	if req.MaxTokens > 0 {
		total++
	}
	if total < 1 {
		return 1
	}
	return total
}

func estimateKiroResponseTokens(parsed kiroParsedResponse) int {
	total := estimateKiroTokensForText(strings.Join(parsed.Content, ""))
	for _, toolUse := range parsed.ToolUses {
		total += 8 + estimateKiroValueTokens(toolUse["name"]) + estimateKiroValueTokens(toolUse["input"])
	}
	if total < 0 {
		return 0
	}
	return total
}

func estimateKiroValueTokens(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		return estimateKiroTokensForText(v)
	case []any:
		total := 0
		for _, item := range v {
			total += estimateKiroValueTokens(item)
		}
		return total
	case []map[string]any:
		total := 0
		for _, item := range v {
			total += estimateKiroValueTokens(item)
		}
		return total
	case map[string]any:
		total := 0
		for key, item := range v {
			total += estimateKiroTokensForText(key)
			total += estimateKiroValueTokens(item)
		}
		return total
	default:
		if raw, err := json.Marshal(value); err == nil {
			return estimateKiroTokensForText(string(raw))
		}
		return estimateKiroTokensForText(fmt.Sprint(value))
	}
}

func estimateKiroTokensForText(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	cjk := 0
	asciiLettersDigits := 0
	other := 0
	for _, r := range text {
		switch {
		case isKiroCJKRune(r):
			cjk++
		case r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			asciiLettersDigits++
		case unicode.IsSpace(r):
			continue
		default:
			other++
		}
	}
	tokens := cjk + int(math.Ceil(float64(asciiLettersDigits)/4.0)) + int(math.Ceil(float64(other)/2.0))
	if tokens < 1 {
		return 1
	}
	return tokens
}

func isKiroCJKRune(r rune) bool {
	return (r >= 0x3400 && r <= 0x4dbf) ||
		(r >= 0x4e00 && r <= 0x9fff) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0x20000 && r <= 0x2ebef)
}

func textOrDefault(text, fallback string) string {
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return text
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
