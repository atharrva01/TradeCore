# TradeCore

A browser-based simulated stock exchange. Fictional stocks trade against each other's orders and against bots, matched by a price-time priority engine written in Go. Prices are set only by orders placed here, so this is an exchange, not a paper-trading wrapper around real market data.

One Go binary serves the API, the WebSocket feed, and the embedded React UI. Postgres is the durable record.

## Measured performance

| Metric | Result | PRD target |
| --- | --- | --- |
| Engine throughput, deep book (10,000 resting orders, mixed add/match/cancel), one core | ≈ 1.2–1.4 million ops/s (700–800 ns/op) | ≥ 50,000 orders/s |
| Engine latency per operation, same benchmark | p50 ≈ 0.45 µs, p99 ≈ 4 µs | p99 < 1 ms |
| Order placement, end to end through HTTP and a Postgres commit, at 500 orders/s with 200 WebSocket clients | p95 24 ms | p95 < 50 ms |
| Order placed → fill event received over WebSocket, same load | p95 73 ms | < 300 ms |
| Balance invariant violations (randomized and fuzz tests) | 0 | 0 |

Engine numbers: `go test ./internal/engine -run '^$' -bench . -benchtime 2s` on an Intel Core i7-12650H under WSL2; CI reruns them on every push and posts them to the job summary. Load numbers: `load/k6.js` against a local server and local Postgres. They are not yet measured on the free deployment, where Neon round trips add latency.

## Features

- **Matching engine** (`internal/engine`): limit and market orders, GTC and IOC time in force, price-time priority, partial fills, maker-price execution, self-trade prevention, O(1) cancel. B-tree price levels with FIFO queues at each price.
- **Wallet and settlement**: ₹10,00,000 starting cash, available and reserved balances for cash and every stock, atomic settlement, average-cost P&L. All money is integer paise.
- **Durability and crash recovery**: commit before acknowledging, group commit, idempotent writes, fencing between overlapping deploys, and books rebuilt in time-priority order on boot. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
- **Live market data**: one WebSocket per client for the order book (batched every 100 ms), trades with gap-detecting sequence numbers, candles, a ticker every second, and private order, fill and balance updates. Events are published only after they are durable.
- **Candles**: 1m, 5m, 1h and 1d OHLCV with IST trading days, served over REST and pushed live.
- **Bots**: a market maker re-quoting three levels around the last price, a noise trader, and a momentum bot. All go through the same order path as people, and their aggressiveness and volatility are tunable.
- **Accounts**: one-click guest accounts (24 hours), email and password (bcrypt cost 12), optional Google sign-in, 15-minute JWT access tokens and rotating 7-day refresh cookies.
- **UI**: landing, sign-in, markets, trade (candles, depth, tape, order ticket, open orders), portfolio with filtered order and trade history, leaderboard, and admin pages. Includes price flashes, optimistic pending orders, a toast on every fill, a connection indicator with auto-reconnect and resync, and a wake-up screen while the free host starts.
- **Admin**: halt or resume a stock, list a new one, tune the bots, reset the market for a new season, and live system stats.
- **Operations**: JSON logs (`log/slog`), Prometheus metrics at `/metrics`, token-bucket rate limits, graceful shutdown.

## Run locally

Requirements: Go 1.23+, Node.js 20+, and optionally Docker for Postgres.

```sh
npm install
npm run build              # builds the UI into internal/web/dist, which the binary embeds
go run ./cmd/tradecore     # http://localhost:8080; in-memory state unless DATABASE_URL is set
```

For UI work, run `npm run dev` alongside the Go server. Vite serves the UI on :5173 and proxies `/api` and `/ws` to :8080.

With persistence:

```sh
docker run -d --name tradecore-pg -e POSTGRES_PASSWORD=tc -p 55432:5432 postgres:16-alpine
DATABASE_URL=postgres://postgres:tc@localhost:55432/postgres \
JWT_SECRET=dev-secret-dev-secret-dev-secret-12 \
ADMIN_EMAILS=you@example.com \
go run ./cmd/tradecore
```

Sign up with an email listed in `ADMIN_EMAILS` to see the admin page.

### Configuration

| Variable | Purpose |
| --- | --- |
| `DATABASE_URL` | Postgres connection string. Required in production; unset in development keeps state in memory |
| `JWT_SECRET` | HS256 key, at least 32 bytes. Required in production; keep it stable across deploys |
| `APP_ENV=production` | Requires the two above, marks cookies `Secure`, and allows no cross-origin requests unless listed |
| `ADDR` / `PORT` | Listen address (`ADDR` wins; hosts such as Render set `PORT`) |
| `ADMIN_EMAILS` | Comma-separated emails that get admin access |
| `TRUST_PROXY=true` | Take the client IP for rate limits from the proxy-appended `X-Forwarded-For` hop |
| `FRONTEND_ORIGIN` | Comma-separated origins allowed for CORS and WebSockets, only needed when the UI is hosted elsewhere |
| `COOKIE_SAMESITE=none` | For a UI on a different site; forces `Secure` cookies |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`, `PUBLIC_URL` | Enable Google sign-in; the redirect URI is `PUBLIC_URL/api/v1/auth/google/callback` |
| `ORDER_RATE_PER_USER`, `ORDER_RATE_PER_IP`, `AUTH_RATE_PER_MINUTE` | Override rate limits (defaults 10/s, 30/s, 10/min); `0` disables, for load testing |
| `VITE_API_URL`, `VITE_WS_URL` | Build-time API and WebSocket URLs for a separately hosted UI |

## Tests

```sh
go test -race ./...                       # engine, exchange, API, WebSocket, admin and OAuth tests
TEST_DATABASE_URL=postgres://postgres:tc@localhost:55432/postgres go test ./cmd/tradecore
                                          # + crash recovery, fencing, idempotency (drops the public schema)
go test ./internal/engine -run '^$' -fuzz FuzzBookInvariants -fuzztime 60s
go test ./internal/engine -run '^$' -bench . -benchtime 2s
npm run lint && npm test                  # ESLint, Vitest and React Testing Library
npm run build && npm run test:e2e         # Playwright, desktop and 375 px mobile, against the real binary
```

Load test, against a server started with relaxed limits:

```sh
ORDER_RATE_PER_USER=50 ORDER_RATE_PER_IP=0 AUTH_RATE_PER_MINUTE=6000 go run ./cmd/tradecore
docker run --rm -i --network host -e BASE_URL=http://127.0.0.1:8080 grafana/k6 run - < load/k6.js
```

CI (`.github/workflows/ci.yml`) runs golangci-lint, the Go tests against a Postgres service, 30 seconds of fuzzing, benchmarks, ESLint, Vitest, the build, Playwright, and a Docker build on every push.

## API

REST routes are under `/api/v1`, JSON in and out, with money in integer paise (₹101.50 is `10150`). Errors are `{"error":{"code","message"}}`. Authenticated routes take `Authorization: Bearer <accessToken>`.

| Method | Route | Purpose |
| --- | --- | --- |
| POST | `/auth/guest`, `/auth/signup`, `/auth/login` | Start a session (sets the refresh cookie) |
| POST | `/auth/refresh`, `/auth/logout` | Rotate or revoke the refresh cookie |
| GET | `/auth/google/start` | Begin Google sign-in, when configured |
| GET | `/me` | The signed-in account |
| GET | `/instruments` | Stocks with last price, day change, range, volume and a sparkline |
| GET | `/instruments/{symbol}/book` | Top 10 levels each side, with `seq` |
| GET | `/instruments/{symbol}/trades?limit=` | Recent trades, newest first |
| GET | `/instruments/{symbol}/candles?interval=1m\|5m\|1h\|1d&from=&to=&limit=` | OHLCV candles |
| POST | `/orders` | `{symbol, side, type, timeInForce?, price?, quantity}` |
| GET | `/orders?status=open\|filled\|cancelled\|rejected\|all&symbol=&limit=` | My orders |
| DELETE | `/orders/{id}` | Cancel an open order |
| GET | `/trades/me?symbol=&limit=` | My trades, with side and value |
| GET | `/portfolio` | Cash, holdings and P&L |
| GET | `/leaderboard` | Top 50 people (bots excluded), refreshed every minute, with my rank |
| GET/PUT/POST | `/admin/...` | Stats, bots, listings, halts and reset (see `cmd/tradecore/admin.go`) |
| GET | `/health` | Liveness, persistence mode, and whether Google sign-in is on |

The WebSocket protocol at `/ws` is documented at the top of `cmd/tradecore/hub.go`.

## Deploying

See [docs/DEPLOY.md](docs/DEPLOY.md): Render (Docker) plus Neon (Postgres) plus an uptime pinger, at ₹0 a month.

## Project layout

```
cmd/tradecore/     HTTP API, WebSocket hub, bots, persistence, admin (package main)
  migrations/      SQL migrations, embedded and applied on startup
internal/engine/   the order book and matching engine, with fuzz tests and benchmarks
internal/web/      embeds and serves the built UI
src/               React + TypeScript UI (TanStack Query, Zustand, Lightweight Charts)
e2e/               Playwright tests
load/              k6 load test
docs/              architecture notes and deployment guide
```
