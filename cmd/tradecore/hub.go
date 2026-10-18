package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"tradecore/internal/engine"
)

// WebSocket protocol (one connection per client at /ws)
//
// Client → server:
//
//	{"type":"subscribe","channels":["book:ATHR","trades:ATHR","candles:ATHR:1m","ticker"]}
//	{"type":"unsubscribe","channels":[...]}
//	{"type":"auth","token":"<access token>"}   enables the private "user" channel
//	{"type":"ping"}
//
// Server → client: {"type","channel","seq","data"}.
//
//	book            full top-10 snapshot; sent on subscribe, then at most every 100 ms
//	trade           one per trade; seq is the instrument's trade sequence, which
//	                increases by exactly one, so a jump means trades were missed
//	trades_snapshot recent trades, sent on subscribe
//	candle          the current bucket of a candle series after it changes
//	ticker          every instrument's price and day change, every second
//	order/fill/balance  private updates on the "user" channel
//
// Every event is published only after the change behind it is durable.

const (
	bookFlushInterval = 100 * time.Millisecond
	tickerInterval    = time.Second
	pingInterval      = 25 * time.Second
	clientBuffer      = 256
	maxSubscriptions  = 40
)

type wsEvent struct {
	Type    string `json:"type"`
	Channel string `json:"channel,omitempty"`
	Seq     uint64 `json:"seq,omitempty"`
	Data    any    `json:"data,omitempty"`

	userID   string // set for private events on the user channel
	coalesce bool   // only the latest event per channel matters
}

type bookData struct {
	Symbol string         `json:"symbol"`
	Bids   []engine.Level `json:"bids"`
	Asks   []engine.Level `json:"asks"`
}

type candleData struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	candle
}

type fillData struct {
	tapeTrade
	Side engine.Side `json:"side"`
}

type hub struct {
	server         *server
	originPatterns []string

	mu       sync.Mutex
	clients  map[*wsClient]struct{}
	channels map[string]map[*wsClient]struct{}
	users    map[string]map[*wsClient]struct{}
	books    map[string]wsEvent
	closed   bool
}

type wsClient struct {
	id     string
	conn   *websocket.Conn
	send   chan []byte
	cancel context.CancelFunc
	subs   map[string]bool // guarded by hub.mu
	userID string          // guarded by hub.mu
}

func newHub(s *server) *hub {
	return &hub{
		server: s, clients: make(map[*wsClient]struct{}), channels: make(map[string]map[*wsClient]struct{}),
		users: make(map[string]map[*wsClient]struct{}), books: make(map[string]wsEvent),
	}
}

// run flushes coalesced book updates and broadcasts the ticker until ctx ends.
func (h *hub) run(ctx context.Context) {
	books := time.NewTicker(bookFlushInterval)
	ticker := time.NewTicker(tickerInterval)
	defer books.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-books.C:
			h.mu.Lock()
			pending := h.books
			h.books = make(map[string]wsEvent, len(pending))
			for _, event := range pending {
				h.deliverLocked(event)
			}
			h.mu.Unlock()
		case <-ticker.C:
			event := h.server.tickerEvent()
			h.mu.Lock()
			h.deliverLocked(event)
			h.mu.Unlock()
		}
	}
}

// publish hands committed events to subscribers. It never takes server.mu,
// so it is safe to call while holding it.
func (h *hub) publish(events []wsEvent) {
	if len(events) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, event := range events {
		if event.coalesce {
			if previous, ok := h.books[event.Channel]; !ok || previous.Seq < event.Seq {
				h.books[event.Channel] = event
			}
			continue
		}
		h.deliverLocked(event)
	}
}

// deliverLocked encodes the event once and queues it for every recipient.
// The caller must hold h.mu.
func (h *hub) deliverLocked(event wsEvent) {
	targets := h.channels[event.Channel]
	if event.userID != "" {
		targets = h.users[event.userID]
	}
	if len(targets) == 0 {
		return
	}
	message, err := json.Marshal(event)
	if err != nil {
		slog.Error("encode websocket event", "type", event.Type, "error", err)
		return
	}
	for client := range targets {
		h.sendLocked(client, message)
	}
}

// sendLocked queues a message, disconnecting a client too slow to keep up;
// it reconnects and resynchronises from fresh snapshots. The caller must hold h.mu.
func (h *hub) sendLocked(client *wsClient, message []byte) {
	select {
	case client.send <- message:
	default:
		slog.Warn("disconnecting slow websocket client", "client", client.id)
		h.removeLocked(client)
		client.cancel()
	}
}

func (h *hub) removeLocked(client *wsClient) {
	if _, ok := h.clients[client]; !ok {
		return
	}
	delete(h.clients, client)
	for channel := range client.subs {
		delete(h.channels[channel], client)
	}
	if client.userID != "" {
		delete(h.users[client.userID], client)
	}
	h.server.metrics.wsClients.Set(float64(len(h.clients)))
}

// close disconnects every client during shutdown.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for client := range h.clients {
		h.removeLocked(client)
		client.cancel()
	}
}

func (h *hub) clientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *hub) serveWS(w http.ResponseWriter, r *http.Request) {
	// The server's read and write timeouts must not apply to a long-lived socket.
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.originPatterns})
	if err != nil {
		return
	}
	conn.SetReadLimit(4096)
	ctx, cancel := context.WithCancel(context.Background())
	client := &wsClient{
		id: h.server.nextID("ws-"), conn: conn, send: make(chan []byte, clientBuffer),
		cancel: cancel, subs: make(map[string]bool),
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	h.clients[client] = struct{}{}
	h.server.metrics.wsClients.Set(float64(len(h.clients)))
	h.mu.Unlock()

	go h.writeLoop(ctx, client)
	h.readLoop(ctx, client)

	h.mu.Lock()
	h.removeLocked(client)
	h.mu.Unlock()
	cancel()
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func (h *hub) writeLoop(ctx context.Context, client *wsClient) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	defer client.cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case message := <-client.send:
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := client.conn.Write(writeCtx, websocket.MessageText, message)
			cancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := client.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

type clientMessage struct {
	Type     string   `json:"type"`
	Channels []string `json:"channels"`
	Token    string   `json:"token"`
}

func (h *hub) readLoop(ctx context.Context, client *wsClient) {
	for {
		kind, payload, err := client.conn.Read(ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText || !h.server.limits.wsMessages.allow(client.id, time.Now()) {
			h.reply(client, wsEvent{Type: "error", Data: map[string]string{"code": "RATE_LIMITED", "message": "Too many messages"}})
			continue
		}
		var message clientMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			h.reply(client, wsEvent{Type: "error", Data: map[string]string{"code": "INVALID_MESSAGE", "message": "Messages must be JSON"}})
			continue
		}
		switch message.Type {
		case "subscribe":
			h.subscribe(client, message.Channels)
		case "unsubscribe":
			h.mu.Lock()
			for _, channel := range message.Channels {
				delete(client.subs, channel)
				delete(h.channels[channel], client)
			}
			h.mu.Unlock()
		case "auth":
			h.authenticate(client, message.Token)
		case "ping":
			h.reply(client, wsEvent{Type: "pong"})
		default:
			h.reply(client, wsEvent{Type: "error", Data: map[string]string{"code": "UNKNOWN_TYPE", "message": "Unknown message type"}})
		}
	}
}

func (h *hub) reply(client *wsClient, event wsEvent) {
	message, err := json.Marshal(event)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[client]; ok {
		h.sendLocked(client, message)
	}
}

// subscribe registers the channels first and then sends their snapshots, so
// no update can fall between the two; clients drop anything older than the
// snapshot by its seq.
func (h *hub) subscribe(client *wsClient, channels []string) {
	accepted := make([]string, 0, len(channels))
	h.mu.Lock()
	for _, channel := range channels {
		if client.subs[channel] {
			continue
		}
		if len(client.subs) >= maxSubscriptions || !h.server.validChannel(channel) {
			continue
		}
		client.subs[channel] = true
		if h.channels[channel] == nil {
			h.channels[channel] = make(map[*wsClient]struct{})
		}
		h.channels[channel][client] = struct{}{}
		accepted = append(accepted, channel)
	}
	h.mu.Unlock()
	for _, channel := range accepted {
		if snapshot, ok := h.server.channelSnapshot(channel); ok {
			h.reply(client, snapshot)
		}
	}
	h.reply(client, wsEvent{Type: "subscribed", Data: map[string]any{"channels": accepted}})
}

func (h *hub) authenticate(client *wsClient, token string) {
	userID, err := h.server.subjectFromRaw(token)
	if err == nil && !h.server.accountActive(userID) {
		err = errors.New("account expired")
	}
	if err != nil {
		h.reply(client, wsEvent{Type: "error", Data: map[string]string{"code": "UNAUTHORIZED", "message": "Session is no longer valid"}})
		return
	}
	h.mu.Lock()
	if client.userID != "" {
		delete(h.users[client.userID], client)
	}
	client.userID = userID
	if h.users[userID] == nil {
		h.users[userID] = make(map[*wsClient]struct{})
	}
	h.users[userID][client] = struct{}{}
	h.mu.Unlock()
	h.reply(client, wsEvent{Type: "authenticated", Channel: "user"})
}

func (s *server) validChannel(channel string) bool {
	if channel == "ticker" {
		return true
	}
	parts := strings.Split(channel, ":")
	s.mu.Lock()
	_, known := s.instruments[strings.ToUpper(firstOr(parts, 1))]
	s.mu.Unlock()
	switch {
	case len(parts) == 2 && (parts[0] == "book" || parts[0] == "trades"):
		return known && parts[1] == strings.ToUpper(parts[1])
	case len(parts) == 3 && parts[0] == "candles":
		_, ok := intervalByName(parts[2])
		return known && ok && parts[1] == strings.ToUpper(parts[1])
	}
	return false
}

func firstOr(parts []string, index int) string {
	if index < len(parts) {
		return parts[index]
	}
	return ""
}

// channelSnapshot builds the message a new subscriber starts from.
func (s *server) channelSnapshot(channel string) (wsEvent, bool) {
	if channel == "ticker" {
		return s.tickerEvent(), true
	}
	parts := strings.Split(channel, ":")
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.instruments[parts[1]]
	if item == nil {
		return wsEvent{}, false
	}
	switch parts[0] {
	case "book":
		bids, asks := s.books[item.Symbol].Snapshot()
		return wsEvent{Type: "book", Channel: channel, Seq: item.Seq, Data: bookData{item.Symbol, bids, asks}}, true
	case "trades":
		return wsEvent{Type: "trades_snapshot", Channel: channel, Seq: item.TradeSeq, Data: s.recentTrades(item.Symbol, 50)}, true
	}
	return wsEvent{}, false
}

func (s *server) tickerEvent() wsEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	items := make([]instrument, 0, len(s.instruments))
	for _, symbol := range sortedKeys(s.instruments) {
		item := s.instruments[symbol]
		s.rollDay(item, now)
		items = append(items, item.view())
	}
	return wsEvent{Type: "ticker", Channel: "ticker", Data: items}
}

// recentTrades returns the newest trades for a symbol, newest first. The
// caller must hold s.mu.
func (s *server) recentTrades(symbol string, limit int) []tapeTrade {
	trades := make([]tapeTrade, 0, limit)
	for i := len(s.trades) - 1; i >= 0 && len(trades) < limit; i-- {
		if s.trades[i].Symbol == symbol {
			trades = append(trades, s.trades[i])
		}
	}
	return trades
}

// collectEvents turns everything changed since the last commit into events.
// It runs inside commitLocked, before the changeset snapshot, because book
// events advance instrument sequence numbers. The caller must hold s.mu.
func (s *server) collectEvents() []wsEvent {
	var events []wsEvent
	for _, symbol := range sortedKeys(s.dirtyBooks) {
		item := s.instruments[symbol]
		if item == nil {
			continue
		}
		item.Seq++
		s.markInstrument(symbol)
		bids, asks := s.books[symbol].Snapshot()
		events = append(events, wsEvent{
			Type: "book", Channel: "book:" + symbol, Seq: item.Seq,
			Data: bookData{symbol, bids, asks}, coalesce: true,
		})
	}
	for _, row := range s.pendingTrades {
		trade := row.Trade
		events = append(events, wsEvent{Type: "trade", Channel: "trades:" + trade.Symbol, Seq: trade.Seq, Data: trade})
		for _, party := range []struct {
			id   string
			side engine.Side
		}{{trade.BuyerID, engine.Buy}, {trade.SellerID, engine.Sell}} {
			if user := s.accounts[party.id]; user != nil && !user.IsBot {
				events = append(events, wsEvent{Type: "fill", Channel: "user", userID: party.id, Data: fillData{trade, party.side}})
			}
		}
	}
	for key := range s.dirtyCandles {
		if c, ok := s.candleAt(key); ok {
			events = append(events, wsEvent{
				Type: "candle", Channel: "candles:" + key.symbol + ":" + key.interval,
				Data: candleData{key.symbol, key.interval, c},
			})
		}
	}
	for _, id := range sortedKeys(s.dirtyOrders) {
		order, exists := s.orders[id]
		if user := s.accounts[order.UserID]; exists && user != nil && !user.IsBot {
			events = append(events, wsEvent{Type: "order", Channel: "user", userID: order.UserID, Data: order})
		}
	}
	for _, id := range sortedKeys(s.dirtyAccounts) {
		if user := s.accounts[id]; user != nil && !user.IsBot {
			events = append(events, wsEvent{Type: "balance", Channel: "user", userID: id, Data: map[string]any{"cash": user.Cash}})
		}
	}
	return events
}
