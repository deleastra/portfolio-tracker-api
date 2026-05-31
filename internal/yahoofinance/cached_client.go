package yahoofinance

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

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
			return &q, nil
		}
	}

	// Cache miss — fetch from Yahoo Finance
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

// GetHistorical delegates to the underlying client (no Redis cache for historical bars —
// stored in price_cache_history DB table instead).
func (cc *CachedClient) GetHistorical(ctx context.Context, symbol string, from, to time.Time) ([]HistoricalBar, error) {
	return cc.client.GetHistorical(ctx, symbol, from, to)
}
