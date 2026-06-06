package instrument

import (
	"context"
	"log"
	"time"

	"portfolio-tracker/internal/model"
	"portfolio-tracker/internal/yahoofinance"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const syncTTL = 7 * 24 * time.Hour

// Service syncs and retrieves instrument metadata from the DB,
// using Yahoo Finance as the source of truth.
type Service struct {
	db *gorm.DB
	yf *yahoofinance.CachedClient
}

func NewService(db *gorm.DB, yf *yahoofinance.CachedClient) *Service {
	return &Service{db: db, yf: yf}
}

// EnsureInstruments upserts instrument metadata for any symbols that are
// missing from the DB or whose LastSyncedAt is older than syncTTL.
// Errors are logged and non-fatal — callers receive best-effort data.
func (s *Service) EnsureInstruments(ctx context.Context, symbols []string) {
	if len(symbols) == 0 {
		return
	}

	// Load existing records
	var existing []model.Instrument
	s.db.WithContext(ctx).Where("symbol IN ?", symbols).Find(&existing)

	existingMap := make(map[string]model.Instrument, len(existing))
	for _, inst := range existing {
		existingMap[inst.Symbol] = inst
	}

	staleCutoff := time.Now().Add(-syncTTL)
	var toSync []string
	for _, sym := range symbols {
		inst, found := existingMap[sym]
		if !found || inst.LastSyncedAt.Before(staleCutoff) {
			toSync = append(toSync, sym)
		}
	}

	if len(toSync) == 0 {
		return
	}

	log.Printf("[instrument] syncing %d symbols: %v", len(toSync), toSync)

	var upserts []model.Instrument
	for _, sym := range toSync {
		q, err := s.yf.GetQuote(ctx, sym)
		if err != nil {
			log.Printf("[instrument] GetQuote failed for %s: %v", sym, err)
			continue
		}
		name := q.LongName
		if name == "" {
			name = q.ShortName
		}

		// sector and industry come from quoteSummary assetProfile, not the quote endpoint
		sector := ""
		industry := ""
		if profile, err := s.yf.GetAssetProfile(ctx, sym); err == nil {
			sector = profile.Sector
			industry = profile.Industry
		} else {
			log.Printf("[instrument] GetAssetProfile failed for %s: %v", sym, err)
		}

		upserts = append(upserts, model.Instrument{
			Symbol:       sym,
			CompanyName:  name,
			Sector:       sector,
			Industry:     industry,
			Currency:     q.Currency,
			Exchange:     q.FullExchangeName,
			LastSyncedAt: time.Now(),
		})
	}

	if len(upserts) == 0 {
		return
	}

	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "symbol"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"company_name", "sector", "industry", "currency", "exchange", "last_synced_at",
		}),
	}).Create(&upserts)

	if result.Error != nil {
		log.Printf("[instrument] upsert error: %v", result.Error)
	} else {
		log.Printf("[instrument] upserted %d instruments", len(upserts))
	}
}

// GetBySymbols returns a map of symbol → Instrument for the given symbols.
// Only records already in the DB are returned; call EnsureInstruments first.
func (s *Service) GetBySymbols(ctx context.Context, symbols []string) map[string]model.Instrument {
	result := make(map[string]model.Instrument, len(symbols))
	if len(symbols) == 0 {
		return result
	}
	var rows []model.Instrument
	s.db.WithContext(ctx).Where("symbol IN ?", symbols).Find(&rows)
	for _, r := range rows {
		result[r.Symbol] = r
	}
	return result
}
