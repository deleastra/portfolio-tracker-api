package portfolio

import (
	"sort"

	"portfolio-tracker/internal/model"
)

// Position represents the current state of a single holding calculated
// purely from transaction history (Weighted Average Cost method).
type Position struct {
	Symbol      string
	CompanyName string
	Quantity    float64
	AvgCost     float64 // weighted average cost per share
	CostBasis   float64 // total cost basis = quantity * avg_cost
}

// RealizedPnL records profit/loss from a completed (partial or full) sell.
type RealizedPnL struct {
	Symbol   string
	Quantity float64
	CostBasis float64
	Proceeds  float64
	PnL       float64
}

// CalculatePositions replays all transactions in chronological order and
// returns current open positions using Weighted Average Cost (WAC).
// Realized P&L per symbol is also returned for closed/partial sells.
func CalculatePositions(txs []model.Transaction) (positions map[string]*Position, realized []RealizedPnL) {
	// Sort by trade date ascending (oldest first)
	sorted := make([]model.Transaction, len(txs))
	copy(sorted, txs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TradeDate.Before(sorted[j].TradeDate)
	})

	positions = make(map[string]*Position)
	var realizedList []RealizedPnL

	for _, tx := range sorted {
		pos, exists := positions[tx.Symbol]
		if !exists {
			pos = &Position{Symbol: tx.Symbol, CompanyName: tx.CompanyName}
			positions[tx.Symbol] = pos
		}

		switch tx.Action {
		case model.ActionBuy:
			// WAC recalculation on each buy
			totalCost := pos.CostBasis + tx.NetAmount
			totalQty := pos.Quantity + tx.Quantity
			if totalQty > 0 {
				pos.AvgCost = totalCost / totalQty
			}
			pos.Quantity = totalQty
			pos.CostBasis = totalCost

		case model.ActionSell:
			costBasisSold := pos.AvgCost * tx.Quantity
			proceeds := tx.NetAmount
			pnl := proceeds - costBasisSold

			realizedList = append(realizedList, RealizedPnL{
				Symbol:    tx.Symbol,
				Quantity:  tx.Quantity,
				CostBasis: costBasisSold,
				Proceeds:  proceeds,
				PnL:       pnl,
			})

			pos.Quantity -= tx.Quantity
			pos.CostBasis = pos.AvgCost * pos.Quantity
			if pos.Quantity <= 0 {
				pos.Quantity = 0
				pos.CostBasis = 0
				pos.AvgCost = 0
			}
		}

		// Keep company name updated (in case different records have different casing)
		if tx.CompanyName != "" {
			pos.CompanyName = tx.CompanyName
		}
	}

	// Remove fully closed positions
	for sym, pos := range positions {
		if pos.Quantity <= 1e-9 {
			delete(positions, sym)
		}
	}

	return positions, realizedList
}

// AggregateRealizedPnL sums realized P&L per symbol.
func AggregateRealizedPnL(realized []RealizedPnL) map[string]float64 {
	agg := make(map[string]float64)
	for _, r := range realized {
		agg[r.Symbol] += r.PnL
	}
	return agg
}
