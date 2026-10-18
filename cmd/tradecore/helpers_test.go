package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tradecore/internal/engine"
)

// noLimits disables rate limiting for tests that are not about it.
func noLimits() *limiters {
	return &limiters{newLimiter(0, 0), newLimiter(0, 0), newLimiter(0, 0), newLimiter(0, 0)}
}

// quietServer is an exchange with the default instruments and funded bots
// but no market-maker quotes, so each test controls the book exactly.
func quietServer(t *testing.T) *server {
	t.Helper()
	s := newBaseServer()
	s.limits = noLimits()
	for _, id := range botIDs {
		bot := newAccount(id, botNames[id])
		bot.IsBot, bot.Cash = true, balance{Available: botCash}
		s.accounts[id] = bot
	}
	for _, item := range defaultInstruments {
		if _, err := s.addInstrument(item.symbol, item.name, item.price); err != nil {
			t.Fatal(err)
		}
	}
	s.clearDirty()
	return s
}

// rest places a resting limit order for userID directly through submit.
func rest(t *testing.T, s *server, userID string, side engine.Side, symbol string, price, quantity int64) engine.Order {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.submit(userID, orderRequest{Symbol: symbol, Side: side, Type: engine.Limit, Price: price, Quantity: quantity})
	if err != nil {
		t.Fatalf("resting order: %v", err)
	}
	s.commitLocked()
	return result.Order
}

func guestToken(t *testing.T, s *server) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	s.guest(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewBufferString(`{"name":"Test Trader"}`)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("guest status = %d: %s", recorder.Code, recorder.Body)
	}
	var response struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.AccessToken
}

func call(t *testing.T, s *server, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, request)
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	return recorder.Code, response
}

func callList(t *testing.T, s *server, path, token string) []map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, recorder.Code, recorder.Body)
	}
	var list []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func placeAs(t *testing.T, s *server, token, body string) (int, map[string]any) {
	t.Helper()
	return call(t, s, http.MethodPost, "/api/v1/orders", token, body)
}

func accountFor(t *testing.T, s *server, token string) (string, *account) {
	t.Helper()
	id, err := s.subjectFromRaw(token)
	if err != nil {
		t.Fatal(err)
	}
	return id, s.accounts[id]
}

func errorCode(body map[string]any) string {
	if detail, ok := body["error"].(map[string]any); ok {
		code, _ := detail["code"].(string)
		return code
	}
	return ""
}

func totals(s *server) (int64, map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cash := int64(0)
	shares := make(map[string]int64)
	for _, user := range s.accounts {
		cash += user.Cash.Available + user.Cash.Reserved
		for symbol, held := range user.Shares {
			shares[symbol] += held.Available + held.Reserved
		}
	}
	return cash, shares
}

func assertConserved(t *testing.T, s *server, cashBefore int64, sharesBefore map[string]int64) {
	t.Helper()
	s.mu.Lock()
	for id, user := range s.accounts {
		if user.Cash.Available < 0 || user.Cash.Reserved < 0 {
			t.Fatalf("%s has negative cash: %+v", id, user.Cash)
		}
		for symbol, held := range user.Shares {
			if held.Available < 0 || held.Reserved < 0 {
				t.Fatalf("%s has negative %s shares: %+v", id, symbol, held)
			}
		}
	}
	s.mu.Unlock()
	cash, shares := totals(s)
	if cash != cashBefore {
		t.Fatalf("total cash changed: before=%d after=%d", cashBefore, cash)
	}
	for symbol, before := range sharesBefore {
		if shares[symbol] != before {
			t.Fatalf("total %s shares changed: before=%d after=%d", symbol, before, shares[symbol])
		}
	}
}

func assertBooksUncrossed(t *testing.T, s *server) {
	t.Helper()
	for symbol, book := range s.books {
		bids, asks := book.Snapshot()
		if len(bids) > 0 && len(asks) > 0 && bids[0].Price >= asks[0].Price {
			t.Fatalf("%s book crossed: bid %d >= ask %d", symbol, bids[0].Price, asks[0].Price)
		}
	}
}
