import { Link, Navigate } from "react-router-dom";
import { useState } from "react";
import { useInstruments, useMyTrades, useOrders, usePortfolio } from "../api/queries";
import { useSession } from "../api/session";
import { OrderRows } from "../components/ActivityTabs";
import { dateTime, money, qty, signedMoney } from "../lib/format";

const STATUSES = ["all", "open", "filled", "partially_filled", "cancelled", "rejected"];

export function Portfolio() {
  const status = useSession((state) => state.status);
  const portfolio = usePortfolio();
  const instruments = useInstruments();
  const [tab, setTab] = useState<"orders" | "trades">("orders");
  const [statusFilter, setStatusFilter] = useState("all");
  const [symbol, setSymbol] = useState("");
  const orders = useOrders({ status: statusFilter, symbol });
  const trades = useMyTrades(symbol);
  if (status === "anonymous") return <Navigate to="/auth" replace />;
  const data = portfolio.data;
  return (
    <div className="content-page">
      <header className="page-header">
        <div>
          <span className="eyebrow">YOUR ACCOUNT</span>
          <h1>Portfolio</h1>
        </div>
      </header>
      <section className="summary-cards">
        <div className="panel summary-card">
          <small>TOTAL VALUE</small>
          <b>{data ? money(data.totalValue, 0) : "—"}</b>
        </div>
        <div className="panel summary-card">
          <small>AVAILABLE CASH</small>
          <b>{data ? money(data.cash.available, 0) : "—"}</b>
          <span className="muted-small">{data ? `${money(data.cash.reserved, 0)} reserved by open orders` : ""}</span>
        </div>
        <div className="panel summary-card">
          <small>UNREALIZED P&L</small>
          <b className={(data?.unrealizedPnl ?? 0) >= 0 ? "positive" : "negative"}>{data ? signedMoney(data.unrealizedPnl) : "—"}</b>
        </div>
        <div className="panel summary-card">
          <small>REALIZED P&L</small>
          <b className={(data?.realizedPnl ?? 0) >= 0 ? "positive" : "negative"}>{data ? signedMoney(data.realizedPnl) : "—"}</b>
        </div>
      </section>
      <section className="panel table">
        <div className="table-title">Holdings</div>
        <div className="table-header holdings-grid">
          <span>STOCK</span>
          <span>QTY</span>
          <span>AVG BUY</span>
          <span>LAST</span>
          <span>VALUE</span>
          <span>UNREALIZED</span>
          <span>REALIZED</span>
        </div>
        {data?.holdings.map((holding) => (
          <div className="table-row holdings-grid" key={holding.symbol}>
            <span>
              <Link to={`/trade/${holding.symbol}`}>
                <b>{holding.symbol}</b>
              </Link>
            </span>
            <span>{qty(holding.quantity)}</span>
            <span>{money(holding.averagePrice)}</span>
            <span>{money(holding.lastPrice)}</span>
            <span>{money(holding.marketValue)}</span>
            <span className={holding.unrealizedPnl >= 0 ? "positive" : "negative"}>{signedMoney(holding.unrealizedPnl)}</span>
            <span className={holding.realizedPnl >= 0 ? "positive" : "negative"}>{signedMoney(holding.realizedPnl)}</span>
          </div>
        ))}
        {!data?.holdings.length && <div className="empty-table">No holdings yet. <Link to="/trade/ATHR">Place your first trade</Link>.</div>}
      </section>
      <section className="panel table">
        <div className="tabs-row">
          <div className="tabs" role="tablist">
            <button role="tab" aria-selected={tab === "orders"} className={tab === "orders" ? "tab-active" : ""} onClick={() => setTab("orders")}>
              Order history
            </button>
            <button role="tab" aria-selected={tab === "trades"} className={tab === "trades" ? "tab-active" : ""} onClick={() => setTab("trades")}>
              Trade history
            </button>
          </div>
          <div className="filters">
            <select aria-label="Filter by stock" value={symbol} onChange={(event) => setSymbol(event.target.value)}>
              <option value="">All stocks</option>
              {instruments.data?.map((item) => (
                <option key={item.symbol} value={item.symbol}>
                  {item.symbol}
                </option>
              ))}
            </select>
            {tab === "orders" && (
              <select aria-label="Filter by status" value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)}>
                {STATUSES.map((value) => (
                  <option key={value} value={value}>
                    {value === "all" ? "All statuses" : value.replace("_", " ")}
                  </option>
                ))}
              </select>
            )}
          </div>
        </div>
        {tab === "orders" ? (
          <>
            <div className="table-header orders-grid">
              <span>MARKET</span>
              <span>SIDE / TYPE</span>
              <span>PRICE</span>
              <span>FILLED / QTY</span>
              <span>STATUS</span>
              <span />
            </div>
            <OrderRows orders={orders.data ?? []} cancellable />
            {orders.data?.length === 0 && <div className="empty-table">No orders match these filters.</div>}
          </>
        ) : (
          <>
            <div className="table-header trades-grid">
              <span>TIME</span>
              <span>STOCK</span>
              <span>SIDE</span>
              <span>PRICE</span>
              <span>QTY</span>
              <span>VALUE</span>
            </div>
            {trades.data?.map((trade) => (
              <div className="table-row trades-grid" key={trade.id}>
                <span>{dateTime(trade.createdAt)}</span>
                <span>
                  <b>{trade.symbol}</b>
                </span>
                <span className={trade.side === "BUY" ? "positive" : "negative"}>{trade.side}</span>
                <span>{money(trade.price)}</span>
                <span>{qty(trade.quantity)}</span>
                <span>{money(trade.value)}</span>
              </div>
            ))}
            {trades.data?.length === 0 && <div className="empty-table">No trades yet.</div>}
          </>
        )}
      </section>
    </div>
  );
}
