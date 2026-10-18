import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ArrowDownRight,
  ArrowUpRight,
  Activity,
  BarChart3,
  ChevronDown,
  CircleHelp,
  Clock3,
  RefreshCw,
  Search,
  ShieldCheck,
  Wallet,
  X,
} from "lucide-react";

const API = import.meta.env.VITE_API_URL ?? "http://localhost:8080/api/v1";
const TOKEN_KEY = "tradecore-session";
const USER_KEY = "tradecore-user";

type Instrument = {
  symbol: string;
  name: string;
  lastPrice: number;
  initialPrice: number;
  high: number;
  low: number;
  changePct: number;
  tickSize: number;
  volume: number;
};
type Level = { price: number; quantity: number; orders: number };
type MarketBook = { bids: Level[]; asks: Level[] };
type Trade = {
  id: string;
  symbol: string;
  buyerId: string;
  sellerId: string;
  price: number;
  quantity: number;
  takerSide: "BUY" | "SELL";
  createdAt: number;
};
type Order = {
  id: string;
  symbol: string;
  side: "BUY" | "SELL";
  type: "LIMIT" | "MARKET";
  price: number;
  quantity: number;
  filledQty: number;
  status: string;
  createdAt: number;
};
type Holding = {
  symbol: string;
  quantity: number;
  averagePrice: number;
  lastPrice: number;
  marketValue: number;
  unrealizedPnl: number;
};
type Portfolio = {
  user: string;
  cash: { available: number; reserved: number };
  holdings: Holding[];
  totalValue: number;
  unrealizedPnl: number;
  realizedPnl: number;
};
type Session = { accessToken: string; user: { id: string; name: string } };
type LeaderboardEntry = { name: string; totalValue: number; me?: boolean };

const money = (paise: number, decimals = 2) =>
  `₹${(paise / 100).toLocaleString("en-IN", {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  })}`;
const qty = (quantity: number) => quantity.toLocaleString("en-IN");
const time = (timestamp: number) =>
  new Date(timestamp).toLocaleTimeString("en-IN", { hour12: false });

class ApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
  }
}

async function request<T>(path: string, token?: string, init?: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(`${API}${path}`, {
      ...init,
      credentials: "include",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...init?.headers,
      },
    });
  } catch {
    throw new ApiError("Cannot reach the exchange. It may be waking up; retrying shortly.", 0);
  }
  if (response.status === 204) return undefined as T;
  // A sleeping host's proxy answers with an HTML page, not our JSON.
  const payload = await response.json().catch(() => null);
  if (!response.ok || payload === null) {
    const message = payload?.error?.message ?? "The exchange is starting up or unavailable. Retrying shortly.";
    throw new ApiError(message, response.status);
  }
  return payload as T;
}

function App() {
  const [token, setToken] = useState(() => localStorage.getItem(TOKEN_KEY) ?? "");
  const [userName, setUserName] = useState(() => localStorage.getItem(USER_KEY) ?? "");
  const [markets, setMarkets] = useState<Instrument[]>([]);
  const [symbol, setSymbol] = useState("ATHR");
  const [book, setBook] = useState<MarketBook>({ bids: [], asks: [] });
  const [trades, setTrades] = useState<Trade[]>([]);
  const [orders, setOrders] = useState<Order[]>([]);
  const [portfolio, setPortfolio] = useState<Portfolio | null>(null);
  const [side, setSide] = useState<"BUY" | "SELL">("BUY");
  const [type, setType] = useState<"LIMIT" | "MARKET">("LIMIT");
  const [price, setPrice] = useState("");
  const [quantity, setQuantity] = useState("10");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [activeTab, setActiveTab] = useState<"orders" | "history" | "holdings">("orders");
  const [search, setSearch] = useState("");
  const [leaderboard, setLeaderboard] = useState<LeaderboardEntry[] | null>(null);
  const tokenRef = useRef(token);
  const renewing = useRef<Promise<string> | null>(null);
  const creatingGuest = useRef(false);

  const selected = markets.find((market) => market.symbol === symbol);
  const currentBook = book;
  const openOrders = orders.filter((order) => order.status === "OPEN" || order.status === "PARTIALLY_FILLED");
  const chartValues = trades.slice().reverse().map((trade) => trade.price);
  if (selected && chartValues[chartValues.length - 1] !== selected.lastPrice) chartValues.push(selected.lastPrice);
  const chartLow = chartValues.length ? Math.min(...chartValues) : 0;
  const chartHigh = chartValues.length ? Math.max(...chartValues) : 0;
  const chartPath = chartValues.length < 2 || chartLow === chartHigh
    ? "M0,105 L900,105"
    : chartValues.map((value, index) => {
      const x = (index / (chartValues.length - 1)) * 900;
      const y = 185 - ((value - chartLow) / (chartHigh - chartLow)) * 160;
      return `${index === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
    }).join(" ");
  const filteredMarkets = useMemo(
    () => markets.filter((market) => `${market.symbol} ${market.name}`.toLowerCase().includes(search.toLowerCase())),
    [markets, search],
  );
  const estimatedTotal = useMemo(() => {
    const amount = Number(quantity);
    const unitPrice = type === "MARKET" ? selected?.lastPrice ?? 0 : Math.round(Number(price) * 100);
    return Number.isFinite(amount) && amount > 0 ? amount * unitPrice : 0;
  }, [price, quantity, selected, type]);

  const storeSession = useCallback((session: Session) => {
    localStorage.setItem(TOKEN_KEY, session.accessToken);
    localStorage.setItem(USER_KEY, session.user.name);
    tokenRef.current = session.accessToken;
    setToken(session.accessToken);
    setUserName(session.user.name);
    return session.accessToken;
  }, []);

  const createGuest = useCallback(async () => {
    if (creatingGuest.current) return "";
    creatingGuest.current = true;
    setError("");
    try {
      return storeSession(await request<Session>("/auth/guest", undefined, {
        method: "POST",
        body: JSON.stringify({ name: "Guest Trader" }),
      }));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not start a guest session.");
      return "";
    } finally {
      creatingGuest.current = false;
    }
  }, [storeSession]);

  // Access tokens last 15 minutes. Renew from the refresh cookie, and only fall
  // back to a fresh guest account when the refresh session is gone too.
  const renewSession = useCallback(() => {
    renewing.current ??= request<Session>("/auth/refresh", undefined, { method: "POST" })
      .then(storeSession)
      .catch(() => createGuest())
      .finally(() => { renewing.current = null; });
    return renewing.current;
  }, [createGuest, storeSession]);

  const authed = useCallback(async <T,>(path: string, init?: RequestInit): Promise<T> => {
    try {
      return await request<T>(path, tokenRef.current, init);
    } catch (err) {
      if (!(err instanceof ApiError) || err.status !== 401) throw err;
      const renewed = await renewSession();
      if (!renewed) throw err;
      return request<T>(path, renewed, init);
    }
  }, [renewSession]);

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      const [instrumentData, bookData, tradeData, portfolioData, orderData] = await Promise.all([
        request<Instrument[]>("/instruments"),
        request<MarketBook>(`/instruments/${symbol}/book`),
        request<Trade[]>(`/instruments/${symbol}/trades?limit=30`),
        authed<Portfolio>("/portfolio"),
        authed<Order[]>("/orders"),
      ]);
      setMarkets(instrumentData);
      setBook(bookData);
      setTrades(tradeData);
      setPortfolio(portfolioData);
      setOrders(orderData);
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Market data could not be loaded.");
    }
  }, [authed, symbol, token]);

  useEffect(() => {
    if (!token) {
      // Keep retrying while a sleeping server wakes up.
      void createGuest();
      const retry = window.setInterval(() => void createGuest(), 5000);
      return () => window.clearInterval(retry);
    }
    void refresh();
    const timer = window.setInterval(() => void refresh(), 1500);
    return () => window.clearInterval(timer);
  }, [createGuest, refresh, token]);

  useEffect(() => {
    if (selected && selected.lastPrice > 0) {
      setPrice((selected.lastPrice / 100).toFixed(2));
    }
  }, [selected?.symbol]);

  useEffect(() => {
    if (!notice) return;
    const timer = window.setTimeout(() => setNotice(""), 3500);
    return () => window.clearTimeout(timer);
  }, [notice]);

  async function placeOrder(event?: React.FormEvent) {
    event?.preventDefault();
    if (!token) return;
    const orderQty = Number(quantity);
    const orderPrice = type === "LIMIT" ? Math.round(Number(price) * 100) : 0;
    if (!Number.isSafeInteger(orderQty) || orderQty < 1) {
      setError("Enter a whole-number quantity greater than zero.");
      return;
    }
    if (type === "LIMIT" && (!Number.isSafeInteger(orderPrice) || orderPrice <= 0 || orderPrice % 5 !== 0)) {
      setError("Limit price must be positive and use ₹0.05 increments.");
      return;
    }
    setPending(true);
    setError("");
    try {
      const response = await authed<{ status: string; filledQty: number }>("/orders", {
        method: "POST",
        body: JSON.stringify({ symbol, side, type, price: orderPrice, quantity: orderQty }),
      });
      setNotice(
        response.filledQty
          ? `${side === "BUY" ? "Bought" : "Sold"} ${qty(response.filledQty)} of ${qty(orderQty)} ${symbol}`
            + (response.filledQty < orderQty && type === "MARKET" ? "; the rest was cancelled" : "")
          : "Order added to the book",
      );
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Order could not be placed.");
    } finally {
      setPending(false);
    }
  }

  async function cancelOrder(id: string) {
    if (!token) return;
    try {
      await authed(`/orders/${id}`, { method: "DELETE" });
      setNotice("Order cancelled. Your reserved balance is available again.");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Order could not be cancelled.");
    }
  }

  async function startNewSession() {
    if (!window.confirm("Start over with a new guest account? Your current guest portfolio will be left behind.")) return;
    await request("/auth/logout", undefined, { method: "POST" }).catch(() => undefined);
    localStorage.removeItem(TOKEN_KEY);
    localStorage.removeItem(USER_KEY);
    tokenRef.current = "";
    setToken("");
    setUserName("");
    setPortfolio(null);
    setOrders([]);
  }

  async function openLeaderboard() {
    try {
      setLeaderboard(await request<LeaderboardEntry[]>("/leaderboard", tokenRef.current));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Leaderboard could not be loaded.");
    }
  }

  const deepestLevel = Math.max(1, ...currentBook.bids.map((level) => level.quantity), ...currentBook.asks.map((level) => level.quantity));
  const depthWidth = (quantity: number) => `${(quantity / deepestLevel) * 100}%`;
  const spread =
    currentBook.asks.length && currentBook.bids.length
      ? currentBook.asks[0].price - currentBook.bids[0].price
      : 0;

  return (
    <main className="app-shell">
      <header className="topbar">
        <a className="brand" href="/" aria-label="TradeCore home">
          <span className="brand-mark"><Activity size={18} strokeWidth={2.5} /></span>
          <span>trade<span>core</span></span>
          <span className="brand-tag">EXCHANGE</span>
        </a>
        <div className="topbar-center"><span className="live-dot" /> OPEN 24/7 <span className="market-separator">·</span> SIMULATED EXCHANGE</div>
        <div className="topbar-actions">
          <button className="icon-button" title="Refresh market data" onClick={() => void refresh()}><RefreshCw size={16} /></button>
          <button className="icon-button" title="About this exchange" onClick={() => setNotice("TradeCore is a simulated exchange: virtual cash, fictional stocks, and prices set only by orders placed here.")}><CircleHelp size={17} /></button>
          <button className="profile-button" title="Start a new guest session" onClick={() => void startNewSession()}>
            <span className="avatar">{userName.slice(0, 1).toUpperCase() || "G"}</span>
            <span>{userName || "Guest Trader"}</span><ChevronDown size={14} />
          </button>
        </div>
      </header>

      <section className="ticker-strip">
        <div className="ticker-label"><Activity size={14} /> MARKET WATCH</div>
        {markets.map((market) => (
          <button className={`ticker-item ${symbol === market.symbol ? "ticker-active" : ""}`} key={market.symbol} onClick={() => setSymbol(market.symbol)}>
            <b>{market.symbol}</b><span>{money(market.lastPrice)}</span>
            <span className={market.changePct >= 0 ? "positive" : "negative"}>
              {market.changePct >= 0 ? "+" : ""}{market.changePct.toFixed(2)}%
            </span>
          </button>
        ))}
      </section>

      <div className="workspace">
        <aside className="sidebar">
          <div className="section-heading"><span>WATCHLIST</span><span className="plain-icon"><BarChart3 size={15} /></span></div>
          <label className="search-box"><Search size={15} /><input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search markets" /></label>
          <div className="market-list">
            {filteredMarkets.map((market) => (
              <button className={`market-row ${symbol === market.symbol ? "selected" : ""}`} key={market.symbol} onClick={() => setSymbol(market.symbol)}>
                <span className="market-icon">{market.symbol.slice(0, 1)}</span>
                <span className="market-name"><b>{market.symbol}</b><small>{market.name}</small></span>
                <span className="market-quote"><b>{money(market.lastPrice)}</b><small className={market.changePct >= 0 ? "positive" : "negative"}>{market.changePct >= 0 ? "+" : ""}{market.changePct.toFixed(2)}%</small></span>
              </button>
            ))}
          </div>
          <div className="sidebar-note"><ShieldCheck size={16} /><span>Practice trading with virtual cash. No real money involved.</span></div>
          <div className="sidebar-footer"><span className="live-dot" /> LIVE MARKET DATA <span>·</span> 1.5s</div>
        </aside>

        <section className="main-column">
          <div className="instrument-header">
            <div>
              <div className="instrument-title"><span className="instrument-badge">{symbol.slice(0, 1)}</span><h1>{symbol}</h1><span className="instrument-name">{selected?.name}</span><span className="tag">VIRTUAL</span></div>
              <div className="instrument-price">
                <strong>{money(selected?.lastPrice ?? 0)}</strong>
                <span className={(selected?.changePct ?? 0) >= 0 ? "positive" : "negative"}>
                  {(selected?.changePct ?? 0) >= 0 ? <ArrowUpRight size={15} /> : <ArrowDownRight size={15} />}
                  {(selected?.changePct ?? 0) >= 0 ? "+" : ""}{(selected?.changePct ?? 0).toFixed(2)}%
                </span>
                <small>since open</small>
              </div>
            </div>
            <div className="market-status"><span className="live-dot" /> MARKET OPEN <small>Trades around the clock</small></div>
          </div>

          <div className="chart-panel">
            <div className="panel-topline"><div><span className="eyebrow">PRICE OVERVIEW</span><span className="chart-subtitle">Recent trade prices</span></div><span className="chart-pill">LIVE <i /></span></div>
            <div className="chart-area">
              <div className="chart-y-labels"><span>{money(chartHigh)}</span><span>{money((chartHigh + chartLow) / 2)}</span><span>{money(chartLow)}</span></div>
              <div className="chart-grid">
                <svg className="price-chart" viewBox="0 0 900 210" preserveAspectRatio="none" aria-label="Price overview chart">
                  <defs><linearGradient id="chart-fill" x1="0" x2="0" y1="0" y2="1"><stop offset="0%" stopColor="#42d6a0" stopOpacity=".22" /><stop offset="100%" stopColor="#42d6a0" stopOpacity="0" /></linearGradient></defs>
                  <path d={`${chartPath} L900,210 L0,210Z`} fill="url(#chart-fill)" />
                  <path d={chartPath} fill="none" stroke="#42d6a0" strokeWidth="2.5" vectorEffect="non-scaling-stroke" />
                  <line x1="0" y1="24" x2="900" y2="24" stroke="#26313c" strokeDasharray="4 6" />
                </svg>
                <div className="chart-time-labels"><span>OLDER TRADES</span><span>RECENT</span><span>LAST</span></div>
              </div>
            </div>
            <div className="chart-footer">
              <div className="chart-metric"><small>OPEN</small><b>{money(selected?.initialPrice ?? 0)}</b></div>
              <div className="chart-metric"><small>HIGH</small><b className="positive">{money(selected?.high ?? 0)}</b></div>
              <div className="chart-metric"><small>LOW</small><b className="negative">{money(selected?.low ?? 0)}</b></div>
              <div className="chart-metric"><small>VOLUME</small><b>{qty(selected?.volume ?? 0)}</b></div>
            </div>
          </div>

          <div className="bottom-grid">
            <section className="data-panel orderbook-panel">
              <div className="panel-heading"><div><span className="eyebrow">MARKET DEPTH</span><h2>Order book</h2></div><span className="more-button">Top 10 levels</span></div>
              <div className="book-columns"><span>PRICE (INR)</span><span>SIZE</span><span>ORDERS</span></div>
              <div className="book-rows asks">
                {currentBook.asks.slice().reverse().map((level) => <button className="book-row ask-row" key={`a-${level.price}`} title="Buy at this price" onClick={() => { setSide("BUY"); setType("LIMIT"); setPrice((level.price / 100).toFixed(2)); }}>
                  <span>{money(level.price)}</span><span>{qty(level.quantity)}</span><span>{level.orders}</span><i style={{ width: depthWidth(level.quantity) }} />
                </button>)}
                {currentBook.asks.length === 0 && <div className="empty-small">No sell orders</div>}
              </div>
              <div className="spread-line"><span>SPREAD</span><b>{money(spread)}</b><span>{selected?.lastPrice ? ((spread / selected.lastPrice) * 100).toFixed(2) : "0.00"}%</span></div>
              <div className="book-rows bids">
                {currentBook.bids.map((level) => <button className="book-row bid-row" key={`b-${level.price}`} title="Sell at this price" onClick={() => { setSide("SELL"); setType("LIMIT"); setPrice((level.price / 100).toFixed(2)); }}>
                  <span>{money(level.price)}</span><span>{qty(level.quantity)}</span><span>{level.orders}</span><i style={{ width: depthWidth(level.quantity) }} />
                </button>)}
                {currentBook.bids.length === 0 && <div className="empty-small">No buy orders</div>}
              </div>
            </section>

            <section className="data-panel tape-panel">
              <div className="panel-heading"><div><span className="eyebrow">LATEST MATCHES</span><h2>Trade tape</h2></div><span className="live-mini"><i /> LIVE</span></div>
              <div className="tape-columns"><span>PRICE (INR)</span><span>SIZE</span><span>TIME</span></div>
              <div className="tape-list">
                {trades.map((trade) => <div className="tape-row" key={trade.id}>
                  <span className={trade.takerSide === "SELL" ? "negative" : "positive"}>{money(trade.price)}</span><span>{qty(trade.quantity)}</span><span>{time(trade.createdAt)}</span>
                </div>)}
                {trades.length === 0 && <div className="empty-state"><Activity size={22} /><span>Trades will appear here</span><small>Place an order to make the first trade</small></div>}
              </div>
            </section>
          </div>

          <section className="data-panel activity-panel">
            <div className="panel-heading activity-heading">
              <div className="tabs">
                <button className={activeTab === "orders" ? "tab-active" : ""} onClick={() => setActiveTab("orders")}>Open orders <span>{openOrders.length}</span></button>
                <button className={activeTab === "history" ? "tab-active" : ""} onClick={() => setActiveTab("history")}>Order history</button>
                <button className={activeTab === "holdings" ? "tab-active" : ""} onClick={() => setActiveTab("holdings")}>Positions <span>{portfolio?.holdings.length ?? 0}</span></button>
              </div>
              <button className="plain-text" onClick={() => void refresh()}><RefreshCw size={13} /> Refresh</button>
            </div>
            {activeTab !== "holdings" ? (
              <div className="activity-table">
                <div className="table-header"><span>MARKET</span><span>SIDE / TYPE</span><span>PRICE</span><span>FILLED / QTY</span><span>STATUS</span><span /></div>
                {(activeTab === "orders" ? openOrders : orders).map((order) => <div className="table-row" key={order.id}>
                  <span><b>{order.symbol}</b><small>{time(order.createdAt)}</small></span>
                  <span className={order.side === "BUY" ? "positive" : "negative"}>{order.side} <small>{order.type}</small></span>
                  <span>{order.type === "MARKET" ? "Market" : money(order.price)}</span>
                  <span>{qty(order.filledQty)} / {qty(order.quantity)}</span>
                  <span><i className="status-dot" /> {order.status.replace("_", " ")}</span>
                  {order.status === "OPEN" || order.status === "PARTIALLY_FILLED"
                    ? <button className="cancel-button" title="Cancel order" onClick={() => void cancelOrder(order.id)}><X size={15} /></button>
                    : <span />}
                </div>)}
                {(activeTab === "orders" ? openOrders : orders).length === 0 && <div className="empty-table"><Clock3 size={16} /> {activeTab === "orders" ? "No open orders. Your resting orders will show up here." : "Your orders will appear here."}</div>}
              </div>
            ) : (
              <div className="activity-table">
                <div className="table-header position-header"><span>ASSET</span><span>QUANTITY</span><span>AVG. PRICE</span><span>LAST PRICE</span><span>UNREALIZED P&L</span></div>
                {portfolio?.holdings.map((holding) => <div className="table-row position-row" key={holding.symbol}>
                  <span><b>{holding.symbol}</b><small>{markets.find((market) => market.symbol === holding.symbol)?.name}</small></span>
                  <span>{qty(holding.quantity)}</span><span>{money(holding.averagePrice)}</span><span>{money(holding.lastPrice)}</span>
                  <span className={holding.unrealizedPnl >= 0 ? "positive" : "negative"}>{money(holding.unrealizedPnl)}</span>
                </div>)}
                {!portfolio?.holdings.length && <div className="empty-table">You do not hold any shares yet.</div>}
              </div>
            )}
          </section>
        </section>

        <aside className="right-column">
          <section className="account-card">
            <div className="section-heading"><span>YOUR ACCOUNT</span><span className="plain-icon"><Wallet size={15} /></span></div>
            <div className="portfolio-label">TOTAL PORTFOLIO VALUE</div>
            <div className="portfolio-value">{portfolio ? money(portfolio.totalValue, 0) : "—"}</div>
            <div className="portfolio-change"><span className={(portfolio?.unrealizedPnl ?? 0) >= 0 ? "positive" : "negative"}>{(portfolio?.unrealizedPnl ?? 0) >= 0 ? "+" : ""}{money(portfolio?.unrealizedPnl ?? 0)}</span><small>unrealized P&L</small></div>
            <div className="account-divider" />
            <div className="balance-line"><span><i className="cash-dot" /> Available cash</span><b>{portfolio ? money(portfolio.cash.available, 0) : "—"}</b></div>
            <div className="balance-line"><span><i className="reserved-dot" /> Reserved</span><b>{money(portfolio?.cash.reserved ?? 0, 0)}</b></div>
            <div className="balance-line"><span><i className="pnl-dot" /> Realized P&L</span><b className={(portfolio?.realizedPnl ?? 0) >= 0 ? "positive" : "negative"}>{money(portfolio?.realizedPnl ?? 0, 0)}</b></div>
            <button className="leaderboard-link" onClick={() => void openLeaderboard()}><BarChart3 size={15} /> View leaderboard <ArrowUpRight size={13} /></button>
          </section>

          <section className="order-form-card">
            <div className="section-heading"><span>PLACE ORDER</span><span className="order-symbol">{symbol}</span></div>
            <div className="side-toggle"><button className={side === "BUY" ? "buy-selected" : ""} onClick={() => setSide("BUY")}>Buy</button><button className={side === "SELL" ? "sell-selected" : ""} onClick={() => setSide("SELL")}>Sell</button></div>
            <div className="type-toggle"><button className={type === "LIMIT" ? "type-selected" : ""} onClick={() => setType("LIMIT")}>Limit</button><button className={type === "MARKET" ? "type-selected" : ""} onClick={() => setType("MARKET")}>Market</button></div>
            {type === "LIMIT" && <label className="field-label">Price <span>INR per share</span>
              <div className="input-wrap"><span>₹</span><input type="number" min="0.05" step="0.05" value={price} onChange={(event) => setPrice(event.target.value)} /><div className="step-buttons"><button type="button" onClick={() => setPrice((Math.max(5, Math.round((Number(price) * 100 - 5) / 5) * 5) / 100).toFixed(2))}>−</button><button type="button" onClick={() => setPrice(((Math.round(Number(price) * 100 / 5) * 5 + 5) / 100).toFixed(2))}>+</button></div></div>
            </label>}
            <label className="field-label">Quantity <span>SHARES</span>
              <div className="input-wrap"><input type="number" min="1" step="1" value={quantity} onChange={(event) => setQuantity(event.target.value)} /><span className="unit-label">SHARES</span></div>
            </label>
            <div className="quick-quantities"><button onClick={() => setQuantity("1")}>1</button><button onClick={() => setQuantity("10")}>10</button><button onClick={() => setQuantity("25")}>25</button><button onClick={() => setQuantity("50")}>50</button><button onClick={() => setQuantity("100")}>100</button></div>
            <div className="estimate-row"><span>Estimated total</span><b>{money(estimatedTotal)}</b></div>
            <div className="estimate-row"><span>Buying power</span><b>{portfolio ? money(portfolio.cash.available, 0) : "—"}</b></div>
            <button className={`submit-order ${side === "SELL" ? "submit-sell" : ""}`} onClick={() => void placeOrder()} disabled={pending || !token}>
              {pending ? "Placing order..." : `${side === "BUY" ? "Buy" : "Sell"} ${symbol}`}
            </button>
            <div className="order-note"><ShieldCheck size={13} /> Orders are matched instantly when prices cross</div>
          </section>

          <div className="market-summary">
            <div className="summary-heading"><span>MARKET DETAILS</span><Activity size={14} /></div>
            <div><span>Tick size</span><b>{money(selected?.tickSize ?? 5)}</b></div>
            <div><span>Day volume</span><b>{qty(selected?.volume ?? 0)}</b></div>
            <div><span>Last updated</span><b><i className="live-dot" /> Live</b></div>
          </div>
        </aside>
      </div>

      <footer className="footer"><span>TRADECORE <b>SIMULATED EXCHANGE</b></span><span>All prices are virtual · No real money involved</span><span>Powered by price-time priority matching</span></footer>
      {error && <div className="toast error-toast"><span>{error}</span><button onClick={() => setError("")}><X size={15} /></button></div>}
      {notice && <div className="toast success-toast"><span>{notice}</span><button onClick={() => setNotice("")}><X size={15} /></button></div>}
      {leaderboard && <div className="modal-backdrop" onClick={() => setLeaderboard(null)}>
        <section className="leaderboard-panel" role="dialog" aria-label="Leaderboard" onClick={(event) => event.stopPropagation()}>
          <div className="panel-heading"><div><span className="eyebrow">TOP TRADERS</span><h2>Leaderboard</h2></div><button className="cancel-button" title="Close" onClick={() => setLeaderboard(null)}><X size={15} /></button></div>
          {leaderboard.map((entry, index) => <div className={`leaderboard-row ${entry.me ? "leaderboard-me" : ""}`} key={`${index}-${entry.name}`}>
            <span>{index + 1}</span><span>{entry.name}{entry.me ? " (you)" : ""}</span><b>{money(entry.totalValue, 0)}</b>
          </div>)}
          {leaderboard.length === 0 && <div className="empty-table">No traders yet.</div>}
        </section>
      </div>}
      {!markets.length && <div className="loading-screen"><div className="brand-mark"><Activity size={18} /></div><strong>Connecting to the exchange</strong><span>{error || "Free servers can take up to a minute to wake up..."}</span></div>}
    </main>
  );
}

export default App;
