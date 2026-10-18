import { useQuery } from "@tanstack/react-query";
import { request } from "./client";
import { authed, useSession } from "./session";
import type {
  AdminStats,
  Book,
  BotSettings,
  Candle,
  CandleInterval,
  Health,
  Instrument,
  Leaderboard,
  MyTrade,
  Order,
  Portfolio,
  TapeTrade,
} from "./types";

export const useHealth = () => useQuery({ queryKey: ["health"], queryFn: () => request<Health>("/health"), staleTime: 60_000 });

export const useInstruments = () =>
  useQuery({ queryKey: ["instruments"], queryFn: () => request<Instrument[]>("/instruments"), refetchInterval: 60_000 });

export const useBookSnapshot = (symbol: string) =>
  useQuery({ queryKey: ["book", symbol], queryFn: () => request<Book>(`/instruments/${symbol}/book`) });

export const useRecentTrades = (symbol: string) =>
  useQuery({ queryKey: ["trades", symbol], queryFn: () => request<TapeTrade[]>(`/instruments/${symbol}/trades?limit=50`) });

export const useCandles = (symbol: string, interval: CandleInterval) =>
  useQuery({
    queryKey: ["candles", symbol, interval],
    queryFn: () => request<{ candles: Candle[] }>(`/instruments/${symbol}/candles?interval=${interval}&limit=500`).then((r) => r.candles),
  });

const useSignedIn = () => useSession((state) => state.status === "signed-in" && Boolean(state.token));

export const usePortfolio = () =>
  useQuery({ queryKey: ["portfolio"], queryFn: () => authed<Portfolio>("/portfolio"), enabled: useSignedIn() });

export const useOrders = (filter: { status?: string; symbol?: string } = {}) => {
  const params = new URLSearchParams();
  if (filter.status) params.set("status", filter.status);
  if (filter.symbol) params.set("symbol", filter.symbol);
  return useQuery({
    queryKey: ["orders", filter.status ?? "", filter.symbol ?? ""],
    queryFn: () => authed<Order[]>(`/orders?${params}`),
    enabled: useSignedIn(),
  });
};

export const useMyTrades = (symbol = "") =>
  useQuery({
    queryKey: ["myTrades", symbol],
    queryFn: () => authed<MyTrade[]>(`/trades/me${symbol ? `?symbol=${symbol}` : ""}`),
    enabled: useSignedIn(),
  });

export const useLeaderboard = () =>
  useQuery({
    queryKey: ["leaderboard"],
    queryFn: () => request<Leaderboard>("/leaderboard", { token: useSession.getState().token }),
    refetchInterval: 60_000,
  });

export const useAdminStats = (enabled: boolean) =>
  useQuery({ queryKey: ["admin", "stats"], queryFn: () => authed<AdminStats>("/admin/stats"), enabled, refetchInterval: 2_000 });

export const useBotSettings = (enabled: boolean) =>
  useQuery({ queryKey: ["admin", "bots"], queryFn: () => authed<BotSettings>("/admin/bots"), enabled });
