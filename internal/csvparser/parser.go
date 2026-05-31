package csvparser

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"portfolio-tracker/internal/model"

	"github.com/google/uuid"
)

var ErrNoTradeRecords = errors.New("no trade records found in CSV")

// Parse reads a monthly statement CSV (as produced by the broker export)
// and returns the list of stock transactions found in the TRADE RECORDS section.
// The PORTFOLIO SUMMARY section is intentionally ignored — cost basis is always
// recomputed from raw transactions.
func Parse(r io.Reader, portfolioID uuid.UUID) ([]model.Transaction, error) {
	scanner := bufio.NewScanner(r)

	// Skip forward to the Stocks section inside TRADE RECORDS
	inTradeRecords := false
	inStocksSection := false
	headerSkipped := false

	var csvLines []string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.Contains(line, "TRADE RECORDS") {
			inTradeRecords = true
			continue
		}
		if strings.Contains(line, "PORTFOLIO SUMMARY") {
			// Stop collecting — we do not import summary data
			break
		}
		if strings.Contains(line, "Options") || strings.Contains(line, "Portfolio Advisory") {
			// Sub-sections we skip
			inStocksSection = false
			continue
		}
		if inTradeRecords && line == "Stocks" {
			inStocksSection = true
			continue
		}
		if !inStocksSection {
			continue
		}
		if line == "" {
			continue
		}
		// Skip metadata lines like "Currency: USD"
		if strings.HasPrefix(line, "Currency:") {
			continue
		}
		// First non-empty, non-metadata line is the header row
		if !headerSkipped {
			headerSkipped = true
			csvLines = append(csvLines, line) // keep header for csv.Reader
			continue
		}
		csvLines = append(csvLines, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %w", err)
	}

	if len(csvLines) <= 1 {
		return nil, ErrNoTradeRecords
	}

	reader := csv.NewReader(strings.NewReader(strings.Join(csvLines, "\n")))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1 // allow variable field count

	// skip header
	if _, err := reader.Read(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var transactions []model.Transaction

	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("csv read error: %w", err)
		}
		if len(record) < 10 {
			continue
		}

		tx, err := parseRecord(record, portfolioID, now)
		if err != nil {
			// skip malformed rows but don't abort
			continue
		}
		transactions = append(transactions, tx)
	}

	if len(transactions) == 0 {
		return nil, ErrNoTradeRecords
	}

	return transactions, nil
}

// parseRecord maps a single CSV row to a Transaction.
// Expected columns (0-indexed):
//   0: Symbol & Name   e.g. "META META PLATFORMS INC"
//   1: Trade Date      DD/MM/YYYY
//   2: Settlement Date DD/MM/YYYY
//   3: Buy/Sell
//   4: Quantity
//   5: Traded Price
//   6: Gross Amount
//   7: Comm/Fee/Tax
//   8: VAT
//   9: Net Amount
func parseRecord(record []string, portfolioID uuid.UUID, importedAt time.Time) (model.Transaction, error) {
	symbolFull := strings.TrimSpace(record[0])
	symbol, companyName := splitSymbol(symbolFull)

	tradeDate, err := parseDate(strings.TrimSpace(record[1]))
	if err != nil {
		return model.Transaction{}, fmt.Errorf("invalid trade date %q: %w", record[1], err)
	}

	settlementDate, err := parseDate(strings.TrimSpace(record[2]))
	if err != nil {
		// Settlement date is not critical — default to trade date
		settlementDate = tradeDate
	}

	actionStr := strings.ToUpper(strings.TrimSpace(record[3]))
	var action model.TransactionAction
	switch actionStr {
	case "BUY":
		action = model.ActionBuy
	case "SELL":
		action = model.ActionSell
	default:
		return model.Transaction{}, fmt.Errorf("unknown action %q", actionStr)
	}

	qty, err := parseFloat(record[4])
	if err != nil {
		return model.Transaction{}, fmt.Errorf("invalid quantity: %w", err)
	}
	price, err := parseFloat(record[5])
	if err != nil {
		return model.Transaction{}, fmt.Errorf("invalid traded price: %w", err)
	}
	gross, err := parseFloat(record[6])
	if err != nil {
		return model.Transaction{}, fmt.Errorf("invalid gross amount: %w", err)
	}
	comm, _ := parseFloat(record[7])   // negative values are rebates — keep as-is
	vat, _ := parseFloat(record[8])
	net, err := parseFloat(record[9])
	if err != nil {
		return model.Transaction{}, fmt.Errorf("invalid net amount: %w", err)
	}

	return model.Transaction{
		PortfolioID:    portfolioID,
		Symbol:         symbol,
		CompanyName:    companyName,
		TradeDate:      tradeDate,
		SettlementDate: settlementDate,
		Action:         action,
		Quantity:       qty,
		TradedPrice:    price,
		GrossAmount:    gross,
		Commission:     comm,
		VAT:            vat,
		NetAmount:      net,
		Currency:       "USD",
		ImportedAt:     &importedAt,
	}, nil
}

// splitSymbol separates "META META PLATFORMS INC" into ("META", "META PLATFORMS INC").
// Handles class-share tickers like "BRK B BERKSHIRE HATHAWAY INC DEL" → ("BRK-B", "BERKSHIRE HATHAWAY INC DEL").
func splitSymbol(s string) (symbol, name string) {
	parts := strings.SplitN(s, " ", 2)
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0]), ""
	}
	sym := strings.TrimSpace(parts[0])
	rest := strings.TrimSpace(parts[1])

	// If the next token is a single uppercase letter (class suffix e.g. "B", "A", "C"),
	// attach it to the symbol with a dash: "BRK B ..." → "BRK-B"
	restParts := strings.SplitN(rest, " ", 2)
	if len(restParts[0]) == 1 && restParts[0] == strings.ToUpper(restParts[0]) && restParts[0] != "" {
		sym = sym + "-" + restParts[0]
		if len(restParts) > 1 {
			rest = strings.TrimSpace(restParts[1])
		} else {
			rest = ""
		}
	}

	return sym, rest
}

func parseDate(s string) (time.Time, error) {
	return time.Parse("02/01/2006", s)
}

func parseFloat(s string) (float64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	return strconv.ParseFloat(s, 64)
}
