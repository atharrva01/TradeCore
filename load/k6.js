// Load test from the PRD: 200 WebSocket clients plus 500 orders/second, and
// the order-to-UI latency (order sent → its fill event arrives over the
// WebSocket). Run against a server started with relaxed per-user and per-IP
// limits, because all traffic comes from one machine:
//
//   ORDER_RATE_PER_USER=50 ORDER_RATE_PER_IP=0 AUTH_RATE_PER_MINUTE=6000 go run ./cmd/tradecore
//   docker run --rm -i --network host -e BASE_URL=http://127.0.0.1:8080 grafana/k6 run - < load/k6.js
import http from "k6/http";
import ws from "k6/ws";
import { check } from "k6";
import { Counter, Trend } from "k6/metrics";

const BASE = __ENV.BASE_URL || "http://127.0.0.1:8080";
const WS = BASE.replace(/^http/, "ws") + "/ws";
const DURATION = __ENV.DURATION || "60s";

const wsMessages = new Counter("ws_messages");
const orderToFill = new Trend("order_to_fill_event_ms", true);

export const options = {
  setupTimeout: "120s",
  scenarios: {
    websocket_clients: { executor: "constant-vus", vus: 200, duration: DURATION, exec: "watch" },
    orders: {
      executor: "constant-arrival-rate", rate: 500, timeUnit: "1s", duration: DURATION,
      preAllocatedVUs: 150, maxVUs: 400, exec: "trade",
    },
    order_to_ui: { executor: "constant-vus", vus: 5, duration: DURATION, exec: "roundTrip" },
  },
  thresholds: {
    "http_req_duration{scenario:orders}": ["p(95)<50"],
    "checks{scenario:orders}": ["rate>0.99"],
    order_to_fill_event_ms: ["p(95)<300"],
  },
};

const json = { headers: { "Content-Type": "application/json" } };

export function setup() {
  const instruments = http.get(`${BASE}/api/v1/instruments`).json();
  const tokens = [];
  for (let i = 0; i < 120; i++) {
    const response = http.post(`${BASE}/api/v1/auth/guest`, JSON.stringify({ name: `Load ${i}` }), json);
    if (response.status === 201) tokens.push(response.json("accessToken"));
  }
  return { tokens, symbols: instruments.map((item) => item.symbol) };
}

function order(data, token, symbol, quantity) {
  return http.post(
    `${BASE}/api/v1/orders`,
    JSON.stringify({ symbol, side: "BUY", type: "MARKET", quantity }),
    { headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` }, tags: { name: "POST /orders" } },
  );
}

export function trade(data) {
  const token = data.tokens[(__VU * 31 + __ITER) % data.tokens.length];
  const symbol = data.symbols[__ITER % data.symbols.length];
  const response = order(data, token, symbol, 1);
  // 400 NO_LIQUIDITY is a valid exchange answer when bots have not re-quoted yet.
  check(response, { "order accepted or cleanly refused": (r) => r.status === 201 || r.status === 400 });
}

export function watch(data) {
  const symbol = data.symbols[__VU % data.symbols.length];
  ws.connect(WS, {}, (socket) => {
    socket.on("open", () => socket.send(JSON.stringify({ type: "subscribe", channels: [`book:${symbol}`, `trades:${symbol}`, "ticker"] })));
    socket.on("message", () => wsMessages.add(1));
    socket.setTimeout(() => socket.close(), 20_000);
  });
}

export function roundTrip(data) {
  const token = data.tokens[__VU % data.tokens.length];
  ws.connect(WS, {}, (socket) => {
    let sentAt = 0;
    socket.on("open", () => socket.send(JSON.stringify({ type: "auth", token })));
    socket.on("message", (raw) => {
      const message = JSON.parse(raw);
      if (message.type === "authenticated" || (message.type === "fill" && sentAt)) {
        if (message.type === "fill") orderToFill.add(Date.now() - sentAt);
        socket.setTimeout(() => {
          sentAt = Date.now();
          const response = order(data, token, data.symbols[0], 1);
          if (response.status !== 201) sentAt = 0;
        }, 500);
      }
    });
    socket.setTimeout(() => socket.close(), 20_000);
  });
}
