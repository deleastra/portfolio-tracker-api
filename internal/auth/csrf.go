package auth

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/gin-gonic/gin"
)

const csrfCookieName = "csrf_token"
const csrfHeaderName = "X-CSRF-Token"

// GenerateCSRFToken creates a cryptographically random 32-byte token.
func GenerateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SetCSRFCookie sets a non-httpOnly CSRF token cookie readable by JavaScript.
// The cookie is SameSite=Strict to prevent cross-origin cookie leakage.
func SetCSRFCookie(c *gin.Context, token string, secure bool, domain string) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(
		csrfCookieName,
		token,
		0, // session cookie — cleared when browser closes
		"/",
		domain,
		secure,
		false, // httpOnly=false — JS must be able to read this
	)
}

// ClearCSRFCookie removes the CSRF token cookie.
func ClearCSRFCookie(c *gin.Context, secure bool, domain string) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(csrfCookieName, "", -1, "/", domain, secure, false)
}

// GetCSRFCookieValue reads the CSRF token from the request cookie.
func GetCSRFCookieValue(c *gin.Context) (string, error) {
	return c.Cookie(csrfCookieName)
}

// CSRFSkipper is a predicate that returns true if CSRF validation should be
// skipped for the current request. By default, safe HTTP methods (GET, HEAD,
// OPTIONS) are skipped.
type CSRFSkipper func(c *gin.Context) bool

// DefaultSkipper skips CSRF validation for safe methods.
func DefaultSkipper(c *gin.Context) bool {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// CSRFMiddleware validates the Double Submit Cookie pattern:
// The cookie `csrf_token` must match the header `X-CSRF-Token`.
// Safe methods (GET/HEAD/OPTIONS) are skipped by default.
func CSRFMiddleware(skippers ...CSRFSkipper) gin.HandlerFunc {
	skip := DefaultSkipper
	if len(skippers) > 0 {
		skip = skippers[0]
	}

	return func(c *gin.Context) {
		if skip(c) {
			c.Next()
			return
		}

		cookieToken, err := c.Cookie(csrfCookieName)
		if err != nil || cookieToken == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf token missing"})
			return
		}

		headerToken := c.GetHeader(csrfHeaderName)
		if headerToken == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf header missing"})
			return
		}

		if cookieToken != headerToken {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "csrf token mismatch"})
			return
		}

		c.Next()
	}
}
