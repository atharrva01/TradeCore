import { useMutation } from "@tanstack/react-query";
import { ShieldCheck } from "lucide-react";
import { type FormEvent, useState } from "react";
import { queryClient } from "../api/queryClient";
import { usePortfolio } from "../api/queries";
import { authed, ensureSession } from "../api/session";
import type { Instrument, Order, PlaceOrderResponse } from "../api/types";
import { money, qty } from "../lib/format";
import { snapToTick, validateTicket } from "../lib/orderValidation";
import { usePending } from "../live/pending";
import { useLive } from "../live/store";
import { toast } from "../live/toasts";
import { useTicket } from "../live/ticket";

let pendingId = 0;

export function OrderForm({ instrument }: { instrument: Instrument }) {
  const ticket = useTicket();
  const portfolio = usePortfolio();
  const connection = useLive((state) => state.connection);
  const [error, setError] = useState("");
  const holding = portfolio.data?.holdings.find((item) => item.symbol === instrument.symbol);
  const availableShares = holding ? holding.quantity - holding.reserved : 0;
  const availableCash = portfolio.data?.cash.available ?? 0;
  const check = validateTicket(ticket, {
    tickSize: instrument.tickSize,
    lastPrice: instrument.lastPrice,
    availableCash,
    availableShares,
  });

  const place = useMutation({
    mutationFn: async (body: object) => {
      await ensureSession();
      return authed<PlaceOrderResponse>("/orders", { method: "POST", body: JSON.stringify(body) });
    },
  });

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!check.ok) {
      setError(check.error);
      return;
    }
    setError("");
    const id = `pending-${++pendingId}`;
    const optimistic: Order = {
      id, userId: "", symbol: instrument.symbol, side: ticket.side, type: ticket.type, timeInForce: ticket.timeInForce,
      price: check.price, quantity: check.quantity, filledQty: 0, status: "OPEN", createdAt: Date.now(),
    };
    usePending.getState().add(optimistic);
    try {
      const response = await place.mutateAsync({
        symbol: instrument.symbol, side: ticket.side, type: ticket.type, timeInForce: ticket.type === "LIMIT" ? ticket.timeInForce : undefined,
        price: check.price, quantity: check.quantity,
      });
      // With the socket up, every fill already raised its own toast.
      if (response.filledQty === 0) toast.info(response.status === "CANCELLED" ? "Nothing filled; the order was cancelled" : "Order added to the book");
      else if (connection !== "open") toast.success(`${ticket.side === "BUY" ? "Bought" : "Sold"} ${qty(response.filledQty)} ${instrument.symbol}`);
      if (response.status === "CANCELLED" && response.filledQty > 0) toast.info(`${qty(check.quantity - response.filledQty)} unfilled shares were cancelled`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Order could not be placed.");
    } finally {
      usePending.getState().remove(id);
      void queryClient.invalidateQueries({ queryKey: ["orders"] });
      void queryClient.invalidateQueries({ queryKey: ["portfolio"] });
    }
  }

  const step = (direction: 1 | -1) => {
    const current = Math.round(Number(ticket.price || instrument.lastPrice / 100) * 100);
    ticket.set({ price: snapToTick(current + direction * instrument.tickSize, instrument.tickSize) });
  };

  return (
    <form className="panel order-form" onSubmit={submit} aria-label="Place order">
      <div className="side-toggle">
        <button type="button" className={ticket.side === "BUY" ? "buy-selected" : ""} onClick={() => ticket.set({ side: "BUY" })}>
          Buy
        </button>
        <button type="button" className={ticket.side === "SELL" ? "sell-selected" : ""} onClick={() => ticket.set({ side: "SELL" })}>
          Sell
        </button>
      </div>
      <div className="type-toggle">
        {(["LIMIT", "MARKET"] as const).map((type) => (
          <button type="button" key={type} className={ticket.type === type ? "type-selected" : ""} onClick={() => ticket.set({ type })}>
            {type === "LIMIT" ? "Limit" : "Market"}
          </button>
        ))}
        {ticket.type === "LIMIT" && (
          <label className="tif-select" title="GTC rests until cancelled; IOC cancels whatever does not fill immediately">
            <select value={ticket.timeInForce} onChange={(event) => ticket.set({ timeInForce: event.target.value as "GTC" | "IOC" })}>
              <option value="GTC">GTC</option>
              <option value="IOC">IOC</option>
            </select>
          </label>
        )}
      </div>
      {ticket.type === "LIMIT" && (
        <label className="field-label">
          Price <span>INR per share</span>
          <div className="input-wrap">
            <span>₹</span>
            <input
              aria-label="Limit price"
              inputMode="decimal"
              value={ticket.price}
              placeholder={(instrument.lastPrice / 100).toFixed(2)}
              onChange={(event) => ticket.set({ price: event.target.value })}
            />
            <div className="step-buttons">
              <button type="button" aria-label="Lower price by one tick" onClick={() => step(-1)}>
                −
              </button>
              <button type="button" aria-label="Raise price by one tick" onClick={() => step(1)}>
                +
              </button>
            </div>
          </div>
        </label>
      )}
      <label className="field-label">
        Quantity <span>SHARES</span>
        <div className="input-wrap">
          <input aria-label="Quantity" inputMode="numeric" value={ticket.quantity} onChange={(event) => ticket.set({ quantity: event.target.value })} />
        </div>
      </label>
      <div className="quick-quantities">
        {[1, 10, 25, 50, 100].map((amount) => (
          <button type="button" key={amount} onClick={() => ticket.set({ quantity: String(amount) })}>
            {amount}
          </button>
        ))}
      </div>
      <div className="estimate-row">
        <span>{ticket.type === "MARKET" ? "Estimated total (at last price)" : "Estimated total"}</span>
        <b>{check.ok ? money(check.estimate) : "—"}</b>
      </div>
      <div className="estimate-row">
        <span>{ticket.side === "BUY" ? "Available cash" : "Shares available"}</span>
        <b>{ticket.side === "BUY" ? money(availableCash, 0) : qty(availableShares)}</b>
      </div>
      {error && (
        <div className="form-error" role="alert">
          {error}
        </div>
      )}
      <button type="submit" className={`submit-order ${ticket.side === "SELL" ? "submit-sell" : ""}`} disabled={instrument.halted}>
        {instrument.halted ? "Trading halted" : `${ticket.side === "BUY" ? "Buy" : "Sell"} ${instrument.symbol}`}
      </button>
      <div className="order-note">
        <ShieldCheck size={13} /> Matched by price, then time. Virtual money only.
      </div>
    </form>
  );
}
