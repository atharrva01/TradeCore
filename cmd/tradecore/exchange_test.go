package main

import (
	"fmt"
	mathrand "math/rand/v2"
	"net/http"
	"testing"
	"time"

	"tradecore/internal/engine"
)

// The PRD's worked example: asks 5 @ ₹101.00 and 10 @ ₹101.50; a buy limit
// for 12 @ ₹101.50 fills 5 @ 101.00 and 7 @ 101.50, leaving 3 resting, and
// the unused reserve is released.
func TestWorkedExampleSettlesAtMakerPrices(t *testing.T) {
	s := quietServer(t)
	rest(t, s, marketMakerID, engine.Sell, "ATHR", 10100, 5)
	rest(t, s, marketMakerID, engine.Sell, "ATHR", 10150, 10)
	token := guestToken(t, s)
	code, body := placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"LIMIT","price":10150,"quantity":12}`)
	if code != http.StatusCreated || body["status"] != "FILLED" || len(body["fills"].([]any)) != 2 {
		t.Fatalf("order: %d %v", code, body)
	}
	_, user := accountFor(t, s, token)
	if want := startingCash - 5*10100 - 7*10150; user.Cash.Available != want || user.Cash.Reserved != 0 {
		t.Fatalf("cash = %+v; want available %d and nothing reserved", user.Cash, want)
	}
	if user.Shares["ATHR"].Available != 12 {
		t.Fatalf("shares = %+v", user.Shares["ATHR"])
	}
	_, asks := s.books["ATHR"].Snapshot()
	if len(asks) != 1 || asks[0].Price != 10150 || asks[0].Quantity != 3 {
		t.Fatalf("remaining asks = %+v; want 3 @ 10150", asks)
	}
}

func TestCancelReturnsReservedCash(t *testing.T) {
	s := quietServer(t)
	token := guestToken(t, s)
	_, body := placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"LIMIT","price":10000,"quantity":10}`)
	code, _ := call(t, s, http.MethodDelete, "/api/v1/orders/"+body["orderId"].(string), token, "")
	if code != http.StatusOK {
		t.Fatalf("cancel status = %d", code)
	}
	if _, user := accountFor(t, s, token); user.Cash.Available != startingCash || user.Cash.Reserved != 0 {
		t.Fatalf("cash was not released: %+v", user.Cash)
	}
	if code, _ := call(t, s, http.MethodDelete, "/api/v1/orders/"+body["orderId"].(string), token, ""); code != http.StatusNotFound {
		t.Fatalf("second cancel = %d; want 404", code)
	}
}

func TestMarketSellRealizesProfitAndLoss(t *testing.T) {
	s := quietServer(t)
	rest(t, s, marketMakerID, engine.Sell, "ATHR", 10125, 5)
	rest(t, s, marketMakerID, engine.Buy, "ATHR", 10075, 5)
	token := guestToken(t, s)
	placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"MARKET","quantity":5}`)
	placeAs(t, s, token, `{"symbol":"ATHR","side":"SELL","type":"MARKET","quantity":3}`)
	_, user := accountFor(t, s, token)
	p := user.Positions["ATHR"]
	if user.Cash.Available != startingCash-5*10125+3*10075 || user.Cash.Reserved != 0 {
		t.Fatalf("cash = %+v", user.Cash)
	}
	if p.Quantity != 2 || p.Cost != 2*10125 || p.Realized != -3*50 {
		t.Fatalf("position = %+v", p)
	}
}

func TestMarketBuyCannotSpendMoreThanAvailableCash(t *testing.T) {
	s := quietServer(t)
	rest(t, s, marketMakerID, engine.Sell, "ATHR", 100_000, 100_000)
	token := guestToken(t, s)
	code, body := placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"MARKET","quantity":100000}`)
	if code != http.StatusCreated || body["status"] != "CANCELLED" || body["filledQty"].(float64) != 1000 {
		t.Fatalf("market buy: %d %v; want 1000 filled (all the cash buys) and the rest cancelled", code, body)
	}
	if _, user := accountFor(t, s, token); user.Cash.Available != 0 || user.Cash.Reserved != 0 {
		t.Fatalf("cash = %+v", user.Cash)
	}
}

func TestSelfTradeOnlyLiquidityRejectsAndReleases(t *testing.T) {
	s := quietServer(t)
	token := guestToken(t, s)
	userID, user := accountFor(t, s, token)
	user.Shares["ATHR"] = balance{Available: 10}
	placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"LIMIT","price":9000,"quantity":10}`)
	code, body := placeAs(t, s, token, `{"symbol":"ATHR","side":"SELL","type":"MARKET","quantity":5}`)
	if code != http.StatusBadRequest || errorCode(body) != "NO_LIQUIDITY" {
		t.Fatalf("self-only market sell: %d %v", code, body)
	}
	if user.Cash.Reserved != 0 || user.Cash.Available != startingCash || user.Shares["ATHR"].Reserved != 0 {
		t.Fatalf("balances still locked: cash %+v shares %+v", user.Cash, user.Shares["ATHR"])
	}
	statuses := map[string]int{}
	for _, order := range s.orders {
		if order.UserID == userID {
			statuses[string(order.Status)]++
		}
	}
	if statuses["CANCELLED"] != 1 || statuses["REJECTED"] != 1 || statuses["OPEN"] != 0 {
		t.Fatalf("order statuses = %v; want the bid cancelled and the market order rejected", statuses)
	}
}

func TestIOCLimitCancelsUnfilledRemainder(t *testing.T) {
	s := quietServer(t)
	rest(t, s, marketMakerID, engine.Sell, "NOVA", 6840, 4)
	token := guestToken(t, s)
	code, body := placeAs(t, s, token, `{"symbol":"NOVA","side":"BUY","type":"LIMIT","timeInForce":"IOC","price":6850,"quantity":10}`)
	if code != http.StatusCreated || body["status"] != "CANCELLED" || body["filledQty"].(float64) != 4 {
		t.Fatalf("IOC: %d %v", code, body)
	}
	if _, user := accountFor(t, s, token); user.Cash.Reserved != 0 || user.Cash.Available != startingCash-4*6840 {
		t.Fatalf("IOC remainder kept a reservation: %+v", user.Cash)
	}
	if bids, _ := s.books["NOVA"].Snapshot(); len(bids) != 0 {
		t.Fatalf("IOC remainder rested: %+v", bids)
	}
}

func TestRejectedOrdersAppearInHistory(t *testing.T) {
	s := quietServer(t)
	token := guestToken(t, s)
	if code, body := placeAs(t, s, token, `{"symbol":"MINT","side":"BUY","type":"MARKET","quantity":5}`); errorCode(body) != "NO_LIQUIDITY" {
		t.Fatalf("market into empty book: %d %v", code, body)
	}
	rejected := callList(t, s, "/api/v1/orders?status=rejected", token)
	if len(rejected) != 1 || rejected[0]["symbol"] != "MINT" {
		t.Fatalf("rejected orders = %v", rejected)
	}
}

func TestOrderAndTradeHistoryFilters(t *testing.T) {
	s := quietServer(t)
	rest(t, s, marketMakerID, engine.Sell, "ATHR", 10100, 50)
	token := guestToken(t, s)
	placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"MARKET","quantity":2}`)
	placeAs(t, s, token, `{"symbol":"NOVA","side":"BUY","type":"LIMIT","price":6000,"quantity":1}`)
	placeAs(t, s, token, `{"symbol":"ORBT","side":"BUY","type":"LIMIT","price":8000,"quantity":1}`)
	if open := callList(t, s, "/api/v1/orders?status=open", token); len(open) != 2 {
		t.Fatalf("open orders = %d; want 2", len(open))
	}
	if nova := callList(t, s, "/api/v1/orders?status=open&symbol=NOVA", token); len(nova) != 1 {
		t.Fatalf("open NOVA orders = %d; want 1", len(nova))
	}
	trades := callList(t, s, "/api/v1/trades/me?symbol=ATHR", token)
	if len(trades) != 1 || trades[0]["side"] != "BUY" || trades[0]["value"].(float64) != 20200 {
		t.Fatalf("my ATHR trades = %v", trades)
	}
	if none := callList(t, s, "/api/v1/trades/me?symbol=NOVA", token); len(none) != 0 {
		t.Fatalf("my NOVA trades = %v", none)
	}
}

func TestOrderRateLimitPerUser(t *testing.T) {
	s := quietServer(t)
	s.limits = newLimitersFromEnv()
	token := guestToken(t, s)
	limited := 0
	for i := 0; i < 15; i++ {
		if code, _ := placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"LIMIT","price":5000,"quantity":1}`); code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 5 {
		t.Fatalf("%d of 15 rapid orders were limited; want 5 (burst of 10)", limited)
	}
}

func TestHaltedInstrumentRefusesOrders(t *testing.T) {
	s := quietServer(t)
	s.instruments["CIRR"].Halted = true
	token := guestToken(t, s)
	if code, body := placeAs(t, s, token, `{"symbol":"CIRR","side":"BUY","type":"LIMIT","price":12000,"quantity":1}`); errorCode(body) != "INSTRUMENT_HALTED" {
		t.Fatalf("order on halted instrument: %d %v", code, body)
	}
}

func TestTradesBuildCandlesAndRollTheDay(t *testing.T) {
	s := quietServer(t)
	item := s.instruments["ATHR"]
	yesterday := time.Now().Add(-24 * time.Hour)
	item.DayStart, item.Volume = istDayStart(yesterday), 999
	minute := bucketStart(time.Now().UnixMilli(), 60_000)
	s.mu.Lock()
	for i, trade := range []struct{ price, qty, at int64 }{
		{10100, 5, minute + 1_000}, {10200, 2, minute + 2_000}, {10050, 1, minute + 3_000}, {10150, 4, minute + 61_000},
	} {
		s.recordTrade(engine.Trade{ID: fmt.Sprint(i), Symbol: "ATHR", Price: trade.price, Quantity: trade.qty, CreatedAt: trade.at})
	}
	s.mu.Unlock()
	if item.DayOpen != 10100 || item.Volume != 12 || item.High != 10200 || item.Low != 10050 {
		t.Fatalf("day not rolled correctly: %+v", *item)
	}
	code, body := call(t, s, http.MethodGet, "/api/v1/instruments/ATHR/candles?interval=1m", "", "")
	candles, _ := body["candles"].([]any)
	if code != http.StatusOK || len(candles) != 2 {
		t.Fatalf("1m candles: %d %v", code, body)
	}
	first := candles[0].(map[string]any)
	if first["open"].(float64) != 10100 || first["high"].(float64) != 10200 || first["low"].(float64) != 10050 ||
		first["close"].(float64) != 10050 || first["volume"].(float64) != 8 {
		t.Fatalf("first minute candle = %v", first)
	}
	if code, _ := call(t, s, http.MethodGet, "/api/v1/instruments/ATHR/candles?interval=7m", "", ""); code != http.StatusBadRequest {
		t.Fatalf("unknown interval = %d; want 400", code)
	}
}

func TestBotsTradeWithoutCreatingMoney(t *testing.T) {
	s := newServer()
	cashBefore, sharesBefore := totals(s)
	for i := 0; i < 50; i++ {
		s.runBotTick()
		assertConserved(t, s, cashBefore, sharesBefore)
		assertBooksUncrossed(t, s)
	}
	if len(s.trades) == 0 {
		t.Fatal("bots produced no trades")
	}
	for symbol := range s.instruments {
		bids, asks := s.books[symbol].Snapshot()
		if len(bids) < 3 || len(asks) < 3 {
			t.Fatalf("%s lacks market-maker depth: %d bids, %d asks", symbol, len(bids), len(asks))
		}
	}
	if open := len(s.botOpen[noiseTraderID]); open > 5*len(s.instruments)*s.bots.Aggressiveness*int(noiseOrderTTL/botInterval) {
		t.Fatalf("noise trader accumulated %d resting orders", open)
	}
}

func TestRandomTradingConservesCashAndShares(t *testing.T) {
	s := newServer()
	s.limits = noLimits()
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
	symbols := sortedKeys(s.instruments)
	for i := 0; i < 3_000; i++ {
		if i%50 == 0 {
			s.runBotTick()
		}
		token := tokens[rng.IntN(len(tokens))]
		symbol := symbols[rng.IntN(len(symbols))]
		side := []string{"BUY", "SELL"}[rng.IntN(2)]
		mid := s.instruments[symbol].LastPrice
		switch rng.IntN(6) {
		case 0:
			placeAs(t, s, token, fmt.Sprintf(`{"symbol":%q,"side":%q,"type":"MARKET","quantity":%d}`, symbol, side, 1+rng.IntN(400)))
		case 1:
			price := (mid/5 + int64(rng.IntN(41)) - 20) * 5
			placeAs(t, s, token, fmt.Sprintf(`{"symbol":%q,"side":%q,"type":"LIMIT","timeInForce":"IOC","price":%d,"quantity":%d}`, symbol, side, price, 1+rng.IntN(60)))
		default:
			price := (mid/5 + int64(rng.IntN(41)) - 20) * 5
			placeAs(t, s, token, fmt.Sprintf(`{"symbol":%q,"side":%q,"type":"LIMIT","price":%d,"quantity":%d}`, symbol, side, price, 1+rng.IntN(60)))
		}
		if i%7 == 0 {
			userID, _ := accountFor(t, s, token)
			for id, order := range s.orders {
				if order.UserID == userID && order.Status == engine.Open {
					call(t, s, http.MethodDelete, "/api/v1/orders/"+id, token, "")
					break
				}
			}
		}
		assertConserved(t, s, cashBefore, sharesBefore)
	}
	assertBooksUncrossed(t, s)
	if problems := s.reservationMismatches(); len(problems) > 0 {
		t.Fatalf("reservations disagree with open orders: %v", problems[:min(3, len(problems))])
	}
}

func TestLeaderboardRefreshesEveryMinuteAndMarksMe(t *testing.T) {
	s := quietServer(t)
	first, second := guestToken(t, s), guestToken(t, s)
	_, body := call(t, s, http.MethodGet, "/api/v1/leaderboard", first, "")
	if body["traders"].(float64) != 2 || body["me"] == nil {
		t.Fatalf("leaderboard = %v", body)
	}
	_, user := accountFor(t, s, second)
	user.Cash.Available += 5_000_000
	_, cached := call(t, s, http.MethodGet, "/api/v1/leaderboard", second, "")
	if rank := cached["me"].(map[string]any)["rank"].(float64); rank == 1 && cached["entries"].([]any)[0].(map[string]any)["totalValue"].(float64) > float64(startingCash) {
		t.Fatal("leaderboard recomputed before a minute passed")
	}
	s.leaderboard.updatedAt = time.Now().Add(-2 * leaderboardRefresh)
	_, fresh := call(t, s, http.MethodGet, "/api/v1/leaderboard", second, "")
	if fresh["me"].(map[string]any)["rank"].(float64) != 1 {
		t.Fatalf("refreshed leaderboard = %v", fresh)
	}
}
