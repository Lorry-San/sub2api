package admin

import (
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type KiroOAuthHandler struct {
	kiroOAuthService *service.KiroOAuthService
}

func NewKiroOAuthHandler(kiroOAuthService *service.KiroOAuthService) *KiroOAuthHandler {
	return &KiroOAuthHandler{kiroOAuthService: kiroOAuthService}
}

func (h *KiroOAuthHandler) requireService() (*service.KiroOAuthService, bool) {
	if h == nil || h.kiroOAuthService == nil {
		return nil, false
	}
	return h.kiroOAuthService, true
}

type KiroDeviceStartRequest struct {
	Region   string `json:"region"`
	StartURL string `json:"start_url"`
	ProxyID  *int64 `json:"proxy_id"`
}

func (h *KiroOAuthHandler) StartDeviceFlow(c *gin.Context) {
	var req KiroDeviceStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	kiroOAuthService, ok := h.requireService()
	if !ok {
		response.ErrorFrom(c, fmt.Errorf("Kiro OAuth service is not configured"))
		return
	}

	result, err := kiroOAuthService.StartDeviceFlow(c.Request.Context(), req.Region, req.StartURL, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

type KiroIDEStartRequest struct {
	Region       string `json:"region"`
	StartURL     string `json:"start_url"`
	RedirectURI  string `json:"redirect_uri"`
	RedirectFrom string `json:"redirect_from"`
	ProxyID      *int64 `json:"proxy_id"`
}

func (h *KiroOAuthHandler) StartKiroIDEAuth(c *gin.Context) {
	var req KiroIDEStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "璇锋眰鏃犳晥: "+err.Error())
		return
	}

	kiroOAuthService, ok := h.requireService()
	if !ok {
		response.ErrorFrom(c, fmt.Errorf("Kiro OAuth service is not configured"))
		return
	}

	result, err := kiroOAuthService.StartKiroIDEAuth(c.Request.Context(), req.Region, req.StartURL, req.RedirectURI, req.RedirectFrom, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

type KiroIDEExchangeRequest struct {
	SessionID   string `json:"session_id" binding:"required"`
	Code        string `json:"code"`
	State       string `json:"state"`
	CallbackURL string `json:"callback_url"`
	ProxyID     *int64 `json:"proxy_id"`
}

func (h *KiroOAuthHandler) ExchangeKiroIDEAuth(c *gin.Context) {
	var req KiroIDEExchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "璇锋眰鏃犳晥: "+err.Error())
		return
	}

	kiroOAuthService, ok := h.requireService()
	if !ok {
		response.ErrorFrom(c, fmt.Errorf("Kiro OAuth service is not configured"))
		return
	}

	tokenInfo, err := kiroOAuthService.ExchangeKiroIDEAuth(c.Request.Context(), &service.KiroIDEExchangeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		CallbackURL: req.CallbackURL,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}

type KiroIDECancelRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

func (h *KiroOAuthHandler) CancelKiroIDEAuth(c *gin.Context) {
	var req KiroIDECancelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "璇锋眰鏃犳晥: "+err.Error())
		return
	}
	kiroOAuthService, ok := h.requireService()
	if !ok {
		response.ErrorFrom(c, fmt.Errorf("Kiro OAuth service is not configured"))
		return
	}
	response.Success(c, gin.H{"canceled": kiroOAuthService.CancelKiroIDEAuth(req.SessionID)})
}

type KiroDevicePollRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	ProxyID   *int64 `json:"proxy_id"`
}

func (h *KiroOAuthHandler) PollDeviceFlow(c *gin.Context) {
	var req KiroDevicePollRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	kiroOAuthService, ok := h.requireService()
	if !ok {
		response.ErrorFrom(c, fmt.Errorf("Kiro OAuth service is not configured"))
		return
	}

	result, err := kiroOAuthService.PollDeviceFlow(c.Request.Context(), req.SessionID, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

type KiroDeviceCancelRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

func (h *KiroOAuthHandler) CancelDeviceFlow(c *gin.Context) {
	var req KiroDeviceCancelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}
	kiroOAuthService, ok := h.requireService()
	if !ok {
		response.ErrorFrom(c, fmt.Errorf("Kiro OAuth service is not configured"))
		return
	}
	response.Success(c, gin.H{"canceled": kiroOAuthService.CancelDeviceFlow(req.SessionID)})
}

type KiroRefreshTokenRequest struct {
	RefreshToken string         `json:"refresh_token" binding:"required"`
	StartURL     string         `json:"start_url"`
	ProxyID      *int64         `json:"proxy_id"`
	Credentials  map[string]any `json:"credentials"`
}

func (h *KiroOAuthHandler) RefreshToken(c *gin.Context) {
	var req KiroRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	kiroOAuthService, ok := h.requireService()
	if !ok {
		response.ErrorFrom(c, fmt.Errorf("Kiro OAuth service is not configured"))
		return
	}
	if req.StartURL != "" {
		if req.Credentials == nil {
			req.Credentials = map[string]any{}
		}
		req.Credentials["start_url"] = req.StartURL
	}

	tokenInfo, err := kiroOAuthService.ValidateRefreshToken(c.Request.Context(), req.RefreshToken, req.ProxyID, req.Credentials)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}
