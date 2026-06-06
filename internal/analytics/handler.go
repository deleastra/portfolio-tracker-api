package analytics

import (
	"context"
	"log"
	"net/http"
	"sort"
	"time"

	"portfolio-tracker/internal/auth"
	"portfolio-tracker/internal/model"
	"portfolio-tracker/internal/portfolio"
	"portfolio-tracker/internal/yahoofinance"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gormClause "gorm.io/gorm/clause"
)

// Handler provides HTTP handlers for analytics endpoints.
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

// PnL returns realized and unrealized P&L per symbol.
func (h *Handler) PnL(c *gin.Context) {
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

	positions, realized := portfolio.CalculatePositions(txs)
	realizedBySymbol := portfolio.AggregateRealizedPnL(realized)

	// Collect open symbols for current price fetch
	var symbols []string
	for sym := range positions {
		symbols = append(symbols, sym)
	}

	prices := make(map[string]float64)
	if len(symbols) > 0 {
		if fetched, err := h.yf.GetCurrentPrices(c.Request.Context(), symbols); err == nil {
			prices = fetched
		}
	}

	type SymbolPnL struct {
		Symbol        string  `json:"symbol"`
		CompanyName   string  `json:"company_name"`
		RealizedPnL   float64 `json:"realized_pnl"`
		UnrealizedPnL float64 `json:"unrealized_pnl"`
		TotalPnL      float64 `json:"total_pnl"`
		CostBasis     float64 `json:"cost_basis"`
		TotalPnLPct   float64 `json:"total_pnl_pct"`
		IsOpen        bool    `json:"is_open"`
	}

	type PnLResponse struct {
		RealizedPnL   float64    `json:"realized_pnl"`
		UnrealizedPnL float64    `json:"unrealized_pnl"`
		TotalPnL      float64    `json:"total_pnl"`
		Entries       []SymbolPnL `json:"entries"`
	}

	// Build company name map from transactions
	companyNames := make(map[string]string)
	for _, tx := range txs {
		if tx.CompanyName != "" {
			companyNames[tx.Symbol] = tx.CompanyName
		}
	}

	// Build realized cost basis per symbol (total cost of all shares sold)
	realizedCostBasis := make(map[string]float64)
	for _, r := range realized {
		realizedCostBasis[r.Symbol] += r.CostBasis
	}

	// Collect all symbols that appear in either positions or realized
	symbolSet := make(map[string]struct{})
	for sym := range positions {
		symbolSet[sym] = struct{}{}
	}
	for sym := range realizedBySymbol {
		symbolSet[sym] = struct{}{}
	}

	var totalRealized, totalUnrealized float64
	result := make([]SymbolPnL, 0, len(symbolSet))
	for sym := range symbolSet {
		var unrealized float64
		isOpen := false
		openCostBasis := 0.0
		if pos, ok := positions[sym]; ok {
			mv := pos.Quantity * prices[sym]
			unrealized = mv - pos.CostBasis
			isOpen = true
			openCostBasis = pos.CostBasis
		}
		realizedPnL := realizedBySymbol[sym]
		totalCostBasis := openCostBasis + realizedCostBasis[sym]
		totalPnL := realizedPnL + unrealized
		var totalPnLPct float64
		if totalCostBasis > 0 {
			totalPnLPct = totalPnL / totalCostBasis * 100
		}
		totalRealized += realizedPnL
		totalUnrealized += unrealized
		result = append(result, SymbolPnL{
			Symbol:        sym,
			CompanyName:   companyNames[sym],
			RealizedPnL:   realizedPnL,
			UnrealizedPnL: unrealized,
			TotalPnL:      totalPnL,
			CostBasis:     totalCostBasis,
			TotalPnLPct:   totalPnLPct,
			IsOpen:        isOpen,
		})
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Symbol < result[j].Symbol })
	c.JSON(http.StatusOK, PnLResponse{
		RealizedPnL:   totalRealized,
		UnrealizedPnL: totalUnrealized,
		TotalPnL:      totalRealized + totalUnrealized,
		Entries:       result,
	})
}

// Performance returns daily cumulative returns for portfolio and benchmark.
// Query params: from (YYYY-MM-DD), to (YYYY-MM-DD), benchmark (default "SPY")
func (h *Handler) Performance(c *gin.Context) {
	userID, ok := auth.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	from, to, benchmark, errMsg := parseDateParams(c)
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}

	p, err := h.getOrCreatePortfolio(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "portfolio error"})
		return
	}

	var txs []model.Transaction
	if err := h.db.Where("portfolio_id = ? AND trade_date <= ?", p.ID, to).
		Order("trade_date ASC").Find(&txs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
		return
	}

	// Clamp from to the earliest transaction so the chart starts when the portfolio did.
	if len(txs) > 0 {
		firstTx := txs[0].TradeDate.Truncate(24 * time.Hour)
		if firstTx.After(from) {
			from = firstTx
		}
	}

	// If the US market is currently open and the requested range includes today,
	// fetch live intraday prices so the chart extends to the current moment.
	var todayPortfolioPrices map[string]float64
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if !to.Before(today) && isUSMarketOpen() {
		symSet := make(map[string]struct{})
		for _, tx := range txs {
			symSet[tx.Symbol] = struct{}{}
		}
		syms := make([]string, 0, len(symSet))
		for sym := range symSet {
			syms = append(syms, sym)
		}
		if fetched, err := h.yf.GetCurrentPrices(c.Request.Context(), syms); err == nil {
			todayPortfolioPrices = fetched
		}
	}

	portfolioReturns, err := h.computePortfolioReturns(c, txs, from, to, todayPortfolioPrices)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to compute portfolio returns: " + err.Error()})
		return
	}

	benchmarkBars, err := h.fetchHistoricalWithCache(c.Request.Context(), benchmark, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch benchmark data: " + err.Error()})
		return
	}

	// Add today's benchmark intraday price when market is open.
	if !to.Before(today) && isUSMarketOpen() {
		if q, err := h.yf.GetQuote(c.Request.Context(), benchmark); err == nil && q.RegularMarketPrice > 0 {
			benchmarkBars[today] = q.RegularMarketPrice
		}
	}

	benchmarkReturns := barsToMapReturns(benchmarkBars)

	// Build cumulative return series
	type DataPoint struct {
		Date               string  `json:"date"`
		PortfolioReturnPct float64 `json:"portfolio_return_pct"` // percent, e.g. 5.12 = +5.12%
		BenchmarkReturnPct float64 `json:"benchmark_return_pct"`
	}

	pAligned, bAligned := AlignReturns(portfolioReturns, benchmarkReturns)

	points := make([]DataPoint, len(pAligned))
	pCum := 1.0
	bCum := 1.0
	for i, r := range pAligned {
		pCum *= (1 + r.Return)
		bCum *= (1 + bAligned[i].Return)
		points[i] = DataPoint{
			Date:               r.Date.Format("2006-01-02"),
			PortfolioReturnPct: (pCum - 1) * 100,
			BenchmarkReturnPct: (bCum - 1) * 100,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"benchmark": benchmark,
		"points":    points,
	})
}

// Metrics returns all risk/return metrics for the authenticated user's portfolio.
// Query params: from (YYYY-MM-DD), to (YYYY-MM-DD), benchmark (default "SPY")
func (h *Handler) Metrics(c *gin.Context) {
	userID, ok := auth.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	from, to, benchmark, errMsg := parseDateParams(c)
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}

	p, err := h.getOrCreatePortfolio(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "portfolio error"})
		return
	}

	var txs []model.Transaction
	if err := h.db.Where("portfolio_id = ? AND trade_date <= ?", p.ID, to).
		Order("trade_date ASC").Find(&txs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
		return
	}

	// Clamp from to the earliest transaction so metrics cover only active period.
	if len(txs) > 0 {
		firstTx := txs[0].TradeDate.Truncate(24 * time.Hour)
		if firstTx.After(from) {
			from = firstTx
		}
	}

	portfolioReturns, err := h.computePortfolioReturns(c, txs, from, to, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to compute portfolio returns: " + err.Error()})
		return
	}

	benchmarkDayMap, err := h.fetchHistoricalWithCache(c.Request.Context(), benchmark, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch benchmark data: " + err.Error()})
		return
	}
	benchmarkReturns := barsToMapReturns(benchmarkDayMap)

	pAligned, bAligned := AlignReturns(portfolioReturns, benchmarkReturns)

	// Win rate and profit factor from trade history
	_, realized := portfolio.CalculatePositions(txs)
	var wins, losses int
	var grossProfit, grossLoss float64
	for _, r := range realized {
		if r.PnL > 0 {
			wins++
			grossProfit += r.PnL
		} else {
			losses++
			grossLoss += -r.PnL
		}
	}
	total := wins + losses
	winRate := 0.0
	if total > 0 {
		winRate = float64(wins) / float64(total)
	}
	profitFactor := 0.0
	if grossLoss > 0 {
		profitFactor = grossProfit / grossLoss
	}

	m := ComputeMetrics(pAligned, bAligned)
	m.WinRate = winRate
	m.ProfitFactor = profitFactor

	// TotalReturn must be computed from the full portfolio series (before alignment),
	// so it does NOT change when the benchmark changes.
	portfolioTotalReturn := 0.0
	if len(portfolioReturns) > 0 {
		cum := 1.0
		for _, r := range portfolioReturns {
			cum *= (1 + r.Return)
		}
		portfolioTotalReturn = cum - 1
	}

	benchmarkReturn := 0.0
	if len(bAligned) > 0 {
		bc := 1.0
		for _, r := range bAligned {
			bc *= (1 + r.Return)
		}
		benchmarkReturn = bc - 1
	}

	// Return flat response matching frontend PerformanceMetrics type.
	// Ratio fields stay as-is; fields shown with % suffix are multiplied by 100.
	type flatMetrics struct {
		Benchmark          string  `json:"benchmark"`
		TotalReturnPct     float64 `json:"total_return_pct"`
		BenchmarkReturnPct float64 `json:"benchmark_return_pct"`
		Alpha              float64 `json:"alpha"`
		Beta               float64 `json:"beta"`
		SharpeRatio        float64 `json:"sharpe_ratio"`
		SortinoRatio       float64 `json:"sortino_ratio"`
		MaxDrawdown        float64 `json:"max_drawdown"`
		CalmarRatio        float64 `json:"calmar_ratio"`
		InformationRatio   float64 `json:"information_ratio"`
		TreynorRatio       float64 `json:"treynor_ratio"`
		TrackingError      float64 `json:"tracking_error"`
		WinRate            float64 `json:"win_rate"`
		ProfitFactor       float64 `json:"profit_factor"`
		PeriodDays         int     `json:"period_days"`
	}

	c.JSON(http.StatusOK, flatMetrics{
		Benchmark:          benchmark,
		TotalReturnPct:     portfolioTotalReturn * 100,
		BenchmarkReturnPct: benchmarkReturn * 100,
		Alpha:              m.Alpha * 100,
		Beta:               m.Beta,
		SharpeRatio:        m.SharpeRatio,
		SortinoRatio:       m.SortinoRatio,
		MaxDrawdown:        m.MaxDrawdown * 100,
		CalmarRatio:        m.CalmarRatio,
		InformationRatio:   m.InformationRatio,
		TreynorRatio:       m.TreynorRatio,
		TrackingError:      m.TrackingError * 100,
		WinRate:            m.WinRate * 100,
		ProfitFactor:       m.ProfitFactor,
		PeriodDays:         len(pAligned),
	})
}

// ---- helpers ----

func parseDateParams(c *gin.Context) (from, to time.Time, benchmark, errMsg string) {
	benchmark = c.DefaultQuery("benchmark", "SPY")

	toStr := c.DefaultQuery("to", time.Now().Format("2006-01-02"))
	fromStr := c.DefaultQuery("from", time.Now().AddDate(-1, 0, 0).Format("2006-01-02"))

	var err error
	from, err = time.Parse("2006-01-02", fromStr)
	if err != nil {
		errMsg = "from must be YYYY-MM-DD"
		return
	}
	to, err = time.Parse("2006-01-02", toStr)
	if err != nil {
		errMsg = "to must be YYYY-MM-DD"
		return
	}
	return
}

// fetchHistoricalWithCache checks price_cache_history DB first, falls back to Yahoo Finance.
func (h *Handler) fetchHistoricalWithCache(ctx context.Context, symbol string, from, to time.Time) (map[time.Time]float64, error) {
	var cached []model.PriceCacheHistory
	h.db.Where("symbol = ? AND date >= ? AND date <= ?", symbol, from, to).
		Order("date ASC").Find(&cached)

	dayMap := make(map[time.Time]float64, len(cached))
	var latestCached time.Time
	for _, c := range cached {
		d := c.Date.Truncate(24 * time.Hour)
		dayMap[d] = c.ClosePrice
		if d.After(latestCached) {
			latestCached = d
		}
	}

	// If DB cache covers ≥50% of expected trading days, skip the API call —
	// but only if the cache is fresh enough (latest entry is within 1 trading day
	// of `to`). This prevents stale cache from creating gaps when `to` is today
	// and yesterday's close was not yet cached.
	expectedDays := int(to.Sub(from).Hours()/24) * 5 / 7
	cacheIsFresh := !latestCached.IsZero() && to.Sub(latestCached) <= 3*24*time.Hour // covers weekends
	if len(dayMap) > 0 && len(dayMap) >= expectedDays/2 && cacheIsFresh {
		return dayMap, nil
	}

	// Fetch from Yahoo Finance
	bars, err := h.yf.GetHistorical(ctx, symbol, from, to)
	if err != nil {
		if len(dayMap) > 0 {
			return dayMap, nil // return partial DB cache on error
		}
		return nil, err
	}

	records := make([]model.PriceCacheHistory, 0, len(bars))
	now := time.Now().UTC()
	for _, bar := range bars {
		d := bar.Date.Truncate(24 * time.Hour)
		dayMap[d] = bar.ClosePrice
		records = append(records, model.PriceCacheHistory{
			Symbol:     symbol,
			Date:       bar.Date,
			ClosePrice: bar.ClosePrice,
			FetchedAt:  now,
		})
	}

	// Persist to DB cache asynchronously
	if len(records) > 0 {
		go func() {
			h.db.Clauses(gormClause.OnConflict{
				Columns:   []gormClause.Column{{Name: "symbol"}, {Name: "date"}},
				DoUpdates: gormClause.AssignmentColumns([]string{"close_price", "fetched_at"}),
			}).CreateInBatches(records, 100)
		}()
	}

	return dayMap, nil
}

// isUSMarketOpen returns true when the US stock market is currently open.
// Market hours: Monday–Friday 09:30–16:00 Eastern Time (no holiday calendar).
func isUSMarketOpen() bool {
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

// computePortfolioReturns builds a daily Time-Weighted Return (TWR) series.
// For each day d, we use positions held at close of d-1 and price them at d-1 vs d.
// This removes the effect of cash flows (new buy/sell orders) from the return.
// todayPrices, if non-nil, injects intraday prices for the current trading day.
func (h *Handler) computePortfolioReturns(c *gin.Context, txs []model.Transaction, from, to time.Time, todayPrices map[string]float64) ([]DailyReturn, error) {
	// Collect all unique symbols
	symbolSet := make(map[string]struct{})
	for _, tx := range txs {
		symbolSet[tx.Symbol] = struct{}{}
	}

	// Fetch historical prices (DB cache first, then Yahoo Finance)
	allPrices := make(map[string]map[time.Time]float64)
	for sym := range symbolSet {
		dayMap, err := h.fetchHistoricalWithCache(c.Request.Context(), sym, from, to)
		if err != nil {
			log.Printf("[analytics] failed to fetch prices for %s: %v", sym, err)
			continue
		}
		allPrices[sym] = dayMap
	}

	// Inject intraday prices for today when the market is open.
	if len(todayPrices) > 0 {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		for sym, price := range todayPrices {
			if price > 0 {
				if allPrices[sym] == nil {
					allPrices[sym] = make(map[time.Time]float64)
				}
				allPrices[sym][today] = price
			}
		}
	}

	// Collect all trading dates from any symbol
	dateSet := make(map[time.Time]struct{})
	for _, dayMap := range allPrices {
		for d := range dayMap {
			dateSet[d] = struct{}{}
		}
	}
	if len(dateSet) == 0 {
		return nil, nil
	}

	dates := make([]time.Time, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })

	// Precompute carry-forward price snapshot at each date.
	// priceAt[date][symbol] = last known price on or before that date.
	lastKnown := make(map[string]float64)
	priceAt := make(map[time.Time]map[string]float64, len(dates))
	for _, d := range dates {
		for sym, dayMap := range allPrices {
			if p, ok := dayMap[d]; ok {
				lastKnown[sym] = p
			}
		}
		snapshot := make(map[string]float64, len(lastKnown))
		for sym, p := range lastKnown {
			snapshot[sym] = p
		}
		priceAt[d] = snapshot
	}

	// TWR: for each consecutive date pair (prevDate, currDate),
	// compute return using prevDate positions × (currPrice - prevPrice) / prevPrice.
	// Cash flows on currDate do NOT affect this period's return.
	returns := make([]DailyReturn, 0, len(dates))

	for i := 1; i < len(dates); i++ {
		prevDate := dates[i-1]
		currDate := dates[i]

		prevPrices := priceAt[prevDate]
		currPrices := priceAt[currDate]

		// Positions as of end of prevDate (all trades on prevDate are included)
		var prevTxs []model.Transaction
		for _, tx := range txs {
			if !tx.TradeDate.Truncate(24 * time.Hour).After(prevDate) {
				prevTxs = append(prevTxs, tx)
			}
		}
		positions, _ := portfolio.CalculatePositions(prevTxs)
		if len(positions) == 0 {
			continue
		}

		navPrev := 0.0
		navCurr := 0.0
		hasPrice := false
		for sym, pos := range positions {
			pp, okPrev := prevPrices[sym]
			cp, okCurr := currPrices[sym]
			if okPrev && okCurr && pp > 0 {
				navPrev += pos.Quantity * pp
				navCurr += pos.Quantity * cp
				hasPrice = true
			}
		}

		if !hasPrice || navPrev <= 0 {
			continue
		}

		returns = append(returns, DailyReturn{
			Date:   currDate,
			Return: (navCurr - navPrev) / navPrev,
		})
	}

	if len(returns) < 2 {
		return nil, nil
	}

	return returns, nil
}

// barsToMapReturns converts a map[date]price (from fetchHistoricalWithCache) to daily return series.
func barsToMapReturns(dayMap map[time.Time]float64) []DailyReturn {
	if len(dayMap) < 2 {
		return nil
	}
	dates := make([]time.Time, 0, len(dayMap))
	for d := range dayMap {
		dates = append(dates, d)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })

	returns := make([]DailyReturn, 0, len(dates)-1)
	for i := 1; i < len(dates); i++ {
		prev := dayMap[dates[i-1]]
		curr := dayMap[dates[i]]
		if prev > 0 {
			returns = append(returns, DailyReturn{
				Date:   dates[i],
				Return: (curr - prev) / prev,
			})
		}
	}
	return returns
}
