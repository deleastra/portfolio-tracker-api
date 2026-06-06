package yahoofinance

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// IsUSMarketOpen returns true when the US stock market is currently open.
// Market hours: Monday–Friday 09:30–16:00 Eastern Time (no holiday calendar).
func IsUSMarketOpen() bool {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return false
	}
	now := time.Now().In(loc)
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	open := time.Date(now.Year(), now.Month(), now.Day(), 9, 30, 0, 0, loc)
	close := time.Date(now.Year(), now.Month(), now.Day(), 16, 0, 0, 0, loc)
	return now.After(open) && now.Before(close)
}

// LastTradingDay returns the most recent weekday on or before today (UTC).
func LastTradingDay() time.Time {
	d := time.Now().UTC().Truncate(24 * time.Hour)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

const quoteKeyPrefix = "price:quote:"

// CachedClient wraps Client and caches current-price quotes in Redis.
type CachedClient struct {
	client *Client
	redis  *redis.Client
	ttl    time.Duration
}

func NewCachedClient(baseURL string, rdb *redis.Client, ttl time.Duration) *CachedClient {
	return &CachedClient{
		client: NewClient(baseURL),
		redis:  rdb,
		ttl:    ttl,
	}
}

func (cc *CachedClient) GetQuote(ctx context.Context, symbol string) (*QuoteResponse, error) {
	key := quoteKeyPrefix + symbol

	// Try cache first
	cached, err := cc.redis.Get(ctx, key).Bytes()
	if err == nil {
		var q QuoteResponse
		if json.Unmarshal(cached, &q) == nil {
			log.Printf("[yahoo] GetQuote cache HIT: %s", symbol)
			return &q, nil
		}
	}

	// Cache miss — fetch from Yahoo Finance
	log.Printf("[yahoo] GetQuote cache MISS: %s", symbol)
	q, err := cc.client.GetQuote(ctx, symbol)
	if err != nil {
		return nil, err
	}

	// Store in cache (fire-and-forget — cache failure is non-fatal)
	if b, err := json.Marshal(q); err == nil {
		cc.redis.Set(ctx, key, b, cc.ttl)
	}

	return q, nil
}

// PriceInfo holds current price and day change percent for a symbol.
type PriceInfo struct {
	Price         float64
	DayChangePct  float64
}

// GetCurrentPrices fetches current prices for multiple symbols, using cache where available.
// Symbols that fail to fetch are skipped (partial results are returned without error).
func (cc *CachedClient) GetCurrentPrices(ctx context.Context, symbols []string) (map[string]float64, error) {
	prices := make(map[string]float64, len(symbols))
	for _, sym := range symbols {
		q, err := cc.GetQuote(ctx, sym)
		if err != nil {
			// Skip symbols we can't price — caller handles missing entries
			continue
		}
		prices[sym] = q.RegularMarketPrice
	}
	return prices, nil
}

// GetCurrentPriceInfos fetches current price and day change % for multiple symbols.
// Symbols that fail to fetch are skipped.
func (cc *CachedClient) GetCurrentPriceInfos(ctx context.Context, symbols []string) (map[string]PriceInfo, error) {
	infos := make(map[string]PriceInfo, len(symbols))
	for _, sym := range symbols {
		q, err := cc.GetQuote(ctx, sym)
		if err != nil {
			continue
		}
		infos[sym] = PriceInfo{
			Price:        q.RegularMarketPrice,
			DayChangePct: q.RegularMarketChangePercent,
		}
	}
	return infos, nil
}

// InvalidateAll removes all cached price quotes from Redis, forcing fresh fetches on the next request.
// Uses SCAN to avoid blocking Redis in production.
func (cc *CachedClient) InvalidateAll(ctx context.Context) error {
	var cursor uint64
	for {
		keys, nextCursor, err := cc.redis.Scan(ctx, cursor, quoteKeyPrefix+"*", 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := cc.redis.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return nil
}

// GetHistorical delegates to the underlying client (no Redis cache for historical bars —
// stored in price_cache_history DB table instead).
func (cc *CachedClient) GetHistorical(ctx context.Context, symbol string, from, to time.Time) ([]HistoricalBar, error) {
	return cc.client.GetHistorical(ctx, symbol, from, to)
}
