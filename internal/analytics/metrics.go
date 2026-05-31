package analytics

import (
	"math"
	"sort"
	"time"
)

// DailyReturn represents a portfolio or benchmark return for a single day.
type DailyReturn struct {
	Date   time.Time
	Return float64 // fractional return, e.g. 0.012 = +1.2%
}

// Metrics contains all computed risk/return statistics.
type Metrics struct {
	// Return
	TotalReturn      float64 `json:"total_return_pct"`  // fractional, e.g. 0.12 = 12%
	CAGR             float64 `json:"cagr"`
	// Risk-adjusted
	SharpeRatio      float64 `json:"sharpe_ratio"`
	SortinoRatio     float64 `json:"sortino_ratio"`
	// Benchmark-relative
	Alpha            float64 `json:"alpha"`
	Beta             float64 `json:"beta"`
	InformationRatio float64 `json:"information_ratio"`
	TreynorRatio     float64 `json:"treynor_ratio"`
	TrackingError    float64 `json:"tracking_error"`
	// Drawdown
	MaxDrawdown      float64 `json:"max_drawdown"` // magnitude, e.g. 0.15 = 15%
	CalmarRatio      float64 `json:"calmar_ratio"`
	// Trade stats
	WinRate          float64 `json:"win_rate"`
	ProfitFactor     float64 `json:"profit_factor"`
}

const riskFreeRate = 0.045 // 4.5% annualised (approximate 10Y US Treasury)
const tradingDaysPerYear = 252.0

// ComputeMetrics derives all metrics from portfolio and benchmark daily return series.
// portfolioReturns and benchmarkReturns must be aligned by date.
func ComputeMetrics(portfolioReturns, benchmarkReturns []DailyReturn) Metrics {
	n := len(portfolioReturns)
	if n == 0 {
		return Metrics{}
	}

	pr := extractReturns(portfolioReturns)
	br := extractReturns(benchmarkReturns)

	// Align lengths
	minLen := len(pr)
	if len(br) < minLen {
		minLen = len(br)
	}
	pr = pr[:minLen]
	br = br[:minLen]

	meanP := mean(pr)
	meanB := mean(br)
	stdP := stddev(pr)

	// Total return
	totalReturn := cumulativeReturn(pr)

	// CAGR
	years := float64(minLen) / tradingDaysPerYear
	var cagr float64
	if years > 0 {
		cagr = math.Pow(1+totalReturn, 1/years) - 1
	}

	// Sharpe Ratio (annualised)
	dailyRf := riskFreeRate / tradingDaysPerYear
	excessReturns := make([]float64, minLen)
	for i := range pr {
		excessReturns[i] = pr[i] - dailyRf
	}
	sharpe := 0.0
	if stdP > 0 {
		sharpe = (mean(excessReturns) / stdP) * math.Sqrt(tradingDaysPerYear)
	}

	// Sortino Ratio
	sortino := sortinoRatio(pr, dailyRf)

	// Beta & Alpha
	beta := computeBeta(pr, br, meanP, meanB)
	alpha := (meanP - dailyRf) - beta*(meanB-dailyRf)
	alpha *= tradingDaysPerYear // annualise

	// Information Ratio
	activeDiff := make([]float64, minLen)
	for i := range pr {
		activeDiff[i] = pr[i] - br[i]
	}
	trackingErr := stddev(activeDiff) * math.Sqrt(tradingDaysPerYear)
	ir := 0.0
	if trackingErr > 0 {
		ir = (mean(activeDiff) * tradingDaysPerYear) / trackingErr
	}

	// Treynor Ratio
	treynor := 0.0
	if beta != 0 {
		treynor = ((meanP - dailyRf) * tradingDaysPerYear) / beta
	}

	// Max Drawdown
	maxDD := maxDrawdown(pr)

	// Calmar Ratio
	calmar := 0.0
	if maxDD > 0 {
		calmar = cagr / maxDD
	}

	return Metrics{
		TotalReturn:      totalReturn,
		CAGR:             cagr,
		SharpeRatio:      sharpe,
		SortinoRatio:     sortino,
		Alpha:            alpha,
		Beta:             beta,
		InformationRatio: ir,
		TreynorRatio:     treynor,
		TrackingError:    trackingErr,
		MaxDrawdown:      maxDD,
		CalmarRatio:      calmar,
	}
}

// ---- helpers ----

func extractReturns(returns []DailyReturn) []float64 {
	out := make([]float64, len(returns))
	for i, r := range returns {
		out[i] = r.Return
	}
	return out
}

func mean(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func stddev(data []float64) float64 {
	if len(data) < 2 {
		return 0
	}
	m := mean(data)
	sum := 0.0
	for _, v := range data {
		diff := v - m
		sum += diff * diff
	}
	return math.Sqrt(sum / float64(len(data)-1))
}

func cumulativeReturn(returns []float64) float64 {
	cumulative := 1.0
	for _, r := range returns {
		cumulative *= (1 + r)
	}
	return cumulative - 1
}

func sortinoRatio(returns []float64, dailyRf float64) float64 {
	excess := make([]float64, len(returns))
	for i, r := range returns {
		excess[i] = r - dailyRf
	}
	meanExcess := mean(excess)

	// Downside deviation — only negative excess returns
	sumSq := 0.0
	count := 0
	for _, e := range excess {
		if e < 0 {
			sumSq += e * e
			count++
		}
	}
	if count == 0 {
		return math.Inf(1)
	}
	downsideDev := math.Sqrt(sumSq/float64(len(returns))) * math.Sqrt(tradingDaysPerYear)
	if downsideDev == 0 {
		return math.Inf(1)
	}
	return (meanExcess * tradingDaysPerYear) / downsideDev
}

func computeBeta(pr, br []float64, meanP, meanB float64) float64 {
	covariance := 0.0
	varianceB := 0.0
	for i := range pr {
		covariance += (pr[i] - meanP) * (br[i] - meanB)
		varianceB += (br[i] - meanB) * (br[i] - meanB)
	}
	if varianceB == 0 {
		return 0
	}
	return covariance / varianceB
}

func maxDrawdown(returns []float64) float64 {
	peak := 1.0
	nav := 1.0
	maxDD := 0.0
	for _, r := range returns {
		nav *= (1 + r)
		if nav > peak {
			peak = nav
		}
		dd := (peak - nav) / peak
		if dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

// AlignReturns aligns two return series by date, keeping only common dates in order.
func AlignReturns(a, b []DailyReturn) ([]DailyReturn, []DailyReturn) {
	bMap := make(map[time.Time]float64, len(b))
	for _, r := range b {
		bMap[r.Date.Truncate(24*time.Hour)] = r.Return
	}

	var alignedA, alignedB []DailyReturn
	for _, r := range a {
		d := r.Date.Truncate(24 * time.Hour)
		if bR, ok := bMap[d]; ok {
			alignedA = append(alignedA, r)
			alignedB = append(alignedB, DailyReturn{Date: d, Return: bR})
		}
	}

	// Ensure chronological order
	sort.Slice(alignedA, func(i, j int) bool { return alignedA[i].Date.Before(alignedA[j].Date) })
	sort.Slice(alignedB, func(i, j int) bool { return alignedB[i].Date.Before(alignedB[j].Date) })

	return alignedA, alignedB
}
