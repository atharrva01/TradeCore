import { useMutation } from "@tanstack/react-query";
import { Clock3, X } from "lucide-react";
import { useState } from "react";
import { queryClient } from "../api/queryClient";
import { useOrders, usePortfolio } from "../api/queries";
import { authed } from "../api/session";
import type { Order } from "../api/types";
import { clock, money, qty, signedMoney, statusLabel } from "../lib/format";
import { usePending } from "../live/pending";
import { toast } from "../live/toasts";

export function OrderRows({ orders, cancellable }: { orders: Order[]; cancellable: boolean }) {
  const cancel = useMutation({
    mutationFn: (id: string) => authed(`/orders/${id}`, { method: "DELETE" }),
    onSuccess: () => toast.info("Order cancelled; its reserved balance is available again"),
    onError: (error) => toast.error(error.message),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["orders"] });
      void queryClient.invalidateQueries({ queryKey: ["portfolio"] });
    },
  });
  return (
    <>
      {orders.map((order) => {
        const pending = order.id.startsWith("pending-");
        const open = order.status === "OPEN" || order.status === "PARTIALLY_FILLED";
        return (
          <div className={`table-row orders-grid ${pending ? "pending-row" : ""}`} key={order.id}>
            <span>
              <b>{order.symbol}</b>
              <small>{clock(order.createdAt)}</small>
            </span>
            <span className={order.side === "BUY" ? "positive" : "negative"}>
              {order.side} <small>{order.type}{order.type === "LIMIT" && order.timeInForce === "IOC" ? " · IOC" : ""}</small>
            </span>
            <span>{order.type === "MARKET" ? "Market" : money(order.price)}</span>
            <span>
              {qty(order.filledQty)} / {qty(order.quantity)}
            </span>
            <span className={`status status-${pending ? "pending" : order.status.toLowerCase()}`}>
              <i /> {pending ? "Pending" : statusLabel(order.status)}
            </span>
            {cancellable && open && !pending ? (
              <button className="cancel-button" title="Cancel order" disabled={cancel.isPending} onClick={() => cancel.mutate(order.id)}>
                <X size={15} />
              </button>
            ) : (
              <span />
            )}
          </div>
        );
      })}
    </>
  );
}

function OrdersHeader() {
  return (
    <div className="table-header orders-grid">
      <span>MARKET</span>
      <span>SIDE / TYPE</span>
      <span>PRICE</span>
      <span>FILLED / QTY</span>
      <span>STATUS</span>
      <span />
    </div>
  );
}

export function ActivityTabs({ symbol }: { symbol: string }) {
  const [tab, setTab] = useState<"open" | "history" | "positions">("open");
  const open = useOrders({ status: "open" });
  const history = useOrders({ symbol });
  const portfolio = usePortfolio();
  const pending = usePending((state) => state.orders);
  const openOrders = [...pending, ...(open.data ?? [])];
  return (
    <section className="panel activity-panel">
      <div className="tabs" role="tablist">
        <button role="tab" aria-selected={tab === "open"} className={tab === "open" ? "tab-active" : ""} onClick={() => setTab("open")}>
          Open orders <span>{openOrders.length}</span>
        </button>
        <button role="tab" aria-selected={tab === "history"} className={tab === "history" ? "tab-active" : ""} onClick={() => setTab("history")}>
          {symbol} history
        </button>
        <button role="tab" aria-selected={tab === "positions"} className={tab === "positions" ? "tab-active" : ""} onClick={() => setTab("positions")}>
          Positions <span>{portfolio.data?.holdings.length ?? 0}</span>
        </button>
      </div>
      {tab !== "positions" ? (
        <div className="table">
          <OrdersHeader />
          <OrderRows orders={tab === "open" ? openOrders : (history.data ?? [])} cancellable />
          {(tab === "open" ? openOrders : (history.data ?? [])).length === 0 && (
            <div className="empty-table">
              <Clock3 size={16} /> {tab === "open" ? "No open orders. Resting orders appear here." : "No orders for this stock yet."}
            </div>
          )}
        </div>
      ) : (
        <div className="table">
          <div className="table-header positions-grid">
            <span>ASSET</span>
            <span>QTY</span>
            <span>AVG PRICE</span>
            <span>LAST</span>
            <span>UNREALIZED P&L</span>
          </div>
          {portfolio.data?.holdings.map((holding) => (
            <div className="table-row positions-grid" key={holding.symbol}>
              <span>
                <b>{holding.symbol}</b>
              </span>
              <span>{qty(holding.quantity)}</span>
              <span>{money(holding.averagePrice)}</span>
              <span>{money(holding.lastPrice)}</span>
              <span className={holding.unrealizedPnl >= 0 ? "positive" : "negative"}>{signedMoney(holding.unrealizedPnl)}</span>
            </div>
          ))}
          {!portfolio.data?.holdings.length && <div className="empty-table">You do not hold any shares yet.</div>}
        </div>
      )}
    </section>
  );
}
