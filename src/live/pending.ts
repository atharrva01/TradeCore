import { create } from "zustand";
import type { Order } from "../api/types";

/** Orders sent but not yet acknowledged, shown optimistically as pending. */
type PendingState = {
  orders: Order[];
  add: (order: Order) => void;
  remove: (id: string) => void;
};

export const usePending = create<PendingState>((set, get) => ({
  orders: [],
  add: (order) => set({ orders: [order, ...get().orders] }),
  remove: (id) => set({ orders: get().orders.filter((order) => order.id !== id) }),
}));
