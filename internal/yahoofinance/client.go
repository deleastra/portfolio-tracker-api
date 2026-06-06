package yahoofinance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// QuoteResponse is the minimal shape returned by Yahoo Finance quote endpoint.
type QuoteResponse struct {
	Symbol                     string  `json:"symbol"`
	LongName                   string  `json:"longName"`
	ShortName                  string  `json:"shortName"`
	RegularMarketPrice         float64 `json:"regularMarketPrice"`
	RegularMarketChangePercent float64 `json:"regularMarketChangePercent"`
	Currency                   string  `json:"currency"`
}

type quoteAPIResponse struct {
	QuoteResponse struct {
		Result []struct {
			Symbol                     string  `json:"symbol"`
			LongName                   string  `json:"longName"`
			ShortName                  string  `json:"shortName"`
			RegularMarketPrice         float64 `json:"regularMarketPrice"`
			RegularMarketChangePercent float64 `json:"regularMarketChangePercent"`
			Currency                   string  `json:"currency"`
		} `json:"result"`
		Error interface{} `json:"error"`
	} `json:"quoteResponse"`
}

// HistoricalBar is a single daily OHLCV bar.
type HistoricalBar struct {
	Date       time.Time
	ClosePrice float64
}

// Client makes requests to Yahoo Finance with crumb-based authentication.
type Client struct {
	baseURL    string
	httpClient *http.Client
	crumb      string
	crumbMu    sync.RWMutex
}

func NewClient(baseURL string) *Client {
	jar, _ := cookiejar.New(nil)
	c := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Jar:     jar,
		},
	}
	// Attempt crumb init; failures are non-fatal (requests will retry)
	if err := c.refreshCrumb(context.Background()); err != nil {
		log.Printf("[yahoo] crumb init failed: %v", err)
	}
	return c
}

// refreshCrumb obtains a new Yahoo Finance crumb by visiting finance.yahoo.com then the crumb endpoint.
func (c *Client) refreshCrumb(ctx context.Context) error {
	// Step 1: visit finance.yahoo.com to set the necessary cookies for authenticated API access
	initURL := "https://finance.yahoo.com"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, initURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cookie init: %w", err)
	}
	resp.Body.Close()

	// Step 2: fetch crumb
	crumbURL := "https://query1.finance.yahoo.com/v1/test/getcrumb"
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, crumbURL, nil)
	if err != nil {
		return err
	}
	req2.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req2.Header.Set("Accept", "application/json, text/plain, */*")
	req2.Header.Set("Referer", "https://finance.yahoo.com/")

	resp2, err := c.httpClient.Do(req2)
	if err != nil {
		return fmt.Errorf("crumb fetch: %w", err)
	}
	defer resp2.Body.Close()

	body, err := io.ReadAll(resp2.Body)
	if err != nil {
		return err
	}

	crumb := strings.TrimSpace(string(body))
	if crumb == "" || strings.Contains(crumb, "<html") {
		return fmt.Errorf("got invalid crumb response (status %d)", resp2.StatusCode)
	}

	c.crumbMu.Lock()
	c.crumb = crumb
	c.crumbMu.Unlock()
	log.Printf("[yahoo] crumb refreshed OK")
	return nil
}

func (c *Client) getCrumb() string {
	c.crumbMu.RLock()
	defer c.crumbMu.RUnlock()
	return c.crumb
}

// doWithCrumb performs a GET, appending the crumb param. Retries once with a fresh crumb on 401/403.
func (c *Client) doWithCrumb(ctx context.Context, rawURL string) ([]byte, error) {
	body, status, err := c.doGet(ctx, rawURL, c.getCrumb())
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		if rerr := c.refreshCrumb(ctx); rerr != nil {
			return nil, fmt.Errorf("status %d and crumb refresh failed: %w", status, rerr)
		}
		body, status, err = c.doGet(ctx, rawURL, c.getCrumb())
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("yahoo finance returned status %d after crumb refresh", status)
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("yahoo finance returned status %d", status)
	}
	return body, nil
}

func (c *Client) doGet(ctx context.Context, rawURL string, crumb string) ([]byte, int, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, 0, err
	}
	if crumb != "" {
		q := u.Query()
		q.Set("crumb", crumb)
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://finance.yahoo.com/")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// GetQuote fetches the current price for a single symbol.
func (c *Client) GetQuote(ctx context.Context, symbol string) (*QuoteResponse, error) {
	log.Printf("[yahoo] GetQuote: fetching quote for %s", symbol)
	start := time.Now()
	rawURL := fmt.Sprintf("%s/v7/finance/quote?symbols=%s", c.baseURL, symbol)
	body, err := c.doWithCrumb(ctx, rawURL)
	if err != nil {
		log.Printf("[yahoo] GetQuote: error fetching %s: %v", symbol, err)
		return nil, fmt.Errorf("GetQuote %s: %w", symbol, err)
	}

	var apiResp quoteAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode quote response: %w", err)
	}
	if len(apiResp.QuoteResponse.Result) == 0 {
		return nil, fmt.Errorf("no quote data for symbol %s", symbol)
	}

	r := apiResp.QuoteResponse.Result[0]
	log.Printf("[yahoo] GetQuote: %s price=%.4f elapsed=%s", symbol, r.RegularMarketPrice, time.Since(start))
	return &QuoteResponse{
		Symbol:                     r.Symbol,
		LongName:                   r.LongName,
		ShortName:                  r.ShortName,
		RegularMarketPrice:         r.RegularMarketPrice,
		RegularMarketChangePercent: r.RegularMarketChangePercent,
		Currency:                   r.Currency,
	}, nil
}

type historicalAPIResponse struct {
	Chart struct {
		Result []struct {
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				AdjClose []struct {
					AdjClose []float64 `json:"adjclose"`
				} `json:"adjclose"`
			} `json:"indicators"`
		} `json:"result"`
		Error interface{} `json:"error"`
	} `json:"chart"`
}

// GetHistorical fetches daily adjusted close prices for a symbol between from and to (inclusive).
func (c *Client) GetHistorical(ctx context.Context, symbol string, from, to time.Time) ([]HistoricalBar, error) {
	log.Printf("[yahoo] GetHistorical: fetching %s from %s to %s", symbol, from.Format("2006-01-02"), to.Format("2006-01-02"))
	start := time.Now()
	rawURL := fmt.Sprintf(
		"%s/v8/finance/chart/%s?period1=%d&period2=%d&interval=1d",
		c.baseURL, symbol, from.Unix(), to.Unix(),
	)
	body, err := c.doWithCrumb(ctx, rawURL)
	if err != nil {
		log.Printf("[yahoo] GetHistorical: error fetching %s: %v", symbol, err)
		return nil, fmt.Errorf("GetHistorical %s: %w", symbol, err)
	}

	var apiResp historicalAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode historical response: %w", err)
	}

	if len(apiResp.Chart.Result) == 0 {
		return nil, fmt.Errorf("no historical data for symbol %s", symbol)
	}

	result := apiResp.Chart.Result[0]
	if len(result.Indicators.AdjClose) == 0 {
		return nil, fmt.Errorf("no adjusted close data for symbol %s", symbol)
	}

	closes := result.Indicators.AdjClose[0].AdjClose
	bars := make([]HistoricalBar, 0, len(result.Timestamp))
	for i, ts := range result.Timestamp {
		if i >= len(closes) {
			break
		}
		if closes[i] == 0 {
			continue
		}
		bars = append(bars, HistoricalBar{
			Date:       time.Unix(ts, 0).UTC().Truncate(24 * time.Hour),
			ClosePrice: closes[i],
		})
	}

	log.Printf("[yahoo] GetHistorical: %s got %d bars elapsed=%s", symbol, len(bars), time.Since(start))
	return bars, nil
}
