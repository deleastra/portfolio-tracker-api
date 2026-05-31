package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type TransactionAction string

const (
	ActionBuy  TransactionAction = "BUY"
	ActionSell TransactionAction = "SELL"
)

// Transaction represents a single stock trade imported from CSV or entered manually.
// Cost basis is always computed from transactions — never from imported summary data.
type Transaction struct {
	ID             uuid.UUID         `gorm:"type:uuid;primaryKey" json:"id"`
	PortfolioID    uuid.UUID         `gorm:"type:uuid;not null;index" json:"portfolio_id"`
	Symbol         string            `gorm:"not null;index" json:"symbol"`
	CompanyName    string            `json:"company_name"`
	TradeDate      time.Time         `gorm:"not null;index" json:"trade_date"`
	SettlementDate time.Time         `json:"settlement_date"`
	Action         TransactionAction `gorm:"type:varchar(4);not null" json:"action"`
	Quantity       float64           `gorm:"type:numeric(18,8);not null" json:"quantity"`
	TradedPrice    float64           `gorm:"type:numeric(18,4);not null" json:"traded_price"`
	GrossAmount    float64           `gorm:"type:numeric(18,4);not null" json:"gross_amount"`
	Commission     float64           `gorm:"type:numeric(18,4)" json:"commission"`
	VAT            float64           `gorm:"type:numeric(18,4)" json:"vat"`
	NetAmount      float64           `gorm:"type:numeric(18,4);not null" json:"net_amount"`
	Currency       string            `gorm:"type:varchar(3);default:'USD'" json:"currency"`
	ImportedAt     *time.Time        `json:"imported_at,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	DeletedAt      gorm.DeletedAt    `gorm:"index" json:"-"`
}

func (t *Transaction) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}
