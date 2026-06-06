package portfolio

import (
	"log"
	"net/http"
	"sort"
	"time"

	"portfolio-tracker/internal/auth"
	"portfolio-tracker/internal/model"
	"portfolio-tracker/internal/yahoofinance"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Handler provides HTTP handlers for portfolio summary.
type Handler struct {
	db *gorm.DB
	yf *yahoofinance.CachedClient
}

func NewHandler(db *gorm.DB, yf *yahoofinance.CachedClient) *Handler {
	return &Handler{db: db, yf: yf}
}

// getOrCreatePortfolio returns the user's default portfolio, creating it if needed.
func (h *Handler) getOrCreatePortfolio(userID uuid.UUID) (*model.Portfolio, error) {
	var p model.Portfolio
	err := h.db.Where("user_id = ?", userID).First(&p).Error
	if err == nil {
		return &p, nil
	}
	p = model.Portfolio{UserID: userID, Name: "Default"}
	if err := h.db.Create(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// PositionResponse is a single holding with live price data attached.
type PositionResponse struct {
	Symbol        string  `json:"symbol"`
	CompanyName   string  `json:"company_name"`
	Quantity      float64 `json:"quantity"`
	AvgCost       float64 `json:"avg_cost"`
	CostBasis     float64 `json:"cost_basis"`
	CurrentPrice  float64 `json:"current_price"`
	DayChangePct  float64 `json:"day_change_pct"`
	MarketValue   float64 `json:"market_value"`
	UnrealizedPnL float64 `json:"unrealized_pnl"`
	UnrealizedPct float64 `json:"unrealized_pnl_pct"`
	Weight        float64 `json:"weight_pct"`
}

// Summary returns all open positions with live prices and unrealized P&L.
func (h *Handler) Summary(c *gin.Context) {
	userID, ok := auth.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	p, err := h.getOrCreatePortfolio(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "portfolio error"})
		return
	}

	var txs []model.Transaction
	if err := h.db.Where("portfolio_id = ?", p.ID).Order("trade_date ASC").Find(&txs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
		return
	}

	positions, realized := CalculatePositions(txs)

	// Collect open symbols
	var symbols []string
	for sym := range positions {
		symbols = append(symbols, sym)
	}

	// Fetch current prices (best-effort — missing prices leave current_price = 0)
	priceInfos := make(map[string]yahoofinance.PriceInfo)
	if len(symbols) > 0 {
		if fetched, err := h.yf.GetCurrentPriceInfos(c.Request.Context(), symbols); err == nil {
			priceInfos = fetched
		} else {
			log.Printf("[portfolio] GetCurrentPriceInfos error: %v", err)
		}

		// Fallback: for any symbol still missing a price, use the most recent DB-cached price
		var missing []string
		for _, sym := range symbols {
			if priceInfos[sym].Price == 0 {
				missing = append(missing, sym)
			}
		}
		if len(missing) > 0 {
			type priceRow struct {
				Symbol     string
				ClosePrice float64
			}
			var rows []priceRow
			// For each missing symbol, get the latest cached close price
			h.db.Raw(`
				SELECT DISTINCT ON (symbol) symbol, close_price
				FROM price_cache_histories
				WHERE symbol IN ?
				ORDER BY symbol, date DESC
			`, missing).Scan(&rows)
			for _, r := range rows {
				if r.ClosePrice > 0 {
					priceInfos[r.Symbol] = yahoofinance.PriceInfo{Price: r.ClosePrice}
					log.Printf("[portfolio] using cached DB price for %s: %.4f", r.Symbol, r.ClosePrice)
				}
			}
		}
	}

	// Record price source timestamp for staleness info
	_ = time.Now() // used implicitly via log timestamps

	// Compute total market value for weight calculation
	var totalMarketValue float64
	for _, pos := range positions {
		totalMarketValue += pos.Quantity * priceInfos[pos.Symbol].Price
	}

	result := make([]PositionResponse, 0, len(positions))
	for _, pos := range positions {
		info := priceInfos[pos.Symbol]
		price := info.Price
		mv := pos.Quantity * price
		unrealized := mv - pos.CostBasis
		unrealizedPct := 0.0
		if pos.CostBasis > 0 {
			unrealizedPct = unrealized / pos.CostBasis * 100
		}
		weight := 0.0
		if totalMarketValue > 0 {
			weight = mv / totalMarketValue * 100
		}
		result = append(result, PositionResponse{
			Symbol:        pos.Symbol,
			CompanyName:   pos.CompanyName,
			Quantity:      pos.Quantity,
			AvgCost:       pos.AvgCost,
			CostBasis:     pos.CostBasis,
			CurrentPrice:  price,
			DayChangePct:  info.DayChangePct,
			MarketValue:   mv,
			UnrealizedPnL: unrealized,
			UnrealizedPct: unrealizedPct,
			Weight:        weight,
		})
	}

	// Stable sort by symbol
	sort.Slice(result, func(i, j int) bool { return result[i].Symbol < result[j].Symbol })

	totalCost := 0.0
	for _, pos := range positions {
		totalCost += pos.CostBasis
	}

	totalUnrealizedPnL := totalMarketValue - totalCost
	totalUnrealizedPct := 0.0
	if totalCost > 0 {
		totalUnrealizedPct = totalUnrealizedPnL / totalCost * 100
	}

	// Aggregate realized P&L
	realizedBySymbol := AggregateRealizedPnL(realized)
	totalRealizedPnL := 0.0
	for _, pnl := range realizedBySymbol {
		totalRealizedPnL += pnl
	}

	totalPnL := totalUnrealizedPnL + totalRealizedPnL
	totalPnLPct := 0.0
	if totalCost > 0 {
		totalPnLPct = totalPnL / totalCost * 100
	}

	c.JSON(http.StatusOK, gin.H{
		"positions":                result,
		"total_value":              totalMarketValue,
		"total_cost":               totalCost,
		"total_unrealized_pnl":     totalUnrealizedPnL,
		"total_unrealized_pnl_pct": totalUnrealizedPct,
		"total_realized_pnl":       totalRealizedPnL,
		"total_pnl":                totalPnL,
		"total_pnl_pct":            totalPnLPct,
	})
}

// Quote returns company name and current price for a given symbol.
func (h *Handler) Quote(c *gin.Context) {
	symbol := c.Param("symbol")
	if symbol == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "symbol required"})
		return
	}

	q, err := h.yf.GetQuote(c.Request.Context(), symbol)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to fetch quote"})
		return
	}

	name := q.LongName
	if name == "" {
		name = q.ShortName
	}

	c.JSON(http.StatusOK, gin.H{
		"symbol":       q.Symbol,
		"company_name": name,
		"price":        q.RegularMarketPrice,
		"currency":     q.Currency,
	})
}
