# Mini Stock Exchange PRD

Oct 18, 2026 · @Atharva Borade

## Overview

Mini Stock Exchange (working name: **TradeCore**) is a web-based simulated stock exchange where users trade fictional stocks against each other and against bots, matched by a real order-matching engine written in Go.

It is not a broker or a paper-trading wrapper over real market data. The platform runs its own order books, so prices are set entirely by orders placed on it, the same way a real exchange works internally.

**One-line pitch:** a live exchange you can trade on in your browser, powered by a price-time priority matching engine that handles thousands of orders per second.

**Why it exists:** a resume project for full-stack internship applications. It combines a polished React frontend, real-time WebSockets, a relational database, auth and deployment with a genuinely hard backend core (the matching engine) that interviewers can probe for 20+ minutes.

## Goals and non-goals

The project succeeds when a recruiter can open a live link, trade within 30 seconds, and the resume bullet carries a measured performance number.

**Goals**

- A correct, fast matching engine with price-time priority, limit and market orders, partial fills and cancels
- A live, always-moving market even with one visitor, driven by bot traders
- Real-time UI: order book depth, candlestick chart, trade tape and portfolio P&L update without refresh
- Survives server restarts with no lost orders or balances
- Deployed publicly at ₹0 running cost

**Non-goals (v1)**

- Real money, real stocks or real market data feeds
- Short selling, margin, options or derivatives
- Mobile native apps (responsive web only)
- Horizontal scaling across multiple engine instances

**Success metrics**

| Metric | Target |
| --- | --- |
| Engine throughput (benchmark, single core) | ≥ 50,000 orders/sec |
| Matching latency p99 (in-process) | < 1 ms |
| Order placed to UI update (deployed) | < 300 ms |
| Time from landing page to first trade | < 30 sec |
| Balance invariant violations in tests | 0 |
| Real users (friends, college, StudLabs) | 30+ within 2 weeks of launch |

## Target users and user stories

Three audiences matter: recruiters who skim, interviewers who dig, and students who actually play with it.

| User | What they want | What they do |
| --- | --- | --- |
| Recruiter | Proof the candidate can ship | Opens demo link, places one trade, leaves in 2 min |
| Interviewer | Depth to question | Reads README and architecture, asks about engine design |
| Student trader | Fun competition | Trades daily, climbs leaderboard |

**User stories**

1. As a visitor, I can try the exchange as a guest in one click, without signing up.
2. As a new user, I sign up with email and password and receive ₹10,00,000 in virtual cash.
3. As a trader, I place a limit or market buy/sell order and see it fill or rest on the book.
4. As a trader, I cancel an open order and get my reserved cash or shares back instantly.
5. As a trader, I watch the order book, last price and candlestick chart update live.
6. As a trader, I see my holdings, average buy price, realized and unrealized P&L.
7. As a trader, I see my order history and trade history.
8. As a competitor, I see a leaderboard ranked by total portfolio value.
9. As an admin, I can pause trading on a stock, adjust bot settings, and reset the market for a new season.

## Functional requirements

Requirements are tagged P0 (MVP, must ship), P1 (ship if time allows) and P2 (future).

**FR1. Authentication**

- P0: Sign up and log in with email and password; passwords hashed with bcrypt
- P0: JWT access token (15 min) plus refresh token in an httpOnly cookie (7 days)
- P0: Guest mode: one click creates a temporary account that expires after 24 hours
- P1: Google OAuth login

**FR2. Wallet and balances**

- P0: Each new user gets ₹10,00,000 virtual cash
- P0: Two balances per asset: available and reserved (locked by open orders)
- P0: Placing a buy order reserves price × quantity cash; a sell order reserves shares
- P0: Balances can never go negative; an order that cannot be funded is rejected

**FR3. Instruments**

- P0: 5 fictional stocks seeded at launch, each with a symbol, name, starting price and tick size of ₹0.05
- P0: Prices stored as integer paise, never floats
- P1: Admin can add or halt an instrument

**FR4. Orders**

- P0: Limit orders (price + quantity) and market orders (quantity only)
- P0: Cancel any open or partially filled order
- P0: Order states: OPEN, PARTIALLY\_FILLED, FILLED, CANCELLED, REJECTED
- P0: Server-side validation: positive quantity, price on tick, sufficient balance
- P0: Rate limit of 10 orders/sec per user
- P1: Time-in-force: GTC (default) and IOC
- P2: Stop-loss orders

**FR5. Matching**

- P0: Price-time priority matching with partial fills (full spec in the next section)
- P0: Each match produces a trade record and settles balances atomically

**FR6. Bot traders**

- P0: Market-maker bot per stock that continuously quotes buy and sell orders around the last price
- P0: Noise-trader bots that place random small market and limit orders
- P0: Bots trade through the same engine and API rules as humans
- P1: Momentum bot that follows short-term trends, creating realistic price swings
- P1: Admin-tunable bot aggressiveness and volatility

**FR7. Market data**

- P0: Live order book depth (top 10 price levels each side)
- P0: Live trade tape (last 50 trades)
- P0: OHLCV candles at 1-minute and 5-minute intervals, plus last price and day change %
- P1: 1-hour and 1-day candles

**FR8. Portfolio**

- P0: Holdings with quantity, average buy price, current value, unrealized P&L
- P0: Realized P&L, cash balance, total portfolio value
- P0: Order history and trade history with filters

**FR9. Leaderboard**

- P1: Top 50 users by total portfolio value, refreshed every minute; bots excluded
- P2: Seasons with resets and archived results

**FR10. Admin**

- P1: Halt or resume trading per stock, tune bots, reset market
- P1: System stats page: orders/sec, connected clients, engine latency

## Matching engine specification

Each stock has its own in-memory order book owned by a single goroutine, so the hot path needs no locks and every match is deterministic.

**Order book structure**

- Bids sorted by price descending, asks by price ascending
- Each price level holds a FIFO queue of orders (oldest first)
- Price levels kept in a sorted structure (red-black tree or B-tree, e.g. `github.com/google/btree`) for O(log n) insert and O(1) best-price lookup
- A hash map from order ID to its queue node for O(1) cancel

**Matching rules (price-time priority)**

1. An incoming buy matches the lowest ask while ask price ≤ buy limit price.
2. An incoming sell matches the highest bid while bid price ≥ sell limit price.
3. At the same price, the earliest resting order fills first.
4. Trade price = the resting order's price (the maker's price), never the incoming order's.
5. If the incoming order has quantity left after matching, a limit order rests on the book; a market order's remainder is cancelled.
6. A market order with no liquidity on the other side is rejected.
7. Self-trade prevention: if the best opposite order belongs to the same user, cancel the resting order and continue.

**Worked example**

Asks: 5 @ ₹101.00 (A), 10 @ ₹101.50 (B). A buy limit for 12 @ ₹101.50 arrives. It fills 5 @ ₹101.00 against A, then 7 @ ₹101.50 against B. B stays on the book with 3 remaining. The buyer's unused reserve (12 × 101.50 minus actual cost) is released.

**Concurrency model**

- One engine goroutine per stock reads commands (`PlaceOrder`, `CancelOrder`) from a buffered channel
- API handlers validate and reserve funds, then send the command and wait on a reply channel
- The engine emits events (`OrderAccepted`, `TradeExecuted`, `OrderCancelled`, `BookUpdated`) to an event bus consumed by the persistence writer and the WebSocket hub

**Invariants (enforced in tests)**

- Total cash across all users + reserved cash is constant (money is never created or destroyed by trading)
- Total shares per stock across all users is constant
- The book is never crossed: best bid < best ask after every operation
- Filled quantity + remaining quantity = original quantity for every order
- Sequence numbers on events are strictly increasing per stock

## System architecture and tech stack

A single Go service hosts the API, the per-stock matching engines, the bots and the WebSocket hub; Postgres is the durable record and the React app is a separate static frontend.

&#91;embedded content: system architecture · 7 components\]

Human orders arrive over REST and bot orders arrive in-process, but both go through the same engine. Every engine event goes to the event bus, which feeds live clients and the persistence writer; on restart the engine rebuilds its books from Postgres.

| Layer | Choice | Why |
| --- | --- | --- |
| Backend language | Go 1.23+ | Goroutines and channels fit the engine model; your strongest language |
| HTTP router | chi or net/http (Go 1.22 routing) | Lightweight, idiomatic |
| WebSockets | `github.com/coder/websocket` | Maintained, context-aware |
| Database access | pgx + sqlc | Type-safe SQL, no ORM magic |
| Migrations | goose or golang-migrate | Versioned schema |
| Frontend | React + TypeScript, Vite or Next.js | Matches full-stack JDs |
| Styling | Tailwind CSS | Fast, consistent UI |
| Data fetching / state | TanStack Query, Zustand | REST caching + live store |
| Charts | TradingView Lightweight Charts | Free, open source, built for candlesticks |
| Containers | Docker (multi-stage) | Reproducible deploys |
| CI | GitHub Actions | Tests, lint, benchmarks on push |

## Data model

Postgres is the source of truth for balances, orders and trades; all money and prices are `BIGINT` in paise to avoid floating-point errors.

| Table | Key columns | Notes |
| --- | --- | --- |
| `users` | id (UUID), email, password\_hash, display\_name, is\_bot, is\_guest, created\_at | Bots are real users with `is_bot = true` |
| `instruments` | id, symbol, name, tick\_size, status (ACTIVE/HALTED), initial\_price | 5 seeded rows |
| `balances` | user\_id, asset ('INR' or symbol), available, reserved, updated\_at | PK (user\_id, asset); CHECK available ≥ 0 and reserved ≥ 0 |
| `orders` | id, user\_id, instrument\_id, side, type, price, quantity, filled\_qty, status, seq, created\_at | Index on (instrument\_id, status) for crash recovery |
| `trades` | id, instrument\_id, buy\_order\_id, sell\_order\_id, price, quantity, seq, executed\_at | Index on (instrument\_id, executed\_at) |
| `candles` | instrument\_id, interval, bucket\_start, open, high, low, close, volume | PK (instrument\_id, interval, bucket\_start) |
| `refresh_tokens` | id, user\_id, token\_hash, expires\_at | Revoked on logout |

**Settlement transaction (per trade)**

In one database transaction: update both orders' `filled_qty` and status, move reserved INR from buyer to seller's available INR, move reserved shares from seller to buyer's available shares, insert the trade row, and upsert the candle.

**Storage budget**

Neon's free plan has 0.5 GB. A nightly job aggregates trades older than 3 days into candles and deletes the raw rows, and deletes cancelled bot orders older than 1 day.

## API specification

REST handles commands and history; one WebSocket connection per client streams all live market data. All REST routes are under `/api/v1`, JSON in and out.

**REST endpoints**

| Method | Path | Auth | Purpose |
| --- | --- | --- | --- |
| POST | `/auth/signup` | No | Create account, seed ₹10,00,000 |
| POST | `/auth/login` | No | Returns access token, sets refresh cookie |
| POST | `/auth/guest` | No | Create 24-hour guest account |
| POST | `/auth/refresh` | Cookie | New access token |
| POST | `/auth/logout` | Yes | Revoke refresh token |
| GET | `/instruments` | No | List stocks with last price and day change |
| GET | `/instruments/{symbol}/book` | No | Order book snapshot (top 10 levels) |
| GET | `/instruments/{symbol}/trades?limit=50` | No | Recent trades |
| GET | `/instruments/{symbol}/candles?interval=1m&from=&to=` | No | OHLCV history |
| POST | `/orders` | Yes | Place order |
| DELETE | `/orders/{id}` | Yes | Cancel order |
| GET | `/orders?status=open` | Yes | My orders |
| GET | `/trades/me` | Yes | My trade history |
| GET | `/portfolio` | Yes | Holdings, cash, P&L |
| GET | `/leaderboard` | No | Top 50 by portfolio value |
| GET | `/health` | No | Liveness, used by uptime pinger |

**Place order request and response**

```json
POST /api/v1/orders
{ "symbol": "ATHR", "side": "BUY", "type": "LIMIT", "price": 10150, "quantity": 12 }

201 Created
{ "orderId": "9f1c...", "status": "PARTIALLY_FILLED", "filledQty": 5,
  "fills": [{ "price": 10100, "quantity": 5 }] }
```

Errors use one shape: `{ "error": { "code": "INSUFFICIENT_FUNDS", "message": "..." } }` with codes `INVALID_PRICE`, `INVALID_QUANTITY`, `INSUFFICIENT_FUNDS`, `NO_LIQUIDITY`, `INSTRUMENT_HALTED`, `RATE_LIMITED`.

**WebSocket: `wss://<host>/ws`**

Client subscribes to channels; server pushes messages tagged with `type` and a per-stock `seq` so the client can detect gaps and re-fetch a snapshot.

| Channel | Direction | Payload |
| --- | --- | --- |
| `subscribe` / `unsubscribe` | Client → server | `{ "channels": ["book:ATHR", "trades:ATHR"] }` |
| `book:{symbol}` | Server → client | Changed price levels (delta) or full snapshot on subscribe |
| `trades:{symbol}` | Server → client | Each executed trade: price, qty, side, time |
| `candles:{symbol}:1m` | Server → client | Current candle updates |
| `ticker` | Server → client | Last price and change % for all stocks, every 1 sec |
| `user` (auto, authenticated) | Server → client | My order status changes, fills, balance updates |

The hub batches book deltas every 100 ms per symbol so a burst of bot trades does not flood clients.

## Frontend pages and UI components

The trading screen is the product; every other page exists to get the user there or show results. Built with React + TypeScript (Vite or Next.js), Tailwind CSS, TanStack Query for REST, Zustand for live state, and TradingView Lightweight Charts.

| Page | Route | Key components |
| --- | --- | --- |
| Landing | `/` | Hero with live ticker strip, "Try as guest" button, short how-it-works, link to GitHub |
| Sign up / log in | `/auth` | Email and password form, guest button |
| Markets | `/markets` | Table of all stocks: last price, change %, volume, sparkline |
| Trade | `/trade/:symbol` | Chart, order book, trade tape, order form, my open orders |
| Portfolio | `/portfolio` | Summary cards, holdings table, P&L, order and trade history tabs |
| Leaderboard | `/leaderboard` | Ranked table, my rank highlighted |
| Admin | `/admin` | Halt toggles, bot sliders, system stats |

**Trade page layout (desktop)**

- Left (60%): candlestick chart with interval switcher, volume bars underneath
- Middle (20%): order book with bid/ask depth bars, spread in the middle; clicking a price fills the order form
- Right (20%): order form (Buy/Sell tabs, Limit/Market toggle, price, quantity, estimated cost, available balance); trade tape below
- Bottom: tabs for Open orders (with cancel), Order history, Positions

**UX details that make it feel real**

- Price flashes green or red on change; trades in the tape colored by aggressor side
- Optimistic UI: order appears as "pending" instantly, confirmed on server reply
- Connection status dot; auto-reconnect with exponential backoff and snapshot re-sync
- Toast on every fill: "Bought 5 ATHR @ ₹101.00"
- Dark theme by default; responsive down to 375 px with the chart and order form stacked

## Non-functional requirements

Correctness comes before speed: a fast engine that loses a rupee is a failed project.

**Performance**

- Engine: ≥ 50,000 orders/sec single-threaded per book, p99 match < 1 ms (Go benchmark)
- API: p95 order placement < 50 ms server-side on the free instance
- WebSocket: support 200 concurrent clients on the free instance with 100 ms batched updates

**Reliability and crash recovery**

- On startup, the engine rebuilds each book by loading OPEN and PARTIALLY\_FILLED orders from Postgres, sorted by `seq`
- Every engine event carries a sequence number; the persistence writer records the last applied `seq` per stock, so replay is idempotent
- Graceful shutdown on SIGTERM: stop accepting orders, drain the event channel, flush writes, then exit
- Bots resume automatically after restart
- The free host restarts and sleeps, so recovery is exercised regularly, not just in theory

**Security**

- bcrypt (cost 12) for passwords; JWT signed with HS256, secret from environment variables
- All SQL through parameterized queries (pgx or sqlc)
- Input validation on every field; integer overflow checks on price × quantity
- Rate limiting per user and per IP (token bucket); WebSocket message size limits
- CORS restricted to the frontend domain; httpOnly, Secure, SameSite cookies for refresh tokens
- Users can only see and cancel their own orders (checked server-side)

**Observability**

- Structured JSON logs (`log/slog`)
- `/metrics` endpoint with Prometheus counters: orders placed, trades, engine latency histogram, connected clients
- Admin stats page reads these metrics

## Testing and benchmarking strategy

The engine gets the heaviest testing because it is the part interviewers will try to break with questions.

| Layer | What | Tool |
| --- | --- | --- |
| Engine unit tests | Every matching rule, partial fills, cancels, self-trade prevention, market order with no liquidity | Go `testing`, table-driven tests |
| Property / fuzz tests | 100,000 random orders; assert all invariants hold after each step | Go native fuzzing (`go test -fuzz`) |
| Benchmarks | Orders/sec and latency for a deep book (10k resting orders) | `go test -bench`, `benchstat` |
| Integration tests | API + real Postgres: place order, settle, restart, recover | `testcontainers-go` |
| Load test | 200 WebSocket clients + 500 orders/sec against the deployed server | k6 |
| Frontend | Order form validation, book rendering | Vitest + React Testing Library |
| End-to-end | Guest login → place trade → see it in portfolio | Playwright |

**CI**

GitHub Actions runs lint (`golangci-lint`, ESLint), unit and integration tests, and benchmarks on every push. Benchmark results are posted to the README so the resume number is always backed by a reproducible run.

## Deployment and infrastructure

The whole stack runs at ₹0 per month on free tiers; the only optional spend is a custom domain.

| Component | Service | Cost | Free-tier limit to watch |
| --- | --- | --- | --- |
| Go backend + WebSockets + bots | [Render](https://render.com/docs/free) free web service (Docker) | ₹0 | Sleeps after 15 min with no inbound traffic; \~1 min cold start; may restart anytime |
| Database | [Neon](https://neon.com/faqs/managed-postgres-databases-free-tier) free Postgres | ₹0 | 0.5 GB storage, 100 CU-hours per project |
| Frontend | Vercel (or Render static site) | ₹0 | Generous for a portfolio project |
| Uptime pinger | cron-job.org hitting `/health` every 10 min | ₹0 | Keeps the backend awake for recruiters |
| CI | GitHub Actions | ₹0 | Free for public repos |
| Domain (optional) | e.g. `.xyz` or `.in` | ₹100–800/year | Not required |

**Deployment notes**

- Backend ships as a small Docker image (multi-stage build, distroless base)
- Do not use Render's free Postgres: it expires after 30 days; use Neon instead
- Secrets (`DATABASE_URL`, `JWT_SECRET`) in Render environment variables, never in the repo
- Use Neon's pooled connection string to stay within connection limits
- Frontend and backend on separate origins: configure CORS and cookie `SameSite=None; Secure`
- Optional alternative: one Go binary that also serves the built React app (`embed`), deployed as a single Render service

## Milestones

The MVP goes live by Nov 9, 2026, assuming about 3 to 4 hours a day alongside other commitments; each week ends with a gate that must pass before the next starts.

&#91;embedded content: 3-week build plan · 3 phases, 3 gates\]

Building the engine first, with no UI, keeps the hardest and most interview-relevant part from being rushed at the end. P1 features (leaderboard, admin, OAuth) start only after Gate 3.

## Risks, future scope and interview prep

The biggest risk is scope creep; ship the P0 list deployed before touching any P1 feature.

**Risks and mitigations**

| Risk | Mitigation |
| --- | --- |
| Scope creep delays launch | Freeze scope to P0; P1 only after deployed MVP |
| Free backend sleeps when a recruiter clicks | Uptime pinger every 10 min; friendly "waking up" screen on the frontend |
| Bots fill the 0.5 GB database | Nightly pruning job; cap bot order rate |
| Bugs that create or destroy money | Invariant fuzz tests run in CI |
| Async persistence loses events on crash | Sequence numbers + replay from last persisted `seq` |
| UI floods under bot activity | 100 ms batched book deltas, render throttling |

**Open questions**

- Final product name and domain
- Next.js or plain Vite React for the frontend
- Single-binary deploy (embedded frontend) or separate Vercel frontend

**Future scope (P2)**

- Stop-loss and IOC/FOK orders, short selling
- Seasons and trading competitions for college clubs
- Replay mode: scrub through a day's order book history
- Strategy API: users write bots in a sandboxed language and compete

**Resume bullet (fill in real numbers after benchmarking)**

- Built a real-time stock exchange simulator with a Go price-time priority matching engine handling **X orders/sec at p99 < Y ms**, live order book and charts over WebSockets, and crash recovery via sequence-numbered event replay
- Stack: Go, React, TypeScript, PostgreSQL, WebSockets, Docker, GitHub Actions

**Interview talking points to prepare**

1. Why a B-tree or heap for price levels, and the complexity of insert, match and cancel
2. Why one goroutine per book instead of mutexes, and what happens at scale
3. How balances stay consistent: reservation on order, settlement on trade, invariant tests
4. Crash recovery: what is in memory, what is persisted, and how replay avoids double-applying
5. Why integers in paise instead of floats
6. How WebSocket fan-out works and how clients detect and fix missed messages
7. What you would change to support 100x the traffic
