import type { Book, Level } from "../api/types";
import { money, qty } from "../lib/format";
import { useTicket } from "../live/ticket";

type Props = { book?: Book; lastPrice: number };

function Row({ level, side, deepest, onPick }: { level: Level; side: "ask" | "bid"; deepest: number; onPick: () => void }) {
  return (
    <button className={`book-row ${side}-row`} onClick={onPick} title={side === "ask" ? "Buy at this price" : "Sell at this price"}>
      <span>{money(level.price)}</span>
      <span>{qty(level.quantity)}</span>
      <span>{level.orders}</span>
      <i style={{ width: `${(level.quantity / deepest) * 100}%` }} />
    </button>
  );
}

export function OrderBook({ book, lastPrice }: Props) {
  const setTicket = useTicket((state) => state.set);
  const bids = book?.bids ?? [];
  const asks = book?.asks ?? [];
  const deepest = Math.max(1, ...bids.map((level) => level.quantity), ...asks.map((level) => level.quantity));
  const spread = bids.length && asks.length ? asks[0].price - bids[0].price : 0;
  const pick = (side: "BUY" | "SELL", price: number) => setTicket({ side, type: "LIMIT", price: (price / 100).toFixed(2) });
  return (
    <section className="panel book-panel" aria-label="Order book">
      <div className="panel-heading">
        <div>
          <span className="eyebrow">MARKET DEPTH</span>
          <h2>Order book</h2>
        </div>
        <span className="muted-small">Top 10</span>
      </div>
      <div className="book-columns">
        <span>PRICE</span>
        <span>SIZE</span>
        <span>ORDERS</span>
      </div>
      <div className="book-side asks">
        {asks
          .slice()
          .reverse()
          .map((level) => (
            <Row key={`a-${level.price}`} level={level} side="ask" deepest={deepest} onPick={() => pick("BUY", level.price)} />
          ))}
        {asks.length === 0 && <div className="empty-small">No sell orders</div>}
      </div>
      <div className="spread-line">
        <span>SPREAD</span>
        <b>{money(spread)}</b>
        <span>{lastPrice ? ((spread / lastPrice) * 100).toFixed(2) : "0.00"}%</span>
      </div>
      <div className="book-side bids">
        {bids.map((level) => (
          <Row key={`b-${level.price}`} level={level} side="bid" deepest={deepest} onPick={() => pick("SELL", level.price)} />
        ))}
        {bids.length === 0 && <div className="empty-small">No buy orders</div>}
      </div>
    </section>
  );
}
