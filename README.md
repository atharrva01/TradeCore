# TradeCore

TradeCore is a browser-based simulated exchange built around a Go price-time priority matching engine. Prices and balances are integer paise; the React client uses a guest session and polls the REST API for current market data.

## Run locally

Requirements: Go 1.23+ and Node.js 20+.

Start the API:

```sh
go run ./cmd/tradecore
```

Start the UI in a second terminal:

```sh
npm install
npm run dev
```

Open the Vite URL (normally `http://localhost:5173`). The API listens on `http://localhost:8080`.

| Variable | Purpose |
| --- | --- |
| `ADDR` / `PORT` | Listen address (`ADDR` wins; `PORT` is what Render and similar hosts set) |
| `APP_ENV=production` | Requires `JWT_SECRET` and `FRONTEND_ORIGIN`, and marks cookies `Secure` |
| `JWT_SECRET` | HS256 signing key, at least 32 bytes. Unset in development gives an ephemeral key |
| `FRONTEND_ORIGIN` | Comma-separated CORS allowlist. Unset in development allows only localhost origins |
| `COOKIE_SAMESITE=none` | Needed when the UI and API are on different sites; forces `Secure` cookies |
| `TRUST_PROXY=true` | Use the proxy-appended `X-Forwarded-For` hop as the client IP for rate limiting |
| `VITE_API_URL` | Build-time API base URL for the UI (default `http://localhost:8080/api/v1`) |

Run checks:

```sh
go test ./...
go vet ./...
npm run build
```

## Current MVP

- Five fictional instruments, each with seeded two-sided market-maker liquidity and a background noise trader that sends small orders through the same matching books.
- Limit and market orders, price-time priority, partial fills, maker-price execution, FIFO at a price, self-trade prevention, and owner-checked cancellation. Price levels are indexed with B-trees and orders at each level use linked FIFO queues.
- Cash and share reservation, paise-based settlement, simple average-cost portfolio tracking, and guest sessions with an initial ₹10,00,000. Human and bot orders share one validation, reservation and settlement path.
- Market orders never rest: a market buy spends at most the buyer's available cash, and any unfilled remainder ends as `CANCELLED` with its `filledQty` reported.
- Email/password signup and login, 15-minute JWT access tokens, rotating refresh tokens in an httpOnly cookie, and per-IP rate limits on account creation and login. Expired guests are purged with their resting orders.
- REST endpoints for sessions, instruments, order books, recent trades, orders, cancellations, portfolios, and a leaderboard.
- Responsive dark trading screen with live polling, market depth, recent trade prices, order entry, open orders, order history, and positions.

### Local engine benchmark

`go test ./internal/engine -run '^$' -bench BenchmarkMatchOneOrder -benchtime=3s` measured approximately 1.22 million single-order matches/second (about 817 ns/op) on an Intel Core i7-12650H in this environment. This is an engine-only benchmark, not an API or end-to-end latency result; run it on the target deployment before using a performance claim.

## API

All routes are under `/api/v1`. Authenticated requests use `Authorization: Bearer <accessToken>`.

| Method | Route | Purpose |
| --- | --- | --- |
| POST | `/auth/guest` | Start a 24-hour guest session |
| POST | `/auth/signup` | Create an email/password account |
| POST | `/auth/login` | Log in with email and password |
| POST | `/auth/refresh` | Exchange the refresh cookie for a new access token |
| POST | `/auth/logout` | Revoke the refresh cookie |
| GET | `/health` | Liveness check |
| GET | `/instruments` | List instruments and current price |
| GET | `/instruments/{symbol}/book` | Top 10 bid and ask levels |
| GET | `/instruments/{symbol}/trades?limit=50` | Recent public trades |
| POST | `/orders` | Place a limit or market order |
| GET | `/orders` | Current user's orders |
| DELETE | `/orders/{id}` | Cancel an open order |
| GET | `/trades/me` | Current user's recent trades |
| GET | `/portfolio` | Cash, holdings and P&L |
| GET | `/leaderboard` | Rank guest users by marked portfolio value |

Money and price fields in API payloads are integer paise. For example, ₹101.50 is `10150`.

## Scope and next steps

This is a runnable MVP foundation, not yet the complete production PRD. State is process-local and resets when the Go service restarts, including all accounts and refresh sessions; the UI only offers guest sessions; the background noise trader uses a simple fixed-size strategy; and the client polls instead of using WebSockets. Durable PostgreSQL storage and replay, OHLCV candles, more realistic/admin-tunable bots, admin controls, metrics, and deployment automation remain to be implemented before a public launch.
