package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	AppPort string
	AppEnv  string

	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	RedisHost     string
	RedisPort     string
	RedisPassword string

	JWTSecret        string
	JWTAccessExpiry  time.Duration
	JWTRefreshExpiry time.Duration

	YahooFinanceBaseURL string
	YahooPriceCacheTTL  time.Duration

	PriceLoaderWorkers        int
	PriceLoaderBaseBackoffMs  int
	PriceLoaderMaxBackoffMs   int

	CookieSecure bool
	CookieDomain string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	accessExpiry, err := time.ParseDuration(getEnv("JWT_ACCESS_EXPIRY", "15m"))
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_ACCESS_EXPIRY: %w", err)
	}
	refreshExpiry, err := time.ParseDuration(getEnv("JWT_REFRESH_EXPIRY", "168h"))
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_REFRESH_EXPIRY: %w", err)
	}
	priceCacheTTL, err := time.ParseDuration(getEnv("YAHOO_PRICE_CACHE_TTL", "15m"))
	if err != nil {
		return nil, fmt.Errorf("invalid YAHOO_PRICE_CACHE_TTL: %w", err)
	}

	return &Config{
		AppPort: getEnv("APP_PORT", "8080"),
		AppEnv:  getEnv("APP_ENV", "development"),

		DBHost:     getEnv("DB_HOST", "localhost"),
		DBPort:     getEnv("DB_PORT", "5432"),
		DBUser:     getEnv("DB_USER", "portfolio"),
		DBPassword: getEnv("DB_PASSWORD", "portfolio"),
		DBName:     getEnv("DB_NAME", "portfolio"),
		DBSSLMode:  getEnv("DB_SSLMODE", "disable"),

		RedisHost:     getEnv("REDIS_HOST", "localhost"),
		RedisPort:     getEnv("REDIS_PORT", "6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),

		JWTSecret:        mustGetEnv("JWT_SECRET"),
		JWTAccessExpiry:  accessExpiry,
		JWTRefreshExpiry: refreshExpiry,

		YahooFinanceBaseURL: getEnv("YAHOO_FINANCE_BASE_URL", "https://query1.finance.yahoo.com"),
		YahooPriceCacheTTL:  priceCacheTTL,

		PriceLoaderWorkers:       getEnvInt("PRICE_LOADER_WORKERS", 3),
		PriceLoaderBaseBackoffMs: getEnvInt("PRICE_LOADER_BASE_BACKOFF_MS", 1000),
		PriceLoaderMaxBackoffMs:  getEnvInt("PRICE_LOADER_MAX_BACKOFF_MS", 32000),

		CookieSecure: getEnv("COOKIE_SECURE", "false") == "true",
		CookieDomain: getEnv("COOKIE_DOMAIN", ""),
	}, nil
}

func (c *Config) DSN() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s TimeZone=UTC",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSSLMode)
}

func (c *Config) RedisAddr() string {
	return fmt.Sprintf("%s:%s", c.RedisHost, c.RedisPort)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustGetEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable %q is not set", key))
	}
	return v
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
