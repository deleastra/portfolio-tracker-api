# Portfolio Tracker — Backend

REST API สำหรับ stock portfolio tracker พัฒนาด้วย Go โดยใช้หลักการ TDD

## Tech Stack

| Layer | Technology |
|---|---|
| Language | Go 1.26 |
| Web Framework | [Gin](https://github.com/gin-gonic/gin) |
| ORM | [GORM](https://gorm.io) + pgx (PostgreSQL driver) |
| Database | PostgreSQL 16 |
| Cache | Redis 7 |
| Auth | JWT (golang-jwt/jwt v5) + bcrypt |
| Market Data | Yahoo Finance v8 API |
| Container | Podman (podman-compose) |
| Testing | testify + testcontainers-go |

## Project Structure

```
portfolio-go-transtack-backend/
├── cmd/server/              # Entry point (main.go + router)
├── internal/
│   ├── analytics/           # Performance metrics (alpha, beta, Sharpe, Sortino, max drawdown ฯลฯ)
│   ├── auth/                # Auth service, handlers, JWT middleware
│   ├── config/              # Environment config loader
│   ├── csvparser/           # Monthly Statement CSV parser
│   ├── database/            # GORM init + auto-migration
│   ├── model/               # GORM models (User, Portfolio, Transaction, PriceCacheHistory)
│   ├── portfolio/           # WAC cost basis calculator + P&L engine
│   ├── priceloader/         # Batch price fetcher with exponential backoff queue
│   └── yahoofinance/        # Yahoo Finance HTTP client + Redis cache
├── example-data/            # ตัวอย่าง CSV format (Monthly Statement)
├── podman-compose.yml       # PostgreSQL 16 + Redis 7
├── .env.example
└── go.mod
```

## Getting Started

### Prerequisites

- Go 1.21+
- [Podman](https://podman.io/) + podman-compose

### 1. Start infrastructure

```bash
podman compose -f podman-compose.yml up -d
```

รอจน postgres และ redis พร้อม (health check ผ่าน)

### 2. Configure environment

```bash
cp .env.example .env
# แก้ไข JWT_SECRET ให้เป็น random string ที่ยาวพอ
```

### 3. Run the server

```bash
go run ./cmd/server
```

Server จะ start ที่ `http://localhost:8080` และ run auto-migration อัตโนมัติเมื่อ startup

### 4. Run tests

```bash
# Unit tests (ไม่ต้องการ Docker)
go test ./internal/auth/... ./internal/csvparser/... ./internal/portfolio/... ./internal/analytics/...

# Integration tests (ต้องการ Docker/Podman daemon)
go test ./...
```

## API Endpoints

### Auth

| Method | Path | Description |
|---|---|---|
| POST | `/api/auth/register` | สร้าง account ใหม่ |
| POST | `/api/auth/login` | รับ access + refresh token |
| POST | `/api/auth/refresh` | ต่ออายุ access token |

### Portfolio

| Method | Path | Description |
|---|---|---|
| GET | `/api/portfolio/summary` | Positions + unrealized P&L ทั้งหมด |

### Transactions

| Method | Path | Description |
|---|---|---|
| GET | `/api/transactions` | รายการ transactions (paginated) |
| POST | `/api/transactions` | เพิ่ม transaction ด้วยตนเอง |
| DELETE | `/api/transactions/:id` | ลบ transaction |
| POST | `/api/transactions/import` | Import CSV (Monthly Statement format) |

### Analytics

| Method | Path | Description |
|---|---|---|
| GET | `/api/analytics/pnl` | Realized + unrealized P&L แยกตาม symbol |
| GET | `/api/analytics/performance` | NAV time-series เทียบ benchmark |
| GET | `/api/analytics/metrics` | Performance metrics ครบชุด |

#### Query parameters — `/api/analytics/performance`

| Parameter | Example | Description |
|---|---|---|
| `from` | `2025-11-01` | วันเริ่มต้น (YYYY-MM-DD) |
| `to` | `2026-05-30` | วันสิ้นสุด |
| `benchmark` | `SPY`, `^GSPC`, `^IXIC`, `^NDX` | Benchmark ที่ใช้เปรียบเทียบ |

#### Query parameters — `/api/analytics/metrics`

เหมือนกับ `/performance` + คำนวณ metrics ดังนี้:

| Metric | Description |
|---|---|
| Alpha | Excess return เทียบ CAPM |
| Beta | Sensitivity ต่อ benchmark |
| Sharpe Ratio | (Rp − Rf) / σp |
| Sortino Ratio | (Rp − Rf) / downside deviation |
| Max Drawdown | Peak-to-trough decline |
| Calmar Ratio | CAGR / Max Drawdown |
| Information Ratio | Active return / Tracking Error |
| Treynor Ratio | (Rp − Rf) / Beta |
| Tracking Error | σ ของ (portfolio − benchmark) returns |
| Win Rate | % ของ winning trades |
| Profit Factor | Gross profit / Gross loss |

## CSV Import Format

รองรับ Monthly Statement format จากโบรกเกอร์:

```
"                                                    Monthly Statement 2025-11"
"TRADE RECORDS "
Stocks
Currency: USD
Symbol & Name,Trade Date,Settlement Date,Buy/Sell,Quantity,Traded Price,Gross Amount,Comm/Fee/Tax,VAT,Net Amount
META META PLATFORMS INC,26/11/2025,28/11/2025,SELL,0.05644,633.96,35.78,-0.05,0.00,35.73
AMZN AMAZON COM INC,24/11/2025,25/11/2025,BUY,0.02211,226.08,5.00,0.00,0.00,5.00
...
```

- **Symbol format**: `TICKER COMPANY NAME` (เช่น `META META PLATFORMS INC`)
- **Date format**: `DD/MM/YYYY`
- **Currency**: USD
- ไฟล์อาจมี `PORTFOLIO SUMMARY` section ท้ายไฟล์ (parser จะข้ามไป — cost basis คำนวณจาก transactions เสมอ)

## Cost Basis Methodology

ใช้ **Weighted Average Cost (WAC)** คำนวณจาก transactions ทั้งหมดตั้งแต่ต้น:

- **Realized P&L**: `(sell_price − avg_cost_at_time) × qty − fees`
- **Unrealized P&L**: `(current_price − avg_cost) × current_qty`
- Portfolio Summary ใน CSV ใช้เป็น reference เท่านั้น ไม่ได้นำมา seed cost basis

## Price Loader — Exponential Backoff Queue

`internal/priceloader` ใช้สำหรับ batch historical price fetch:

- **Workers**: 3 (configurable via `PRICE_LOADER_WORKERS`)
- **Retry strategy**: exponential backoff base 1s → max 32s + jitter
- **ป้องกัน**: Yahoo Finance rate limit สำหรับการดึงข้อมูล historical จำนวนมาก

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `APP_PORT` | `8080` | Server port |
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `portfolio` | PostgreSQL user |
| `DB_PASSWORD` | `portfolio` | PostgreSQL password |
| `DB_NAME` | `portfolio` | PostgreSQL database |
| `REDIS_HOST` | `localhost` | Redis host |
| `REDIS_PORT` | `6379` | Redis port |
| `JWT_SECRET` | — | **ต้องตั้ง** — random string ยาว ≥ 32 chars |
| `JWT_ACCESS_EXPIRY` | `15m` | Access token TTL |
| `JWT_REFRESH_EXPIRY` | `168h` | Refresh token TTL (7 days) |
| `YAHOO_PRICE_CACHE_TTL` | `15m` | Redis cache TTL สำหรับ current price |
| `PRICE_LOADER_WORKERS` | `3` | Concurrent workers สำหรับ batch fetch |
| `PRICE_LOADER_BASE_BACKOFF_MS` | `1000` | Base backoff (ms) |
| `PRICE_LOADER_MAX_BACKOFF_MS` | `32000` | Max backoff (ms) |
