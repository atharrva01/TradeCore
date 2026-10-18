package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"tradecore/internal/engine"
)

func readEvent(t *testing.T, ctx context.Context, conn *websocket.Conn, want string) wsEvent {
	t.Helper()
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
		var event struct {
			wsEvent
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == want {
			event.wsEvent.Data = event.Data
			return event.wsEvent
		}
	}
}

func TestWebSocketStreamsSnapshotsTradesAndPrivateFills(t *testing.T) {
	s := quietServer(t)
	rest(t, s, marketMakerID, engine.Sell, "ATHR", 10100, 50)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go s.hub.run(ctx)
	httpServer := httptest.NewServer(s.routes())
	defer httpServer.Close()
	token := guestToken(t, s)

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	send := func(message string) {
		if err := conn.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
			t.Fatal(err)
		}
	}
	send(`{"type":"subscribe","channels":["book:ATHR","trades:ATHR","candles:ATHR:1m","nonsense:ATHR"]}`)
	book := readEvent(t, ctx, conn, "book")
	if !strings.Contains(string(book.Data.(json.RawMessage)), `"price":10100`) {
		t.Fatalf("book snapshot = %s", book.Data)
	}
	snapshot := readEvent(t, ctx, conn, "trades_snapshot")
	subscribed := readEvent(t, ctx, conn, "subscribed")
	if !strings.Contains(string(subscribed.Data.(json.RawMessage)), "candles:ATHR:1m") || strings.Contains(string(subscribed.Data.(json.RawMessage)), "nonsense") {
		t.Fatalf("subscribed = %s", subscribed.Data)
	}
	send(`{"type":"auth","token":"` + token + `"}`)
	readEvent(t, ctx, conn, "authenticated")

	if code, body := placeAs(t, s, token, `{"symbol":"ATHR","side":"BUY","type":"MARKET","quantity":3}`); code != http.StatusCreated {
		t.Fatalf("order: %d %v", code, body)
	}
	trade := readEvent(t, ctx, conn, "trade")
	if trade.Seq != snapshot.Seq+1 {
		t.Fatalf("trade seq = %d after snapshot seq %d; want contiguous", trade.Seq, snapshot.Seq)
	}
	fill := readEvent(t, ctx, conn, "fill")
	if !strings.Contains(string(fill.Data.(json.RawMessage)), `"side":"BUY"`) {
		t.Fatalf("fill = %s", fill.Data)
	}
	readEvent(t, ctx, conn, "candle")
	updated := readEvent(t, ctx, conn, "book")
	if updated.Seq <= book.Seq {
		t.Fatalf("book seq did not advance: %d then %d", book.Seq, updated.Seq)
	}
}

func TestAdminEndpoints(t *testing.T) {
	s := quietServer(t)
	s.adminEmails = map[string]bool{"admin@example.com": true}
	guest := guestToken(t, s)
	if code, _ := call(t, s, http.MethodGet, "/api/v1/admin/stats", guest, ""); code != http.StatusForbidden {
		t.Fatalf("guest admin access = %d; want 403", code)
	}
	code, body := call(t, s, http.MethodPost, "/api/v1/auth/signup", "",
		`{"email":"admin@example.com","password":"a long admin password","displayName":"Admin"}`)
	if code != http.StatusCreated || body["user"].(map[string]any)["isAdmin"] != true {
		t.Fatalf("admin signup: %d %v", code, body)
	}
	admin := body["accessToken"].(string)

	if code, body := call(t, s, http.MethodPost, "/api/v1/admin/instruments/NOVA/halt", admin, ""); code != http.StatusOK || body["halted"] != true {
		t.Fatalf("halt: %d %v", code, body)
	}
	if _, body := placeAs(t, s, guest, `{"symbol":"NOVA","side":"BUY","type":"LIMIT","price":6000,"quantity":1}`); errorCode(body) != "INSTRUMENT_HALTED" {
		t.Fatalf("order after halt: %v", body)
	}
	call(t, s, http.MethodPost, "/api/v1/admin/instruments/NOVA/resume", admin, "")

	if code, body := call(t, s, http.MethodPut, "/api/v1/admin/bots", admin, `{"enabled":true,"aggressiveness":11,"volatility":1,"momentum":true}`); code != http.StatusBadRequest {
		t.Fatalf("invalid bot settings: %d %v", code, body)
	}
	if code, body := call(t, s, http.MethodPut, "/api/v1/admin/bots", admin, `{"enabled":false,"aggressiveness":4,"volatility":2.5,"momentum":false}`); code != http.StatusOK || s.bots.Volatility != 2.5 {
		t.Fatalf("bot settings: %d %v", code, body)
	}

	if code, body := call(t, s, http.MethodPost, "/api/v1/admin/instruments", admin, `{"symbol":"zeta","name":"Zeta Labs","price":2505}`); code != http.StatusCreated || body["symbol"] != "ZETA" {
		t.Fatalf("add instrument: %d %v", code, body)
	}
	if code, _ := call(t, s, http.MethodPost, "/api/v1/admin/instruments", admin, `{"symbol":"ZETA","name":"Again","price":2500}`); code != http.StatusConflict {
		t.Fatalf("duplicate symbol = %d; want 409", code)
	}
	if bids, asks := s.books["ZETA"].Snapshot(); len(bids) == 0 || len(asks) == 0 {
		t.Fatal("new instrument has no market-maker quotes")
	}

	rest(t, s, marketMakerID, engine.Sell, "ATHR", 10100, 10)
	placeAs(t, s, guest, `{"symbol":"ATHR","side":"BUY","type":"MARKET","quantity":2}`)
	code, stats := call(t, s, http.MethodGet, "/api/v1/admin/stats", admin, "")
	if code != http.StatusOK || stats["engine"].(map[string]any)["matchSamples"].(float64) == 0 {
		t.Fatalf("stats: %d %v", code, stats)
	}

	if code, _ := call(t, s, http.MethodPost, "/api/v1/admin/reset", admin, `{}`); code != http.StatusBadRequest {
		t.Fatalf("reset without confirmation = %d; want 400", code)
	}
	if code, _ := call(t, s, http.MethodPost, "/api/v1/admin/reset", admin, `{"confirm":"RESET"}`); code != http.StatusOK {
		t.Fatalf("reset = %d", code)
	}
	_, user := accountFor(t, s, guest)
	if user.Cash.Available != startingCash || len(user.Shares) != 0 || len(s.trades) != 0 || s.instruments["ATHR"].LastPrice != 10100 {
		t.Fatalf("reset left state behind: cash %+v shares %v trades %d", user.Cash, user.Shares, len(s.trades))
	}
}

func TestGoogleSignInCreatesAccountAndSession(t *testing.T) {
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("code") != "good-code" || r.Form.Get("client_secret") != "secret" {
				http.Error(w, "bad code", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"google-access"}`)
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer google-access" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"email":"Person@Example.com","email_verified":true,"name":"Pat Person"}`)
		}
	}))
	defer google.Close()
	s := quietServer(t)
	s.google = &googleOAuth{
		clientID: "client", clientSecret: "secret", redirectURL: "http://app/api/v1/auth/google/callback",
		afterLogin: "/auth/callback", authURL: google.URL + "/auth", tokenURL: google.URL + "/token",
		userinfoURL: google.URL + "/userinfo", client: google.Client(),
	}
	app := httptest.NewServer(s.routes())
	defer app.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	start, err := client.Get(app.URL + "/api/v1/auth/google/start")
	if err != nil {
		t.Fatal(err)
	}
	redirect, _ := url.Parse(start.Header.Get("Location"))
	state := redirect.Query().Get("state")
	if start.StatusCode != http.StatusFound || state == "" || redirect.Query().Get("client_id") != "client" {
		t.Fatalf("start redirected to %s", redirect)
	}
	if forged, _ := client.Get(app.URL + "/api/v1/auth/google/callback?code=good-code&state=forged"); !strings.Contains(forged.Header.Get("Location"), "error=google") {
		t.Fatalf("forged state accepted: %s", forged.Header.Get("Location"))
	}
	start, _ = client.Get(app.URL + "/api/v1/auth/google/start")
	redirect, _ = url.Parse(start.Header.Get("Location"))
	callback, err := client.Get(app.URL + "/api/v1/auth/google/callback?code=good-code&state=" + redirect.Query().Get("state"))
	if err != nil || callback.Header.Get("Location") != "/auth/callback" {
		t.Fatalf("callback redirected to %q (%v)", callback.Header.Get("Location"), err)
	}
	refreshed, err := client.Post(app.URL+"/api/v1/auth/refresh", "application/json", bytes.NewReader(nil))
	if err != nil || refreshed.StatusCode != http.StatusOK {
		t.Fatalf("refresh after Google sign-in: %v %v", refreshed.Status, err)
	}
	var session struct {
		AccessToken string `json:"accessToken"`
	}
	_ = json.NewDecoder(refreshed.Body).Decode(&session)
	_, me := call(t, s, http.MethodGet, "/api/v1/me", session.AccessToken, "")
	if me["email"] != "person@example.com" || me["isGuest"] != false {
		t.Fatalf("me = %v", me)
	}
}

func TestCORSAllowsOnlyKnownOrigins(t *testing.T) {
	s := quietServer(t)
	for _, tc := range []struct {
		allowed    string
		production bool
		origin     string
		want       bool
	}{
		{"", false, "http://localhost:5173", true},
		{"", false, "https://evil.example", false},
		{"", true, "http://localhost:5173", false},
		{"https://tradecore.example", true, "https://tradecore.example", true},
		{"https://tradecore.example", true, "https://evil.example", false},
	} {
		handler := cors(allowedOrigins(tc.allowed), tc.production, s.routes())
		request := httptest.NewRequest(http.MethodGet, "/api/v1/instruments", nil)
		request.Header.Set("Origin", tc.origin)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if got := recorder.Header().Get("Access-Control-Allow-Origin") == tc.origin; got != tc.want {
			t.Errorf("allowed=%q production=%v origin=%s: allowed=%v; want %v", tc.allowed, tc.production, tc.origin, got, tc.want)
		}
	}
}

func TestMetricsEndpointExportsEngineMetrics(t *testing.T) {
	s := quietServer(t)
	rest(t, s, marketMakerID, engine.Sell, "ATHR", 10100, 10)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, request)
	for _, name := range []string{"tradecore_orders_total", "tradecore_engine_match_seconds_bucket", "tradecore_websocket_clients"} {
		if !strings.Contains(recorder.Body.String(), name) {
			t.Errorf("/metrics is missing %s", name)
		}
	}
}
