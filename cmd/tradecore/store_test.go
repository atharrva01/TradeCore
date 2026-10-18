package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"tradecore/internal/engine"
)

// These tests need a disposable Postgres database, for example:
//
//	docker run -d --name tradecore-pg -e POSTGRES_PASSWORD=tc -p 55432:5432 postgres:16-alpine
//	TEST_DATABASE_URL=postgres://postgres:tc@localhost:55432/postgres go test ./cmd/tradecore
//
// They drop and recreate the public schema.
func testDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(context.Background(), `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func openTestServer(t *testing.T, dsn string) (*server, *store, *[]error) {
	t.Helper()
	st, err := openStore(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	failures := &[]error{}
	st.onFailure = func(err error) { *failures = append(*failures, err) }
	s, err := openPersistentServer(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	// A restarted process keeps its signing key in production via JWT_SECRET.
	s.jwtSecret = []byte("integration-test-signing-key-0123456789")
	s.limits = noLimits()
	t.Cleanup(func() { st.pool.Close() })
	return s, st, failures
}

// flush waits until everything queued so far is durable.
func flush(t *testing.T, s *server) {
	t.Helper()
	s.mu.Lock()
	s.markInstrument("ATHR") // guarantees a changeset to wait on
	if err := s.commitAndUnlock(); err != nil {
		t.Fatal(err)
	}
}

type exchangeImage struct {
	Accounts    map[string]accountRow
	Orders      map[string]engine.Order
	Books       map[string][2][]engine.Level
	Instruments map[string]instrument
	Tokens      map[string]int64
	OrderSeq    uint64
	TradeSeq    uint64
	LastTrades  []tapeTrade
	UserTrades  map[string]int
	Candles     map[string]map[string][]candle
	Bots        botSettings
	BotOpen     map[string]int
}

func image(s *server) exchangeImage {
	s.mu.Lock()
	defer s.mu.Unlock()
	img := exchangeImage{
		Accounts: map[string]accountRow{}, Orders: map[string]engine.Order{},
		Books: map[string][2][]engine.Level{}, Instruments: map[string]instrument{},
		Tokens: map[string]int64{}, OrderSeq: s.orderSeq, TradeSeq: s.tradeSeq,
		UserTrades: map[string]int{}, Candles: s.candles, Bots: s.bots, BotOpen: map[string]int{},
	}
	for id, history := range s.userTrades {
		img.UserTrades[id] = len(history)
	}
	for id, open := range s.botOpen {
		if len(open) > 0 {
			img.BotOpen[id] = len(open)
		}
	}
	for id, user := range s.accounts {
		row := snapshotAccount(user)
		row.ExpiresAt = row.ExpiresAt.UTC().Truncate(time.Microsecond) // Postgres precision
		for symbol, p := range row.Positions {
			if p == (position{}) {
				delete(row.Positions, symbol)
			}
		}
		img.Accounts[id] = row
	}
	for id, order := range s.orders {
		order.Budget = 0
		img.Orders[id] = order
	}
	for symbol, book := range s.books {
		bids, asks := book.Snapshot()
		img.Books[symbol] = [2][]engine.Level{bids, asks}
	}
	for symbol, item := range s.instruments {
		img.Instruments[symbol] = *item
	}
	for hash, record := range s.refreshTokens {
		img.Tokens[hash] = record.ExpiresAt.UnixMicro()
	}
	for i := max(0, len(s.trades)-200); i < len(s.trades); i++ {
		img.LastTrades = append(img.LastTrades, s.trades[i])
	}
	return img
}

func diffImages(t *testing.T, before, after exchangeImage) {
	t.Helper()
	v1, v2 := reflect.ValueOf(before), reflect.ValueOf(after)
	for i := 0; i < v1.NumField(); i++ {
		name := v1.Type().Field(i).Name
		if !reflect.DeepEqual(v1.Field(i).Interface(), v2.Field(i).Interface()) {
			if name == "Accounts" {
				for id, row := range before.Accounts {
					if !reflect.DeepEqual(row, after.Accounts[id]) {
						t.Errorf("account %s differs after recovery:\n before %+v\n after  %+v", id, row, after.Accounts[id])
					}
				}
			}
			t.Errorf("%s differs after recovery", name)
		}
	}
}

func TestCrashRecoveryRestoresExactState(t *testing.T) {
	dsn := testDatabase(t)
	s, _, failures := openTestServer(t, dsn)

	rng := mathrand.New(mathrand.NewPCG(3, 5))
	tokens := make([]string, 5)
	for i := range tokens {
		tokens[i] = guestToken(t, s)
	}
	symbols := []string{"ATHR", "NOVA", "CIRR", "MINT", "ORBT"}
	for i := 0; i < 600; i++ {
		if i%25 == 0 {
			s.runBotTick()
		}
		token := tokens[rng.IntN(len(tokens))]
		symbol := symbols[rng.IntN(len(symbols))]
		side := []string{"BUY", "SELL"}[rng.IntN(2)]
		s.mu.Lock()
		mid := s.instruments[symbol].LastPrice
		s.mu.Unlock()
		if rng.IntN(5) == 0 {
			placeAs(t, s, token, fmt.Sprintf(`{"symbol":%q,"side":%q,"type":"MARKET","quantity":%d}`, symbol, side, 1+rng.IntN(50)))
		} else {
			price := (mid/5 + int64(rng.IntN(21)) - 10) * 5
			placeAs(t, s, token, fmt.Sprintf(`{"symbol":%q,"side":%q,"type":"LIMIT","price":%d,"quantity":%d}`, symbol, side, price, 1+rng.IntN(40)))
		}
		if i%9 == 0 {
			s.mu.Lock()
			var cancelID string
			for id, order := range s.orders {
				if !s.accounts[order.UserID].IsBot && order.Status == engine.Open {
					cancelID = id
					break
				}
			}
			s.mu.Unlock()
			if cancelID != "" {
				request := httptest.NewRequest(http.MethodDelete, "/api/v1/orders/"+cancelID, nil)
				for _, token := range tokens {
					request.Header.Set("Authorization", "Bearer "+token)
					s.cancelOrder(httptest.NewRecorder(), request)
				}
			}
		}
	}
	signup := httptest.NewRecorder()
	s.signup(signup, httptest.NewRequest(http.MethodPost, "/api/v1/auth/signup",
		bytes.NewBufferString(`{"email":"durable@example.com","password":"correct horse battery","displayName":"Durable"}`)))
	if signup.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", signup.Code, signup.Body)
	}
	flush(t, s)
	before := image(s)
	if len(before.LastTrades) == 0 {
		t.Fatal("the scenario produced no trades")
	}

	// Crash: abandon the first process without any shutdown, then recover.
	recovered, _, _ := openTestServer(t, dsn)
	if problems := recovered.reservationMismatches(); len(problems) > 0 {
		t.Fatalf("reservations disagree with open orders after recovery: %v", problems)
	}
	diffImages(t, before, image(recovered))

	login := httptest.NewRecorder()
	recovered.login(login, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		bytes.NewBufferString(`{"email":"durable@example.com","password":"correct horse battery"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login after recovery: %d %s", login.Code, login.Body)
	}

	// The stale process is fenced: its next change is refused, never written.
	code, body := placeAs(t, s, tokens[0], `{"symbol":"ATHR","side":"BUY","type":"LIMIT","price":5000,"quantity":1}`)
	if code != http.StatusServiceUnavailable || len(*failures) == 0 || !errors.Is((*failures)[0], errFenced) {
		t.Fatalf("stale process wrote after takeover: %d %v failures=%v", code, body, *failures)
	}
	if again := image(func() *server { s, _, _ := openTestServer(t, dsn); return s }()); !reflect.DeepEqual(again.Orders, before.Orders) {
		t.Fatal("a fenced write reached the database")
	}
}

func TestRecoveredBookKeepsTimePriority(t *testing.T) {
	dsn := testDatabase(t)
	s, _, _ := openTestServer(t, dsn)
	first, second, taker := guestToken(t, s), guestToken(t, s), guestToken(t, s)
	// The market maker quotes 68.35 / 68.45, so bids at 68.40 are the best.
	for _, token := range []string{first, second} {
		if code, body := placeAs(t, s, token, `{"symbol":"NOVA","side":"BUY","type":"LIMIT","price":6840,"quantity":5}`); code != http.StatusCreated {
			t.Fatalf("resting bid: %d %v", code, body)
		}
	}

	recovered, _, _ := openTestServer(t, dsn)
	_, takerAccount := accountFor(t, recovered, taker)
	takerAccount.Shares["NOVA"] = balance{Available: 1_000}
	code, body := placeAs(t, recovered, taker, `{"symbol":"NOVA","side":"SELL","type":"LIMIT","price":6840,"quantity":6}`)
	if code != http.StatusCreated {
		t.Fatalf("taker sell: %d %v", code, body)
	}
	_, firstAccount := accountFor(t, recovered, first)
	_, secondAccount := accountFor(t, recovered, second)
	if firstAccount.Shares["NOVA"].Available != 5 || secondAccount.Shares["NOVA"].Available != 1 {
		t.Fatalf("time priority lost across restart: first=%+v second=%+v",
			firstAccount.Shares["NOVA"], secondAccount.Shares["NOVA"])
	}
}

func TestCommitIsIdempotent(t *testing.T) {
	dsn := testDatabase(t)
	s, st, _ := openTestServer(t, dsn)
	token := guestToken(t, s)
	s.mu.Lock()
	_, err := s.submit(mustSubject(t, s, token), orderRequest{Symbol: "ATHR", Side: engine.Buy, Type: engine.Market, Quantity: 10})
	if err != nil {
		t.Fatal(err)
	}
	cs := &changeset{orderSeq: s.orderSeq, tradeSeq: s.tradeSeq, trades: s.pendingTrades}
	for _, id := range sortedKeys(s.dirtyAccounts) {
		cs.accounts = append(cs.accounts, snapshotAccount(s.accounts[id]))
	}
	for _, id := range sortedKeys(s.dirtyOrders) {
		if order, ok := s.orders[id]; ok {
			cs.orders = append(cs.orders, order)
		}
	}
	s.clearDirty()
	s.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if err := st.commit([]*changeset{cs}); err != nil {
			t.Fatalf("commit attempt %d: %v", attempt, err)
		}
	}
	var trades int
	if err := st.pool.QueryRow(context.Background(), `SELECT count(*) FROM trades`).Scan(&trades); err != nil {
		t.Fatal(err)
	}
	if trades != len(cs.trades) {
		t.Fatalf("replayed commit stored %d trades; want %d", trades, len(cs.trades))
	}
}

func TestGracefulShutdownFlushesBotActivity(t *testing.T) {
	dsn := testDatabase(t)
	s, _, _ := openTestServer(t, dsn)
	for i := 0; i < 5; i++ {
		s.runBotTick() // bots never wait for their commits
	}
	before := image(s)
	s.closeStore()
	recovered, _, _ := openTestServer(t, dsn)
	diffImages(t, before, image(recovered))
}

func mustSubject(t *testing.T, s *server, token string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	id, err := s.subjectFromToken(request)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMarketResetSurvivesRestart(t *testing.T) {
	dsn := testDatabase(t)
	s, _, _ := openTestServer(t, dsn)
	token := guestToken(t, s)
	for i := 0; i < 5; i++ {
		s.runBotTick()
	}
	placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"MARKET","quantity":20}`)
	placeAs(t, s, token, `{"symbol":"NOVA","side":"BUY","type":"LIMIT","price":6000,"quantity":3}`)
	s.mu.Lock()
	s.instruments["MINT"].Halted = true
	s.markInstrument("MINT")
	s.resetMarket()
	if err := s.commitAndUnlock(); err != nil {
		t.Fatal(err)
	}
	before := image(s)
	recovered, _, _ := openTestServer(t, dsn)
	diffImages(t, before, image(recovered))
	_, user := accountFor(t, recovered, token)
	if user.Cash.Available != startingCash || len(user.Shares) != 0 || len(user.Positions) != 0 {
		t.Fatalf("reset did not persist for people: cash %+v shares %v positions %v", user.Cash, user.Shares, user.Positions)
	}
	if !recovered.instruments["MINT"].Halted || len(recovered.userTrades) != 0 {
		t.Fatalf("halt or trade history wrong after reset and restart")
	}
}
