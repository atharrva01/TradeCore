# Architecture

## Overview

```
 browser ──REST──▶ ┌──────────────────────── tradecore (one Go process) ─────────────────────────┐
         ◀──WS──── │ HTTP handlers ─┐                                                           │
                   │ bots ──────────┼─▶ submit() ─▶ engine.Book (per stock) ─▶ settle()          │
                   │                │   └──────── all under one exchange lock ────────┘          │
                   │                └─▶ commitLocked(): snapshot dirty rows + events             │
                   │                       │                                                      │
                   │                       ▼                                                      │
                   │                 single writer ──group commit──▶ Postgres                     │
                   │                       │ (after commit)                                       │
                   │                       ▼                                                      │
                   │                 WebSocket hub ──▶ clients                                    │
                   └──────────────────────────────────────────────────────────────────────────────┘
```

## Matching engine (`internal/engine`)

Each stock has a `Book`: bids and asks as B-trees of price levels (`github.com/google/btree`), a FIFO `container/list` per level, and a map from order ID to its list element.

| Operation | Cost |
| --- | --- |
| Best bid or ask | O(log L) via B-tree `Min` (the bid tree is ordered descending) |
| Add a resting order | O(log L) to find or create the level, O(1) to append |
| Cancel | O(1) map lookup and list unlink, plus O(log L) when a level empties |
| Match | O(log L) per level crossed, O(1) per order filled |

The engine implements every PRD matching rule: price-time priority, the maker's price, market remainders cancelled, IOC remainders cancelled, and self-trade prevention by cancelling the resting order. A market buy carries a budget (the buyer's available cash), so it stops filling before it can overspend. `Restore` puts recovered resting orders back without matching them.

Invariants are checked by `FuzzBookInvariants` (native Go fuzzing) and a seeded randomized test after every operation: the book is never crossed, filled plus remaining equals quantity, no self trades, every trade happens at a resting order's price, and resting quantity is conserved.

## Exchange and settlement (`cmd/tradecore/exchange.go`)

Every order, from a person or a bot, goes through `submit`. It validates, reserves cash (price × quantity, or all available cash for a market buy) or shares, matches, settles each fill (moving reserved cash to the seller and reserved shares to the buyer, refunding price improvement to the buyer), and releases whatever the order no longer needs. Bots skip only the HTTP rate limits.

Money and shares are conserved: a randomized test runs thousands of human and bot orders and checks after each step that total cash and shares are unchanged, nothing is negative, and every reservation matches the open orders.

## Persistence and crash recovery (`cmd/tradecore/store.go`)

1. Every mutation marks the rows it touched as dirty.
2. At the end of each operation, still under the lock, `commitLocked` copies those rows into a changeset and queues it.
3. One writer goroutine drains the queue. It merges waiting changesets so each row is written once at its latest value, and commits them in a single transaction and a single network round trip.
4. A request is answered only after its changeset commits. Bots do not wait.
5. Events for WebSocket clients come from the same changesets and are published only after the commit.

Every write is a full-row upsert or an insert with `ON CONFLICT DO NOTHING`, so a transaction retried after a lost acknowledgement cannot double-apply anything. Because the writer is ordered, Postgres always holds the exchange after some complete prefix of operations.

On boot the process bumps an epoch in `exchange_state` (fencing), loads accounts, balances, positions, orders, recent trades, candles and settings, rebuilds every book from `OPEN` and `PARTIALLY_FILLED` orders sorted by arrival sequence (so time priority survives), and recomputes every reservation from the resting orders as a consistency check. If a commit still fails after retrying for about 15 seconds, the process exits: memory is ahead of the database, and the next process recovers from the last committed state.

**Fencing.** During a deploy, Render can briefly run the old and new process together. The new one bumps the epoch, and each commit begins `UPDATE exchange_state ... WHERE epoch = $mine`. The old process's next commit changes zero rows, so it is refused and that process exits instead of overwriting newer state.

## Real-time feed (`cmd/tradecore/hub.go`)

One WebSocket per client. Subscribing sends a snapshot first (the book, or recent trades) and then live events. Book updates are coalesced to at most one per stock every 100 ms. Trades carry a per-stock trade sequence that increases by exactly one, so a client that sees a jump re-fetches the tape. Book messages carry the stock's event sequence, so a client keeps only the newest. A client that cannot keep up is disconnected; it reconnects and resynchronises from snapshots.

## Deviations from the PRD, and why

| PRD | Built | Reason |
| --- | --- | --- |
| One goroutine per book reading commands from a channel, no locks | One exchange lock around books and balances | Settlement touches accounts shared by every stock, so per-book goroutines still need cross-book coordination for balances. A single lock keeps reservation and settlement atomic and simple; the engine is not the bottleneck (about 1 µs per order) and Postgres commits happen outside the lock. Per-book actors become worthwhile with account sharding, which is out of scope for one instance. |
| Async persistence writer with event replay from the last `seq` | Commit-before-acknowledge of state snapshots, with group commit | Acknowledged orders are never lost and recovery is a plain load, with no replay to get wrong. Group commit keeps it fast (p95 24 ms at 500 orders/s locally). Events still carry strictly increasing per-stock sequence numbers. |
| Book deltas | Full top-10 snapshots, at most every 100 ms | Twenty levels is a few hundred bytes. Snapshots are self-healing, so a missed message never leaves a client wrong. |
| Tailwind CSS | Hand-written CSS with design tokens | The UI already had a consistent dark design system; rewriting it in Tailwind would add churn without changing what users see. |
| sqlc, goose | Hand-written SQL with pgx, and a small embedded migrator | A small number of fixed queries; the migrator is about 40 lines and runs inside one transaction with an advisory lock. |
| testcontainers-go | `TEST_DATABASE_URL`, and a Postgres service in CI | Same coverage against real Postgres, without needing Docker inside the test process. |
| UUID user IDs | Random 128-bit hex IDs as `TEXT` | Equivalent uniqueness; bots keep readable IDs such as `market-maker`. |
| Nightly job aggregating old trades | Candles are built live; an hourly job drops bot-only trades after a day, and 1m and 5m candles once they fall outside the window the server keeps | The same storage goal without a batch job. |

Not built: P2 scope (stop-loss and FOK orders, short selling, archived seasons, replay mode, a strategy API).
