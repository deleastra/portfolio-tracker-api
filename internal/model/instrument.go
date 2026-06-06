package model

import "time"

// Instrument stores static/slow-changing metadata for a traded symbol,
// sourced from Yahoo Finance and refreshed periodically.
// Symbol is the natural primary key — no UUID needed.
type Instrument struct {
	Symbol        string    `gorm:"primaryKey" json:"symbol"`
	CompanyName   string    `gorm:"not null" json:"company_name"`
	Sector        string    `json:"sector"`
	Industry      string    `json:"industry"`
	Currency      string    `gorm:"type:varchar(3)" json:"currency"`
	Exchange      string    `gorm:"type:varchar(20)" json:"exchange"`
	LastSyncedAt  time.Time `gorm:"not null" json:"last_synced_at"`
}
