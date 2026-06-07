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
	Region  string `json:"region"`
	ProxyID *int64 `json:"proxy_id"`
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

	result, err := kiroOAuthService.StartDeviceFlow(c.Request.Context(), req.Region, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
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

	tokenInfo, err := kiroOAuthService.ValidateRefreshToken(c.Request.Context(), req.RefreshToken, req.ProxyID, req.Credentials)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tokenInfo)
}
