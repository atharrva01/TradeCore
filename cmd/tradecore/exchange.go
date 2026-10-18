package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"tradecore/internal/engine"
)

const (
	startingCash   int64 = 100_000_000
	maxPrice       int64 = 100_000_000 // ₹10,00,000 per share
	maxQuantity    int64 = 1_000_000
	botCash        int64 = 9_000_000_000_000_000
	botShares      int64 = 1_000_000_000
	defaultTick    int64 = 5
	guestLifetime        = 24 * time.Hour
	tapeLength           = 10_000
	userTradeLimit       = 500
)

type instrument struct {
	Symbol       string  `json:"symbol"`
	Name         string  `json:"name"`
	LastPrice    int64   `json:"lastPrice"`
	InitialPrice int64   `json:"initialPrice"`
	DayOpen      int64   `json:"dayOpen"`
	High         int64   `json:"high"`
	Low          int64   `json:"low"`
	Volume       int64   `json:"volume"`
	ChangePct    float64 `json:"changePct"`
	TickSize     int64   `json:"tickSize"`
	Halted       bool    `json:"halted"`
	// Seq increases with every event published for this instrument; TradeSeq
	// increases by exactly one per trade so clients can detect missed trades.
	Seq      uint64 `json:"seq"`
	TradeSeq uint64 `json:"tradeSeq"`
	DayStart int64  `json:"-"`
}

// view returns a copy with the day change filled in.
func (item instrument) view() instrument {
	if item.DayOpen > 0 {
		item.ChangePct = float64(item.LastPrice-item.DayOpen) / float64(item.DayOpen) * 100
	}
	return item
}

type balance struct {
	Available int64 `json:"available"`
	Reserved  int64 `json:"reserved"`
}

type position struct {
	Quantity int64
	Cost     int64
	Realized int64
}

type account struct {
	ID           string
	Name         string
	Email        string
	PasswordHash string
	Cash         balance
	Shares       map[string]balance
	Positions    map[string]*position
	IsBot        bool
	IsAdmin      bool
	ExpiresAt    time.Time
}

func (a *account) isGuest() bool { return !a.IsBot && a.Email == "" }

func newAccount(id, name string) *account {
	return &account{ID: id, Name: name, Shares: make(map[string]balance), Positions: make(map[string]*position)}
}

// tapeTrade is a trade as the exchange records and publishes it, carrying the
// instrument's trade sequence number.
type tapeTrade struct {
	engine.Trade
	Seq uint64 `json:"seq"`
}

type refreshRecord struct {
	UserID    string
	ExpiresAt time.Time
}

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.message }

// server is the whole exchange. Every field below mu is guarded by it: the
// books, balances and order state change together under one lock, which keeps
// settlement across instruments trivially atomic. See docs/ARCHITECTURE.md.
type server struct {
	mu            sync.Mutex
	books         map[string]*engine.Book
	instruments   map[string]*instrument
	accounts      map[string]*account
	refreshTokens map[string]refreshRecord
	orders        map[string]engine.Order
	trades        []tapeTrade
	userTrades    map[string][]tapeTrade
	candles       map[string]map[string][]candle
	recentPrices  map[string][]int64
	botOpen       map[string]map[string]bool
	bots          botSettings
	orderSeq      uint64
	tradeSeq      uint64
	leaderboard   leaderboardCache

	// Persistence; see store.go. store is nil when running in memory only.
	store            *store
	dirtyAccounts    map[string]bool
	dirtyOrders      map[string]bool
	dirtyTokens      map[string]bool
	dirtyInstruments map[string]bool
	dirtyBooks       map[string]bool
	dirtyCandles     map[candleKey]bool
	dirtyBots        bool
	resetPending     bool
	pendingTrades    []tradeRow

	// Not guarded by mu.
	jwtSecret        []byte
	secureCookies    bool
	crossSiteCookies bool
	trustProxy       bool
	adminEmails      map[string]bool
	limits           *limiters
	hub              *hub
	metrics          *metrics
	google           *googleOAuth
}

// newServer returns an in-memory exchange seeded with instruments, bots and
// initial market-maker quotes.
func newServer() *server {
	s := newBaseServer()
	s.seedDefaults()
	return s
}

// newBaseServer returns an empty exchange, ready to be seeded or restored.
func newBaseServer() *server {
	jwtSecret := make([]byte, 32)
	if _, err := rand.Read(jwtSecret); err != nil {
		panic(fmt.Errorf("generate development JWT key: %w", err))
	}
	s := &server{
		books: make(map[string]*engine.Book), instruments: make(map[string]*instrument),
		accounts: make(map[string]*account), refreshTokens: make(map[string]refreshRecord),
		orders: make(map[string]engine.Order), userTrades: make(map[string][]tapeTrade),
		candles: make(map[string]map[string][]candle), recentPrices: make(map[string][]int64),
		botOpen: make(map[string]map[string]bool), bots: defaultBotSettings(),
		dirtyAccounts: make(map[string]bool), dirtyOrders: make(map[string]bool),
		dirtyTokens: make(map[string]bool), dirtyInstruments: make(map[string]bool),
		dirtyBooks: make(map[string]bool), dirtyCandles: make(map[candleKey]bool),
		jwtSecret:        jwtSecret,
		secureCookies:    os.Getenv("COOKIE_SECURE") == "true" || os.Getenv("APP_ENV") == "production",
		crossSiteCookies: strings.EqualFold(os.Getenv("COOKIE_SAMESITE"), "none"),
		trustProxy:       os.Getenv("TRUST_PROXY") == "true",
		adminEmails:      parseEmailList(os.Getenv("ADMIN_EMAILS")),
		limits:           newLimitersFromEnv(),
		metrics:          newMetrics(),
		google:           googleOAuthFromEnv(),
	}
	// Browsers reject SameSite=None cookies that are not also Secure.
	s.secureCookies = s.secureCookies || s.crossSiteCookies
	s.hub = newHub(s)
	return s
}

func parseEmailList(raw string) map[string]bool {
	emails := make(map[string]bool)
	for _, email := range strings.Split(raw, ",") {
		if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
			emails[email] = true
		}
	}
	return emails
}

var defaultInstruments = []struct {
	symbol, name string
	price        int64
}{
	{"ATHR", "Atharva Systems", 10100},
	{"NOVA", "Nova Energy", 6840},
	{"CIRR", "Cirrus Mobility", 12550},
	{"MINT", "Mint Financial", 4325},
	{"ORBT", "Orbit Robotics", 8720},
}

func (s *server) seedDefaults() {
	for _, id := range botIDs {
		bot := newAccount(id, botNames[id])
		bot.IsBot, bot.Cash = true, balance{Available: botCash}
		s.accounts[id] = bot
		s.markAccount(id)
	}
	for _, item := range defaultInstruments {
		if _, err := s.addInstrument(item.symbol, item.name, item.price); err != nil {
			panic(err)
		}
	}
	for symbol := range s.instruments {
		s.quoteMarket(symbol)
	}
}

// addInstrument lists a new instrument and funds every bot with inventory in
// it. The caller must hold s.mu.
func (s *server) addInstrument(symbol, name string, price int64) (*instrument, *apiError) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	name = strings.TrimSpace(name)
	if len(symbol) < 2 || len(symbol) > 6 || strings.Trim(symbol, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return nil, &apiError{http.StatusBadRequest, "INVALID_SYMBOL", "Symbol must be 2 to 6 letters"}
	}
	if name == "" || len(name) > 48 {
		return nil, &apiError{http.StatusBadRequest, "INVALID_NAME", "Name must be between 1 and 48 characters"}
	}
	if price <= 0 || price > maxPrice || price%defaultTick != 0 {
		return nil, &apiError{http.StatusBadRequest, "INVALID_PRICE", "Price must be positive and aligned to the ₹0.05 tick"}
	}
	if _, exists := s.instruments[symbol]; exists {
		return nil, &apiError{http.StatusConflict, "SYMBOL_TAKEN", "That symbol is already listed"}
	}
	item := &instrument{
		Symbol: symbol, Name: name, LastPrice: price, InitialPrice: price, DayOpen: price,
		High: price, Low: price, TickSize: defaultTick, DayStart: istDayStart(time.Now()),
	}
	s.instruments[symbol] = item
	s.books[symbol] = engine.NewBook(symbol)
	s.markInstrument(symbol)
	for _, id := range botIDs {
		if bot := s.accounts[id]; bot != nil {
			held := bot.Shares[symbol]
			held.Available += botShares
			bot.Shares[symbol] = held
			s.markAccount(id)
		}
	}
	return item, nil
}

func (s *server) nextID(prefix string) string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(bytes)
}

func (s *server) nextTradeID() string {
	s.tradeSeq++
	return fmt.Sprintf("trade-%d", s.tradeSeq)
}

type orderRequest struct {
	Symbol      string             `json:"symbol"`
	Side        engine.Side        `json:"side"`
	Type        engine.OrderType   `json:"type"`
	TimeInForce engine.TimeInForce `json:"timeInForce"`
	Price       int64              `json:"price"`
	Quantity    int64              `json:"quantity"`
}

// submit is the single path every order takes, from humans and bots alike:
// validate, reserve, match, apply the book's side effects, settle each fill,
// and release whatever reservation the order no longer needs. The caller must
// hold s.mu. Rate limits are applied by the HTTP handler, not here.
func (s *server) submit(userID string, input orderRequest) (engine.Result, *apiError) {
	input.Symbol = strings.ToUpper(strings.TrimSpace(input.Symbol))
	item, exists := s.instruments[input.Symbol]
	if !exists {
		return engine.Result{}, &apiError{http.StatusBadRequest, "UNKNOWN_INSTRUMENT", "Instrument not found"}
	}
	if item.Halted {
		return engine.Result{}, &apiError{http.StatusConflict, "INSTRUMENT_HALTED", "Trading is halted for this instrument"}
	}
	if input.Quantity <= 0 || input.Quantity > maxQuantity {
		return engine.Result{}, &apiError{http.StatusBadRequest, "INVALID_QUANTITY", "Quantity must be between 1 and 1,000,000"}
	}
	if input.Side != engine.Buy && input.Side != engine.Sell {
		return engine.Result{}, &apiError{http.StatusBadRequest, "INVALID_SIDE", "Side must be BUY or SELL"}
	}
	if input.Type != engine.Limit && input.Type != engine.Market {
		return engine.Result{}, &apiError{http.StatusBadRequest, "INVALID_TYPE", "Type must be LIMIT or MARKET"}
	}
	if input.TimeInForce == "" {
		input.TimeInForce = engine.GTC
	}
	if input.TimeInForce != engine.GTC && input.TimeInForce != engine.IOC {
		return engine.Result{}, &apiError{http.StatusBadRequest, "INVALID_TIME_IN_FORCE", "Time in force must be GTC or IOC"}
	}
	if input.Type == engine.Limit && (input.Price <= 0 || input.Price > maxPrice || input.Price%item.TickSize != 0) {
		return engine.Result{}, &apiError{http.StatusBadRequest, "INVALID_PRICE", "Limit price must be positive, at most ₹10,00,000, and aligned to the ₹0.05 tick"}
	}
	if input.Type == engine.Market {
		// Market orders never rest, so they are inherently immediate-or-cancel.
		input.Price, input.TimeInForce = 0, engine.IOC
	}
	user := s.accounts[userID]
	if user == nil {
		return engine.Result{}, &apiError{http.StatusUnauthorized, "UNAUTHORIZED", "Session is no longer valid"}
	}

	var reservedCash int64
	if input.Side == engine.Buy {
		// maxPrice × maxQuantity fits comfortably in int64, so this cannot overflow.
		amount := user.Cash.Available
		if input.Type == engine.Limit {
			amount = input.Price * input.Quantity
		}
		if amount <= 0 || user.Cash.Available < amount {
			return engine.Result{}, &apiError{http.StatusBadRequest, "INSUFFICIENT_FUNDS", "Not enough available cash"}
		}
		user.Cash.Available -= amount
		user.Cash.Reserved += amount
		reservedCash = amount
	} else {
		shares := user.Shares[input.Symbol]
		if shares.Available < input.Quantity {
			return engine.Result{}, &apiError{http.StatusBadRequest, "INSUFFICIENT_FUNDS", "Not enough available shares"}
		}
		shares.Available -= input.Quantity
		shares.Reserved += input.Quantity
		user.Shares[input.Symbol] = shares
	}
	s.markAccount(userID)

	s.orderSeq++
	order := engine.Order{
		ID: s.nextID("order-"), UserID: userID, Symbol: input.Symbol, Side: input.Side,
		Type: input.Type, TIF: input.TimeInForce, Price: input.Price, Quantity: input.Quantity,
		CreatedAt: time.Now().UnixMilli(), Seq: s.orderSeq,
	}
	if input.Type == engine.Market && input.Side == engine.Buy {
		order.Budget = reservedCash
	}
	s.orders[order.ID] = order
	started := time.Now()
	result, err := s.books[input.Symbol].Place(order, order.CreatedAt, s.nextTradeID)
	s.metrics.observeMatch(time.Since(started), user.IsBot)
	if err != nil {
		s.releaseOrderReservation(user, order, reservedCash)
		delete(s.orders, order.ID)
		return engine.Result{}, &apiError{http.StatusBadRequest, "INVALID_ORDER", "The order could not be accepted"}
	}
	s.dirtyBooks[input.Symbol] = true

	// Apply everything the book did before deciding how to respond, so orders
	// cancelled by self-trade prevention always release their reservations.
	for _, removed := range result.Removed {
		s.orders[removed.ID] = removed
		s.markOrder(removed.ID)
		s.releaseOrderReservation(s.accounts[removed.UserID], removed, 0)
	}
	for _, updated := range result.Updated {
		s.orders[updated.ID] = updated
		s.markOrder(updated.ID)
	}
	spent := int64(0)
	for _, fill := range result.Fills {
		s.settle(fill.Trade)
		s.recordTrade(fill.Trade)
		spent += fill.Trade.Price * fill.Trade.Quantity
	}
	if result.Order.Type == engine.Market && result.Order.Side == engine.Buy {
		// A market buy reserved all available cash; return whatever it did
		// not spend, whether it filled completely or not.
		refund := reservedCash - spent
		user.Cash.Reserved -= refund
		user.Cash.Available += refund
	} else if result.Order.Status == engine.Cancelled {
		// A market sell or IOC remainder: release what it no longer needs.
		s.releaseOrderReservation(user, result.Order, 0)
	}
	var noFill *apiError
	if result.Order.Type == engine.Market && result.Order.FilledQty == 0 {
		result.Order.Status = engine.Rejected
		bids, asks := s.books[input.Symbol].Snapshot()
		if (input.Side == engine.Buy && len(asks) > 0) || (input.Side == engine.Sell && len(bids) > 0) {
			noFill = &apiError{http.StatusBadRequest, "INSUFFICIENT_FUNDS", "Not enough available cash for one share at the best price"}
		} else {
			noFill = &apiError{http.StatusBadRequest, "NO_LIQUIDITY", "No executable liquidity on the other side of the book"}
		}
	}
	s.orders[result.Order.ID] = result.Order
	s.markOrder(result.Order.ID)
	s.trackBotOrders(result)
	return result, noFill
}

func (s *server) settle(trade engine.Trade) {
	buyerOrder := s.orders[trade.BuyOrder]
	buyer, seller := s.accounts[trade.BuyerID], s.accounts[trade.SellerID]
	value := trade.Price * trade.Quantity
	buyerReserve := value
	if buyerOrder.Type == engine.Limit {
		buyerReserve = buyerOrder.Price * trade.Quantity
	}
	s.markAccount(buyer.ID)
	s.markAccount(seller.ID)
	buyer.Cash.Reserved -= buyerReserve
	buyer.Cash.Available += buyerReserve - value
	seller.Cash.Available += value

	sellerShares := seller.Shares[trade.Symbol]
	sellerShares.Reserved -= trade.Quantity
	seller.Shares[trade.Symbol] = sellerShares
	buyerShares := buyer.Shares[trade.Symbol]
	buyerShares.Available += trade.Quantity
	buyer.Shares[trade.Symbol] = buyerShares
	if !buyer.IsBot {
		applyPurchase(buyer.Positions, trade.Symbol, trade.Quantity, value)
	}
	if !seller.IsBot {
		applySale(seller.Positions, trade.Symbol, trade.Quantity, value)
	}
}

func applyPurchase(positions map[string]*position, symbol string, quantity, cost int64) {
	p := positions[symbol]
	if p == nil {
		p = &position{}
		positions[symbol] = p
	}
	p.Quantity += quantity
	p.Cost += cost
}

func applySale(positions map[string]*position, symbol string, quantity, proceeds int64) {
	p := positions[symbol]
	if p == nil || p.Quantity < quantity {
		slog.Warn("position smaller than sale; realized P&L skipped", "symbol", symbol, "quantity", quantity)
		return
	}
	cost := p.Cost * quantity / p.Quantity
	p.Quantity -= quantity
	p.Cost -= cost
	p.Realized += proceeds - cost
}

func (s *server) releaseOrderReservation(user *account, order engine.Order, marketCash int64) {
	if user == nil {
		return
	}
	s.markAccount(user.ID)
	remaining := order.Quantity - order.FilledQty
	if order.Side == engine.Buy {
		amount := marketCash
		if order.Type == engine.Limit {
			amount = order.Price * remaining
		}
		if amount > user.Cash.Reserved {
			amount = user.Cash.Reserved
		}
		user.Cash.Reserved -= amount
		user.Cash.Available += amount
		return
	}
	shares := user.Shares[order.Symbol]
	if remaining > shares.Reserved {
		remaining = shares.Reserved
	}
	shares.Reserved -= remaining
	shares.Available += remaining
	user.Shares[order.Symbol] = shares
}

// cancelLocked cancels a resting order on behalf of its owner. The caller
// must hold s.mu.
func (s *server) cancelLocked(id, userID string) (engine.Order, bool) {
	order, exists := s.orders[id]
	if !exists || order.UserID != userID || (order.Status != engine.Open && order.Status != engine.PartiallyFilled) {
		return engine.Order{}, false
	}
	cancelled, err := s.books[order.Symbol].Cancel(id, userID)
	if err != nil {
		return engine.Order{}, false
	}
	s.orders[id] = cancelled
	s.markOrder(id)
	s.dirtyBooks[order.Symbol] = true
	s.releaseOrderReservation(s.accounts[userID], cancelled, 0)
	s.trackBotOrders(engine.Result{Order: cancelled})
	return cancelled, true
}

func (s *server) recordTrade(trade engine.Trade) {
	item := s.instruments[trade.Symbol]
	s.rollDay(item, time.UnixMilli(trade.CreatedAt))
	item.Seq++
	item.TradeSeq++
	tape := tapeTrade{Trade: trade, Seq: item.TradeSeq}
	buyer, seller := s.accounts[trade.BuyerID], s.accounts[trade.SellerID]
	human := buyer == nil || !buyer.IsBot || seller == nil || !seller.IsBot
	s.pendingTrades = append(s.pendingTrades, tradeRow{Trade: tape, InvolvesHuman: human})
	s.markInstrument(trade.Symbol)
	s.trades = append(s.trades, tape)
	if len(s.trades) > tapeLength {
		s.trades = s.trades[len(s.trades)-tapeLength:]
	}
	for _, party := range []*account{buyer, seller} {
		if party != nil && !party.IsBot {
			history := append(s.userTrades[party.ID], tape)
			if len(history) > userTradeLimit {
				history = history[len(history)-userTradeLimit:]
			}
			s.userTrades[party.ID] = history
		}
	}
	item.LastPrice = trade.Price
	item.Volume += trade.Quantity
	item.High = max(item.High, trade.Price)
	item.Low = min(item.Low, trade.Price)
	s.updateCandles(trade)
	prices := append(s.recentPrices[trade.Symbol], trade.Price)
	if len(prices) > momentumWindow {
		prices = prices[len(prices)-momentumWindow:]
	}
	s.recentPrices[trade.Symbol] = prices
	s.metrics.observeTrade()
}

// rollDay starts a new trading day (IST midnight) for the instrument: the day
// open becomes the last price and the day's range and volume reset.
func (s *server) rollDay(item *instrument, now time.Time) {
	if start := istDayStart(now); start > item.DayStart {
		item.DayStart, item.DayOpen = start, item.LastPrice
		item.High, item.Low, item.Volume = item.LastPrice, item.LastPrice, 0
		s.markInstrument(item.Symbol)
	}
}

// trackBotOrders keeps each bot's set of resting orders current and drops
// finished bot orders, which nobody needs to see in history.
func (s *server) trackBotOrders(result engine.Result) {
	candidates := append([]engine.Order{result.Order}, result.Updated...)
	candidates = append(candidates, result.Removed...)
	for _, order := range candidates {
		user := s.accounts[order.UserID]
		if user == nil || !user.IsBot {
			continue
		}
		open := s.botOpen[order.UserID]
		if open == nil {
			open = make(map[string]bool)
			s.botOpen[order.UserID] = open
		}
		if order.Status == engine.Open || order.Status == engine.PartiallyFilled {
			open[order.ID] = true
		} else {
			delete(open, order.ID)
			delete(s.orders, order.ID)
			s.markOrder(order.ID)
		}
	}
}

// purgeExpired removes guest accounts past their lifetime, cancelling their
// resting orders, and drops stale refresh tokens and rate-limit buckets.
func (s *server) purgeExpired(now time.Time) {
	s.mu.Lock()
	defer func() { s.commitLocked(); s.mu.Unlock() }()
	expired := make(map[string]bool)
	for id, user := range s.accounts {
		if !user.IsBot && !user.ExpiresAt.IsZero() && now.After(user.ExpiresAt) {
			expired[id] = true
		}
	}
	for id, order := range s.orders {
		if !expired[order.UserID] {
			continue
		}
		if order.Status == engine.Open || order.Status == engine.PartiallyFilled {
			_, _ = s.books[order.Symbol].Cancel(id, order.UserID)
			s.dirtyBooks[order.Symbol] = true
		}
		delete(s.orders, id)
		s.markOrder(id)
	}
	for id := range expired {
		delete(s.accounts, id)
		delete(s.userTrades, id)
		s.markAccount(id)
	}
	for hash, record := range s.refreshTokens {
		if expired[record.UserID] || now.After(record.ExpiresAt) {
			delete(s.refreshTokens, hash)
			s.markToken(hash)
		}
	}
	if len(expired) > 0 {
		slog.Info("purged expired guests", "count", len(expired))
	}
	s.limits.prune(now)
}

// resetMarket starts a new season: every order, trade and candle is cleared,
// prices return to their listing price, and every person gets fresh starting
// cash. The caller must hold s.mu.
func (s *server) resetMarket() {
	for symbol, item := range s.instruments {
		s.books[symbol] = engine.NewBook(symbol)
		item.LastPrice, item.DayOpen = item.InitialPrice, item.InitialPrice
		item.High, item.Low, item.Volume = item.InitialPrice, item.InitialPrice, 0
		item.DayStart = istDayStart(time.Now())
		item.Seq++
		s.markInstrument(symbol)
		s.dirtyBooks[symbol] = true
	}
	s.orders = make(map[string]engine.Order)
	s.trades = nil
	s.userTrades = make(map[string][]tapeTrade)
	s.candles = make(map[string]map[string][]candle)
	s.recentPrices = make(map[string][]int64)
	s.botOpen = make(map[string]map[string]bool)
	s.pendingTrades = nil
	clear(s.dirtyOrders)
	clear(s.dirtyCandles)
	for id, user := range s.accounts {
		user.Shares = make(map[string]balance)
		user.Positions = make(map[string]*position)
		if user.IsBot {
			user.Cash = balance{Available: botCash}
			for symbol := range s.instruments {
				user.Shares[symbol] = balance{Available: botShares}
			}
		} else {
			user.Cash = balance{Available: startingCash}
		}
		s.markAccount(id)
	}
	s.leaderboard = leaderboardCache{}
	s.resetPending = true
	for symbol := range s.instruments {
		s.quoteMarket(symbol)
	}
	slog.Info("market reset for a new season")
}
