import { ArrowRight, Github, Layers, LineChart, Zap } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useInstruments } from "../api/queries";
import { startGuestSession, useSession } from "../api/session";
import { Sparkline } from "../components/Sparkline";
import { money, pct } from "../lib/format";
import { useLive } from "../live/store";
import { toast } from "../live/toasts";

export function Landing() {
  const navigate = useNavigate();
  const status = useSession((state) => state.status);
  const [starting, setStarting] = useState(false);
  const instruments = useInstruments();
  const ticker = useLive((state) => state.ticker);

  async function tryAsGuest() {
    setStarting(true);
    try {
      if (status !== "signed-in") await startGuestSession();
      navigate("/trade/ATHR");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Could not start a guest session");
    } finally {
      setStarting(false);
    }
  }

  return (
    <div className="landing">
      <section className="hero">
        <span className="eyebrow">A REAL ORDER BOOK, IN YOUR BROWSER</span>
        <h1>Trade on a live exchange, matched by a real engine.</h1>
        <p>
          TradeCore runs its own order books. Every price comes from orders placed here, by people and by bots, matched by a
          price-time priority engine written in Go. You start with ₹10,00,000 of virtual cash.
        </p>
        <div className="hero-actions">
          <button className="primary-button" onClick={tryAsGuest} disabled={starting}>
            {starting ? "Opening your account…" : status === "signed-in" ? "Go to trading" : "Try as guest"} <ArrowRight size={16} />
          </button>
          <Link className="ghost-button" to="/auth?mode=signup">
            Create an account
          </Link>
          <a className="ghost-button" href="https://github.com/atharrva01/TradeCore" target="_blank" rel="noreferrer">
            <Github size={15} /> GitHub
          </a>
        </div>
      </section>
      <section className="landing-markets" aria-label="Live markets">
        {(instruments.data ?? []).map((item) => {
          const live = ticker[item.symbol] ?? item;
          return (
            <Link className="market-card" key={item.symbol} to={`/trade/${item.symbol}`}>
              <div>
                <b>{item.symbol}</b>
                <small>{item.name}</small>
              </div>
              <Sparkline points={item.spark ?? []} />
              <div className="market-card-price">
                <b>{money(live.lastPrice)}</b>
                <small className={live.changePct >= 0 ? "positive" : "negative"}>{pct(live.changePct)}</small>
              </div>
            </Link>
          );
        })}
      </section>
      <section className="how-it-works">
        <h2>How it works</h2>
        <div className="steps">
          <article>
            <Zap size={18} />
            <h3>Place an order</h3>
            <p>Buy or sell with a limit price, or at market. Your cash or shares are reserved the moment the order is accepted.</p>
          </article>
          <article>
            <Layers size={18} />
            <h3>The engine matches it</h3>
            <p>Best price first, then earliest order. Trades execute at the resting order’s price and settle atomically.</p>
          </article>
          <article>
            <LineChart size={18} />
            <h3>Watch it live</h3>
            <p>The order book, trade tape, candles and your portfolio update over a WebSocket as trades happen.</p>
          </article>
        </div>
      </section>
    </div>
  );
}
