import { ArrowDownRight, ArrowUpRight } from "lucide-react";
import { useEffect } from "react";
import { Navigate, useParams } from "react-router-dom";
import { useBookSnapshot, useInstruments, useRecentTrades } from "../api/queries";
import { ensureSession, useSession } from "../api/session";
import { ActivityTabs } from "../components/ActivityTabs";
import { FlashPrice } from "../components/FlashPrice";
import { OrderBook } from "../components/OrderBook";
import { OrderForm } from "../components/OrderForm";
import { PriceChart } from "../components/PriceChart";
import { TradeTape } from "../components/TradeTape";
import { money, pct, qty } from "../lib/format";
import { useChannels } from "../live/socket";
import { useLive } from "../live/store";
import { toast } from "../live/toasts";
import { useTicket } from "../live/ticket";

export function Trade() {
  const symbol = (useParams().symbol ?? "ATHR").toUpperCase();
  const instruments = useInstruments();
  const status = useSession((state) => state.status);
  const live = useLive((state) => state.ticker[symbol]);
  const liveBook = useLive((state) => state.books[symbol]);
  const tape = useLive((state) => state.tapes[symbol]);
  const bookSnapshot = useBookSnapshot(symbol);
  const recentTrades = useRecentTrades(symbol);
  useChannels([`book:${symbol}`, `trades:${symbol}`]);

  // Visitors can trade straight away: a guest account is created on arrival.
  useEffect(() => {
    if (status === "anonymous") ensureSession().catch((error: Error) => toast.error(error.message));
  }, [status]);

  const instrument = live ?? instruments.data?.find((item) => item.symbol === symbol);
  useEffect(() => {
    if (instrument) useTicket.getState().set({ price: (instrument.lastPrice / 100).toFixed(2) });
    // Prefill only when the stock changes, not on every price tick.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [symbol, Boolean(instrument)]);

  if (instruments.data && !instruments.data.some((item) => item.symbol === symbol)) return <Navigate to="/markets" replace />;
  if (!instrument) return <div className="page-loading">Loading {symbol}…</div>;

  const book = liveBook && (!bookSnapshot.data || liveBook.seq >= bookSnapshot.data.seq) ? liveBook : bookSnapshot.data;
  const trades = tape?.list ?? recentTrades.data ?? [];
  const up = instrument.changePct >= 0;
  return (
    <div className="trade-page">
      <header className="instrument-header">
        <div className="instrument-title">
          <span className="instrument-badge">{symbol.slice(0, 1)}</span>
          <h1>{symbol}</h1>
          <span className="instrument-name">{instrument.name}</span>
          {instrument.halted && <span className="halted-tag">TRADING HALTED</span>}
        </div>
        <div className="instrument-stats">
          <FlashPrice className="instrument-price" value={instrument.lastPrice} />
          <span className={up ? "positive" : "negative"}>
            {up ? <ArrowUpRight size={15} /> : <ArrowDownRight size={15} />}
            {pct(instrument.changePct)} today
          </span>
          <span className="stat">
            <small>HIGH</small>
            {money(instrument.high)}
          </span>
          <span className="stat">
            <small>LOW</small>
            {money(instrument.low)}
          </span>
          <span className="stat">
            <small>VOLUME</small>
            {qty(instrument.volume)}
          </span>
        </div>
      </header>
      <div className="trade-grid">
        <div className="trade-chart">
          <PriceChart key={symbol} symbol={symbol} />
        </div>
        <div className="trade-book">
          <OrderBook book={book} lastPrice={instrument.lastPrice} />
        </div>
        <div className="trade-side">
          <OrderForm instrument={instrument} />
          <TradeTape trades={trades} />
        </div>
        <div className="trade-activity">
          <ActivityTabs symbol={symbol} />
        </div>
      </div>
    </div>
  );
}
