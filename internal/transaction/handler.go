package transaction

import (
	"net/http"
	"strconv"
	"time"

	"portfolio-tracker/internal/auth"
	"portfolio-tracker/internal/csvparser"
	"portfolio-tracker/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Handler provides HTTP handlers for transaction CRUD + CSV import.
type Handler struct {
	db *gorm.DB
}

func NewHandler(db *gorm.DB) *Handler {
	return &Handler{db: db}
}

// getOrCreatePortfolio returns the user's default portfolio, creating it if needed.
func (h *Handler) getOrCreatePortfolio(userID uuid.UUID) (*model.Portfolio, error) {
	var p model.Portfolio
	err := h.db.Where("user_id = ?", userID).First(&p).Error
	if err == nil {
		return &p, nil
	}
	p = model.Portfolio{UserID: userID, Name: "Default"}
	if err := h.db.Create(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// List returns paginated transactions for the authenticated user.
// Query params: page (default 1), per_page (default 20, max 100), action (BUY|SELL)
func (h *Handler) List(c *gin.Context) {
	userID, ok := auth.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	p, err := h.getOrCreatePortfolio(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "portfolio error"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	// accept both page_size and per_page
	perPageStr := c.Query("page_size")
	if perPageStr == "" {
		perPageStr = c.DefaultQuery("per_page", "20")
	}
	perPage, _ := strconv.Atoi(perPageStr)
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}

	query := h.db.Model(&model.Transaction{}).Where("portfolio_id = ?", p.ID).Order("trade_date DESC")
	if action := c.Query("action"); action == "BUY" || action == "SELL" {
		query = query.Where("action = ?", action)
	}
	if symbol := c.Query("symbol"); symbol != "" {
		query = query.Where("UPPER(symbol) = UPPER(?)", symbol)
	}

	var total int64
	query.Count(&total)

	var txs []model.Transaction
	if err := query.Offset((page - 1) * perPage).Limit(perPage).Find(&txs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      txs,
		"total":     total,
		"page":      page,
		"page_size": perPage,
	})
}

type createRequest struct {
	Symbol      string  `json:"symbol" binding:"required"`
	CompanyName string  `json:"company_name"`
	TradeDate   string  `json:"trade_date" binding:"required"` // YYYY-MM-DD
	Action      string  `json:"action" binding:"required,oneof=BUY SELL"`
	Quantity    float64 `json:"quantity" binding:"required,gt=0"`
	TradedPrice float64 `json:"traded_price" binding:"required,gt=0"`
	Commission  float64 `json:"commission"`
	VAT         float64 `json:"vat"`
}

// Create adds a single transaction manually.
func (h *Handler) Create(c *gin.Context) {
	userID, ok := auth.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tradeDate, err := time.Parse("2006-01-02", req.TradeDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "trade_date must be YYYY-MM-DD"})
		return
	}

	p, err := h.getOrCreatePortfolio(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "portfolio error"})
		return
	}

	gross := req.Quantity * req.TradedPrice
	net := gross - req.Commission - req.VAT

	now := time.Now()
	tx := model.Transaction{
		PortfolioID:    p.ID,
		Symbol:         req.Symbol,
		CompanyName:    req.CompanyName,
		TradeDate:      tradeDate,
		SettlementDate: tradeDate.AddDate(0, 0, 2),
		Action:         model.TransactionAction(req.Action),
		Quantity:       req.Quantity,
		TradedPrice:    req.TradedPrice,
		GrossAmount:    gross,
		Commission:     req.Commission,
		VAT:            req.VAT,
		NetAmount:      net,
		Currency:       "USD",
		ImportedAt:     &now,
	}

	if err := h.db.Create(&tx).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}

	c.JSON(http.StatusCreated, tx)
}

// Delete soft-deletes a transaction owned by the authenticated user.
func (h *Handler) Delete(c *gin.Context) {
	userID, ok := auth.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid transaction id"})
		return
	}

	p, err := h.getOrCreatePortfolio(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "portfolio error"})
		return
	}

	result := h.db.Where("id = ? AND portfolio_id = ?", id, p.ID).Delete(&model.Transaction{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "transaction not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// ImportCSV parses a multipart/form-data CSV file and bulk-inserts transactions.
func (h *Handler) ImportCSV(c *gin.Context) {
	userID, ok := auth.GetCurrentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	p, err := h.getOrCreatePortfolio(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "portfolio error"})
		return
	}

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file field is required"})
		return
	}
	defer file.Close()

	txs, err := csvparser.Parse(file, p.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now()
	for i := range txs {
		txs[i].ImportedAt = &now
	}

	if err := h.db.Create(&txs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "import failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"imported": len(txs)})
}
