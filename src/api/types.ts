import type { OrderType, Side, TimeInForce } from "../lib/orderValidation";

export type Instrument = {
  symbol: string;
  name: string;
  lastPrice: number;
  initialPrice: number;
  dayOpen: number;
  high: number;
  low: number;
  volume: number;
  changePct: number;
  tickSize: number;
  halted: boolean;
  seq: number;
  tradeSeq: number;
  spark?: number[];
};

export type Level = { price: number; quantity: number; orders: number };
export type Book = { symbol: string; seq: number; bids: Level[]; asks: Level[] };

export type TapeTrade = {
  id: string;
  symbol: string;
  buyOrderId: string;
  sellOrderId: string;
  buyerId: string;
  sellerId: string;
  price: number;
  quantity: number;
  takerSide: Side;
  createdAt: number;
  seq: number;
};

export type MyTrade = TapeTrade & { side: Side; value: number };

export type OrderStatus = "OPEN" | "PARTIALLY_FILLED" | "FILLED" | "CANCELLED" | "REJECTED";

export type Order = {
  id: string;
  userId: string;
  symbol: string;
  side: Side;
  type: OrderType;
  timeInForce?: TimeInForce;
  price: number;
  quantity: number;
  filledQty: number;
  status: OrderStatus;
  createdAt: number;
};

export type Candle = { time: number; open: number; high: number; low: number; close: number; volume: number };
export type CandleInterval = "1m" | "5m" | "1h" | "1d";

export type Holding = {
  symbol: string;
  quantity: number;
  reserved: number;
  averagePrice: number;
  lastPrice: number;
  marketValue: number;
  unrealizedPnl: number;
  realizedPnl: number;
};

export type Portfolio = {
  user: string;
  cash: { available: number; reserved: number };
  holdings: Holding[];
  totalValue: number;
  unrealizedPnl: number;
  realizedPnl: number;
};

export type Me = {
  id: string;
  name: string;
  email: string;
  isGuest: boolean;
  isAdmin: boolean;
  expiresAt: number | null;
};

export type Session = { accessToken: string; user: { id: string; name: string; isGuest: boolean; isAdmin: boolean } };

export type LeaderboardEntry = { rank: number; name: string; totalValue: number; me?: boolean };
export type Leaderboard = { entries: LeaderboardEntry[]; me: LeaderboardEntry | null; traders: number; updatedAt: number };

export type PlaceOrderResponse = {
  orderId: string;
  status: OrderStatus;
  filledQty: number;
  fills: { price: number; quantity: number }[];
  order: Order;
};

export type BotSettings = { enabled: boolean; aggressiveness: number; volatility: number; momentum: boolean };

export type AdminStats = {
  engine: { ordersPerSecond: number; tradesPerSecond: number; matchP50Micros: number; matchP99Micros: number; matchSamples: number };
  connectedClients: number;
  accounts: number;
  guests: number;
  restingOrders: number;
  commitQueue: number;
  persistence: string;
  goroutines: number;
  heapMB: number;
  instruments: Instrument[];
};

export type Health = { status: string; persistence: string; googleSignIn: boolean };
