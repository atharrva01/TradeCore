package engine

import (
	"math/rand/v2"
	"sort"
	"strconv"
	"testing"
	"time"
)

// BenchmarkMatchOneOrder is the best case: one market order against a single
// resting order at a single price level.
func BenchmarkMatchOneOrder(b *testing.B) {
	book := NewBook("TEST")
	_, err := book.Place(Order{
		ID: "maker", UserID: "maker-user", Symbol: "TEST", Side: Buy,
		Type: Limit, Price: 10_000, Quantity: int64(b.N),
	}, 1, func() string { return "setup-trade" })
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]string, b.N)
	for i := range ids {
		ids[i] = "sell-" + strconv.Itoa(i)
	}
	nextTradeID := func() string { return "trade" }
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := book.Place(Order{
			ID: ids[i], UserID: "seller", Symbol: "TEST", Side: Sell, Type: Market, Quantity: 1,
		}, int64(i+2), nextTradeID)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDeepBookMixed is the workload the PRD targets: a book holding
// 10,000 resting orders over 400 price levels. One operation in five is a
// crossing limit order that matches one or more resting orders; the rest are
// passive limit orders or cancels, chosen to hold the depth near 10,000. It
// reports per-operation latency percentiles as p50-ns and p99-ns; ns/op gives
// single-core throughput.
func BenchmarkDeepBookMixed(b *testing.B) {
	const depth = 10_000
	rng := rand.New(rand.NewPCG(42, 7))
	book := NewBook("TEST")
	nextTradeID := func() string { return "trade" }
	const mid = 100_000
	live := make([]string, 0, 4*depth)
	seq := 0
	newOrder := func(side Side, price, quantity int64) Order {
		seq++
		return Order{
			ID: "o" + strconv.Itoa(seq), UserID: "u" + strconv.Itoa(rng.IntN(500)), Symbol: "TEST",
			Side: side, Type: Limit, Price: price, Quantity: quantity,
		}
	}
	passive := func() Order {
		if rng.IntN(2) == 0 {
			return newOrder(Buy, mid-5*int64(1+rng.IntN(200)), int64(1+rng.IntN(100)))
		}
		return newOrder(Sell, mid+5*int64(1+rng.IntN(200)), int64(1+rng.IntN(100)))
	}
	crossing := func() Order {
		if rng.IntN(2) == 0 {
			return newOrder(Buy, mid+5*int64(1+rng.IntN(6)), int64(20+rng.IntN(200)))
		}
		return newOrder(Sell, mid-5*int64(1+rng.IntN(6)), int64(20+rng.IntN(200)))
	}
	place := func(order Order, now int64) {
		result, err := book.Place(order, now, nextTradeID)
		if err != nil {
			b.Fatal(err)
		}
		if result.Order.Status == Open || result.Order.Status == PartiallyFilled {
			live = append(live, result.Order.ID)
		}
	}
	for len(book.orders) < depth {
		place(passive(), 0)
	}

	// Generate inputs up front so the timed loop measures only the book.
	passives, crossings := make([]Order, b.N), make([]Order, b.N)
	cross := make([]bool, b.N)
	for i := 0; i < b.N; i++ {
		passives[i], crossings[i], cross[i] = passive(), crossing(), rng.IntN(5) == 0
	}
	latencies := make([]time.Duration, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		switch {
		case cross[i]:
			place(crossings[i], int64(i))
		case len(book.orders) < depth:
			place(passives[i], int64(i))
		default:
			// Cancel a random live order, discarding IDs that already filled.
			for len(live) > 0 {
				j := rng.IntN(len(live))
				id := live[j]
				live[j] = live[len(live)-1]
				live = live[:len(live)-1]
				if loc, ok := book.orders[id]; ok {
					_, _ = book.Cancel(id, loc.elem.Value.(*Order).UserID)
					break
				}
			}
		}
		latencies[i] = time.Since(start)
	}
	b.StopTimer()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	b.ReportMetric(float64(latencies[len(latencies)/2].Nanoseconds()), "p50-ns")
	b.ReportMetric(float64(latencies[len(latencies)*99/100].Nanoseconds()), "p99-ns")
	b.ReportMetric(float64(len(book.orders)), "resting-orders")
}
