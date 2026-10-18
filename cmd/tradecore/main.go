package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"tradecore/internal/engine"
)

const (
	startingCash    int64 = 100_000_000
	maxPrice        int64 = 100_000_000 // ₹10,00,000 per share
	maxQuantity     int64 = 1_000_000
	marketMakerID         = "market-maker"
	noiseTraderID         = "noise-trader"
	guestLifetime         = 24 * time.Hour
	authAttempts          = 10
	authWindow            = time.Minute
	ordersPerSecond       = 10
)

type instrument struct {
	Symbol       string  `json:"symbol"`
	Name         string  `json:"name"`
	LastPrice    int64   `json:"lastPrice"`
	InitialPrice int64   `json:"initialPrice"`
	High         int64   `json:"high"`
	Low          int64   `json:"low"`
	ChangePct    float64 `json:"changePct"`
	TickSize     int64   `json:"tickSize"`
	Volume       int64   `json:"volume"`
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
	Name         string
	Email        string
	PasswordHash string
	Cash         balance
	Shares       map[string]balance
	Positions    map[string]*position
	IsBot        bool
	ExpiresAt    time.Time
}

type server struct {
	mu               sync.Mutex
	books            map[string]*engine.Book
	instruments      map[string]*instrument
	accounts         map[string]*account
	refreshTokens    map[string]refreshRecord
	jwtSecret        []byte
	orders           map[string]engine.Order
	trades           []engine.Trade
	orderSeq         uint64
	tradeSeq         uint64
	orderTimes       map[string][]time.Time
	authTimes        map[string][]time.Time
	halted           map[string]bool
	secureCookies    bool
	crossSiteCookies bool
	trustProxy       bool
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

func main() {
	s := newServer()
	production := os.Getenv("APP_ENV") == "production"
	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		if len(secret) < 32 {
			log.Fatal("JWT_SECRET must be at least 32 bytes")
		}
		s.jwtSecret = []byte(secret)
	} else if production {
		log.Fatal("JWT_SECRET is required in production")
	} else {
		log.Print("JWT_SECRET is unset; using an ephemeral development key")
	}
	origins := allowedOrigins(os.Getenv("FRONTEND_ORIGIN"))
	if production && len(origins) == 0 {
		log.Fatal("FRONTEND_ORIGIN is required in production")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
		if port := os.Getenv("PORT"); port != "" {
			addr = ":" + port
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go s.runBots(ctx)

	httpServer := &http.Server{
		Addr: addr, Handler: cors(origins, s.routes()),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 1 << 16,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("TradeCore API listening on %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.health)
	mux.HandleFunc("/api/v1/auth/guest", s.guest)
	mux.HandleFunc("/api/v1/auth/signup", s.signup)
	mux.HandleFunc("/api/v1/auth/login", s.login)
	mux.HandleFunc("/api/v1/auth/refresh", s.refresh)
	mux.HandleFunc("/api/v1/auth/logout", s.logout)
	mux.HandleFunc("/api/v1/instruments", s.listInstruments)
	mux.HandleFunc("/api/v1/instruments/", s.instrumentRoute)
	mux.HandleFunc("/api/v1/orders", s.ordersRoute)
	mux.HandleFunc("/api/v1/orders/", s.cancelOrder)
	mux.HandleFunc("/api/v1/trades/me", s.myTrades)
	mux.HandleFunc("/api/v1/portfolio", s.portfolio)
	mux.HandleFunc("/api/v1/leaderboard", s.leaderboard)
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Endpoint not found")
	})
	return mux
}

func newServer() *server {
	jwtSecret := make([]byte, 32)
	if _, err := rand.Read(jwtSecret); err != nil {
		panic(fmt.Errorf("generate development JWT key: %w", err))
	}
	s := &server{
		books: make(map[string]*engine.Book), instruments: make(map[string]*instrument),
		accounts: make(map[string]*account), refreshTokens: make(map[string]refreshRecord),
		orders: make(map[string]engine.Order), orderTimes: make(map[string][]time.Time),
		authTimes: make(map[string][]time.Time), halted: make(map[string]bool),
		jwtSecret:        jwtSecret,
		secureCookies:    os.Getenv("COOKIE_SECURE") == "true" || os.Getenv("APP_ENV") == "production",
		crossSiteCookies: strings.EqualFold(os.Getenv("COOKIE_SAMESITE"), "none"),
		trustProxy:       os.Getenv("TRUST_PROXY") == "true",
	}
	// Browsers reject SameSite=None cookies that are not also Secure.
	s.secureCookies = s.secureCookies || s.crossSiteCookies
	for _, item := range []instrument{
		{Symbol: "ATHR", Name: "Atharva Systems", LastPrice: 10100, TickSize: 5},
		{Symbol: "NOVA", Name: "Nova Energy", LastPrice: 6840, TickSize: 5},
		{Symbol: "CIRR", Name: "Cirrus Mobility", LastPrice: 12550, TickSize: 5},
		{Symbol: "MINT", Name: "Mint Financial", LastPrice: 4325, TickSize: 5},
		{Symbol: "ORBT", Name: "Orbit Robotics", LastPrice: 8720, TickSize: 5},
	} {
		copy := item
		copy.InitialPrice, copy.High, copy.Low = item.LastPrice, item.LastPrice, item.LastPrice
		s.instruments[item.Symbol] = &copy
		s.books[item.Symbol] = engine.NewBook(item.Symbol)
	}
	for id, name := range map[string]string{marketMakerID: "Market Maker", noiseTraderID: "Noise Trader"} {
		bot := &account{
			Name: name, Cash: balance{Available: 9_000_000_000_000_000},
			Shares: make(map[string]balance), Positions: make(map[string]*position), IsBot: true,
		}
		for symbol := range s.instruments {
			bot.Shares[symbol] = balance{Available: 1_000_000_000}
		}
		s.accounts[id] = bot
	}
	for symbol, item := range s.instruments {
		s.seedQuote(symbol, item.LastPrice)
	}
	return s
}

// seedQuote places two-sided market-maker liquidity around mid. It goes
// through submit like every other order, so crossing quotes settle normally.
func (s *server) seedQuote(symbol string, mid int64) {
	for _, quote := range []struct {
		side  engine.Side
		price int64
		qty   int64
	}{{engine.Buy, mid - 25, 120}, {engine.Buy, mid - 50, 250}, {engine.Sell, mid + 25, 120}, {engine.Sell, mid + 50, 250}} {
		_, err := s.submit(marketMakerID, orderRequest{
			Symbol: symbol, Side: quote.side, Type: engine.Limit, Price: quote.price, Quantity: quote.qty,
		})
		if err != nil {
			log.Printf("market maker quote %s %s @ %d rejected: %v", symbol, quote.side, quote.price, err)
		}
	}
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

func (s *server) runBots(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for tick := 0; ; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.runBotTick()
		if tick%30 == 0 {
			s.purgeExpired(time.Now())
		}
	}
}

func (s *server) runBotTick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for symbol, item := range s.instruments {
		if s.halted[symbol] {
			continue
		}
		book := s.books[symbol]
		bids, asks := book.Snapshot()
		if len(bids) < 2 || len(asks) < 2 {
			s.seedQuote(symbol, item.LastPrice)
			bids, asks = book.Snapshot()
		}
		side, levels := engine.Sell, bids
		if mathrand.IntN(2) == 0 {
			side, levels = engine.Buy, asks
		}
		if len(levels) == 0 {
			continue
		}
		_, err := s.submit(noiseTraderID, orderRequest{
			Symbol: symbol, Side: side, Type: engine.Limit,
			Price: levels[0].Price, Quantity: int64(mathrand.IntN(5) + 1),
		})
		if err != nil {
			log.Printf("noise bot order %s rejected: %v", symbol, err)
		}
	}
}

// purgeExpired removes guest accounts past their lifetime, cancelling their
// resting orders, and drops stale rate-limit and refresh-token entries.
func (s *server) purgeExpired(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
			s.books[order.Symbol].Cancel(id, order.UserID)
		}
		delete(s.orders, id)
	}
	for id := range expired {
		delete(s.accounts, id)
		delete(s.orderTimes, id)
	}
	for hash, record := range s.refreshTokens {
		if expired[record.UserID] || now.After(record.ExpiresAt) {
			delete(s.refreshTokens, hash)
		}
	}
	for key, times := range s.orderTimes {
		if len(times) == 0 || now.Sub(times[len(times)-1]) > time.Second {
			delete(s.orderTimes, key)
		}
	}
	for key, times := range s.authTimes {
		if len(times) == 0 || now.Sub(times[len(times)-1]) > authWindow {
			delete(s.authTimes, key)
		}
	}
}

func (s *server) recordTrade(trade engine.Trade) {
	s.trades = append(s.trades, trade)
	if len(s.trades) > 10_000 {
		s.trades = s.trades[len(s.trades)-10_000:]
	}
	item := s.instruments[trade.Symbol]
	item.LastPrice = trade.Price
	item.Volume += trade.Quantity
	item.High = max(item.High, trade.Price)
	item.Low = min(item.Low, trade.Price)
}

func (s *server) cleanupBotOrders(result engine.Result) {
	candidates := append([]engine.Order{result.Order}, result.Updated...)
	candidates = append(candidates, result.Removed...)
	for _, order := range candidates {
		user := s.accounts[order.UserID]
		if user != nil && user.IsBot && (order.Status == engine.Filled || order.Status == engine.Cancelled) {
			delete(s.orders, order.ID)
		}
	}
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) guest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A valid JSON request is required")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "Guest Trader"
	}
	if len(name) > 32 {
		writeError(w, http.StatusBadRequest, "INVALID_NAME", "Display name must be 32 characters or fewer")
		return
	}
	userID := s.nextID("user-")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.allowAuthAttempt(r, time.Now()) {
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many attempts; try again in a minute")
		return
	}
	user := &account{
		Name: name, Cash: balance{Available: startingCash},
		Shares: make(map[string]balance), Positions: make(map[string]*position),
		ExpiresAt: time.Now().Add(guestLifetime),
	}
	s.accounts[userID] = user
	s.createSession(w, userID, user, http.StatusCreated)
}

// allowAuthAttempt is a per-IP sliding window for account creation and login.
// The caller must hold s.mu.
func (s *server) allowAuthAttempt(r *http.Request, now time.Time) bool {
	ip := s.clientIP(r)
	recent := s.authTimes[ip][:0]
	for _, at := range s.authTimes[ip] {
		if now.Sub(at) < authWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= authAttempts {
		s.authTimes[ip] = recent
		return false
	}
	s.authTimes[ip] = append(recent, now)
	return true
}

func (s *server) clientIP(r *http.Request) string {
	if s.trustProxy {
		// The last hop is appended by our own proxy and cannot be forged by the client.
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) listInstruments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]instrument, 0, len(s.instruments))
	for _, item := range s.instruments {
		copy := *item
		copy.ChangePct = float64(copy.LastPrice-copy.InitialPrice) / float64(copy.InitialPrice) * 100
		items = append(items, copy)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Symbol < items[j].Symbol })
	writeJSON(w, http.StatusOK, items)
}

func (s *server) instrumentRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/instruments/"), "/"), "/")
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Market data endpoint not found")
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	symbol := strings.ToUpper(parts[0])
	s.mu.Lock()
	book := s.books[symbol]
	s.mu.Unlock()
	if book == nil {
		writeError(w, http.StatusNotFound, "UNKNOWN_INSTRUMENT", "Instrument not found")
		return
	}
	switch parts[1] {
	case "book":
		bids, asks := book.Snapshot()
		writeJSON(w, http.StatusOK, map[string]any{"symbol": symbol, "bids": bids, "asks": asks})
	case "trades":
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				writeError(w, http.StatusBadRequest, "INVALID_LIMIT", "Trade limit must be between 1 and 100")
				return
			}
			limit = parsed
		}
		s.mu.Lock()
		trades := make([]engine.Trade, 0, limit)
		for i := len(s.trades) - 1; i >= 0 && len(trades) < limit; i-- {
			if s.trades[i].Symbol == symbol {
				trades = append(trades, s.trades[i])
			}
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, trades)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Market data endpoint not found")
	}
}

type orderRequest struct {
	Symbol   string           `json:"symbol"`
	Side     engine.Side      `json:"side"`
	Type     engine.OrderType `json:"type"`
	Price    int64            `json:"price"`
	Quantity int64            `json:"quantity"`
}

func (s *server) ordersRoute(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.authenticatedUser(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.placeOrder(w, r, userID)
	case http.MethodGet:
		status := strings.ToUpper(r.URL.Query().Get("status"))
		s.mu.Lock()
		orders := make([]engine.Order, 0)
		for _, order := range s.orders {
			if order.UserID == userID && (status == "" || status == "ALL" || string(order.Status) == status) {
				orders = append(orders, order)
			}
		}
		s.mu.Unlock()
		sort.Slice(orders, func(i, j int) bool { return orders[i].CreatedAt > orders[j].CreatedAt })
		writeJSON(w, http.StatusOK, orders)
	default:
		methodNotAllowed(w)
	}
}

func (s *server) placeOrder(w http.ResponseWriter, r *http.Request, userID string) {
	var input orderRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A valid JSON request is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.submit(userID, input)
	if err != nil {
		writeError(w, err.status, err.code, err.message)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"orderId": result.Order.ID, "status": result.Order.Status,
		"filledQty": result.Order.FilledQty, "fills": result.Fills,
	})
}

// submit is the single path every order takes, from humans and bots alike:
// validate, reserve, match, apply the book's side effects, settle each fill,
// and release whatever reservation the order no longer needs. The caller must
// hold s.mu.
func (s *server) submit(userID string, input orderRequest) (engine.Result, *apiError) {
	input.Symbol = strings.ToUpper(strings.TrimSpace(input.Symbol))
	item, exists := s.instruments[input.Symbol]
	if !exists {
		return engine.Result{}, &apiError{http.StatusBadRequest, "UNKNOWN_INSTRUMENT", "Instrument not found"}
	}
	if s.halted[input.Symbol] {
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
	if input.Type == engine.Limit && (input.Price <= 0 || input.Price > maxPrice || input.Price%item.TickSize != 0) {
		return engine.Result{}, &apiError{http.StatusBadRequest, "INVALID_PRICE", "Limit price must be positive, at most ₹10,00,000, and aligned to the ₹0.05 tick"}
	}
	if input.Type == engine.Market {
		input.Price = 0
	}
	user := s.accounts[userID]
	if user == nil {
		return engine.Result{}, &apiError{http.StatusUnauthorized, "UNAUTHORIZED", "Session is no longer valid"}
	}
	if !user.IsBot && !s.allowOrder(userID, time.Now()) {
		return engine.Result{}, &apiError{http.StatusTooManyRequests, "RATE_LIMITED", "Maximum 10 orders per second"}
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

	s.orderSeq++
	order := engine.Order{
		ID: s.nextID("order-"), UserID: userID, Symbol: input.Symbol, Side: input.Side,
		Type: input.Type, Price: input.Price, Quantity: input.Quantity,
		CreatedAt: time.Now().UnixMilli(),
	}
	if input.Type == engine.Market && input.Side == engine.Buy {
		order.Budget = reservedCash
	}
	s.orders[order.ID] = order
	result, err := s.books[input.Symbol].Place(order, order.CreatedAt, s.nextTradeID)
	if err != nil {
		s.releaseOrderReservation(user, order, reservedCash)
		delete(s.orders, order.ID)
		return engine.Result{}, &apiError{http.StatusBadRequest, "INVALID_ORDER", "The order could not be accepted"}
	}

	// Apply everything the book did before deciding how to respond, so orders
	// cancelled by self-trade prevention always release their reservations.
	for _, removed := range result.Removed {
		s.orders[removed.ID] = removed
		s.releaseOrderReservation(s.accounts[removed.UserID], removed, 0)
	}
	for _, updated := range result.Updated {
		s.orders[updated.ID] = updated
	}
	s.orders[result.Order.ID] = result.Order
	spent := int64(0)
	for _, fill := range result.Fills {
		s.settle(fill.Trade)
		s.recordTrade(fill.Trade)
		spent += fill.Trade.Price * fill.Trade.Quantity
	}
	if result.Order.Type == engine.Market {
		if result.Order.Side == engine.Buy {
			refund := reservedCash - spent
			user.Cash.Reserved -= refund
			user.Cash.Available += refund
		} else {
			s.releaseOrderReservation(user, result.Order, 0)
		}
	}
	s.cleanupBotOrders(result)
	if result.Order.Type == engine.Market && result.Order.FilledQty == 0 {
		delete(s.orders, result.Order.ID)
		bids, asks := s.books[input.Symbol].Snapshot()
		if (input.Side == engine.Buy && len(asks) > 0) || (input.Side == engine.Sell && len(bids) > 0) {
			return result, &apiError{http.StatusBadRequest, "INSUFFICIENT_FUNDS", "Not enough available cash for one share at the best price"}
		}
		return result, &apiError{http.StatusBadRequest, "NO_LIQUIDITY", "No executable liquidity on the other side of the book"}
	}
	return result, nil
}

// allowOrder enforces the per-user order rate limit. The caller must hold s.mu.
func (s *server) allowOrder(userID string, now time.Time) bool {
	recent := s.orderTimes[userID][:0]
	for _, placedAt := range s.orderTimes[userID] {
		if now.Sub(placedAt) < time.Second {
			recent = append(recent, placedAt)
		}
	}
	if len(recent) >= ordersPerSecond {
		s.orderTimes[userID] = recent
		return false
	}
	s.orderTimes[userID] = append(recent, now)
	return true
}

func (s *server) settle(trade engine.Trade) {
	buyerOrder := s.orders[trade.BuyOrder]
	buyer, seller := s.accounts[trade.BuyerID], s.accounts[trade.SellerID]
	value := trade.Price * trade.Quantity
	buyerReserve := value
	if buyerOrder.Type == engine.Limit {
		buyerReserve = buyerOrder.Price * trade.Quantity
	}
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
		s.applyPosition(buyer.Positions, trade.Symbol, trade.Quantity, value)
	}
	if !seller.IsBot {
		s.applySalePosition(seller.Positions, trade.Symbol, trade.Quantity, value)
	}
}

func (s *server) applyPosition(positions map[string]*position, symbol string, quantity, cost int64) {
	p := positions[symbol]
	if p == nil {
		p = &position{}
		positions[symbol] = p
	}
	p.Quantity += quantity
	p.Cost += cost
}

func (s *server) applySalePosition(positions map[string]*position, symbol string, quantity, proceeds int64) {
	p := positions[symbol]
	if p == nil || p.Quantity < quantity {
		log.Printf("position for %s is smaller than a %d-share sale; realized P&L skipped", symbol, quantity)
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

func (s *server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	userID, ok := s.authenticatedUser(w, r)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/orders/")
	s.mu.Lock()
	defer s.mu.Unlock()
	order, exists := s.orders[id]
	if !exists || order.UserID != userID || (order.Status != engine.Open && order.Status != engine.PartiallyFilled) {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "Open order not found")
		return
	}
	cancelled, err := s.books[order.Symbol].Cancel(id, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "Open order not found")
		return
	}
	s.orders[id] = cancelled
	s.releaseOrderReservation(s.accounts[userID], cancelled, 0)
	writeJSON(w, http.StatusOK, cancelled)
}

func (s *server) portfolio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	userID, ok := s.authenticatedUser(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.accounts[userID]
	if user == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Session is no longer valid")
		return
	}
	holdings := make([]map[string]any, 0)
	totalValue := user.Cash.Available + user.Cash.Reserved
	unrealized, realized := int64(0), int64(0)
	symbols := make([]string, 0, len(user.Positions))
	for symbol := range user.Positions {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	for _, symbol := range symbols {
		p := user.Positions[symbol]
		realized += p.Realized
		if p.Quantity <= 0 {
			continue
		}
		price := s.instruments[symbol].LastPrice
		value := p.Quantity * price
		upl := value - p.Cost
		totalValue += value
		unrealized += upl
		holdings = append(holdings, map[string]any{
			"symbol": symbol, "quantity": p.Quantity, "averagePrice": p.Cost / p.Quantity,
			"lastPrice": price, "marketValue": value, "unrealizedPnl": upl,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": user.Name, "cash": user.Cash, "holdings": holdings,
		"totalValue": totalValue, "unrealizedPnl": unrealized, "realizedPnl": realized,
	})
}

func (s *server) myTrades(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	userID, ok := s.authenticatedUser(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	trades := make([]engine.Trade, 0)
	for i := len(s.trades) - 1; i >= 0 && len(trades) < 100; i-- {
		trade := s.trades[i]
		if trade.BuyerID == userID || trade.SellerID == userID {
			trades = append(trades, trade)
		}
	}
	writeJSON(w, http.StatusOK, trades)
}

type leaderboardEntry struct {
	Name       string `json:"name"`
	TotalValue int64  `json:"totalValue"`
	Me         bool   `json:"me,omitempty"`
}

func (s *server) leaderboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	viewer := ""
	if r.Header.Get("Authorization") != "" {
		viewer, _ = s.subjectFromToken(r)
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	type ranked struct {
		id    string
		entry leaderboardEntry
	}
	entries := make([]ranked, 0, len(s.accounts))
	for id, user := range s.accounts {
		if user.IsBot || (!user.ExpiresAt.IsZero() && now.After(user.ExpiresAt)) {
			continue
		}
		value := user.Cash.Available + user.Cash.Reserved
		for symbol, shares := range user.Shares {
			value += (shares.Available + shares.Reserved) * s.instruments[symbol].LastPrice
		}
		entries = append(entries, ranked{id, leaderboardEntry{Name: user.Name, TotalValue: value, Me: id == viewer}})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].entry.TotalValue != entries[j].entry.TotalValue {
			return entries[i].entry.TotalValue > entries[j].entry.TotalValue
		}
		return entries[i].id < entries[j].id
	})
	if len(entries) > 50 {
		entries = entries[:50]
	}
	out := make([]leaderboardEntry, len(entries))
	for i, e := range entries {
		out[i] = e.entry
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) subjectFromToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	raw, found := strings.CutPrefix(auth, "Bearer ")
	if !found || raw == "" {
		return "", errors.New("missing bearer token")
	}
	claims := &accessClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %s", token.Method.Alg())
		}
		return s.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return "", errors.New("invalid token")
	}
	return claims.Subject, nil
}

func (s *server) authenticatedUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID, err := s.subjectFromToken(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Session is no longer valid")
		return "", false
	}
	s.mu.Lock()
	user := s.accounts[userID]
	s.mu.Unlock()
	if user == nil || (!user.ExpiresAt.IsZero() && time.Now().After(user.ExpiresAt)) {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Session is no longer valid")
		return "", false
	}
	return userID, true
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
}

// allowedOrigins parses the comma-separated FRONTEND_ORIGIN value.
func allowedOrigins(raw string) map[string]bool {
	origins := make(map[string]bool)
	for _, origin := range strings.Split(raw, ",") {
		if origin = strings.TrimRight(strings.TrimSpace(origin), "/"); origin != "" {
			origins[origin] = true
		}
	}
	return origins
}

// originAllowed accepts configured origins, or any localhost origin when none
// are configured (local development only; production refuses to start without
// FRONTEND_ORIGIN).
func originAllowed(allowed map[string]bool, origin string) bool {
	if len(allowed) > 0 {
		return allowed[origin]
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func cors(allowed map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origin != "" && originAllowed(allowed, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
