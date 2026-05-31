package database

import (
	"context"
	"portfolio-tracker/internal/config"

	"github.com/redis/go-redis/v9"
)

func NewRedis(cfg *config.Config) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr(),
		Password: cfg.RedisPassword,
		DB:       0,
	})

	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}
	return rdb, nil
}
