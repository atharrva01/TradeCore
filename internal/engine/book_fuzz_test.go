package engine

import (
	"fmt"
	"testing"
)

// FuzzBookInvariants drives the book with arbitrary operation streams and
// checks the PRD invariants after every step:
//   - the book is never crossed
//   - filled + remaining = original quantity, and fills add up
//   - no order trades with itself
//   - every trade happens at the resting (maker) order's price
//   - resting quantity is conserved: what left the book equals what traded
//     plus what was cancelled
//
// Run longer with: go test ./internal/engine -run '^$' -fuzz FuzzBookInvariants -fuzztime 60s
func FuzzBookInvariants(f *testing.F) {
	f.Add([]byte{0, 10, 3, 1, 12, 4, 2, 0, 0, 3, 9, 7})
	f.Add([]byte{1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 5, 5, 5, 5, 5, 5})
	f.Fuzz(func(t *testing.T, ops []byte) {
		book := NewBook("TEST")
		next := 0
		nextTradeID := func() string { next++; return fmt.Sprintf("t%d", next) }
		resting := map[string]*Order{}
		ids := []string{}
		for i := 0; i+2 < len(ops); i += 3 {
			op, a, b := ops[i]%6, int64(ops[i+1]), int64(ops[i+2])
			if op == 5 && len(ids) > 0 {
				id := ids[int(a)%len(ids)]
				if order, ok := resting[id]; ok {
					if _, err := book.Cancel(id, order.UserID); err != nil {
						t.Fatalf("cancel of resting %s failed: %v", id, err)
					}
					delete(resting, id)
				}
				continue
			}
			side := Buy
			if op%2 == 1 {
				side = Sell
			}
			in := Order{
				ID: fmt.Sprintf("o%d", i), UserID: fmt.Sprintf("u%d", b%4), Symbol: "TEST",
				Side: side, Type: Limit, Price: 100 + a%20, Quantity: 1 + b%25,
			}
			switch op {
			case 2, 3:
				in.Type, in.Price, in.Budget = Market, 0, 1+a*b*7
			case 4:
				in.TIF = IOC
			}
			restingBefore := map[string]int64{}
			for id, order := range resting {
				restingBefore[id] = order.Quantity - order.FilledQty
			}
			result, err := book.Place(in, int64(i), nextTradeID)
			if err != nil {
				if in.Type == Market && in.Side == Buy && in.Budget <= 0 {
					continue
				}
				t.Fatalf("place %+v: %v", in, err)
			}
			var filled int64
			consumed := map[string]int64{}
			for _, fill := range result.Fills {
				trade := fill.Trade
				filled += trade.Quantity
				if trade.BuyerID == trade.SellerID {
					t.Fatalf("self trade %+v", trade)
				}
				maker := trade.SellOrder
				if in.Side == Sell {
					maker = trade.BuyOrder
				}
				if order, ok := resting[maker]; !ok || order.Price != trade.Price {
					t.Fatalf("trade %+v not at a resting maker's price", trade)
				}
				consumed[maker] += trade.Quantity
			}
			if filled != result.Order.FilledQty || result.Order.FilledQty > result.Order.Quantity {
				t.Fatalf("fill accounting broken: %+v with %d filled", result.Order, filled)
			}
			for _, removed := range result.Removed {
				delete(resting, removed.ID)
				delete(restingBefore, removed.ID)
			}
			for _, updated := range result.Updated {
				if left := restingBefore[updated.ID] - consumed[updated.ID]; left != updated.Quantity-updated.FilledQty {
					t.Fatalf("resting %s has %d left; want %d", updated.ID, updated.Quantity-updated.FilledQty, left)
				}
				if updated.Status == Filled {
					delete(resting, updated.ID)
				} else {
					copy := updated
					resting[updated.ID] = &copy
				}
			}
			if result.Order.Status == Open || result.Order.Status == PartiallyFilled {
				if in.Type == Market || in.TIF == IOC {
					t.Fatalf("%s %s order rested: %+v", in.Type, in.TIF, result.Order)
				}
				copy := result.Order
				resting[copy.ID] = &copy
				ids = append(ids, copy.ID)
			}
			if bid, ok := book.bidPrice.Min(); ok {
				if ask, ok := book.askPrice.Min(); ok && bid >= ask {
					t.Fatalf("book crossed: bid %d >= ask %d", bid, ask)
				}
			}
			if len(resting) != len(book.orders) {
				t.Fatalf("tracking %d resting orders; book has %d", len(resting), len(book.orders))
			}
		}
	})
}
