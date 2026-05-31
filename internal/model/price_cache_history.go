package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PriceCacheHistory stores daily closing prices fetched from Yahoo Finance.
// Used for analytics (portfolio NAV time series, benchmark comparison, metrics).
type PriceCacheHistory struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Symbol     string    `gorm:"not null;index:idx_symbol_date,unique" json:"symbol"`
	Date       time.Time `gorm:"type:date;not null;index:idx_symbol_date,unique" json:"date"`
	ClosePrice float64   `gorm:"type:numeric(18,4);not null" json:"close_price"`
	FetchedAt  time.Time `json:"fetched_at"`
}

func (p *PriceCacheHistory) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}
