package main

import (
	"context"
	"log"
	"time"

	"portfolio-tracker/internal/analytics"
	"portfolio-tracker/internal/auth"
	"portfolio-tracker/internal/config"
	"portfolio-tracker/internal/database"
	"portfolio-tracker/internal/instrument"
	"portfolio-tracker/internal/portfolio"
	"portfolio-tracker/internal/transaction"
	"portfolio-tracker/internal/yahoofinance"

	"github.com/gin-gonic/gin"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	db, err := database.NewPostgres(cfg)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}

	rdb, err := database.NewRedis(cfg)
	if err != nil {
		log.Fatalf("failed to connect to redis: %v", err)
	}

	if err := database.Migrate(db); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	yfClient := yahoofinance.NewCachedClient(cfg.YahooFinanceBaseURL, rdb, cfg.YahooPriceCacheTTL)

	// Invalidate stale price cache on startup so first requests fetch fresh prices
	if err := yfClient.InvalidateAll(context.Background()); err != nil {
		log.Printf("warning: failed to invalidate price cache on startup: %v", err)
	} else {
		log.Println("price cache invalidated on startup")
	}

	authSvc := auth.NewService(db, rdb, cfg.JWTSecret, cfg.JWTAccessExpiry, cfg.JWTRefreshExpiry)
	authHandler := auth.NewHandler(authSvc, cfg)
	txHandler := transaction.NewHandler(db)
	instrumentSvc := instrument.NewService(db, yfClient)
	portfolioHandler := portfolio.NewHandler(db, yfClient, instrumentSvc)
	analyticsHandler := analytics.NewHandler(db, yfClient, instrumentSvc)

	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	{
		authGroup := api.Group("/auth")
		{
			authGroup.POST("/register", auth.NewRateLimitMiddleware(rdb, 3, time.Minute), authHandler.Register)
			authGroup.POST("/login", auth.NewRateLimitMiddleware(rdb, 5, time.Minute), authHandler.Login)
			authGroup.POST("/refresh", authHandler.Refresh)
			authGroup.POST("/logout", authHandler.Logout)
		}

		protected := api.Group("/")
		protected.Use(auth.JWTMiddleware(authSvc))
		{
			protected.GET("/portfolio/summary", portfolioHandler.Summary)
			protected.GET("/portfolio/export/csv", portfolioHandler.ExportCSV)
			protected.GET("/portfolio/quote/:symbol", portfolioHandler.Quote)

			protected.GET("/transactions", txHandler.List)
			protected.POST("/transactions", txHandler.Create)
			protected.PUT("/transactions/:id", txHandler.Update)
			protected.DELETE("/transactions/:id", txHandler.Delete)
			protected.POST("/transactions/import", txHandler.ImportCSV)

			protected.GET("/analytics/pnl", analyticsHandler.PnL)
			protected.GET("/analytics/performance", analyticsHandler.Performance)
			protected.GET("/analytics/metrics", analyticsHandler.Metrics)
		}
	}

	log.Printf("starting server on :%s (env=%s)", cfg.AppPort, cfg.AppEnv)
	if err := r.Run(":" + cfg.AppPort); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
