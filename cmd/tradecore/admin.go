package main

import (
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// Admins are accounts whose email is listed in ADMIN_EMAILS; the flag is
// refreshed at every signup and login.
//
//	GET  /api/v1/admin/stats
//	GET  /api/v1/admin/bots           PUT /api/v1/admin/bots
//	POST /api/v1/admin/instruments    {"symbol","name","price"}
//	POST /api/v1/admin/instruments/{symbol}/halt | /resume
//	POST /api/v1/admin/reset          {"confirm":"RESET"}
func (s *server) adminRoute(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.authenticatedUser(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	isAdmin := s.accounts[userID] != nil && s.accounts[userID].IsAdmin
	s.mu.Unlock()
	if !isAdmin {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "Admin access is required")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/admin"), "/")
	parts := strings.Split(path, "/")
	switch {
	case path == "stats" && r.Method == http.MethodGet:
		s.adminStats(w)
	case path == "bots" && r.Method == http.MethodGet:
		s.mu.Lock()
		settings := s.bots
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, settings)
	case path == "bots" && r.Method == http.MethodPut:
		s.adminUpdateBots(w, r, userID)
	case path == "instruments" && r.Method == http.MethodPost:
		s.adminAddInstrument(w, r, userID)
	case len(parts) == 3 && parts[0] == "instruments" && (parts[2] == "halt" || parts[2] == "resume") && r.Method == http.MethodPost:
		s.adminHalt(w, userID, strings.ToUpper(parts[1]), parts[2] == "halt")
	case path == "reset" && r.Method == http.MethodPost:
		s.adminReset(w, r, userID)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Admin endpoint not found")
	}
}

func (s *server) adminStats(w http.ResponseWriter) {
	now := time.Now()
	engine := s.metrics.snapshot(now)
	clients := s.hub.clientCount()
	s.mu.Lock()
	humans, guests, resting := 0, 0, 0
	for _, user := range s.accounts {
		if !user.IsBot {
			humans++
			if user.isGuest() {
				guests++
			}
		}
	}
	for _, order := range s.orders {
		if order.Status == "OPEN" || order.Status == "PARTIALLY_FILLED" {
			resting++
		}
	}
	queued := 0
	if s.store != nil && !s.store.closed {
		queued = len(s.store.queue)
	}
	instruments := make([]instrument, 0, len(s.instruments))
	for _, symbol := range sortedKeys(s.instruments) {
		instruments = append(instruments, s.instruments[symbol].view())
	}
	persistence := "memory"
	if s.store != nil {
		persistence = "postgres"
	}
	s.mu.Unlock()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	writeJSON(w, http.StatusOK, map[string]any{
		"engine": engine, "connectedClients": clients, "accounts": humans, "guests": guests,
		"restingOrders": resting, "commitQueue": queued, "persistence": persistence,
		"goroutines": runtime.NumGoroutine(), "heapMB": float64(memory.HeapAlloc) / (1 << 20),
		"instruments": instruments,
	})
}

func (s *server) adminUpdateBots(w http.ResponseWriter, r *http.Request, userID string) {
	var settings botSettings
	if err := decodeJSON(r, &settings); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A valid JSON request is required")
		return
	}
	if err := settings.validate(); err != nil {
		writeError(w, err.status, err.code, err.message)
		return
	}
	s.mu.Lock()
	s.bots = settings
	s.dirtyBots = true
	if !s.durable(w) {
		return
	}
	slog.Info("bot settings changed", "admin", userID, "settings", settings)
	writeJSON(w, http.StatusOK, settings)
}

func (s *server) adminAddInstrument(w http.ResponseWriter, r *http.Request, userID string) {
	var input struct {
		Symbol string `json:"symbol"`
		Name   string `json:"name"`
		Price  int64  `json:"price"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "A valid JSON request is required")
		return
	}
	s.mu.Lock()
	item, err := s.addInstrument(input.Symbol, input.Name, input.Price)
	if err != nil {
		s.mu.Unlock()
		writeError(w, err.status, err.code, err.message)
		return
	}
	s.quoteMarket(item.Symbol)
	view := item.view()
	if !s.durable(w) {
		return
	}
	slog.Info("instrument listed", "admin", userID, "symbol", view.Symbol)
	writeJSON(w, http.StatusCreated, view)
}

func (s *server) adminHalt(w http.ResponseWriter, userID, symbol string, halt bool) {
	s.mu.Lock()
	item := s.instruments[symbol]
	if item == nil {
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, "UNKNOWN_INSTRUMENT", "Instrument not found")
		return
	}
	item.Halted = halt
	item.Seq++
	s.markInstrument(symbol)
	view := item.view()
	if !s.durable(w) {
		return
	}
	slog.Info("instrument trading status changed", "admin", userID, "symbol", symbol, "halted", halt)
	writeJSON(w, http.StatusOK, view)
}

func (s *server) adminReset(w http.ResponseWriter, r *http.Request, userID string) {
	var input struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(r, &input); err != nil || input.Confirm != "RESET" {
		writeError(w, http.StatusBadRequest, "CONFIRMATION_REQUIRED", `Send {"confirm":"RESET"} to reset the market`)
		return
	}
	s.mu.Lock()
	s.resetMarket()
	if !s.durable(w) {
		return
	}
	slog.Warn("market reset", "admin", userID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}
