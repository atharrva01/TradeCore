import { useMutation } from "@tanstack/react-query";
import { type FormEvent, useEffect, useState } from "react";
import { Navigate } from "react-router-dom";
import { queryClient } from "../api/queryClient";
import { useAdminStats, useBotSettings } from "../api/queries";
import { authed, useSession } from "../api/session";
import type { BotSettings } from "../api/types";
import { money, pct, qty, toPaise } from "../lib/format";
import { toast } from "../live/toasts";

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="panel summary-card">
      <small>{label}</small>
      <b>{value}</b>
    </div>
  );
}

export function Admin() {
  const user = useSession((state) => state.user);
  const status = useSession((state) => state.status);
  const isAdmin = Boolean(user?.isAdmin);
  const stats = useAdminStats(isAdmin);
  const savedBots = useBotSettings(isAdmin);
  const [bots, setBots] = useState<BotSettings | null>(null);
  const [listing, setListing] = useState({ symbol: "", name: "", price: "" });
  const [confirm, setConfirm] = useState("");
  useEffect(() => {
    if (savedBots.data) setBots(savedBots.data);
  }, [savedBots.data]);

  const run = useMutation({
    mutationFn: ({ path, method = "POST", body }: { path: string; method?: string; body?: object }) =>
      authed(path, { method, body: body ? JSON.stringify(body) : undefined }),
    onError: (error) => toast.error(error.message),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ["admin"] });
      void queryClient.invalidateQueries({ queryKey: ["instruments"] });
    },
  });

  if (status === "loading") return <div className="page-loading">Loading…</div>;
  if (!isAdmin) return <Navigate to="/" replace />;
  const engine = stats.data?.engine;

  async function addInstrument(event: FormEvent) {
    event.preventDefault();
    await run.mutateAsync({ path: "/admin/instruments", body: { ...listing, price: toPaise(listing.price) } });
    toast.success(`${listing.symbol.toUpperCase()} is now listed`);
    setListing({ symbol: "", name: "", price: "" });
  }

  return (
    <div className="content-page">
      <header className="page-header">
        <div>
          <span className="eyebrow">OPERATIONS</span>
          <h1>Admin</h1>
        </div>
        <p className="muted">Refreshes every 2 seconds. Persistence: {stats.data?.persistence ?? "—"}.</p>
      </header>
      <section className="summary-cards">
        <Stat label="ORDERS / SEC (1 MIN)" value={engine ? engine.ordersPerSecond.toFixed(1) : "—"} />
        <Stat label="TRADES / SEC (1 MIN)" value={engine ? engine.tradesPerSecond.toFixed(1) : "—"} />
        <Stat label="MATCH LATENCY P50 / P99" value={engine ? `${engine.matchP50Micros.toFixed(1)} / ${engine.matchP99Micros.toFixed(1)} µs` : "—"} />
        <Stat label="CONNECTED CLIENTS" value={stats.data ? String(stats.data.connectedClients) : "—"} />
        <Stat label="ACCOUNTS (GUESTS)" value={stats.data ? `${stats.data.accounts} (${stats.data.guests})` : "—"} />
        <Stat label="RESTING ORDERS" value={stats.data ? qty(stats.data.restingOrders) : "—"} />
        <Stat label="COMMIT QUEUE" value={stats.data ? String(stats.data.commitQueue) : "—"} />
        <Stat label="HEAP" value={stats.data ? `${stats.data.heapMB.toFixed(1)} MB` : "—"} />
      </section>

      <section className="panel table">
        <div className="table-title">Instruments</div>
        <div className="table-header admin-grid">
          <span>STOCK</span>
          <span>LAST</span>
          <span>CHANGE</span>
          <span>VOLUME</span>
          <span>STATUS</span>
          <span />
        </div>
        {stats.data?.instruments.map((item) => (
          <div className="table-row admin-grid" key={item.symbol}>
            <span>
              <b>{item.symbol}</b>
              <small>{item.name}</small>
            </span>
            <span>{money(item.lastPrice)}</span>
            <span className={item.changePct >= 0 ? "positive" : "negative"}>{pct(item.changePct)}</span>
            <span>{qty(item.volume)}</span>
            <span className={item.halted ? "negative" : "positive"}>{item.halted ? "Halted" : "Trading"}</span>
            <button
              className={`ghost-button small ${item.halted ? "" : "danger"}`}
              onClick={() => run.mutate({ path: `/admin/instruments/${item.symbol}/${item.halted ? "resume" : "halt"}` })}
            >
              {item.halted ? "Resume" : "Halt"}
            </button>
          </div>
        ))}
        <form className="inline-form" onSubmit={addInstrument}>
          <input aria-label="New symbol" placeholder="SYMB" maxLength={6} value={listing.symbol} onChange={(e) => setListing({ ...listing, symbol: e.target.value })} required />
          <input aria-label="Company name" placeholder="Company name" value={listing.name} onChange={(e) => setListing({ ...listing, name: e.target.value })} required />
          <input aria-label="Listing price" placeholder="Price ₹" inputMode="decimal" value={listing.price} onChange={(e) => setListing({ ...listing, price: e.target.value })} required />
          <button className="primary-button small">List stock</button>
        </form>
      </section>

      <div className="admin-columns">
        <section className="panel admin-card">
          <div className="table-title">Bots</div>
          {bots && (
            <form
              onSubmit={async (event) => {
                event.preventDefault();
                await run.mutateAsync({ path: "/admin/bots", method: "PUT", body: bots });
                toast.success("Bot settings saved");
              }}
            >
              <label className="check-row">
                <input type="checkbox" checked={bots.enabled} onChange={(e) => setBots({ ...bots, enabled: e.target.checked })} /> Bots enabled
              </label>
              <label className="check-row">
                <input type="checkbox" checked={bots.momentum} onChange={(e) => setBots({ ...bots, momentum: e.target.checked })} /> Momentum bot
              </label>
              <label className="slider-row">
                Aggressiveness <b>{bots.aggressiveness}</b>
                <input type="range" min={1} max={10} step={1} value={bots.aggressiveness} onChange={(e) => setBots({ ...bots, aggressiveness: Number(e.target.value) })} />
              </label>
              <label className="slider-row">
                Volatility <b>{bots.volatility.toFixed(1)}×</b>
                <input type="range" min={0.2} max={5} step={0.1} value={bots.volatility} onChange={(e) => setBots({ ...bots, volatility: Number(e.target.value) })} />
              </label>
              <button className="primary-button small">Save bot settings</button>
            </form>
          )}
        </section>
        <section className="panel admin-card danger-zone">
          <div className="table-title">New season</div>
          <p className="muted">
            Resetting cancels every order, clears trades and candles, returns prices to their listing price and gives everyone a fresh
            ₹10,00,000. This cannot be undone.
          </p>
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              await run.mutateAsync({ path: "/admin/reset", body: { confirm } });
              toast.success("The market was reset");
              setConfirm("");
              void queryClient.invalidateQueries();
            }}
          >
            <input aria-label="Type RESET to confirm" placeholder="Type RESET" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
            <button className="ghost-button small danger" disabled={confirm !== "RESET"}>
              Reset market
            </button>
          </form>
        </section>
      </div>
    </div>
  );
}
