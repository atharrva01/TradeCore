package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	mathrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func guestToken(t *testing.T, s *server) string {
	t.Helper()
	body := bytes.NewBufferString(`{"name":"Test Trader"}`)
	recorder := httptest.NewRecorder()
	s.guest(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", body))
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

func TestOrderSettlementPreservesPortfolioValue(t *testing.T) {
	s := newServer()
	token := guestToken(t, s)
	requestBody := []byte(`{"symbol":"ATHR","side":"BUY","type":"LIMIT","price":10150,"quantity":130}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	s.ordersRoute(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("place order status = %d: %s", recorder.Code, recorder.Body)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var trader *account
	for _, user := range s.accounts {
		if !user.IsBot {
			trader = user
			break
		}
	}
	if trader == nil {
		t.Fatal("guest account not found")
	}
	if trader.Cash.Available != startingCash-1_316_500 || trader.Cash.Reserved != 0 || trader.Shares["ATHR"].Available != 130 {
		t.Fatalf("unexpected post-settlement balances: cash=%+v shares=%+v", trader.Cash, trader.Shares["ATHR"])
	}
}

func TestCancelReturnsReservedCash(t *testing.T) {
	s := newServer()
	token := guestToken(t, s)
	requestBody := []byte(`{"symbol":"ATHR","side":"BUY","type":"LIMIT","price":10000,"quantity":10}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	s.ordersRoute(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("place order status = %d: %s", recorder.Code, recorder.Body)
	}
	var response struct {
		OrderID string `json:"orderId"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}

	cancel := httptest.NewRequest(http.MethodDelete, "/api/v1/orders/"+response.OrderID, nil)
	cancel.Header.Set("Authorization", "Bearer "+token)
	cancelRecorder := httptest.NewRecorder()
	s.cancelOrder(cancelRecorder, cancel)
	if cancelRecorder.Code != http.StatusOK {
		t.Fatalf("cancel status = %d: %s", cancelRecorder.Code, cancelRecorder.Body)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range s.accounts {
		if !user.IsBot && (user.Cash.Available != startingCash || user.Cash.Reserved != 0) {
			t.Fatalf("cash was not released after cancellation: %+v", user.Cash)
		}
	}
}

func TestMarketSellSettlesSharesAndRealizedPnl(t *testing.T) {
	s := newServer()
	token := guestToken(t, s)
	for _, body := range []string{
		`{"symbol":"ATHR","side":"BUY","type":"MARKET","quantity":5}`,
		`{"symbol":"ATHR","side":"SELL","type":"MARKET","quantity":3}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		s.ordersRoute(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("place market order status = %d: %s", recorder.Code, recorder.Body)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var user *account
	for _, candidate := range s.accounts {
		if !candidate.IsBot {
			user = candidate
			break
		}
	}
	if user == nil {
		t.Fatal("guest account not found")
	}
	shares := user.Shares["ATHR"]
	p := user.Positions["ATHR"]
	if user.Cash.Available != startingCash-50_625+30_225 || user.Cash.Reserved != 0 {
		t.Fatalf("unexpected settled cash: %+v", user.Cash)
	}
	if shares.Available != 2 || shares.Reserved != 0 {
		t.Fatalf("unexpected settled shares: %+v", shares)
	}
	if p.Quantity != 2 || p.Cost != 20_250 || p.Realized != -150 {
		t.Fatalf("unexpected position after sale: %+v", p)
	}
}

func TestNoiseBotTradesThroughBooksWithoutChangingBalanceTotals(t *testing.T) {
	s := newServer()
	totalCashBefore := int64(0)
	sharesBefore := make(map[string]int64)
	for _, user := range s.accounts {
		totalCashBefore += user.Cash.Available + user.Cash.Reserved
		for symbol, shares := range user.Shares {
			sharesBefore[symbol] += shares.Available + shares.Reserved
		}
	}

	s.runBotTick()

	if len(s.trades) != len(s.instruments) {
		t.Fatalf("bot produced %d trades; want one per instrument", len(s.instruments))
	}
	totalCashAfter := int64(0)
	sharesAfter := make(map[string]int64)
	for _, user := range s.accounts {
		totalCashAfter += user.Cash.Available + user.Cash.Reserved
		for symbol, shares := range user.Shares {
			sharesAfter[symbol] += shares.Available + shares.Reserved
		}
	}
	if totalCashAfter != totalCashBefore {
		t.Fatalf("bot changed total cash: before=%d after=%d", totalCashBefore, totalCashAfter)
	}
	for symbol, before := range sharesBefore {
		if sharesAfter[symbol] != before {
			t.Fatalf("bot changed %s share total: before=%d after=%d", symbol, before, sharesAfter[symbol])
		}
	}
}

func placeAs(t *testing.T, s *server, token, body string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	s.ordersRoute(recorder, request)
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return recorder.Code, response
}

func accountFor(t *testing.T, s *server, token string) (string, *account) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	id, err := s.subjectFromToken(request)
	if err != nil {
		t.Fatal(err)
	}
	return id, s.accounts[id]
}

func assertConserved(t *testing.T, s *server, cashBefore int64, sharesBefore map[string]int64) {
	t.Helper()
	cash := int64(0)
	shares := make(map[string]int64)
	for id, user := range s.accounts {
		if user.Cash.Available < 0 || user.Cash.Reserved < 0 {
			t.Fatalf("%s has negative cash: %+v", id, user.Cash)
		}
		cash += user.Cash.Available + user.Cash.Reserved
		for symbol, held := range user.Shares {
			if held.Available < 0 || held.Reserved < 0 {
				t.Fatalf("%s has negative %s shares: %+v", id, symbol, held)
			}
			shares[symbol] += held.Available + held.Reserved
		}
	}
	if cash != cashBefore {
		t.Fatalf("total cash changed: before=%d after=%d", cashBefore, cash)
	}
	for symbol, before := range sharesBefore {
		if shares[symbol] != before {
			t.Fatalf("total %s shares changed: before=%d after=%d", symbol, before, shares[symbol])
		}
	}
}

func totals(s *server) (int64, map[string]int64) {
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

func TestMarketBuyCannotSpendMoreThanAvailableCash(t *testing.T) {
	s := newServer()
	seller := guestToken(t, s)
	_, sellerAccount := accountFor(t, s, seller)
	sellerAccount.Shares["ATHR"] = balance{Available: 100_000}
	if code, body := placeAs(t, s, seller, `{"symbol":"ATHR","side":"SELL","type":"LIMIT","price":100000,"quantity":100000}`); code != http.StatusCreated {
		t.Fatalf("seller order: %d %v", code, body)
	}
	buyer := guestToken(t, s)
	code, body := placeAs(t, s, buyer, `{"symbol":"ATHR","side":"BUY","type":"MARKET","quantity":100000}`)
	if code != http.StatusCreated || body["status"] != "CANCELLED" {
		t.Fatalf("market buy: %d %v; want a capped fill with the remainder cancelled", code, body)
	}
	_, buyerAccount := accountFor(t, s, buyer)
	if buyerAccount.Cash.Available < 0 || buyerAccount.Cash.Reserved != 0 {
		t.Fatalf("buyer cash after capped market buy: %+v", buyerAccount.Cash)
	}
}

func TestMarketOrderMeetingOnlyOwnOrdersReleasesThem(t *testing.T) {
	s := newServer()
	token := guestToken(t, s)
	userID, user := accountFor(t, s, token)
	user.Shares["ATHR"] = balance{Available: 10}
	placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"LIMIT","price":9000,"quantity":10}`)
	other := guestToken(t, s)
	_, otherAccount := accountFor(t, s, other)
	otherAccount.Shares["ATHR"] = balance{Available: 370}
	placeAs(t, s, other, `{"symbol":"ATHR","side":"SELL","type":"MARKET","quantity":370}`)

	code, body := placeAs(t, s, token, `{"symbol":"ATHR","side":"SELL","type":"MARKET","quantity":5}`)
	if code != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "NO_LIQUIDITY" {
		t.Fatalf("self-only market sell: %d %v", code, body)
	}
	if user.Cash.Reserved != 0 || user.Cash.Available != startingCash {
		t.Fatalf("cash still locked after self-trade prevention: %+v", user.Cash)
	}
	for _, order := range s.orders {
		if order.UserID == userID && (order.Status == "OPEN" || order.Status == "PARTIALLY_FILLED") {
			t.Fatalf("order left open after self-trade prevention removed it: %+v", order)
		}
	}
}

func TestPartiallyFilledMarketOrderIsNotLeftOpen(t *testing.T) {
	s := newServer()
	token := guestToken(t, s)
	code, body := placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"MARKET","quantity":1000}`)
	if code != http.StatusCreated || body["status"] != "CANCELLED" || body["filledQty"].(float64) != 370 {
		t.Fatalf("market buy against 370 shares: %d %v", code, body)
	}
	_, user := accountFor(t, s, token)
	if user.Cash.Reserved != 0 {
		t.Fatalf("market remainder left cash reserved: %+v", user.Cash)
	}
}

func TestMarketMakerReseedSettlesCrossingOrders(t *testing.T) {
	s := newServer()
	token := guestToken(t, s)
	cashBefore, sharesBefore := totals(s)
	placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"LIMIT","price":10300,"quantity":1000}`)
	_, user := accountFor(t, s, token)
	sharesAfterFirst := user.Shares["ATHR"].Available
	tradesBefore := len(s.trades)

	s.mu.Lock()
	s.seedQuote("ATHR", s.instruments["ATHR"].LastPrice)
	s.mu.Unlock()

	if len(s.trades) == tradesBefore || user.Shares["ATHR"].Available <= sharesAfterFirst {
		t.Fatalf("crossing reseed was not settled: trades %d->%d, shares %d->%d",
			tradesBefore, len(s.trades), sharesAfterFirst, user.Shares["ATHR"].Available)
	}
	assertConserved(t, s, cashBefore, sharesBefore)
}

func TestRandomTradingConservesCashAndShares(t *testing.T) {
	s := newServer()
	rng := mathrand.New(mathrand.NewPCG(7, 11))
	tokens := make([]string, 6)
	for i := range tokens {
		tokens[i] = guestToken(t, s)
		_, user := accountFor(t, s, tokens[i])
		for symbol := range s.instruments {
			user.Shares[symbol] = balance{Available: 500}
		}
	}
	cashBefore, sharesBefore := totals(s)
	symbols := []string{"ATHR", "NOVA", "CIRR", "MINT", "ORBT"}
	for i := 0; i < 3_000; i++ {
		if i%50 == 0 {
			s.runBotTick()
		}
		token := tokens[rng.IntN(len(tokens))]
		symbol := symbols[rng.IntN(len(symbols))]
		side := []string{"BUY", "SELL"}[rng.IntN(2)]
		mid := s.instruments[symbol].LastPrice
		if rng.IntN(6) == 0 {
			placeAs(t, s, token, fmt.Sprintf(`{"symbol":%q,"side":%q,"type":"MARKET","quantity":%d}`, symbol, side, 1+rng.IntN(400)))
		} else {
			price := (mid/5 + int64(rng.IntN(41)) - 20) * 5
			placeAs(t, s, token, fmt.Sprintf(`{"symbol":%q,"side":%q,"type":"LIMIT","price":%d,"quantity":%d}`, symbol, side, price, 1+rng.IntN(60)))
		}
		s.mu.Lock()
		s.orderTimes = make(map[string][]time.Time) // the rate limit is not under test here
		s.mu.Unlock()
		if i%7 == 0 {
			for id, order := range s.orders {
				if order.Status == "OPEN" && rng.IntN(3) == 0 {
					request := httptest.NewRequest(http.MethodDelete, "/api/v1/orders/"+id, nil)
					request.Header.Set("Authorization", "Bearer "+token)
					s.cancelOrder(httptest.NewRecorder(), request)
					break
				}
			}
		}
		assertConserved(t, s, cashBefore, sharesBefore)
	}
	// Once every resting order is cancelled, nothing should remain reserved.
	for id, order := range s.orders {
		if order.Status == "OPEN" || order.Status == "PARTIALLY_FILLED" {
			if cancelled, err := s.books[order.Symbol].Cancel(id, order.UserID); err == nil {
				s.orders[id] = cancelled
				s.releaseOrderReservation(s.accounts[order.UserID], cancelled, 0)
			}
		}
	}
	for _, token := range tokens {
		_, user := accountFor(t, s, token)
		if user.Cash.Reserved != 0 {
			t.Fatalf("cash still reserved with no open orders: %+v", user.Cash)
		}
		for symbol, held := range user.Shares {
			if held.Reserved != 0 {
				t.Fatalf("%s shares still reserved with no open orders: %+v", symbol, held)
			}
		}
	}
}
