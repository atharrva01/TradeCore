import { useEffect } from "react";
import { request, websocketURL } from "../api/client";
import { queryClient } from "../api/queryClient";
import { useSession } from "../api/session";
import type { Book, Candle, Instrument, Order, TapeTrade } from "../api/types";
import { money, qty } from "../lib/format";
import { useLive } from "./store";
import { toast } from "./toasts";

type ServerEvent = { type: string; channel?: string; seq?: number; data?: unknown };

/**
 * One WebSocket per tab. Components declare the channels they need with
 * useChannels; the socket reference-counts them, reconnects with exponential
 * backoff and jitter, and on every (re)connect re-authenticates and
 * re-subscribes, which makes the server send fresh snapshots.
 */
class Socket {
  private ws: WebSocket | null = null;
  private counts = new Map<string, number>();
  private attempt = 0;
  private reconnectTimer = 0;
  private refetchTimer = 0;
  private token = "";
  private everConnected = false;

  start() {
    if (this.ws || this.reconnectTimer) return;
    this.connect();
    useSession.subscribe((state) => {
      if (state.token !== this.token) {
        this.token = state.token;
        this.authenticate();
      }
    });
  }

  subscribe(channels: string[]) {
    const fresh = channels.filter((channel) => {
      const count = this.counts.get(channel) ?? 0;
      this.counts.set(channel, count + 1);
      return count === 0;
    });
    if (fresh.length) this.send({ type: "subscribe", channels: fresh });
    return () => {
      const gone = channels.filter((channel) => {
        const count = (this.counts.get(channel) ?? 1) - 1;
        if (count <= 0) this.counts.delete(channel);
        else this.counts.set(channel, count);
        return count <= 0;
      });
      if (gone.length) this.send({ type: "unsubscribe", channels: gone });
    };
  }

  private connect() {
    this.reconnectTimer = 0;
    useLive.getState().setConnection("connecting");
    const ws = new WebSocket(websocketURL());
    this.ws = ws;
    ws.onopen = () => {
      this.attempt = 0;
      useLive.getState().setConnection("open");
      this.token = useSession.getState().token;
      this.authenticate();
      if (this.counts.size) this.send({ type: "subscribe", channels: [...this.counts.keys()] });
      if (this.everConnected) {
        // Anything private may have changed while we were away.
        this.invalidateAccount();
      }
      this.everConnected = true;
    };
    ws.onmessage = (message) => {
      try {
        this.dispatch(JSON.parse(message.data as string) as ServerEvent);
      } catch {
        // Ignore malformed frames.
      }
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      useLive.getState().setConnection("closed");
      const delay = Math.min(15_000, 500 * 2 ** this.attempt) * (0.75 + Math.random() * 0.5);
      this.attempt += 1;
      this.reconnectTimer = window.setTimeout(() => this.connect(), delay);
    };
  }

  private send(message: object) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify(message));
  }

  private authenticate() {
    if (this.token) this.send({ type: "auth", token: this.token });
  }

  private invalidateAccount() {
    window.clearTimeout(this.refetchTimer);
    this.refetchTimer = window.setTimeout(() => {
      void queryClient.invalidateQueries({ queryKey: ["portfolio"] });
      void queryClient.invalidateQueries({ queryKey: ["orders"] });
      void queryClient.invalidateQueries({ queryKey: ["myTrades"] });
    }, 150);
  }

  private dispatch(event: ServerEvent) {
    const live = useLive.getState();
    switch (event.type) {
      case "ticker":
        live.setTicker(event.data as Instrument[]);
        break;
      case "book":
        live.applyBook({ ...(event.data as Omit<Book, "seq">), seq: event.seq ?? 0 });
        break;
      case "trades_snapshot": {
        const symbol = event.channel?.split(":")[1] ?? "";
        live.applyTradesSnapshot(symbol, event.seq ?? 0, event.data as TapeTrade[]);
        break;
      }
      case "trade":
        if (live.applyTrade(event.data as TapeTrade)) void this.resyncTape((event.data as TapeTrade).symbol);
        break;
      case "candle": {
        const { symbol, interval, ...candle } = event.data as Candle & { symbol: string; interval: string };
        live.applyCandle(symbol, interval, candle);
        break;
      }
      case "fill": {
        const fill = event.data as TapeTrade & { side: "BUY" | "SELL" };
        toast.success(`${fill.side === "BUY" ? "Bought" : "Sold"} ${qty(fill.quantity)} ${fill.symbol} @ ${money(fill.price)}`);
        this.invalidateAccount();
        break;
      }
      case "order":
      case "balance":
        this.invalidateAccount();
        if (event.type === "order") {
          const order = event.data as Order;
          queryClient.setQueriesData<Order[]>({ queryKey: ["orders"] }, (orders) =>
            orders?.map((existing) => (existing.id === order.id ? order : existing)),
          );
        }
        break;
    }
  }

  private async resyncTape(symbol: string) {
    const trades = await request<TapeTrade[]>(`/instruments/${symbol}/trades?limit=50`).catch(() => null);
    if (trades) useLive.getState().applyTradesSnapshot(symbol, trades[0]?.seq ?? 0, trades);
  }
}

export const socket = new Socket();

/** Subscribes the calling component to WebSocket channels while mounted. */
export function useChannels(channels: string[]) {
  const key = channels.join(",");
  useEffect(() => {
    if (!key) return;
    return socket.subscribe(key.split(","));
  }, [key]);
}
