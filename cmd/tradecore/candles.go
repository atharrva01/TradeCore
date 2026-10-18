package main

import (
	"time"

	"tradecore/internal/engine"
)

// India has no daylight saving, so a fixed UTC+5:30 offset avoids needing
// tzdata in the distroless image.
const istOffsetMs = (5*3600 + 30*60) * 1000

type candle struct {
	Time   int64 `json:"time"` // bucket start, Unix milliseconds
	Open   int64 `json:"open"`
	High   int64 `json:"high"`
	Low    int64 `json:"low"`
	Close  int64 `json:"close"`
	Volume int64 `json:"volume"`
}

type candleInterval struct {
	name string
	ms   int64
	keep int
}

// Buckets align to IST, so daily candles start at IST midnight.
var candleIntervals = []candleInterval{
	{"1m", 60_000, 1_440},
	{"5m", 300_000, 864},
	{"1h", 3_600_000, 720},
	{"1d", 86_400_000, 400},
}

func intervalByName(name string) (candleInterval, bool) {
	for _, interval := range candleIntervals {
		if interval.name == name {
			return interval, true
		}
	}
	return candleInterval{}, false
}

type candleKey struct {
	symbol   string
	interval string
	start    int64
}

func bucketStart(ms, width int64) int64 {
	return (ms+istOffsetMs)/width*width - istOffsetMs
}

func istDayStart(t time.Time) int64 {
	return bucketStart(t.UnixMilli(), 86_400_000)
}

// updateCandles folds a trade into every interval's current bucket. Trades
// arrive in time order, so only the newest bucket ever changes.
func (s *server) updateCandles(trade engine.Trade) {
	series := s.candles[trade.Symbol]
	if series == nil {
		series = make(map[string][]candle)
		s.candles[trade.Symbol] = series
	}
	for _, interval := range candleIntervals {
		start := bucketStart(trade.CreatedAt, interval.ms)
		list := series[interval.name]
		if n := len(list); n > 0 && list[n-1].Time >= start {
			last := &list[n-1]
			last.High = max(last.High, trade.Price)
			last.Low = min(last.Low, trade.Price)
			last.Close = trade.Price
			last.Volume += trade.Quantity
			start = last.Time
		} else {
			list = append(list, candle{
				Time: start, Open: trade.Price, High: trade.Price, Low: trade.Price,
				Close: trade.Price, Volume: trade.Quantity,
			})
			if len(list) > interval.keep {
				list = list[len(list)-interval.keep:]
			}
		}
		series[interval.name] = list
		s.dirtyCandles[candleKey{trade.Symbol, interval.name, start}] = true
	}
}

// candleAt finds a bucket by start time, searching from the newest.
func (s *server) candleAt(key candleKey) (candle, bool) {
	list := s.candles[key.symbol][key.interval]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Time == key.start {
			return list[i], true
		}
		if list[i].Time < key.start {
			break
		}
	}
	return candle{}, false
}

func (s *server) candleRange(symbol string, interval candleInterval, from, to int64, limit int) []candle {
	list := s.candles[symbol][interval.name]
	out := make([]candle, 0, min(limit, len(list)))
	for i := len(list) - 1; i >= 0 && len(out) < limit; i-- {
		c := list[i]
		if (to > 0 && c.Time > to) || c.Time < from {
			continue
		}
		out = append(out, c)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// sparkline returns the last closes of the 5-minute series for the markets table.
func (s *server) sparkline(symbol string, points int) []int64 {
	list := s.candles[symbol]["5m"]
	start := max(0, len(list)-points)
	out := make([]int64, 0, len(list)-start)
	for _, c := range list[start:] {
		out = append(out, c.Close)
	}
	return out
}
