package portfolio

import (
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"portfolio-tracker/internal/auth"
	"portfolio-tracker/internal/instrument"
	"portfolio-tracker/internal/model"
	"portfolio-tracker/internal/yahoofinance"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Handler provides HTTP handlers for portfolio summary.
type Handler struct {
	db             *gorm.DB
	yf             *yahoofinance.CachedClient
	instrumentSvc  *instrument.Service
}

func NewHandler(db *gorm.DB, yf *yahoofinance.CachedClient, instrumentSvc *instrument.Service) *Handler {
	return &Handler{db: db, yf: yf, instrumentSvc: instrumentSvc}
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
	Sector        string  `json:"sector"`
	EntryDate     string  `json:"entry_date"`
}

type summaryData struct {
	Positions           []PositionResponse
	TotalValue          float64
	TotalCost           float64
	TotalUnrealizedPnL  float64
	TotalUnrealizedPct  float64
	TotalRealizedPnL    float64
	TotalPnL            float64
	TotalPnLPct         float64
}

// buildSummaryData fetches positions, prices, and computes all portfolio metrics.
// Shared by Summary and ExportCSV.
func (h *Handler) buildSummaryData(c *gin.Context, portfolioID uuid.UUID, txs []model.Transaction) (*summaryData, error) {
	positions, realized := CalculatePositions(txs)

	// Collect open symbols
	var symbols []string
	for sym := range positions {
		symbols = append(symbols, sym)
	}

	// Fetch current prices (best-effort — missing prices leave current_price = 0)
	priceInfos := make(map[string]yahoofinance.PriceInfo)
	if len(symbols) > 0 {
		if yahoofinance.IsUSMarketOpen() {
			if fetched, err := h.yf.GetCurrentPriceInfos(c.Request.Context(), symbols); err == nil {
				priceInfos = fetched
			} else {
				log.Printf("[portfolio] GetCurrentPriceInfos error: %v", err)
			}
		} else {
			lastDay := yahoofinance.LastTradingDay()
			type priceRow struct {
				Symbol     string
				ClosePrice float64
			}
			var rows []priceRow
			h.db.Raw(`
				SELECT DISTINCT ON (symbol) symbol, close_price
				FROM price_cache_histories
				WHERE symbol IN ? AND date <= ?
				ORDER BY symbol, date DESC
			`, symbols, lastDay.Add(24*time.Hour)).Scan(&rows)
			for _, r := range rows {
				if r.ClosePrice > 0 {
					priceInfos[r.Symbol] = yahoofinance.PriceInfo{Price: r.ClosePrice}
				}
			}

			var missing []string
			for _, sym := range symbols {
				if priceInfos[sym].Price == 0 {
					missing = append(missing, sym)
				}
			}
			for _, sym := range missing {
				bars, err := h.yf.GetHistorical(c.Request.Context(), sym, lastDay.AddDate(0, 0, -7), lastDay)
				if err != nil {
					log.Printf("[portfolio] GetHistorical fallback error for %s: %v", sym, err)
					continue
				}
				if len(bars) > 0 {
					last := bars[len(bars)-1]
					priceInfos[sym] = yahoofinance.PriceInfo{Price: last.ClosePrice}
				}
			}
		}

		// Final fallback: newest price in DB
		var stillMissing []string
		for _, sym := range symbols {
			if priceInfos[sym].Price == 0 {
				stillMissing = append(stillMissing, sym)
			}
		}
		if len(stillMissing) > 0 {
			type priceRow struct {
				Symbol     string
				ClosePrice float64
			}
			var rows []priceRow
			h.db.Raw(`
				SELECT DISTINCT ON (symbol) symbol, close_price
				FROM price_cache_histories
				WHERE symbol IN ?
				ORDER BY symbol, date DESC
			`, stillMissing).Scan(&rows)
			for _, r := range rows {
				if r.ClosePrice > 0 {
					priceInfos[r.Symbol] = yahoofinance.PriceInfo{Price: r.ClosePrice}
					log.Printf("[portfolio] using cached DB price for %s: %.4f", r.Symbol, r.ClosePrice)
				}
			}
		}

	}

	// Ensure instrument metadata (sector, company_name) is up to date in DB.
	// EnsureInstruments is best-effort and non-blocking on error.
	h.instrumentSvc.EnsureInstruments(c.Request.Context(), symbols)

	instruments := h.instrumentSvc.GetBySymbols(c.Request.Context(), symbols)

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
		entryDate := ""
		if !pos.EntryDate.IsZero() {
			entryDate = pos.EntryDate.Format("2006-01-02")
		}
		// Use instrument metadata (DB) as source of truth; fall back to transaction data.
		companyName := pos.CompanyName
		sector := ""
		if inst, ok := instruments[pos.Symbol]; ok {
			if inst.CompanyName != "" {
				companyName = inst.CompanyName
			}
			sector = inst.Sector
		}
		result = append(result, PositionResponse{
			Symbol:        pos.Symbol,
			CompanyName:   companyName,
			Quantity:      pos.Quantity,
			AvgCost:       pos.AvgCost,
			CostBasis:     pos.CostBasis,
			CurrentPrice:  price,
			DayChangePct:  info.DayChangePct,
			MarketValue:   mv,
			UnrealizedPnL: unrealized,
			UnrealizedPct: unrealizedPct,
			Weight:        weight,
			Sector:        sector,
			EntryDate:     entryDate,
		})
	}

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

	return &summaryData{
		Positions:          result,
		TotalValue:         totalMarketValue,
		TotalCost:          totalCost,
		TotalUnrealizedPnL: totalUnrealizedPnL,
		TotalUnrealizedPct: totalUnrealizedPct,
		TotalRealizedPnL:   totalRealizedPnL,
		TotalPnL:           totalPnL,
		TotalPnLPct:        totalPnLPct,
	}, nil
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

	data, err := h.buildSummaryData(c, p.ID, txs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "summary failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"positions":                data.Positions,
		"total_value":              data.TotalValue,
		"total_cost":               data.TotalCost,
		"total_unrealized_pnl":     data.TotalUnrealizedPnL,
		"total_unrealized_pnl_pct": data.TotalUnrealizedPct,
		"total_realized_pnl":       data.TotalRealizedPnL,
		"total_pnl":                data.TotalPnL,
		"total_pnl_pct":            data.TotalPnLPct,
	})
}

// ExportCSV streams portfolio holdings as a CSV file download.
func (h *Handler) ExportCSV(c *gin.Context) {
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

	data, err := h.buildSummaryData(c, p.ID, txs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export failed"})
		return
	}

	filename := fmt.Sprintf("portfolio_%s.csv", time.Now().Format("2006-01-02"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"Ticker", "Shares", "Avg Cost (USD)", "Current Price (USD)", "Sector", "Entry Date", "Portfolio % weight"})
	for _, pos := range data.Positions {
		_ = w.Write([]string{
			pos.Symbol,
			fmt.Sprintf("%.5f", pos.Quantity),
			fmt.Sprintf("%.4f", pos.AvgCost),
			fmt.Sprintf("%.4f", pos.CurrentPrice),
			pos.Sector,
			pos.EntryDate,
			fmt.Sprintf("%.2f", pos.Weight),
		})
	}
	w.Flush()
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
		"symbol":         q.Symbol,
		"company_name":   name,
		"price":          q.RegularMarketPrice,
		"day_change_pct": q.RegularMarketChangePercent,
		"currency":       q.Currency,
	})
}
