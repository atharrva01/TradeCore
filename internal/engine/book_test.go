package engine

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

func tradeIDFactory() func() string {
	var next int
	return func() string {
		next++
		return fmt.Sprintf("trade-%d", next)
	}
}

func order(id, user string, side Side, kind OrderType, price, quantity int64) Order {
	return Order{ID: id, UserID: user, Symbol: "TEST", Side: side, Type: kind, Price: price, Quantity: quantity}
}

func TestPriceTimePriorityAndPartialFill(t *testing.T) {
	book := NewBook("TEST")
	nextTradeID := tradeIDFactory()
	for _, maker := range []Order{
		order("ask-1", "seller-1", Sell, Limit, 10_100, 5),
		order("ask-2", "seller-2", Sell, Limit, 10_100, 10),
		order("ask-3", "seller-3", Sell, Limit, 10_150, 10),
	} {
		if _, err := book.Place(maker, 1, nextTradeID); err != nil {
			t.Fatal(err)
		}
	}

	result, err := book.Place(order("buy", "buyer", Buy, Limit, 10_150, 12), 2, nextTradeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Order.FilledQty != 12 || result.Order.Status != Filled {
		t.Fatalf("incoming order = %+v; want fully filled", result.Order)
	}
	if len(result.Fills) != 2 {
		t.Fatalf("got %d fills, want 2", len(result.Fills))
	}
	if result.Fills[0].Trade.SellOrder != "ask-1" || result.Fills[0].Trade.Price != 10_100 || result.Fills[0].Trade.Quantity != 5 {
		t.Fatalf("first fill = %+v; want oldest order at maker price", result.Fills[0])
	}
	if result.Fills[1].Trade.SellOrder != "ask-2" || result.Fills[1].Trade.Quantity != 7 {
		t.Fatalf("second fill = %+v; want next FIFO order partially filled", result.Fills[1])
	}
	_, asks := book.Snapshot()
	if len(asks) != 2 || asks[0].Quantity != 3 || asks[1].Price != 10_150 {
		t.Fatalf("remaining asks = %+v", asks)
	}
}

func TestMarketRemainderDoesNotRest(t *testing.T) {
	book := NewBook("TEST")
	nextTradeID := tradeIDFactory()
	_, err := book.Place(order("ask", "seller", Sell, Limit, 100, 3), 1, nextTradeID)
	if err != nil {
		t.Fatal(err)
	}
	buy := order("market-buy", "buyer", Buy, Market, 0, 5)
	buy.Budget = 1_000_000
	result, err := book.Place(buy, 2, nextTradeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Order.Status != Cancelled || result.Order.FilledQty != 3 {
		t.Fatalf("market order = %+v; want 3 filled and the remainder cancelled", result.Order)
	}
	bids, asks := book.Snapshot()
	if len(bids) != 0 || len(asks) != 0 {
		t.Fatalf("market remainder unexpectedly rested: bids=%v asks=%v", bids, asks)
	}
}

func TestSelfTradePreventionCancelsRestingOrder(t *testing.T) {
	book := NewBook("TEST")
	nextTradeID := tradeIDFactory()
	_, err := book.Place(order("own-ask", "same-user", Sell, Limit, 100, 4), 1, nextTradeID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := book.Place(order("buy", "same-user", Buy, Limit, 100, 2), 2, nextTradeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Fills) != 0 || len(result.Removed) != 1 || result.Removed[0].Status != Cancelled {
		t.Fatalf("self-trade outcome = %+v", result)
	}
	bids, asks := book.Snapshot()
	if len(bids) != 1 || len(asks) != 0 {
		t.Fatalf("post-prevention book: bids=%v asks=%v", bids, asks)
	}
}

func TestCancelOnlyOwnOpenOrder(t *testing.T) {
	book := NewBook("TEST")
	_, err := book.Place(order("bid", "owner", Buy, Limit, 100, 2), 1, tradeIDFactory())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := book.Cancel("bid", "other"); err == nil {
		t.Fatal("cancel by non-owner should fail")
	}
	cancelled, err := book.Cancel("bid", "owner")
	if err != nil || cancelled.Status != Cancelled {
		t.Fatalf("cancel result = %+v, %v", cancelled, err)
	}
	bids, _ := book.Snapshot()
	if len(bids) != 0 {
		t.Fatalf("cancelled order remains on book: %v", bids)
	}
}

func TestMarketBuyStopsAtBudget(t *testing.T) {
	book := NewBook("TEST")
	nextTradeID := tradeIDFactory()
	if _, err := book.Place(order("ask", "seller", Sell, Limit, 100, 50), 1, nextTradeID); err != nil {
		t.Fatal(err)
	}
	buy := order("market-buy", "buyer", Buy, Market, 0, 50)
	buy.Budget = 1_050
	result, err := book.Place(buy, 2, nextTradeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Order.FilledQty != 10 || result.Order.Status != Cancelled {
		t.Fatalf("market order = %+v; want 10 filled within budget", result.Order)
	}
	if _, err := book.Place(order("no-budget", "buyer", Buy, Market, 0, 1), 3, nextTradeID); err == nil {
		t.Fatal("market buy without a budget should be rejected")
	}
}

func TestTradesRecordTakerSide(t *testing.T) {
	book := NewBook("TEST")
	nextTradeID := tradeIDFactory()
	if _, err := book.Place(order("bid", "buyer", Buy, Limit, 100, 1), 1, nextTradeID); err != nil {
		t.Fatal(err)
	}
	result, err := book.Place(order("sell", "seller", Sell, Limit, 100, 1), 2, nextTradeID)
	if err != nil || len(result.Fills) != 1 || result.Fills[0].Trade.TakerSide != Sell {
		t.Fatalf("fills = %+v, %v; want one fill with seller as taker", result.Fills, err)
	}
}

func TestRandomOrdersKeepBookInvariants(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	book := NewBook("TEST")
	nextTradeID := tradeIDFactory()
	live := []string{}
	for i := 0; i < 20_000; i++ {
		if len(live) > 0 && rng.IntN(4) == 0 {
			j := rng.IntN(len(live))
			id := live[j]
			live = append(live[:j], live[j+1:]...)
			if loc, ok := book.orders[id]; ok {
				book.Cancel(id, loc.elem.Value.(*Order).UserID)
			}
			continue
		}
		side := Buy
		if rng.IntN(2) == 0 {
			side = Sell
		}
		in := order(fmt.Sprintf("o-%d", i), fmt.Sprintf("u-%d", rng.IntN(8)), side, Limit, int64(90+rng.IntN(21))*5, int64(1+rng.IntN(20)))
		if rng.IntN(5) == 0 {
			in.Type, in.Price, in.Budget = Market, 0, int64(rng.IntN(20_000))+1
		}
		result, err := book.Place(in, int64(i), nextTradeID)
		if err != nil {
			t.Fatal(err)
		}
		var filled int64
		for _, fill := range result.Fills {
			filled += fill.Trade.Quantity
			if fill.Trade.BuyerID == fill.Trade.SellerID {
				t.Fatalf("self trade executed: %+v", fill.Trade)
			}
		}
		if filled != result.Order.FilledQty || result.Order.FilledQty > result.Order.Quantity {
			t.Fatalf("fill accounting broken for %+v (fills total %d)", result.Order, filled)
		}
		if result.Order.Status == Open || result.Order.Status == PartiallyFilled {
			live = append(live, result.Order.ID)
		}
		bid, hasBid := book.bidPrice.Min()
		ask, hasAsk := book.askPrice.Min()
		if hasBid && hasAsk && bid >= ask {
			t.Fatalf("book crossed after order %d: bid %d >= ask %d", i, bid, ask)
		}
	}
}
