import { create } from "zustand";
import type { Book, Candle, Instrument, TapeTrade } from "../api/types";
import { applySnapshot, mergeTrade, type Tape } from "./trades";

export type Connection = "connecting" | "open" | "closed";

type LiveState = {
  connection: Connection;
  ticker: Record<string, Instrument>;
  books: Record<string, Book>;
  tapes: Record<string, Tape>;
  /** The latest live candle per "SYMBOL:interval". */
  candles: Record<string, Candle>;
  setConnection: (connection: Connection) => void;
  setTicker: (items: Instrument[]) => void;
  applyBook: (book: Book) => void;
  applyTradesSnapshot: (symbol: string, seq: number, trades: TapeTrade[]) => void;
  /** Returns true when a gap was detected and the tape should be re-fetched. */
  applyTrade: (trade: TapeTrade) => boolean;
  applyCandle: (symbol: string, interval: string, candle: Candle) => void;
};

export const useLive = create<LiveState>((set, get) => ({
  connection: "connecting",
  ticker: {},
  books: {},
  tapes: {},
  candles: {},
  setConnection: (connection) => set({ connection }),
  setTicker: (items) => set({ ticker: Object.fromEntries(items.map((item) => [item.symbol, item])) }),
  applyBook: (book) => {
    const current = get().books[book.symbol];
    // Snapshots can arrive out of order around a subscribe; keep the newest.
    if (current && current.seq > book.seq) return;
    set({ books: { ...get().books, [book.symbol]: book } });
  },
  applyTradesSnapshot: (symbol, seq, trades) =>
    set({ tapes: { ...get().tapes, [symbol]: applySnapshot(get().tapes[symbol], seq, trades) } }),
  applyTrade: (trade) => {
    const { tape, gap } = mergeTrade(get().tapes[trade.symbol], trade);
    set({ tapes: { ...get().tapes, [trade.symbol]: tape } });
    return gap;
  },
  applyCandle: (symbol, interval, candle) => set({ candles: { ...get().candles, [`${symbol}:${interval}`]: candle } }),
}));
