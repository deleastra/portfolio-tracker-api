package database

import (
	"portfolio-tracker/internal/model"

	"gorm.io/gorm"
)

func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.User{},
		&model.Portfolio{},
		&model.Transaction{},
		&model.PriceCacheHistory{},
		&model.Instrument{},
	)
}
