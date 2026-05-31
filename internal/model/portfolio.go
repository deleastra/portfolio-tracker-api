package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Portfolio struct {
	ID           uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	UserID       uuid.UUID      `gorm:"type:uuid;not null;index" json:"user_id"`
	Name         string         `gorm:"not null" json:"name"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
	Transactions []Transaction  `gorm:"foreignKey:PortfolioID" json:"transactions,omitempty"`
}

func (p *Portfolio) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}
