package auth

import (
	"errors"
	"log"
	"net/http"

	"portfolio-tracker/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const refreshTokenCookie = "refresh_token"

type Handler struct {
	svc *Service
	cfg *config.Config
}

func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{svc: svc, cfg: cfg}
}

func (h *Handler) setRefreshCookie(c *gin.Context, token string) {
	sameSite := http.SameSiteStrictMode
	maxAge := int(h.svc.refreshExpiry.Seconds())
	c.SetSameSite(sameSite)
	c.SetCookie(
		refreshTokenCookie,
		token,
		maxAge,
		"/",
		h.cfg.CookieDomain,
		h.cfg.CookieSecure,
		true, // httpOnly
	)
}

func (h *Handler) clearRefreshCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(refreshTokenCookie, "", -1, "/", h.cfg.CookieDomain, h.cfg.CookieSecure, true)
}

// setCSRFCookie generates a new CSRF token and sets it as a non-httpOnly cookie.
// On error, the request is not aborted — CSRF is defense-in-depth.
func (h *Handler) setCSRFCookie(c *gin.Context) {
	token, err := GenerateCSRFToken()
	if err != nil {
		log.Printf("[auth] failed to generate CSRF token: %v", err)
		return
	}
	SetCSRFCookie(c, token, h.cfg.CookieSecure, h.cfg.CookieDomain)
}

func (h *Handler) clearCSRFCookie(c *gin.Context) {
	ClearCSRFCookie(c, h.cfg.CookieSecure, h.cfg.CookieDomain)
}

func (h *Handler) Register(c *gin.Context) {
	var req struct {
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required,min=8"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, err := h.svc.Register(req.Email, req.Password)
	if err != nil {
		if errors.Is(err, ErrEmailTaken) {
			c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
		return
	}

	pair, err := h.svc.Login(req.Email, req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration succeeded but login failed"})
		return
	}

	h.setRefreshCookie(c, pair.RefreshToken)
	h.setCSRFCookie(c)
	c.JSON(http.StatusCreated, gin.H{"id": user.ID, "email": user.Email, "access_token": pair.AccessToken})
}

func (h *Handler) Login(c *gin.Context) {
	var req struct {
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	pair, err := h.svc.Login(req.Email, req.Password)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "login failed"})
		return
	}

	h.setRefreshCookie(c, pair.RefreshToken)
	h.setCSRFCookie(c)
	c.JSON(http.StatusOK, gin.H{"access_token": pair.AccessToken})
}

func (h *Handler) Refresh(c *gin.Context) {
	refreshToken, err := c.Cookie(refreshTokenCookie)
	if err != nil || refreshToken == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing refresh token"})
		return
	}

	pair, err := h.svc.RefreshToken(c.Request.Context(), refreshToken)
	if err != nil {
		h.clearRefreshCookie(c)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired refresh token"})
		return
	}

	h.setRefreshCookie(c, pair.RefreshToken)
	h.setCSRFCookie(c)
	c.JSON(http.StatusOK, gin.H{"access_token": pair.AccessToken})
}

func (h *Handler) Logout(c *gin.Context) {
	refreshToken, err := c.Cookie(refreshTokenCookie)
	if err == nil && refreshToken != "" {
		_ = h.svc.InvalidateRefreshToken(c.Request.Context(), refreshToken)
	}
	h.clearRefreshCookie(c)
	h.clearCSRFCookie(c)
	c.Status(http.StatusNoContent)
}

// GetCurrentUserID extracts the authenticated user ID from gin context.
func GetCurrentUserID(c *gin.Context) (uuid.UUID, bool) {
	v, exists := c.Get(userIDKey)
	if !exists {
		return uuid.Nil, false
	}
	id, ok := v.(uuid.UUID)
	return id, ok
}
