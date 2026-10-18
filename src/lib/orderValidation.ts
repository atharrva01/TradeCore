import { money, toPaise } from "./format";

export type Side = "BUY" | "SELL";
export type OrderType = "LIMIT" | "MARKET";
export type TimeInForce = "GTC" | "IOC";

export type Ticket = {
  side: Side;
  type: OrderType;
  timeInForce: TimeInForce;
  price: string;
  quantity: string;
};

export type TicketContext = {
  tickSize: number;
  lastPrice: number;
  availableCash: number;
  availableShares: number;
};

export type ValidTicket = {
  ok: true;
  price: number;
  quantity: number;
  estimate: number;
};

export type InvalidTicket = { ok: false; error: string };

const MAX_QUANTITY = 1_000_000;
const MAX_PRICE = 100_000_000;

/**
 * Mirrors the server's checks so mistakes are caught before a round trip:
 * whole-number quantity, a positive price on the tick, and enough cash or
 * shares. The server re-validates everything.
 */
export function validateTicket(ticket: Ticket, context: TicketContext): ValidTicket | InvalidTicket {
  const quantity = Number(ticket.quantity);
  if (!Number.isSafeInteger(quantity) || quantity < 1) {
    return { ok: false, error: "Enter a whole-number quantity of at least 1." };
  }
  if (quantity > MAX_QUANTITY) {
    return { ok: false, error: "Quantity can be at most 10,00,000 shares." };
  }
  let price = 0;
  if (ticket.type === "LIMIT") {
    price = toPaise(ticket.price);
    if (!Number.isFinite(price) || price <= 0) {
      return { ok: false, error: "Enter a limit price greater than zero." };
    }
    if (price > MAX_PRICE) {
      return { ok: false, error: "Limit price can be at most ₹10,00,000." };
    }
    if (price % context.tickSize !== 0) {
      return { ok: false, error: `Price must be a multiple of ${money(context.tickSize)}.` };
    }
  }
  const unit = ticket.type === "LIMIT" ? price : context.lastPrice;
  const estimate = unit * quantity;
  if (ticket.side === "BUY" && ticket.type === "LIMIT" && estimate > context.availableCash) {
    return { ok: false, error: `This order needs ${money(estimate)}; you have ${money(context.availableCash)} available.` };
  }
  if (ticket.side === "BUY" && ticket.type === "MARKET" && context.availableCash <= 0) {
    return { ok: false, error: "You have no available cash." };
  }
  if (ticket.side === "SELL" && quantity > context.availableShares) {
    return { ok: false, error: `You have ${context.availableShares.toLocaleString("en-IN")} shares available to sell.` };
  }
  return { ok: true, price, quantity, estimate };
}

/** Rounds a rupee amount to the nearest tick and formats it for the price box. */
export const snapToTick = (paise: number, tickSize: number) =>
  ((Math.max(tickSize, Math.round(paise / tickSize) * tickSize)) / 100).toFixed(2);
