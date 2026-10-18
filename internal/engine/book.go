package engine

import (
	"container/list"
	"errors"
	"sync"

	"github.com/google/btree"
)

type Side string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

type OrderType string

const (
	Limit  OrderType = "LIMIT"
	Market OrderType = "MARKET"
)

type Status string

const (
	Open            Status = "OPEN"
	PartiallyFilled Status = "PARTIALLY_FILLED"
	Filled          Status = "FILLED"
	Cancelled       Status = "CANCELLED"
	// Rejected is assigned by the exchange, never by the book: an order that
	// was accepted for matching but could not execute at all.
	Rejected Status = "REJECTED"
)

type TimeInForce string

const (
	// GTC (good till cancelled) rests any unfilled limit quantity. It is the default.
	GTC TimeInForce = "GTC"
	// IOC (immediate or cancel) cancels whatever does not fill on arrival.
	IOC TimeInForce = "IOC"
)

type Order struct {
	ID        string      `json:"id"`
	UserID    string      `json:"userId"`
	Symbol    string      `json:"symbol"`
	Side      Side        `json:"side"`
	Type      OrderType   `json:"type"`
	TIF       TimeInForce `json:"timeInForce,omitempty"`
	Price     int64       `json:"price"`
	Quantity  int64       `json:"quantity"`
	FilledQty int64       `json:"filledQty"`
	Status    Status      `json:"status"`
	CreatedAt int64       `json:"createdAt"`
	// Budget caps the cash a market buy may spend; it is required for market buys.
	Budget int64 `json:"-"`
	// Seq is the exchange-wide arrival sequence, used to restore time priority.
	Seq uint64 `json:"-"`
}

type Trade struct {
	ID        string `json:"id"`
	Symbol    string `json:"symbol"`
	BuyOrder  string `json:"buyOrderId"`
	SellOrder string `json:"sellOrderId"`
	BuyerID   string `json:"buyerId"`
	SellerID  string `json:"sellerId"`
	Price     int64  `json:"price"`
	Quantity  int64  `json:"quantity"`
	TakerSide Side   `json:"takerSide"`
	CreatedAt int64  `json:"createdAt"`
}

type Fill struct {
	Trade Trade `json:"trade"`
}

type Result struct {
	Order   Order   `json:"order"`
	Fills   []Fill  `json:"fills"`
	Removed []Order `json:"removed"`
	Updated []Order `json:"updated"`
}

type level struct {
	orders *list.List
}

type location struct {
	side  Side
	price int64
	level *level
	elem  *list.Element
}

type Book struct {
	mu       sync.Mutex
	symbol   string
	bids     map[int64]*level
	asks     map[int64]*level
	bidPrice *btree.BTreeG[int64]
	askPrice *btree.BTreeG[int64]
	orders   map[string]location
}

func NewBook(symbol string) *Book {
	return &Book{
		symbol:   symbol,
		bids:     make(map[int64]*level),
		asks:     make(map[int64]*level),
		bidPrice: btree.NewG(8, func(a, b int64) bool { return a > b }),
		askPrice: btree.NewOrderedG[int64](8),
		orders:   make(map[string]location),
	}
}

func (b *Book) Place(in Order, now int64, nextTradeID func() string) (Result, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if in.ID == "" || in.UserID == "" || in.Symbol != b.symbol || in.Quantity <= 0 {
		return Result{}, errors.New("invalid order")
	}
	if _, exists := b.orders[in.ID]; exists {
		return Result{}, errors.New("duplicate order ID")
	}
	if in.Side != Buy && in.Side != Sell {
		return Result{}, errors.New("invalid side")
	}
	if in.Type != Limit && in.Type != Market {
		return Result{}, errors.New("invalid order type")
	}
	if in.TIF == "" {
		in.TIF = GTC
	}
	if in.TIF != GTC && in.TIF != IOC {
		return Result{}, errors.New("invalid time in force")
	}
	if in.Type == Limit && in.Price <= 0 {
		return Result{}, errors.New("invalid limit price")
	}
	if in.Type == Market {
		in.Price = 0
		if in.Side == Buy && in.Budget <= 0 {
			return Result{}, errors.New("market buy requires a budget")
		}
	}
	in.CreatedAt = now
	in.FilledQty = 0
	in.Status = Open
	remaining := in.Quantity
	result := Result{Fills: []Fill{}, Removed: []Order{}, Updated: []Order{}}

	for remaining > 0 {
		best, ok := b.bestOpposite(in.Side)
		if !ok || (in.Type == Limit && !crosses(in.Side, in.Price, best)) {
			break
		}
		opposite := Sell
		if in.Side == Sell {
			opposite = Buy
		}
		queue := b.levels(opposite)[best].orders
		elem := queue.Front()
		resting := elem.Value.(*Order)
		if resting.UserID == in.UserID {
			resting.Status = Cancelled
			result.Removed = append(result.Removed, b.remove(resting.ID))
			continue
		}

		available := resting.Quantity - resting.FilledQty
		quantity := min(remaining, available)
		if in.Type == Market && in.Side == Buy {
			quantity = min(quantity, in.Budget/best)
			if quantity == 0 {
				break
			}
			in.Budget -= quantity * best
		}
		resting.FilledQty += quantity
		remaining -= quantity
		in.FilledQty += quantity

		buy, sell := in, *resting
		if in.Side == Sell {
			buy, sell = *resting, in
		}
		result.Fills = append(result.Fills, Fill{Trade: Trade{
			ID: nextTradeID(), Symbol: b.symbol, BuyOrder: buy.ID, SellOrder: sell.ID,
			BuyerID: buy.UserID, SellerID: sell.UserID, Price: resting.Price,
			Quantity: quantity, TakerSide: in.Side, CreatedAt: now,
		}})
		if resting.FilledQty == resting.Quantity {
			resting.Status = Filled
			result.Updated = append(result.Updated, b.remove(resting.ID))
		} else {
			resting.Status = PartiallyFilled
			result.Updated = append(result.Updated, *resting)
		}
	}

	switch {
	case in.FilledQty == in.Quantity:
		in.Status = Filled
	case in.Type == Market || in.TIF == IOC:
		// Market and IOC orders never rest, so any unfilled remainder is cancelled.
		in.Status = Cancelled
	case in.FilledQty > 0:
		in.Status = PartiallyFilled
		b.add(&in)
	default:
		in.Status = Open
		b.add(&in)
	}
	result.Order = in
	return result, nil
}

// Restore puts a previously accepted resting order back on the book without
// matching it. Orders must be restored in their original arrival order so
// that time priority at each price is preserved.
func (b *Book) Restore(order Order) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if order.Symbol != b.symbol || order.Type != Limit || order.Price <= 0 ||
		order.FilledQty < 0 || order.FilledQty >= order.Quantity ||
		(order.Status != Open && order.Status != PartiallyFilled) {
		return errors.New("only open limit orders can be restored")
	}
	if order.Side != Buy && order.Side != Sell {
		return errors.New("invalid side")
	}
	if _, exists := b.orders[order.ID]; exists {
		return errors.New("duplicate order ID")
	}
	if best, ok := b.bestOpposite(order.Side); ok && crosses(order.Side, order.Price, best) {
		return errors.New("restored order would cross the book")
	}
	b.add(&order)
	return nil
}

func (b *Book) Cancel(id string, userID string) (Order, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	loc, ok := b.orders[id]
	if !ok {
		return Order{}, errors.New("order not found")
	}
	order := loc.elem.Value.(*Order)
	if order.UserID != userID {
		return Order{}, errors.New("order not found")
	}
	order.Status = Cancelled
	b.remove(id)
	return *order, nil
}

func (b *Book) Snapshot() (bids, asks []Level) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotSide(Buy), b.snapshotSide(Sell)
}

type Level struct {
	Price    int64 `json:"price"`
	Quantity int64 `json:"quantity"`
	Orders   int   `json:"orders"`
}

func (b *Book) snapshotSide(side Side) []Level {
	prices := b.bidPrice
	if side == Sell {
		prices = b.askPrice
	}
	out := make([]Level, 0, min(10, prices.Len()))
	prices.Ascend(func(price int64) bool {
		level := b.levels(side)[price]
		var quantity int64
		for e := level.orders.Front(); e != nil; e = e.Next() {
			order := e.Value.(*Order)
			quantity += order.Quantity - order.FilledQty
		}
		out = append(out, Level{Price: price, Quantity: quantity, Orders: level.orders.Len()})
		return len(out) < 10
	})
	return out
}

func (b *Book) bestOpposite(side Side) (int64, bool) {
	prices := b.askPrice
	if side == Sell {
		prices = b.bidPrice
	}
	best, ok := prices.Min()
	if !ok {
		return 0, false
	}
	return best, true
}

func crosses(side Side, limit, resting int64) bool {
	if side == Buy {
		return resting <= limit
	}
	return resting >= limit
}

func (b *Book) levels(side Side) map[int64]*level {
	if side == Buy {
		return b.bids
	}
	return b.asks
}

func (b *Book) add(order *Order) {
	levels := b.levels(order.Side)
	lvl, exists := levels[order.Price]
	if !exists {
		lvl = &level{orders: list.New()}
		levels[order.Price] = lvl
		if order.Side == Buy {
			b.bidPrice.ReplaceOrInsert(order.Price)
		} else {
			b.askPrice.ReplaceOrInsert(order.Price)
		}
	}
	elem := lvl.orders.PushBack(order)
	b.orders[order.ID] = location{side: order.Side, price: order.Price, level: lvl, elem: elem}
}

func (b *Book) remove(id string) Order {
	loc := b.orders[id]
	order := loc.elem.Value.(*Order)
	loc.level.orders.Remove(loc.elem)
	delete(b.orders, id)
	if loc.level.orders.Len() == 0 {
		delete(b.levels(loc.side), loc.price)
		if loc.side == Buy {
			b.bidPrice.Delete(loc.price)
		} else {
			b.askPrice.Delete(loc.price)
		}
	}
	return *order
}
