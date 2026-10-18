import { Activity } from "lucide-react";
import type { TapeTrade } from "../api/types";
import { clock, money, qty } from "../lib/format";

export function TradeTape({ trades }: { trades: TapeTrade[] }) {
  return (
    <section className="panel tape-panel" aria-label="Trade tape">
      <div className="panel-heading">
        <div>
          <span className="eyebrow">LATEST MATCHES</span>
          <h2>Trade tape</h2>
        </div>
      </div>
      <div className="tape-columns">
        <span>PRICE</span>
        <span>SIZE</span>
        <span>TIME</span>
      </div>
      <div className="tape-list">
        {trades.map((trade) => (
          <div className="tape-row" key={trade.id}>
            {/* Coloured by the aggressor: green when a buyer took liquidity. */}
            <span className={trade.takerSide === "BUY" ? "positive" : "negative"}>{money(trade.price)}</span>
            <span>{qty(trade.quantity)}</span>
            <span>{clock(trade.createdAt)}</span>
          </div>
        ))}
        {trades.length === 0 && (
          <div className="empty-state">
            <Activity size={20} />
            <span>Trades will appear here</span>
          </div>
        )}
      </div>
    </section>
  );
}
