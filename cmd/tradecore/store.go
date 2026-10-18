package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tradecore/internal/engine"
)

// Persistence model
//
// Every mutating operation runs under server.mu, marks what it touched as
// dirty, and finishes with commitLocked, which copies the dirty state into a
// changeset and queues it for the single writer goroutine. The writer applies
// queued changesets in order, grouping whatever is waiting into one
// transaction, so Postgres always holds the state after some prefix of the
// operations. Human requests are answered only after their changeset commits;
// bot activity does not wait.
//
// Writes are full-row upserts and idempotent inserts, so a retried
// transaction cannot double-apply anything. On boot the server loads that
// state and rebuilds each book from its resting orders in arrival order.
//
// The same changeset is the exchange's event stream: once it commits, its
// events go to the WebSocket hub, so clients only ever see durable state.

//go:embed migrations/*.sql
var migrationFiles embed.FS

const (
	maxGroupCommit   = 128
	commitAttempts   = 6
	botTradeLifetime = 24 * time.Hour
)

var errFenced = errors.New("another TradeCore process now owns the database")

type commitWait chan error

func (c commitWait) wait() error {
	if c == nil {
		return nil
	}
	return <-c
}

type accountRow struct {
	ID           string
	Name         string
	Email        string
	PasswordHash string
	IsBot        bool
	IsAdmin      bool
	ExpiresAt    time.Time
	Cash         balance
	Shares       map[string]balance
	Positions    map[string]position
}

type tradeRow struct {
	Trade         tapeTrade
	InvolvesHuman bool
}

type candleRow struct {
	key    candleKey
	candle candle
}

type tokenRow struct {
	Hash   string
	Record refreshRecord
}

type changeset struct {
	instruments     []instrument
	accounts        []accountRow
	deletedAccounts []string
	orders          []engine.Order
	deletedOrders   []string
	trades          []tradeRow
	tokens          []tokenRow
	deletedTokens   []string
	candles         []candleRow
	botSettings     *botSettings
	reset           bool
	orderSeq        uint64
	tradeSeq        uint64
	events          []wsEvent
	result          chan error
}

type store struct {
	pool   *pgxpool.Pool
	epoch  int64
	queue  chan *changeset
	done   chan struct{}
	closed bool // guarded by server.mu
	// onFailure runs once when a commit cannot be made durable. Memory is
	// then ahead of the database, so the default exits and lets the next
	// process recover from the last committed state.
	onFailure func(error)
	failed    error
	// publish receives each changeset's events after it commits.
	publish func([]wsEvent)
	metrics *metrics
}

func openStore(ctx context.Context, dsn string) (*store, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	config.MaxConns = 4
	// Plain extended-protocol queries work through transaction poolers such as
	// Neon's pooled endpoint, which cannot hold named prepared statements.
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect to Postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to Postgres: %w", err)
	}
	st := &store{
		pool: pool, queue: make(chan *changeset, 1024), done: make(chan struct{}),
		onFailure: func(err error) {
			slog.Error("persistence failed, shutting down for recovery", "error", err)
			os.Exit(1)
		},
		publish: func([]wsEvent) {},
	}
	if err := st.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return st, nil
}

func (st *store) migrate(ctx context.Context) error {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	return pgx.BeginFunc(ctx, st.pool, func(tx pgx.Tx) error {
		// Transaction-scoped, so it is safe behind a transaction pooler.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7262651)`); err != nil {
			return fmt.Errorf("lock migrations: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
		for _, name := range names {
			var applied bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&applied); err != nil {
				return err
			}
			if applied {
				continue
			}
			sql, err := migrationFiles.ReadFile(name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("apply %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
				return err
			}
			slog.Info("applied migration", "name", name)
		}
		return nil
	})
}

// claim takes ownership of the database by bumping the writer epoch. Commits
// from any process holding an older epoch fail from this point on.
func (st *store) claim(ctx context.Context) error {
	return pgx.BeginFunc(ctx, st.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO exchange_state (id, order_seq, trade_seq, epoch)
			VALUES (1, 0, 0, 0) ON CONFLICT (id) DO NOTHING`); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `UPDATE exchange_state SET epoch = epoch + 1 WHERE id = 1 RETURNING epoch`).Scan(&st.epoch)
	})
}

// openPersistentServer claims the database, then either restores the saved
// exchange or seeds a new one and saves it before serving traffic.
func openPersistentServer(ctx context.Context, st *store) (*server, error) {
	if err := st.claim(ctx); err != nil {
		return nil, fmt.Errorf("claim database: %w", err)
	}
	s := newBaseServer()
	found, err := st.load(ctx, s)
	if err != nil {
		return nil, err
	}
	if found {
		for _, problem := range s.reservationMismatches() {
			slog.Error("recovery check failed", "problem", problem)
		}
		slog.Info("restored exchange from Postgres", "accounts", len(s.accounts), "orders", len(s.orders),
			"trades", len(s.trades), "epoch", st.epoch)
	} else {
		s.seedDefaults()
		s.markAllDirty()
		slog.Info("initialised a new exchange in Postgres", "epoch", st.epoch)
	}
	s.store = st
	st.publish = s.hub.publish
	st.metrics = s.metrics
	go st.run()
	s.mu.Lock()
	err = s.commitAndUnlock()
	return s, err
}

func (st *store) load(ctx context.Context, s *server) (bool, error) {
	tx, err := st.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `SELECT symbol, name, tick_size, initial_price, last_price, high, low, volume, halted,
		day_open, day_start, seq, trade_seq FROM instruments`)
	if err != nil {
		return false, fmt.Errorf("load instruments: %w", err)
	}
	for rows.Next() {
		item := &instrument{}
		if err := rows.Scan(&item.Symbol, &item.Name, &item.TickSize, &item.InitialPrice, &item.LastPrice,
			&item.High, &item.Low, &item.Volume, &item.Halted, &item.DayOpen, &item.DayStart, &item.Seq, &item.TradeSeq); err != nil {
			return false, err
		}
		s.instruments[item.Symbol] = item
		s.books[item.Symbol] = engine.NewBook(item.Symbol)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(s.instruments) == 0 {
		return false, nil
	}

	var settings []byte
	if err := tx.QueryRow(ctx, `SELECT order_seq, trade_seq, bot_settings FROM exchange_state WHERE id = 1`).
		Scan(&s.orderSeq, &s.tradeSeq, &settings); err != nil {
		return false, fmt.Errorf("load exchange state: %w", err)
	}
	if settings != nil {
		if err := json.Unmarshal(settings, &s.bots); err != nil {
			return false, fmt.Errorf("decode bot settings: %w", err)
		}
	}

	rows, err = tx.Query(ctx, `SELECT id, COALESCE(email, ''), COALESCE(password_hash, ''), display_name, is_bot, is_admin, expires_at FROM users`)
	if err != nil {
		return false, fmt.Errorf("load users: %w", err)
	}
	for rows.Next() {
		user := newAccount("", "")
		var expiresAt *time.Time
		if err := rows.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Name, &user.IsBot, &user.IsAdmin, &expiresAt); err != nil {
			return false, err
		}
		if expiresAt != nil {
			user.ExpiresAt = *expiresAt
		}
		s.accounts[user.ID] = user
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	rows, err = tx.Query(ctx, `SELECT user_id, asset, available, reserved FROM balances`)
	if err != nil {
		return false, fmt.Errorf("load balances: %w", err)
	}
	for rows.Next() {
		var userID, asset string
		var held balance
		if err := rows.Scan(&userID, &asset, &held.Available, &held.Reserved); err != nil {
			return false, err
		}
		if user := s.accounts[userID]; user != nil {
			if asset == "INR" {
				user.Cash = held
			} else {
				user.Shares[asset] = held
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	rows, err = tx.Query(ctx, `SELECT user_id, symbol, quantity, cost, realized FROM positions`)
	if err != nil {
		return false, fmt.Errorf("load positions: %w", err)
	}
	for rows.Next() {
		var userID, symbol string
		p := &position{}
		if err := rows.Scan(&userID, &symbol, &p.Quantity, &p.Cost, &p.Realized); err != nil {
			return false, err
		}
		if user := s.accounts[userID]; user != nil {
			user.Positions[symbol] = p
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	rows, err = tx.Query(ctx, `SELECT token_hash, user_id, expires_at FROM refresh_tokens WHERE expires_at > now()`)
	if err != nil {
		return false, fmt.Errorf("load refresh tokens: %w", err)
	}
	for rows.Next() {
		var hash string
		var record refreshRecord
		if err := rows.Scan(&hash, &record.UserID, &record.ExpiresAt); err != nil {
			return false, err
		}
		s.refreshTokens[hash] = record
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	rows, err = tx.Query(ctx, `SELECT id, seq, user_id, symbol, side, type, tif, price, quantity, filled_qty, status, created_at
		FROM orders ORDER BY seq`)
	if err != nil {
		return false, fmt.Errorf("load orders: %w", err)
	}
	resting := make([]engine.Order, 0)
	for rows.Next() {
		var order engine.Order
		if err := rows.Scan(&order.ID, &order.Seq, &order.UserID, &order.Symbol, &order.Side, &order.Type, &order.TIF,
			&order.Price, &order.Quantity, &order.FilledQty, &order.Status, &order.CreatedAt); err != nil {
			return false, err
		}
		s.orders[order.ID] = order
		s.orderSeq = max(s.orderSeq, order.Seq)
		if order.Status == engine.Open || order.Status == engine.PartiallyFilled {
			resting = append(resting, order)
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, order := range resting {
		book := s.books[order.Symbol]
		if book == nil {
			return false, fmt.Errorf("order %s references unknown instrument %s", order.ID, order.Symbol)
		}
		if err := book.Restore(order); err != nil {
			return false, fmt.Errorf("restore order %s: %w", order.ID, err)
		}
		s.trackBotOrders(engine.Result{Order: order})
	}

	const tradeColumns = `id, symbol, buy_order_id, sell_order_id, buyer_id, seller_id, price, quantity, taker_side, created_at, symbol_seq`
	scanTrade := func(rows pgx.Rows) (tapeTrade, error) {
		var trade tapeTrade
		err := rows.Scan(&trade.ID, &trade.Symbol, &trade.BuyOrder, &trade.SellOrder, &trade.BuyerID, &trade.SellerID,
			&trade.Price, &trade.Quantity, &trade.TakerSide, &trade.CreatedAt, &trade.Seq)
		return trade, err
	}
	rows, err = tx.Query(ctx, `SELECT `+tradeColumns+`
		FROM (SELECT * FROM trades ORDER BY seq DESC LIMIT 10000) recent ORDER BY seq`)
	if err != nil {
		return false, fmt.Errorf("load trades: %w", err)
	}
	for rows.Next() {
		trade, err := scanTrade(rows)
		if err != nil {
			return false, err
		}
		s.trades = append(s.trades, trade)
		prices := append(s.recentPrices[trade.Symbol], trade.Price)
		s.recentPrices[trade.Symbol] = prices[max(0, len(prices)-momentumWindow):]
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	// Each person's most recent trades, for their trade history.
	rows, err = tx.Query(ctx, `SELECT `+tradeColumns+` FROM (
		SELECT t.*, row_number() OVER (PARTITION BY u.id ORDER BY t.seq DESC) AS n, u.id AS owner
		FROM users u JOIN trades t ON t.involves_human AND (t.buyer_id = u.id OR t.seller_id = u.id)
		WHERE NOT u.is_bot) ranked WHERE n <= $1 ORDER BY seq`, userTradeLimit)
	if err != nil {
		return false, fmt.Errorf("load user trades: %w", err)
	}
	for rows.Next() {
		trade, err := scanTrade(rows)
		if err != nil {
			return false, err
		}
		for _, id := range []string{trade.BuyerID, trade.SellerID} {
			if user := s.accounts[id]; user != nil && !user.IsBot {
				history := s.userTrades[id]
				if n := len(history); n == 0 || history[n-1].ID != trade.ID {
					s.userTrades[id] = append(history, trade)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	rows, err = tx.Query(ctx, `SELECT symbol, interval, bucket_start, open, high, low, close, volume FROM candles ORDER BY bucket_start`)
	if err != nil {
		return false, fmt.Errorf("load candles: %w", err)
	}
	for rows.Next() {
		var symbol, interval string
		var c candle
		if err := rows.Scan(&symbol, &interval, &c.Time, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
			return false, err
		}
		if s.candles[symbol] == nil {
			s.candles[symbol] = make(map[string][]candle)
		}
		s.candles[symbol][interval] = append(s.candles[symbol][interval], c)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, series := range s.candles {
		for _, interval := range candleIntervals {
			if list := series[interval.name]; len(list) > interval.keep {
				series[interval.name] = list[len(list)-interval.keep:]
			}
		}
	}
	return true, nil
}

// run is the single writer. It exits once the queue is closed and drained.
func (st *store) run() {
	defer close(st.done)
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		select {
		case first, ok := <-st.queue:
			if !ok {
				return
			}
			batch := []*changeset{first}
		drain:
			for len(batch) < maxGroupCommit {
				select {
				case next, ok := <-st.queue:
					if !ok {
						break drain
					}
					batch = append(batch, next)
				default:
					break drain
				}
			}
			err := st.failed
			if err == nil {
				started := time.Now()
				if err = st.commitWithRetry(batch); err != nil {
					st.failed = err
					st.onFailure(err)
				} else if st.metrics != nil {
					st.metrics.commitTime.Observe(time.Since(started).Seconds())
					st.metrics.commitBatch.Observe(float64(len(batch)))
				}
			}
			for _, cs := range batch {
				if err == nil {
					st.publish(cs.events)
				}
				cs.result <- err
			}
		case <-prune.C:
			st.pruneBotTrades()
		}
	}
}

func (st *store) commitWithRetry(batch []*changeset) error {
	delay := 250 * time.Millisecond
	var err error
	for attempt := 1; attempt <= commitAttempts; attempt++ {
		if err = st.commit(batch); err == nil || errors.Is(err, errFenced) {
			return err
		}
		slog.Warn("commit attempt failed", "attempt", attempt, "of", commitAttempts, "error", err)
		if attempt < commitAttempts {
			time.Sleep(delay)
			delay *= 2
		}
	}
	return err
}

func (st *store) commit(batch []*changeset) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return pgx.BeginFunc(ctx, st.pool, func(tx pgx.Tx) error {
		last := batch[len(batch)-1]
		// The fence goes first so this transaction holds the state row lock
		// before writing anything else.
		tag, err := tx.Exec(ctx, `UPDATE exchange_state
			SET order_seq = GREATEST(order_seq, $1), trade_seq = GREATEST(trade_seq, $2)
			WHERE id = 1 AND epoch = $3`, int64(last.orderSeq), int64(last.tradeSeq), st.epoch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errFenced
		}
		statements := &pgx.Batch{}
		for _, cs := range mergeChangesets(batch) {
			queueChangeset(statements, cs)
		}
		return tx.SendBatch(ctx, statements).Close()
	})
}

// mergeChangesets collapses a group so every row is written once, with its
// latest value. Because each changeset holds full rows, the merged result
// equals applying them one by one. Without this the market maker, which is on
// one side of most trades, would have its balances rewritten for every order
// in the group. A market reset splits the group, since its deletes must land
// between the changesets before and after it.
func mergeChangesets(batch []*changeset) []*changeset {
	var segments []*changeset
	var current *changeset
	var accounts map[string]int
	var orders, tokens, instruments map[string]int
	var candles map[candleKey]int
	start := func(reset bool) {
		current = &changeset{reset: reset}
		segments = append(segments, current)
		accounts, orders, tokens, instruments = map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
		candles = map[candleKey]int{}
	}
	upsert := func(index map[string]int, key string, add func(), replace func(int)) {
		if i, seen := index[key]; seen {
			replace(i)
			return
		}
		index[key] = -1
		add()
	}
	for _, cs := range batch {
		if current == nil || cs.reset {
			start(cs.reset)
		}
		for _, item := range cs.instruments {
			item := item
			upsert(instruments, item.Symbol, func() {
				instruments[item.Symbol] = len(current.instruments)
				current.instruments = append(current.instruments, item)
			}, func(i int) { current.instruments[i] = item })
		}
		for _, user := range cs.accounts {
			user := user
			upsert(accounts, user.ID, func() {
				accounts[user.ID] = len(current.accounts)
				current.accounts = append(current.accounts, user)
			}, func(i int) { current.accounts[i] = user })
		}
		for _, order := range cs.orders {
			order := order
			upsert(orders, order.ID, func() {
				orders[order.ID] = len(current.orders)
				current.orders = append(current.orders, order)
			}, func(i int) { current.orders[i] = order })
		}
		for _, token := range cs.tokens {
			token := token
			upsert(tokens, token.Hash, func() {
				tokens[token.Hash] = len(current.tokens)
				current.tokens = append(current.tokens, token)
			}, func(i int) { current.tokens[i] = token })
		}
		for _, row := range cs.candles {
			if i, seen := candles[row.key]; seen {
				current.candles[i] = row
			} else {
				candles[row.key] = len(current.candles)
				current.candles = append(current.candles, row)
			}
		}
		current.trades = append(current.trades, cs.trades...)
		current.deletedOrders = append(current.deletedOrders, cs.deletedOrders...)
		current.deletedTokens = append(current.deletedTokens, cs.deletedTokens...)
		current.deletedAccounts = append(current.deletedAccounts, cs.deletedAccounts...)
		if cs.botSettings != nil {
			current.botSettings = cs.botSettings
		}
	}
	return segments
}

func queueChangeset(b *pgx.Batch, cs *changeset) {
	if cs.reset {
		// A new season: clear market history before writing the fresh state.
		for _, statement := range []string{
			`DELETE FROM orders`, `DELETE FROM trades`, `DELETE FROM candles`,
			`DELETE FROM positions`, `DELETE FROM balances WHERE asset <> 'INR'`,
		} {
			b.Queue(statement)
		}
	}
	for _, item := range cs.instruments {
		b.Queue(`INSERT INTO instruments (symbol, name, tick_size, initial_price, last_price, high, low, volume, halted,
				day_open, day_start, seq, trade_seq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (symbol) DO UPDATE SET last_price = EXCLUDED.last_price, high = EXCLUDED.high,
				low = EXCLUDED.low, volume = EXCLUDED.volume, halted = EXCLUDED.halted, day_open = EXCLUDED.day_open,
				day_start = EXCLUDED.day_start, seq = EXCLUDED.seq, trade_seq = EXCLUDED.trade_seq`,
			item.Symbol, item.Name, item.TickSize, item.InitialPrice, item.LastPrice, item.High, item.Low, item.Volume, item.Halted,
			item.DayOpen, item.DayStart, int64(item.Seq), int64(item.TradeSeq))
	}
	for _, user := range cs.accounts {
		b.Queue(`INSERT INTO users (id, email, password_hash, display_name, is_bot, is_admin, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO UPDATE SET display_name = EXCLUDED.display_name, expires_at = EXCLUDED.expires_at,
				is_admin = EXCLUDED.is_admin`,
			user.ID, nullString(user.Email), nullString(user.PasswordHash), user.Name, user.IsBot, user.IsAdmin, nullTime(user.ExpiresAt))
		queueBalance(b, user.ID, "INR", user.Cash)
		for _, symbol := range sortedKeys(user.Shares) {
			queueBalance(b, user.ID, symbol, user.Shares[symbol])
		}
		for _, symbol := range sortedKeys(user.Positions) {
			p := user.Positions[symbol]
			b.Queue(`INSERT INTO positions (user_id, symbol, quantity, cost, realized) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (user_id, symbol) DO UPDATE SET quantity = EXCLUDED.quantity, cost = EXCLUDED.cost, realized = EXCLUDED.realized`,
				user.ID, symbol, p.Quantity, p.Cost, p.Realized)
		}
	}
	for _, token := range cs.tokens {
		b.Queue(`INSERT INTO refresh_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3) ON CONFLICT (token_hash) DO NOTHING`,
			token.Hash, token.Record.UserID, token.Record.ExpiresAt)
	}
	for _, order := range cs.orders {
		tif := order.TIF
		if tif == "" {
			tif = engine.GTC
		}
		b.Queue(`INSERT INTO orders (id, seq, user_id, symbol, side, type, tif, price, quantity, filled_qty, status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (id) DO UPDATE SET filled_qty = EXCLUDED.filled_qty, status = EXCLUDED.status`,
			order.ID, int64(order.Seq), order.UserID, order.Symbol, string(order.Side), string(order.Type), string(tif),
			order.Price, order.Quantity, order.FilledQty, string(order.Status), order.CreatedAt)
	}
	for _, row := range cs.trades {
		trade := row.Trade
		b.Queue(`INSERT INTO trades (id, symbol, buy_order_id, sell_order_id, buyer_id, seller_id, price, quantity, taker_side,
				created_at, involves_human, symbol_seq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT (id) DO NOTHING`,
			trade.ID, trade.Symbol, trade.BuyOrder, trade.SellOrder, trade.BuyerID, trade.SellerID,
			trade.Price, trade.Quantity, string(trade.TakerSide), trade.CreatedAt, row.InvolvesHuman, int64(trade.Seq))
	}
	for _, row := range cs.candles {
		c := row.candle
		b.Queue(`INSERT INTO candles (symbol, interval, bucket_start, open, high, low, close, volume)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (symbol, interval, bucket_start) DO UPDATE SET open = EXCLUDED.open, high = EXCLUDED.high,
				low = EXCLUDED.low, close = EXCLUDED.close, volume = EXCLUDED.volume`,
			row.key.symbol, row.key.interval, c.Time, c.Open, c.High, c.Low, c.Close, c.Volume)
	}
	if cs.botSettings != nil {
		encoded, _ := json.Marshal(cs.botSettings)
		b.Queue(`UPDATE exchange_state SET bot_settings = $1::jsonb WHERE id = 1`, string(encoded))
	}
	for _, id := range cs.deletedOrders {
		b.Queue(`DELETE FROM orders WHERE id = $1`, id)
	}
	for _, hash := range cs.deletedTokens {
		b.Queue(`DELETE FROM refresh_tokens WHERE token_hash = $1`, hash)
	}
	for _, id := range cs.deletedAccounts {
		b.Queue(`DELETE FROM users WHERE id = $1`, id)
	}
}

func queueBalance(b *pgx.Batch, userID, asset string, held balance) {
	b.Queue(`INSERT INTO balances (user_id, asset, available, reserved) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, asset) DO UPDATE SET available = EXCLUDED.available, reserved = EXCLUDED.reserved`,
		userID, asset, held.Available, held.Reserved)
}

// pruneBotTrades keeps the free-tier database small: trades between two bots
// only feed the tape and are dropped after a day.
func (st *store) pruneBotTrades() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cutoff := time.Now().Add(-botTradeLifetime).UnixMilli()
	tag, err := st.pool.Exec(ctx, `DELETE FROM trades WHERE NOT involves_human AND created_at < $1`, cutoff)
	if err != nil {
		slog.Warn("prune bot trades", "error", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("pruned bot-only trades", "count", tag.RowsAffected())
	}
	// Drop candles older than the in-memory window, which is all that is served.
	for _, interval := range candleIntervals[:2] {
		cutoff := time.Now().UnixMilli() - interval.ms*int64(interval.keep+60)
		if _, err := st.pool.Exec(ctx, `DELETE FROM candles WHERE interval = $1 AND bucket_start < $2`, interval.name, cutoff); err != nil {
			slog.Warn("prune candles", "interval", interval.name, "error", err)
		}
	}
}

// close stops accepting changesets, waits for queued ones to commit, and
// releases the pool. The caller must not hold server.mu.
func (s *server) closeStore() {
	s.mu.Lock()
	st := s.store
	if st == nil || st.closed {
		s.mu.Unlock()
		return
	}
	st.closed = true
	close(st.queue)
	s.mu.Unlock()
	<-st.done
	st.pool.Close()
}

// commitLocked snapshots everything marked dirty since the last commit and
// queues it for the writer. The caller must hold s.mu.
func (s *server) commitLocked() commitWait {
	if len(s.dirtyAccounts)+len(s.dirtyOrders)+len(s.dirtyTokens)+len(s.dirtyInstruments)+len(s.dirtyBooks)+
		len(s.dirtyCandles)+len(s.pendingTrades) == 0 && !s.dirtyBots && !s.resetPending {
		return nil
	}
	events := s.collectEvents()
	if s.store == nil || s.store.closed {
		if s.store != nil {
			slog.Warn("persistence is closed; dropping late changes")
		}
		s.clearDirty()
		s.hub.publish(events)
		return nil
	}
	cs := &changeset{
		orderSeq: s.orderSeq, tradeSeq: s.tradeSeq, trades: s.pendingTrades, reset: s.resetPending,
		events: events, result: make(chan error, 1),
	}
	if s.dirtyBots {
		settings := s.bots
		cs.botSettings = &settings
	}
	for key := range s.dirtyCandles {
		if c, ok := s.candleAt(key); ok {
			cs.candles = append(cs.candles, candleRow{key, c})
		}
	}
	for _, symbol := range sortedKeys(s.dirtyInstruments) {
		cs.instruments = append(cs.instruments, *s.instruments[symbol])
	}
	for _, id := range sortedKeys(s.dirtyAccounts) {
		if user, exists := s.accounts[id]; exists {
			cs.accounts = append(cs.accounts, snapshotAccount(user))
		} else {
			cs.deletedAccounts = append(cs.deletedAccounts, id)
		}
	}
	for _, id := range sortedKeys(s.dirtyOrders) {
		if order, exists := s.orders[id]; exists {
			cs.orders = append(cs.orders, order)
		} else {
			cs.deletedOrders = append(cs.deletedOrders, id)
		}
	}
	for _, hash := range sortedKeys(s.dirtyTokens) {
		if record, exists := s.refreshTokens[hash]; exists {
			cs.tokens = append(cs.tokens, tokenRow{Hash: hash, Record: record})
		} else {
			cs.deletedTokens = append(cs.deletedTokens, hash)
		}
	}
	s.clearDirty()
	s.store.queue <- cs
	return cs.result
}

// commitAndUnlock queues pending changes, releases s.mu, and waits until they
// are durable. Handlers call it before answering a request.
func (s *server) commitAndUnlock() error {
	wait := s.commitLocked()
	s.mu.Unlock()
	return wait.wait()
}

func (s *server) clearDirty() {
	clear(s.dirtyAccounts)
	clear(s.dirtyOrders)
	clear(s.dirtyTokens)
	clear(s.dirtyInstruments)
	clear(s.dirtyBooks)
	clear(s.dirtyCandles)
	s.dirtyBots, s.resetPending = false, false
	s.pendingTrades = nil
}

func (s *server) markAccount(id string)     { s.dirtyAccounts[id] = true }
func (s *server) markOrder(id string)       { s.dirtyOrders[id] = true }
func (s *server) markToken(hash string)     { s.dirtyTokens[hash] = true }
func (s *server) markInstrument(sym string) { s.dirtyInstruments[sym] = true }

func (s *server) markAllDirty() {
	for symbol := range s.instruments {
		s.markInstrument(symbol)
	}
	for id := range s.accounts {
		s.markAccount(id)
	}
	for id := range s.orders {
		s.markOrder(id)
	}
	for hash := range s.refreshTokens {
		s.markToken(hash)
	}
	for symbol, series := range s.candles {
		for interval, list := range series {
			for _, c := range list {
				s.dirtyCandles[candleKey{symbol, interval, c.Time}] = true
			}
		}
	}
	s.dirtyBots = true
}

func snapshotAccount(user *account) accountRow {
	row := accountRow{
		ID: user.ID, Name: user.Name, Email: user.Email, PasswordHash: user.PasswordHash,
		IsBot: user.IsBot, IsAdmin: user.IsAdmin, ExpiresAt: user.ExpiresAt, Cash: user.Cash,
		Shares: make(map[string]balance, len(user.Shares)), Positions: make(map[string]position, len(user.Positions)),
	}
	for symbol, held := range user.Shares {
		row.Shares[symbol] = held
	}
	for symbol, p := range user.Positions {
		row.Positions[symbol] = *p
	}
	return row
}

// reservationMismatches recomputes every account's reserved balances from its
// resting orders. After a correct recovery the two always agree.
func (s *server) reservationMismatches() []string {
	cash := make(map[string]int64)
	shares := make(map[string]map[string]int64)
	for _, order := range s.orders {
		if order.Status != engine.Open && order.Status != engine.PartiallyFilled {
			continue
		}
		remaining := order.Quantity - order.FilledQty
		if order.Side == engine.Buy {
			cash[order.UserID] += order.Price * remaining
		} else {
			if shares[order.UserID] == nil {
				shares[order.UserID] = make(map[string]int64)
			}
			shares[order.UserID][order.Symbol] += remaining
		}
	}
	var problems []string
	for _, id := range sortedKeys(s.accounts) {
		user := s.accounts[id]
		if user.Cash.Reserved != cash[id] {
			problems = append(problems, fmt.Sprintf("%s reserves %d paise but its open buys need %d", id, user.Cash.Reserved, cash[id]))
		}
		for _, symbol := range sortedKeys(user.Shares) {
			if want := shares[id][symbol]; user.Shares[symbol].Reserved != want {
				problems = append(problems, fmt.Sprintf("%s reserves %d %s but its open sells need %d", id, user.Shares[symbol].Reserved, symbol, want))
			}
		}
	}
	return problems
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func nullString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
