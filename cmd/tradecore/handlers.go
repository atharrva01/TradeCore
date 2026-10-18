package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"tradecore/internal/engine"
)

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	persistence := "memory"
	if s.store != nil {
		persistence = "postgres"
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "persistence": persistence, "googleSignIn": s.google != nil})
}

// durable commits pending changes and releases s.mu. When the commit fails it
// answers the request itself and returns false. The caller must hold s.mu.
func (s *server) durable(w http.ResponseWriter) bool {
	if err := s.commitAndUnlock(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "PERSISTENCE_ERROR", "The change could not be saved; please retry")
		return false
	}
	return true
}

type instrumentSummary struct {
	instrument
	Spark []int64 `json:"spark"`
}

func (s *server) listInstruments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	items := make([]instrumentSummary, 0, len(s.instruments))
	for _, symbol := range sortedKeys(s.instruments) {
		item := s.instruments[symbol]
		s.rollDay(item, now)
		items = append(items, instrumentSummary{item.view(), s.sparkline(symbol, 48)})
	}
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
	defer s.mu.Unlock()
	item := s.instruments[symbol]
	if item == nil {
		writeError(w, http.StatusNotFound, "UNKNOWN_INSTRUMENT", "Instrument not found")
		return
	}
	query := r.URL.Query()
	switch parts[1] {
	case "book":
		bids, asks := s.books[symbol].Snapshot()
		writeJSON(w, http.StatusOK, map[string]any{"symbol": symbol, "seq": item.Seq, "bids": bids, "asks": asks})
	case "trades":
		limit, ok := intParam(w, query.Get("limit"), 50, 1, 100, "INVALID_LIMIT", "Trade limit must be between 1 and 100")
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, s.recentTrades(symbol, limit))
	case "candles":
		interval, known := intervalByName(query.Get("interval"))
		if query.Get("interval") == "" {
			interval, known = candleIntervals[0], true
		}
		if !known {
			writeError(w, http.StatusBadRequest, "INVALID_INTERVAL", "Interval must be 1m, 5m, 1h or 1d")
			return
		}
		from, okFrom := int64Param(query.Get("from"))
		to, okTo := int64Param(query.Get("to"))
		limit, okLimit := intParam(w, query.Get("limit"), 500, 1, 1500, "INVALID_LIMIT", "Candle limit must be between 1 and 1500")
		if !okLimit {
			return
		}
		if !okFrom || !okTo {
			writeError(w, http.StatusBadRequest, "INVALID_RANGE", "from and to must be Unix milliseconds")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"symbol": symbol, "interval": interval.name,
			"candles": s.candleRange(symbol, interval, from, to, limit),
		})
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Market data endpoint not found")
	}
}

func intParam(w http.ResponseWriter, raw string, fallback, low, high int, code, message string) (int, bool) {
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < low || value > high {
		writeError(w, http.StatusBadRequest, code, message)
		return 0, false
	}
	return value, true
}

func int64Param(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value >= 0
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
		s.listOrders(w, r, userID)
	default:
		methodNotAllowed(w)
	}
}

// listOrders supports ?status=open|filled|cancelled|rejected|partially_filled|all,
// ?symbol= and ?limit=. "open" means OPEN or PARTIALLY_FILLED.
func (s *server) listOrders(w http.ResponseWriter, r *http.Request, userID string) {
	query := r.URL.Query()
	status := strings.ToUpper(query.Get("status"))
	symbol := strings.ToUpper(query.Get("symbol"))
	limit, ok := intParam(w, query.Get("limit"), 200, 1, 1000, "INVALID_LIMIT", "Order limit must be between 1 and 1000")
	if !ok {
		return
	}
	matches := func(order engine.Order) bool {
		switch status {
		case "", "ALL":
		case "OPEN":
			if order.Status != engine.Open && order.Status != engine.PartiallyFilled {
				return false
			}
		default:
			if string(order.Status) != status {
				return false
			}
		}
		return symbol == "" || order.Symbol == symbol
	}
	s.mu.Lock()
	orders := make([]engine.Order, 0)
	for _, order := range s.orders {
		if order.UserID == userID && matches(order) {
			orders = append(orders, order)
		}
	}
	s.mu.Unlock()
	sortedOrders(orders)
	if len(orders) > limit {
		orders = orders[:limit]
	}
	writeJSON(w, http.StatusOK, orders)
}

func (s *server) placeOrder(w http.ResponseWriter, r *http.Request, userID string) {
	now := time.Now()
	if !s.limits.ordersPerUser.allow(userID, now) || !s.limits.ordersPerIP.allow(s.clientIP(r), now) {
		s.metrics.rejections.WithLabelValues("RATE_LIMITED").Inc()
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Maximum 10 orders per second")
		return
	}
	var input orderRequest
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A valid JSON request is required")
		return
	}
	s.mu.Lock()
	result, err := s.submit(userID, input)
	if !s.durable(w) {
		return
	}
	if err != nil {
		s.metrics.rejections.WithLabelValues(err.code).Inc()
		writeError(w, err.status, err.code, err.message)
		return
	}
	fills := make([]map[string]int64, 0, len(result.Fills))
	for _, fill := range result.Fills {
		fills = append(fills, map[string]int64{"price": fill.Trade.Price, "quantity": fill.Trade.Quantity})
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"orderId": result.Order.ID, "status": result.Order.Status,
		"filledQty": result.Order.FilledQty, "fills": fills, "order": result.Order,
	})
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
	cancelled, found := s.cancelLocked(id, userID)
	if !found {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "Open order not found")
		return
	}
	if !s.durable(w) {
		return
	}
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
	for _, symbol := range sortedKeys(user.Positions) {
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
		held := user.Shares[symbol]
		holdings = append(holdings, map[string]any{
			"symbol": symbol, "quantity": p.Quantity, "reserved": held.Reserved, "averagePrice": p.Cost / p.Quantity,
			"lastPrice": price, "marketValue": value, "unrealizedPnl": upl, "realizedPnl": p.Realized,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": user.Name, "cash": user.Cash, "holdings": holdings,
		"totalValue": totalValue, "unrealizedPnl": unrealized, "realizedPnl": realized,
	})
}

type myTrade struct {
	tapeTrade
	Side  engine.Side `json:"side"`
	Value int64       `json:"value"`
}

// myTrades supports ?symbol= and ?limit=, newest first.
func (s *server) myTrades(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	userID, ok := s.authenticatedUser(w, r)
	if !ok {
		return
	}
	symbol := strings.ToUpper(r.URL.Query().Get("symbol"))
	limit, ok := intParam(w, r.URL.Query().Get("limit"), 100, 1, userTradeLimit, "INVALID_LIMIT", "Trade limit must be between 1 and 500")
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.userTrades[userID]
	trades := make([]myTrade, 0, min(limit, len(history)))
	for i := len(history) - 1; i >= 0 && len(trades) < limit; i-- {
		trade := history[i]
		if symbol != "" && trade.Symbol != symbol {
			continue
		}
		side := engine.Sell
		if trade.BuyerID == userID {
			side = engine.Buy
		}
		trades = append(trades, myTrade{trade, side, trade.Price * trade.Quantity})
	}
	writeJSON(w, http.StatusOK, trades)
}

const leaderboardRefresh = time.Minute

type leaderboardEntry struct {
	Rank       int    `json:"rank"`
	Name       string `json:"name"`
	TotalValue int64  `json:"totalValue"`
	Me         bool   `json:"me,omitempty"`
	userID     string
}

type leaderboardCache struct {
	entries   []leaderboardEntry
	updatedAt time.Time
}

// ranking recomputes the full ranking at most once a minute, as the PRD
// specifies. The caller must hold s.mu.
func (s *server) ranking(now time.Time) leaderboardCache {
	if !s.leaderboard.updatedAt.IsZero() && now.Sub(s.leaderboard.updatedAt) < leaderboardRefresh {
		return s.leaderboard
	}
	entries := make([]leaderboardEntry, 0, len(s.accounts))
	for id, user := range s.accounts {
		if user.IsBot || (!user.ExpiresAt.IsZero() && now.After(user.ExpiresAt)) {
			continue
		}
		value := user.Cash.Available + user.Cash.Reserved
		for symbol, shares := range user.Shares {
			value += (shares.Available + shares.Reserved) * s.instruments[symbol].LastPrice
		}
		entries = append(entries, leaderboardEntry{Name: user.Name, TotalValue: value, userID: id})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].TotalValue != entries[j].TotalValue {
			return entries[i].TotalValue > entries[j].TotalValue
		}
		return entries[i].userID < entries[j].userID
	})
	for i := range entries {
		entries[i].Rank = i + 1
	}
	s.leaderboard = leaderboardCache{entries: entries, updatedAt: now}
	return s.leaderboard
}

func (s *server) leaderboardHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	viewer := ""
	if r.Header.Get("Authorization") != "" {
		viewer, _ = s.subjectFromToken(r)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	board := s.ranking(time.Now())
	top := make([]leaderboardEntry, 0, 50)
	var mine *leaderboardEntry
	for _, entry := range board.entries {
		entry.Me = entry.userID == viewer && viewer != ""
		if entry.Me {
			copy := entry
			mine = &copy
		}
		if len(top) < 50 {
			top = append(top, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": top, "me": mine, "traders": len(board.entries), "updatedAt": board.updatedAt.UnixMilli(),
	})
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
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
	var expiresAt *int64
	if !user.ExpiresAt.IsZero() {
		ms := user.ExpiresAt.UnixMilli()
		expiresAt = &ms
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": userID, "name": user.Name, "email": user.Email,
		"isGuest": user.isGuest(), "isAdmin": user.IsAdmin, "expiresAt": expiresAt,
	})
}
