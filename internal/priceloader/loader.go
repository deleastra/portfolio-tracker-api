package priceloader

import (
	"context"
	"log"
	"math"
	"math/rand"
	"sync"
	"time"

	"portfolio-tracker/internal/model"
	"portfolio-tracker/internal/yahoofinance"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Job is a request to fetch and store historical prices for a symbol over a date range.
type Job struct {
	Symbol string
	From   time.Time
	To     time.Time
}

// Config controls the loader's concurrency and retry behaviour.
type Config struct {
	Workers        int
	BaseBackoffMs  int
	MaxBackoffMs   int
	MaxRetries     int
}

func DefaultConfig() Config {
	return Config{
		Workers:       3,
		BaseBackoffMs: 1000,
		MaxBackoffMs:  32000,
		MaxRetries:    5,
	}
}

// Loader fetches historical prices in batches with exponential backoff + jitter
// to avoid hitting Yahoo Finance rate limits.
type Loader struct {
	cfg    Config
	yf     *yahoofinance.CachedClient
	db     *gorm.DB
	jobs   chan Job
	wg     sync.WaitGroup
}

func New(cfg Config, yf *yahoofinance.CachedClient, db *gorm.DB) *Loader {
	return &Loader{
		cfg:  cfg,
		yf:   yf,
		db:   db,
		jobs: make(chan Job, 256),
	}
}

// Start launches the worker pool. Cancel ctx to shut down.
func (l *Loader) Start(ctx context.Context) {
	for i := 0; i < l.cfg.Workers; i++ {
		l.wg.Add(1)
		go l.worker(ctx)
	}
}

// Wait blocks until all workers have finished.
func (l *Loader) Wait() {
	l.wg.Wait()
}

// Enqueue adds a job to the queue. Returns false if the queue is full.
func (l *Loader) Enqueue(j Job) bool {
	select {
	case l.jobs <- j:
		return true
	default:
		return false
	}
}

// Close signals workers to drain and stop.
func (l *Loader) Close() {
	close(l.jobs)
}

func (l *Loader) worker(ctx context.Context) {
	defer l.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-l.jobs:
			if !ok {
				return
			}
			l.processWithRetry(ctx, job)
		}
	}
}

func (l *Loader) processWithRetry(ctx context.Context, job Job) {
	var lastErr error
	for attempt := 0; attempt <= l.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := l.backoffDuration(attempt)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}

		bars, err := l.yf.GetHistorical(ctx, job.Symbol, job.From, job.To)
		if err != nil {
			lastErr = err
			log.Printf("[priceloader] attempt %d/%d failed for %s: %v",
				attempt+1, l.cfg.MaxRetries+1, job.Symbol, err)
			continue
		}

		if err := l.upsertBars(job.Symbol, bars); err != nil {
			lastErr = err
			log.Printf("[priceloader] db upsert failed for %s: %v", job.Symbol, err)
			continue
		}

		return // success
	}

	log.Printf("[priceloader] giving up on %s after %d attempts: %v",
		job.Symbol, l.cfg.MaxRetries+1, lastErr)
}

// backoffDuration calculates exponential backoff with full jitter.
// duration = rand(0, min(maxBackoff, base * 2^attempt))
func (l *Loader) backoffDuration(attempt int) time.Duration {
	cap := float64(l.cfg.MaxBackoffMs)
	base := float64(l.cfg.BaseBackoffMs)
	ceiling := math.Min(cap, base*math.Pow(2, float64(attempt)))
	jitter := rand.Float64() * ceiling
	return time.Duration(jitter) * time.Millisecond
}

func (l *Loader) upsertBars(symbol string, bars []yahoofinance.HistoricalBar) error {
	if len(bars) == 0 {
		return nil
	}

	records := make([]model.PriceCacheHistory, 0, len(bars))
	now := time.Now().UTC()
	for _, b := range bars {
		records = append(records, model.PriceCacheHistory{
			Symbol:     symbol,
			Date:       b.Date,
			ClosePrice: b.ClosePrice,
			FetchedAt:  now,
		})
	}

	// Upsert: on conflict (symbol, date) update close_price and fetched_at
	return l.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "symbol"}, {Name: "date"}},
		DoUpdates: clause.AssignmentColumns([]string{"close_price", "fetched_at"}),
	}).CreateInBatches(records, 100).Error
}
