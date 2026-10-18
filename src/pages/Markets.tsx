import { Link } from "react-router-dom";
import { useInstruments } from "../api/queries";
import { FlashPrice } from "../components/FlashPrice";
import { Sparkline } from "../components/Sparkline";
import { money, pct, qty } from "../lib/format";
import { useLive } from "../live/store";

export function Markets() {
  const instruments = useInstruments();
  const ticker = useLive((state) => state.ticker);
  return (
    <div className="content-page">
      <header className="page-header">
        <div>
          <span className="eyebrow">ALL STOCKS</span>
          <h1>Markets</h1>
        </div>
        <p className="muted">Fictional companies. Prices move only when orders trade on this exchange.</p>
      </header>
      <section className="panel table markets-table">
        <div className="table-header markets-grid">
          <span>STOCK</span>
          <span>LAST</span>
          <span>CHANGE</span>
          <span>DAY RANGE</span>
          <span>VOLUME</span>
          <span>LAST 4 HOURS</span>
          <span />
        </div>
        {(instruments.data ?? []).map((item) => {
          const live = ticker[item.symbol] ?? item;
          return (
            <div className="table-row markets-grid" key={item.symbol}>
              <span>
                <b>{item.symbol}</b>
                <small>{item.name}</small>
              </span>
              <FlashPrice value={live.lastPrice} />
              <span className={live.changePct >= 0 ? "positive" : "negative"}>{pct(live.changePct)}</span>
              <span>
                {money(live.low)} – {money(live.high)}
              </span>
              <span>{qty(live.volume)}</span>
              <Sparkline points={item.spark ?? []} />
              <Link className="ghost-button small" to={`/trade/${item.symbol}`}>
                {live.halted ? "Halted" : "Trade"}
              </Link>
            </div>
          );
        })}
      </section>
    </div>
  );
}
