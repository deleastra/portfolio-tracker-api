package analytics_test

import (
	"math"
	"testing"
	"time"

	"portfolio-tracker/internal/analytics"

	"github.com/stretchr/testify/assert"
)

func makeReturns(vals []float64) []analytics.DailyReturn {
	out := make([]analytics.DailyReturn, len(vals))
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, v := range vals {
		out[i] = analytics.DailyReturn{Date: base.AddDate(0, 0, i), Return: v}
	}
	return out
}

// Simple flat returns for sanity checks
var flatPortfolio = makeReturns(repeatFloat(0.001, 252))  // +0.1% / day
var flatBenchmark = makeReturns(repeatFloat(0.0005, 252)) // +0.05% / day

func repeatFloat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestComputeMetrics_PositiveSharpe(t *testing.T) {
	m := analytics.ComputeMetrics(flatPortfolio, flatBenchmark)
	assert.Greater(t, m.SharpeRatio, 0.0, "sharpe should be positive for above-RF returns")
}

func TestComputeMetrics_PositiveAlpha(t *testing.T) {
	// Portfolio outperforms benchmark → alpha > 0
	m := analytics.ComputeMetrics(flatPortfolio, flatBenchmark)
	assert.Greater(t, m.Alpha, 0.0)
}

func TestComputeMetrics_Beta_FlatReturns(t *testing.T) {
	// Identical returns → beta ≈ 1
	m := analytics.ComputeMetrics(flatPortfolio, flatPortfolio)
	assert.InDelta(t, 1.0, m.Beta, 0.01)
}

func TestComputeMetrics_MaxDrawdown_AllPositive(t *testing.T) {
	// Always going up → no drawdown
	m := analytics.ComputeMetrics(flatPortfolio, flatBenchmark)
	assert.InDelta(t, 0.0, m.MaxDrawdown, 1e-9)
}

func TestComputeMetrics_MaxDrawdown_WithDrop(t *testing.T) {
	// 50% drop then flat
	returns := make([]float64, 252)
	returns[125] = -0.50
	m := analytics.ComputeMetrics(makeReturns(returns), flatBenchmark)
	assert.InDelta(t, 0.50, m.MaxDrawdown, 0.01)
}

func TestComputeMetrics_SortinoRatio_NoDownside(t *testing.T) {
	// Only positive returns → sortino = +Inf
	m := analytics.ComputeMetrics(flatPortfolio, flatBenchmark)
	assert.True(t, math.IsInf(m.SortinoRatio, 1) || m.SortinoRatio > 100,
		"sortino should be very high with no downside")
}

func TestComputeMetrics_EmptyInput(t *testing.T) {
	m := analytics.ComputeMetrics(nil, nil)
	assert.Equal(t, analytics.Metrics{}, m)
}

func TestAlignReturns_CommonDatesOnly(t *testing.T) {
	a := makeReturns([]float64{0.01, 0.02, 0.03})
	b := []analytics.DailyReturn{
		{Date: a[0].Date, Return: 0.005},
		// gap on a[1].Date
		{Date: a[2].Date, Return: 0.007},
	}
	alignedA, alignedB := analytics.AlignReturns(a, b)
	assert.Len(t, alignedA, 2)
	assert.Len(t, alignedB, 2)
	assert.Equal(t, a[0].Date, alignedA[0].Date)
	assert.Equal(t, a[2].Date, alignedA[1].Date)
}
