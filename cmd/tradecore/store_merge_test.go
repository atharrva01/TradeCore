package main

import (
	"testing"

	"tradecore/internal/engine"
)

func TestMergeChangesetsKeepsLatestRowsAndSplitsAtReset(t *testing.T) {
	account := func(cash int64) accountRow { return accountRow{ID: "u1", Cash: balance{Available: cash}} }
	order := func(filled int64) engine.Order { return engine.Order{ID: "o1", Quantity: 10, FilledQty: filled} }
	trade := func(id string) tradeRow { return tradeRow{Trade: tapeTrade{Trade: engine.Trade{ID: id}}} }
	merged := mergeChangesets([]*changeset{
		{accounts: []accountRow{account(100)}, orders: []engine.Order{order(0)}, trades: []tradeRow{trade("t1")}},
		{accounts: []accountRow{account(90), {ID: "u2"}}, orders: []engine.Order{order(4)}, trades: []tradeRow{trade("t2")}},
		{deletedOrders: []string{"o1"}, botSettings: &botSettings{Aggressiveness: 3}},
		{reset: true, accounts: []accountRow{account(1000)}},
		{accounts: []accountRow{account(990)}},
	})
	if len(merged) != 2 || merged[0].reset || !merged[1].reset {
		t.Fatalf("got %d segments; want the reset to start a second one", len(merged))
	}
	first := merged[0]
	if len(first.accounts) != 2 || first.accounts[0].Cash.Available != 90 {
		t.Fatalf("accounts = %+v; want u1 once at its latest value, then u2", first.accounts)
	}
	if len(first.orders) != 1 || first.orders[0].FilledQty != 4 || len(first.deletedOrders) != 1 {
		t.Fatalf("orders = %+v deleted = %v", first.orders, first.deletedOrders)
	}
	if len(first.trades) != 2 || first.botSettings == nil {
		t.Fatalf("trades = %d, bot settings kept = %v", len(first.trades), first.botSettings != nil)
	}
	if second := merged[1]; len(second.accounts) != 1 || second.accounts[0].Cash.Available != 990 {
		t.Fatalf("after reset accounts = %+v", second.accounts)
	}
}
