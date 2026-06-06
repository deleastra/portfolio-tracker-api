package auth

import (
	"context"
	"errors"
	"fmt"
	"portfolio-tracker/internal/model"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	TokenTypeAccess  = "access"
	TokenTypeRefresh = "refresh"
)

var (
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrUserNotFound       = errors.New("user not found")
	ErrWrongTokenType     = errors.New("wrong token type")
	ErrTokenRevoked       = errors.New("token has been revoked")
)

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"-"` // set as httpOnly cookie, never serialised to JSON
}

type Claims struct {
	UserID    uuid.UUID `json:"user_id"`
	TokenType string    `json:"typ"`
	JTI       string    `json:"jti"`
	jwt.RegisteredClaims
}

type Service struct {
	db            *gorm.DB
	rdb           *redis.Client
	jwtSecret     []byte
	accessExpiry  time.Duration
	refreshExpiry time.Duration
}

func NewService(db *gorm.DB, rdb *redis.Client, jwtSecret string, accessExpiry, refreshExpiry time.Duration) *Service {
	return &Service{
		db:            db,
		rdb:           rdb,
		jwtSecret:     []byte(jwtSecret),
		accessExpiry:  accessExpiry,
		refreshExpiry: refreshExpiry,
	}
}

func (s *Service) Register(email, password string) (*model.User, error) {
	var existing model.User
	if err := s.db.Where("email = ?", email).First(&existing).Error; err == nil {
		return nil, ErrEmailTaken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	user := &model.User{
		Email:        email,
		PasswordHash: string(hash),
	}
	if err := s.db.Create(user).Error; err != nil {
		return nil, err
	}

	// Create a default portfolio for the user
	portfolio := &model.Portfolio{
		UserID: user.ID,
		Name:   "My Portfolio",
	}
	if err := s.db.Create(portfolio).Error; err != nil {
		return nil, err
	}

	return user, nil
}

func (s *Service) Login(email, password string) (*TokenPair, error) {
	var user model.User
	if err := s.db.Where("email = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return s.generateTokenPair(user.ID)
}

func (s *Service) RefreshToken(ctx context.Context, refreshToken string) (*TokenPair, error) {
	claims, err := s.validateToken(refreshToken)
	if err != nil {
		return nil, err
	}

	if claims.TokenType != TokenTypeRefresh {
		return nil, ErrWrongTokenType
	}

	// Check revocation blocklist
	revokedKey := fmt.Sprintf("revoked:jti:%s", claims.JTI)
	exists, err := s.rdb.Exists(ctx, revokedKey).Result()
	if err != nil {
		return nil, err
	}
	if exists > 0 {
		return nil, ErrTokenRevoked
	}

	var user model.User
	if err := s.db.First(&user, "id = ?", claims.UserID).Error; err != nil {
		return nil, ErrUserNotFound
	}

	pair, err := s.generateTokenPair(user.ID)
	if err != nil {
		return nil, err
	}

	// Invalidate old refresh token JTI
	remaining := time.Until(claims.ExpiresAt.Time)
	if remaining > 0 {
		if err := s.rdb.Set(ctx, revokedKey, "1", remaining).Err(); err != nil {
			return nil, err
		}
	}

	return pair, nil
}

func (s *Service) InvalidateRefreshToken(ctx context.Context, refreshToken string) error {
	claims, err := s.validateToken(refreshToken)
	if err != nil {
		// Token already invalid — treat as success
		return nil
	}
	if claims.TokenType != TokenTypeRefresh {
		return nil
	}
	revokedKey := fmt.Sprintf("revoked:jti:%s", claims.JTI)
	remaining := time.Until(claims.ExpiresAt.Time)
	if remaining > 0 {
		return s.rdb.Set(ctx, revokedKey, "1", remaining).Err()
	}
	return nil
}

func (s *Service) ValidateAccessToken(tokenStr string) (*Claims, error) {
	claims, err := s.validateToken(tokenStr)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != TokenTypeAccess {
		return nil, ErrWrongTokenType
	}
	return claims, nil
}

func (s *Service) generateTokenPair(userID uuid.UUID) (*TokenPair, error) {
	now := time.Now()

	accessClaims := Claims{
		UserID:    userID,
		TokenType: TokenTypeAccess,
		JTI:       uuid.New().String(),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   userID.String(),
		},
	}
	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims).SignedString(s.jwtSecret)
	if err != nil {
		return nil, err
	}

	refreshClaims := Claims{
		UserID:    userID,
		TokenType: TokenTypeRefresh,
		JTI:       uuid.New().String(),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   userID.String(),
		},
	}
	refreshToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims).SignedString(s.jwtSecret)
	if err != nil {
		return nil, err
	}

	return &TokenPair{AccessToken: accessToken, RefreshToken: refreshToken}, nil
}

func (s *Service) validateToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}
