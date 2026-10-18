import { create } from "zustand";

export type Toast = { id: number; kind: "success" | "error" | "info"; text: string };

type ToastState = {
  toasts: Toast[];
  push: (kind: Toast["kind"], text: string) => void;
  dismiss: (id: number) => void;
};

let nextId = 1;

export const useToasts = create<ToastState>((set, get) => ({
  toasts: [],
  push: (kind, text) => {
    const id = nextId++;
    set({ toasts: [...get().toasts.slice(-3), { id, kind, text }] });
    window.setTimeout(() => get().dismiss(id), kind === "error" ? 6_000 : 4_000);
  },
  dismiss: (id) => set({ toasts: get().toasts.filter((toast) => toast.id !== id) }),
}));

export const toast = {
  success: (text: string) => useToasts.getState().push("success", text),
  error: (text: string) => useToasts.getState().push("error", text),
  info: (text: string) => useToasts.getState().push("info", text),
};
