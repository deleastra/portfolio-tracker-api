package auth

import (
	"errors"
	"portfolio-tracker/internal/model"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var (
	ErrEmailTaken      = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrUserNotFound    = errors.New("user not found")
)

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type Claims struct {
	UserID uuid.UUID `json:"user_id"`
	jwt.RegisteredClaims
}

type Service struct {
	db            *gorm.DB
	jwtSecret     []byte
	accessExpiry  time.Duration
	refreshExpiry time.Duration
}

func NewService(db *gorm.DB, jwtSecret string, accessExpiry, refreshExpiry time.Duration) *Service {
	return &Service{
		db:            db,
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

func (s *Service) RefreshToken(refreshToken string) (*TokenPair, error) {
	claims, err := s.validateToken(refreshToken)
	if err != nil {
		return nil, err
	}

	var user model.User
	if err := s.db.First(&user, "id = ?", claims.UserID).Error; err != nil {
		return nil, ErrUserNotFound
	}

	return s.generateTokenPair(user.ID)
}

func (s *Service) ValidateAccessToken(tokenStr string) (*Claims, error) {
	return s.validateToken(tokenStr)
}

func (s *Service) generateTokenPair(userID uuid.UUID) (*TokenPair, error) {
	now := time.Now()

	accessClaims := Claims{
		UserID: userID,
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
		UserID: userID,
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
