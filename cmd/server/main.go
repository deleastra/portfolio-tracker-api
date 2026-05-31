package main

import (
	"log"

	"portfolio-tracker/internal/analytics"
	"portfolio-tracker/internal/auth"
	"portfolio-tracker/internal/config"
	"portfolio-tracker/internal/database"
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

	authSvc := auth.NewService(db, cfg.JWTSecret, cfg.JWTAccessExpiry, cfg.JWTRefreshExpiry)
	authHandler := auth.NewHandler(authSvc)
	txHandler := transaction.NewHandler(db)
	portfolioHandler := portfolio.NewHandler(db, yfClient)
	analyticsHandler := analytics.NewHandler(db, yfClient)

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
			authGroup.POST("/register", authHandler.Register)
			authGroup.POST("/login", authHandler.Login)
			authGroup.POST("/refresh", authHandler.Refresh)
		}

		protected := api.Group("/")
		protected.Use(auth.JWTMiddleware(authSvc))
		{
			protected.GET("/portfolio/summary", portfolioHandler.Summary)

			protected.GET("/transactions", txHandler.List)
			protected.POST("/transactions", txHandler.Create)
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
