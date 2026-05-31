package csvparser_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"portfolio-tracker/internal/csvparser"
	"portfolio-tracker/internal/model"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testPortfolioID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	path := filepath.Join("..", "..", "example-data", name)
	f, err := os.Open(path)
	require.NoError(t, err, "fixture not found: %s", path)
	t.Cleanup(func() { f.Close() })
	return f
}

func TestParse_November2025(t *testing.T) {
	f := openFixture(t, "2025-11.csv")
	txs, err := csvparser.Parse(f, testPortfolioID)
	require.NoError(t, err)

	// 24 trade records in the file (verified manually)
	assert.NotEmpty(t, txs)
	assert.GreaterOrEqual(t, len(txs), 20)

	// Spot-check first SELL: META on 26/11/2025
	var meta *model.Transaction
	for i := range txs {
		if txs[i].Symbol == "META" && txs[i].Action == model.ActionSell {
			meta = &txs[i]
			break
		}
	}
	require.NotNil(t, meta, "expected META SELL transaction")
	assert.Equal(t, model.ActionSell, meta.Action)
	assert.InDelta(t, 0.05644, meta.Quantity, 1e-5)
	assert.InDelta(t, 633.96, meta.TradedPrice, 1e-2)
	assert.InDelta(t, 35.73, meta.NetAmount, 1e-2)
	assert.Equal(t, "USD", meta.Currency)
	assert.Equal(t, testPortfolioID, meta.PortfolioID)
	assert.NotNil(t, meta.ImportedAt)
}

func TestParse_AllFiles_NoError(t *testing.T) {
	files := []string{
		"2025-11.csv",
		"2025-12.csv",
		"2026-01.csv",
		"2026-02.csv",
		"2026-03.csv",
		"2026-04.csv",
	}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			f := openFixture(t, name)
			txs, err := csvparser.Parse(f, testPortfolioID)
			require.NoError(t, err, "parse failed for %s", name)
			assert.NotEmpty(t, txs, "no transactions parsed from %s", name)
		})
	}
}

func TestParse_PortfolioSummaryIgnored(t *testing.T) {
	// 2026-03.csv and 2026-04.csv contain PORTFOLIO SUMMARY sections.
	// Those rows must NOT appear as transactions.
	f := openFixture(t, "2026-03.csv")
	txs, err := csvparser.Parse(f, testPortfolioID)
	require.NoError(t, err)

	for _, tx := range txs {
		// Summary rows have no valid BUY/SELL action and would fail parse
		assert.True(t, tx.Action == model.ActionBuy || tx.Action == model.ActionSell,
			"unexpected action %q in tx %+v", tx.Action, tx)
		assert.NotEmpty(t, tx.Symbol)
		assert.False(t, tx.TradeDate.IsZero())
	}
}

func TestParse_SplitSymbol(t *testing.T) {
	f := openFixture(t, "2025-11.csv")
	txs, err := csvparser.Parse(f, testPortfolioID)
	require.NoError(t, err)

	for _, tx := range txs {
		assert.NotEmpty(t, tx.Symbol, "symbol must not be empty")
		assert.NotContains(t, tx.Symbol, " ", "symbol must not contain spaces")
	}
}

func TestParse_EmptyCSV(t *testing.T) {
	r := require.New(t)
	_, err := csvparser.Parse(strings.NewReader(""), testPortfolioID)
	r.ErrorIs(err, csvparser.ErrNoTradeRecords)
}
