import { create } from "zustand";
import type { OrderType, Side, TimeInForce } from "../lib/orderValidation";

/** The order form's inputs, shared so the order book can prefill them. */
type TicketState = {
  side: Side;
  type: OrderType;
  timeInForce: TimeInForce;
  price: string;
  quantity: string;
  set: (patch: Partial<Omit<TicketState, "set">>) => void;
};

export const useTicket = create<TicketState>((set) => ({
  side: "BUY",
  type: "LIMIT",
  timeInForce: "GTC",
  price: "",
  quantity: "10",
  set: (patch) => set(patch),
}));
