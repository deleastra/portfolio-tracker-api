package portfolio_test

import (
	"testing"
	"time"

	"portfolio-tracker/internal/model"
	"portfolio-tracker/internal/portfolio"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var pid = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func date(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func buy(sym string, qty, price, net float64, d string) model.Transaction {
	return model.Transaction{
		PortfolioID: pid, Symbol: sym, Action: model.ActionBuy,
		Quantity: qty, TradedPrice: price, NetAmount: net, TradeDate: date(d),
	}
}

func sell(sym string, qty, net float64, d string) model.Transaction {
	return model.Transaction{
		PortfolioID: pid, Symbol: sym, Action: model.ActionSell,
		Quantity: qty, NetAmount: net, TradeDate: date(d),
	}
}

func TestCalculatePositions_SimpleBuy(t *testing.T) {
	txs := []model.Transaction{
		buy("AAPL", 10, 150, 1500, "2024-01-01"),
	}
	positions, realized := portfolio.CalculatePositions(txs)

	require.Contains(t, positions, "AAPL")
	pos := positions["AAPL"]
	assert.InDelta(t, 10.0, pos.Quantity, 1e-9)
	assert.InDelta(t, 150.0, pos.AvgCost, 1e-6)
	assert.InDelta(t, 1500.0, pos.CostBasis, 1e-6)
	assert.Empty(t, realized)
}

func TestCalculatePositions_WAC_MultipleBuys(t *testing.T) {
	txs := []model.Transaction{
		buy("NVDA", 4, 100, 400, "2024-01-01"), // avg = 100
		buy("NVDA", 6, 150, 900, "2024-01-10"), // avg = (400+900)/10 = 130
	}
	positions, _ := portfolio.CalculatePositions(txs)

	pos := positions["NVDA"]
	require.NotNil(t, pos)
	assert.InDelta(t, 10.0, pos.Quantity, 1e-9)
	assert.InDelta(t, 130.0, pos.AvgCost, 1e-6)
	assert.InDelta(t, 1300.0, pos.CostBasis, 1e-6)
}

func TestCalculatePositions_PartialSell_RealizedPnL(t *testing.T) {
	txs := []model.Transaction{
		buy("TSM", 10, 100, 1000, "2024-01-01"),
		sell("TSM", 4, 480, "2024-02-01"), // sell 4 at avg cost 100 => cost=400, proceeds=480, pnl=+80
	}
	positions, realized := portfolio.CalculatePositions(txs)

	pos := positions["TSM"]
	require.NotNil(t, pos)
	assert.InDelta(t, 6.0, pos.Quantity, 1e-9)
	assert.InDelta(t, 100.0, pos.AvgCost, 1e-6)

	require.Len(t, realized, 1)
	assert.InDelta(t, 80.0, realized[0].PnL, 1e-6)
}

func TestCalculatePositions_FullSell_PositionRemoved(t *testing.T) {
	txs := []model.Transaction{
		buy("RKLB", 5, 40, 200, "2024-01-01"),
		sell("RKLB", 5, 300, "2024-03-01"),
	}
	positions, realized := portfolio.CalculatePositions(txs)

	assert.NotContains(t, positions, "RKLB", "fully sold position should be removed")
	require.Len(t, realized, 1)
	assert.InDelta(t, 100.0, realized[0].PnL, 1e-6) // 300-200
}

func TestCalculatePositions_FractionalShares(t *testing.T) {
	txs := []model.Transaction{
		buy("MELI", 0.00351, 1994.00, 7.00, "2024-01-01"),
		buy("MELI", 0.0029, 2063.30, 5.98, "2024-01-10"),
		buy("MELI", 0.00288, 2078.89, 5.99, "2024-01-17"),
	}
	positions, _ := portfolio.CalculatePositions(txs)

	pos := positions["MELI"]
	require.NotNil(t, pos)
	expectedQty := 0.00351 + 0.0029 + 0.00288
	assert.InDelta(t, expectedQty, pos.Quantity, 1e-8)
}

func TestCalculatePositions_ChronologicalOrder(t *testing.T) {
	// Out-of-order input — calculator must sort before processing
	txs := []model.Transaction{
		sell("ASTS", 2, 200, "2024-02-01"),
		buy("ASTS", 10, 50, 500, "2024-01-01"),
	}
	positions, realized := portfolio.CalculatePositions(txs)

	pos := positions["ASTS"]
	require.NotNil(t, pos)
	assert.InDelta(t, 8.0, pos.Quantity, 1e-9)
	require.Len(t, realized, 1)
	assert.InDelta(t, 100.0, realized[0].PnL, 1e-6) // 200 - (50*2)
}

func TestAggregateRealizedPnL(t *testing.T) {
	realized := []portfolio.RealizedPnL{
		{Symbol: "AAPL", PnL: 100},
		{Symbol: "AAPL", PnL: -30},
		{Symbol: "NVDA", PnL: 200},
	}
	agg := portfolio.AggregateRealizedPnL(realized)
	assert.InDelta(t, 70.0, agg["AAPL"], 1e-6)
	assert.InDelta(t, 200.0, agg["NVDA"], 1e-6)
}
