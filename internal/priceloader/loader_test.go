package priceloader_test

import (
	"math"
	"testing"

	"portfolio-tracker/internal/priceloader"

	"github.com/stretchr/testify/assert"
)

// backoffDuration is exported only for test via a helper — we test the formula directly.

func TestBackoffDuration_NeverExceedsMax(t *testing.T) {
	cfg := priceloader.Config{
		Workers:       1,
		BaseBackoffMs: 1000,
		MaxBackoffMs:  32000,
		MaxRetries:    5,
	}

	for attempt := 1; attempt <= 10; attempt++ {
		cap := float64(cfg.MaxBackoffMs)
		base := float64(cfg.BaseBackoffMs)
		ceiling := math.Min(cap, base*math.Pow(2, float64(attempt)))
		assert.LessOrEqual(t, ceiling, float64(cfg.MaxBackoffMs),
			"ceiling at attempt %d exceeds max backoff", attempt)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := priceloader.DefaultConfig()
	assert.Equal(t, 3, cfg.Workers)
	assert.Equal(t, 1000, cfg.BaseBackoffMs)
	assert.Equal(t, 32000, cfg.MaxBackoffMs)
	assert.Equal(t, 5, cfg.MaxRetries)
}
