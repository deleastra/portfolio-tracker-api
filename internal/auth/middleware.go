package auth

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const userIDKey = "userID"

// JWTMiddleware validates the Bearer token and sets userID in the context.
func JWTMiddleware(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid authorization header"})
			return
		}

		token := strings.TrimPrefix(header, "Bearer ")
		claims, err := svc.ValidateAccessToken(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		c.Set(userIDKey, claims.UserID)
		c.Next()
	}
}
