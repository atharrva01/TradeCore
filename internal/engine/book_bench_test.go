package engine

import (
	"strconv"
	"testing"
)

func BenchmarkMatchOneOrder(b *testing.B) {
	book := NewBook("TEST")
	_, err := book.Place(Order{
		ID: "maker", UserID: "maker-user", Symbol: "TEST", Side: Buy,
		Type: Limit, Price: 10_000, Quantity: int64(b.N),
	}, 1, func() string { return "setup-trade" })
	if err != nil {
		b.Fatal(err)
	}
	nextTradeID := func() string { return "trade" }
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := book.Place(Order{
			ID: "sell-" + strconv.Itoa(i), UserID: "seller-" + strconv.Itoa(i),
			Symbol: "TEST", Side: Sell, Type: Market, Quantity: 1,
		}, int64(i+2), nextTradeID)
		if err != nil {
			b.Fatal(err)
		}
	}
}
