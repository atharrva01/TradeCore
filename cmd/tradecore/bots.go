package main

import (
	"context"
	"log/slog"
	"math"
	mathrand "math/rand/v2"
	"net/http"
	"sort"
	"time"

	"tradecore/internal/engine"
)

const (
	marketMakerID  = "market-maker"
	noiseTraderID  = "noise-trader"
	momentumID     = "momentum-trader"
	botInterval    = 2 * time.Second
	momentumWindow = 20
	noiseOrderTTL  = 30 * time.Second
)

var (
	botIDs   = []string{marketMakerID, noiseTraderID, momentumID}
	botNames = map[string]string{
		marketMakerID: "Market Maker", noiseTraderID: "Noise Trader", momentumID: "Momentum Trader",
	}
)

// botSettings are tunable from the admin page.
type botSettings struct {
	Enabled bool `json:"enabled"`
	// Aggressiveness (1–10) scales how many orders the noise and momentum
	// bots send per tick and how large they are.
	Aggressiveness int `json:"aggressiveness"`
	// Volatility (0.2–5) widens the market maker's quotes and the range the
	// noise trader prices around, so prices swing further.
	Volatility float64 `json:"volatility"`
	Momentum   bool    `json:"momentum"`
}

func defaultBotSettings() botSettings {
	return botSettings{Enabled: true, Aggressiveness: 2, Volatility: 1, Momentum: true}
}

func (b botSettings) validate() *apiError {
	if b.Aggressiveness < 1 || b.Aggressiveness > 10 {
		return &apiError{http.StatusBadRequest, "INVALID_SETTINGS", "Aggressiveness must be between 1 and 10"}
	}
	if math.IsNaN(b.Volatility) || b.Volatility < 0.2 || b.Volatility > 5 {
		return &apiError{http.StatusBadRequest, "INVALID_SETTINGS", "Volatility must be between 0.2 and 5"}
	}
	return nil
}

func (s *server) runBots(ctx context.Context) {
	ticker := time.NewTicker(botInterval)
	defer ticker.Stop()
	for tick := 1; ; tick++ {
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

// runBotTick lets every bot act once on every active instrument. Bots go
// through submit like people do; they only skip the HTTP rate limits.
func (s *server) runBotTick() {
	s.mu.Lock()
	// Bots never wait for durability; their changes join the writer queue.
	defer func() { s.commitLocked(); s.mu.Unlock() }()
	if !s.bots.Enabled {
		return
	}
	now := time.Now()
	symbols := sortedKeys(s.instruments)
	for _, symbol := range symbols {
		if s.instruments[symbol].Halted {
			continue
		}
		s.quoteMarket(symbol)
		s.noiseTrade(symbol, now)
		if s.bots.Momentum {
			s.momentumTrade(symbol)
		}
	}
}

// quoteMarket replaces the market maker's quotes with three levels each side
// around the last price. The caller must hold s.mu.
func (s *server) quoteMarket(symbol string) {
	item := s.instruments[symbol]
	for _, id := range sortedKeys(s.botOpen[marketMakerID]) {
		if s.orders[id].Symbol == symbol {
			s.cancelLocked(id, marketMakerID)
		}
	}
	tick := item.TickSize
	step := tick * max(1, int64(math.Round(s.bots.Volatility*float64(item.LastPrice)*0.0008/float64(tick))))
	for level := int64(1); level <= 3; level++ {
		size := 40 * level * int64(1+mathrand.IntN(3))
		for _, quote := range []struct {
			side  engine.Side
			price int64
		}{{engine.Buy, item.LastPrice - step*level}, {engine.Sell, item.LastPrice + step*level}} {
			if quote.price <= 0 {
				continue
			}
			if _, err := s.submit(marketMakerID, orderRequest{
				Symbol: symbol, Side: quote.side, Type: engine.Limit, Price: quote.price, Quantity: size,
			}); err != nil {
				slog.Warn("market maker quote rejected", "symbol", symbol, "side", quote.side, "price", quote.price, "error", err.message)
			}
		}
	}
}

// noiseTrade sends a few small random orders: some at market, most as limits
// scattered around the last price. Stale noise limits are cancelled so the
// bot does not accumulate resting orders.
func (s *server) noiseTrade(symbol string, now time.Time) {
	for _, id := range sortedKeys(s.botOpen[noiseTraderID]) {
		if order := s.orders[id]; order.Symbol == symbol && now.Sub(time.UnixMilli(order.CreatedAt)) > noiseOrderTTL {
			s.cancelLocked(id, noiseTraderID)
		}
	}
	item := s.instruments[symbol]
	for i := 0; i < s.bots.Aggressiveness; i++ {
		side := engine.Buy
		if mathrand.IntN(2) == 0 {
			side = engine.Sell
		}
		request := orderRequest{Symbol: symbol, Side: side, Quantity: int64(1 + mathrand.IntN(5*s.bots.Aggressiveness))}
		if mathrand.IntN(10) < 4 {
			request.Type = engine.Market
		} else {
			spread := max(1, int(4*s.bots.Volatility))
			offset := int64(mathrand.IntN(2*spread+1) - spread)
			request.Type, request.Price = engine.Limit, item.LastPrice+offset*item.TickSize
			if request.Price <= 0 {
				continue
			}
		}
		if _, err := s.submit(noiseTraderID, request); err != nil && err.code != "NO_LIQUIDITY" {
			slog.Warn("noise order rejected", "symbol", symbol, "error", err.message)
		}
	}
}

// momentumTrade follows short-term trends: when the last price has moved
// clearly away from its recent average, it sometimes trades in that direction.
func (s *server) momentumTrade(symbol string) {
	prices := s.recentPrices[symbol]
	if len(prices) < momentumWindow/2 || mathrand.IntN(10) >= 3 {
		return
	}
	sum := int64(0)
	for _, price := range prices {
		sum += price
	}
	average := float64(sum) / float64(len(prices))
	last := float64(prices[len(prices)-1])
	if math.Abs(last-average)/average < 0.001 {
		return
	}
	side := engine.Buy
	if last < average {
		side = engine.Sell
	}
	request := orderRequest{
		Symbol: symbol, Side: side, Type: engine.Market, Quantity: int64(5 * s.bots.Aggressiveness * (1 + mathrand.IntN(3))),
	}
	if _, err := s.submit(momentumID, request); err != nil && err.code != "NO_LIQUIDITY" {
		slog.Warn("momentum order rejected", "symbol", symbol, "error", err.message)
	}
}

func sortedOrders(orders []engine.Order) {
	sort.Slice(orders, func(i, j int) bool { return orders[i].CreatedAt > orders[j].CreatedAt })
}
