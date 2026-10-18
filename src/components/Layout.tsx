import { Activity, LogOut, UserRound } from "lucide-react";
import { Link, NavLink, Outlet, useNavigate } from "react-router-dom";
import { useInstruments } from "../api/queries";
import { signOut, useSession } from "../api/session";
import { pct } from "../lib/format";
import { useChannels } from "../live/socket";
import { useLive } from "../live/store";
import { FlashPrice } from "./FlashPrice";
import { Toasts } from "./Toasts";
import { WakeScreen } from "./WakeScreen";

function ConnectionDot() {
  const connection = useLive((state) => state.connection);
  const label = { open: "Live", connecting: "Connecting", closed: "Reconnecting" }[connection];
  return (
    <span className={`connection connection-${connection}`} title={`Market data: ${label}`}>
      <i /> {label}
    </span>
  );
}

export function TickerStrip() {
  useChannels(["ticker"]);
  const ticker = useLive((state) => state.ticker);
  const { data } = useInstruments();
  const items = Object.keys(ticker).length ? Object.values(ticker) : (data ?? []);
  return (
    <div className="ticker-strip">
      <div className="ticker-label">
        <Activity size={14} /> MARKET WATCH
      </div>
      {items.map((item) => (
        <Link className="ticker-item" key={item.symbol} to={`/trade/${item.symbol}`}>
          <b>{item.symbol}</b>
          <FlashPrice value={item.lastPrice} />
          <span className={item.changePct >= 0 ? "positive" : "negative"}>{pct(item.changePct)}</span>
          {item.halted && <span className="halted-tag">HALTED</span>}
        </Link>
      ))}
    </div>
  );
}

export function Layout() {
  const { user, status } = useSession();
  const navigate = useNavigate();
  const instruments = useInstruments();
  const waking = instruments.isPending && instruments.failureCount > 0;
  return (
    <div className="app-shell">
      <header className="topbar">
        <Link className="brand" to="/" aria-label="TradeCore home">
          <span className="brand-mark">
            <Activity size={18} strokeWidth={2.5} />
          </span>
          <span>
            trade<span>core</span>
          </span>
        </Link>
        <nav className="main-nav">
          <NavLink to="/markets">Markets</NavLink>
          <NavLink to="/trade/ATHR">Trade</NavLink>
          <NavLink to="/portfolio">Portfolio</NavLink>
          <NavLink to="/leaderboard">Leaderboard</NavLink>
          {user?.isAdmin && <NavLink to="/admin">Admin</NavLink>}
        </nav>
        <div className="topbar-actions">
          <ConnectionDot />
          {status === "signed-in" && user ? (
            <>
              <span className="profile-chip" title={user.email || "Guest account (24 hours)"}>
                <span className="avatar">{user.name.slice(0, 1).toUpperCase()}</span>
                <span className="profile-name">{user.name}</span>
                {user.isGuest && <span className="guest-tag">GUEST</span>}
              </span>
              {user.isGuest && (
                <Link className="ghost-button" to="/auth?mode=signup">
                  Save progress
                </Link>
              )}
              <button
                className="icon-button"
                title="Sign out"
                onClick={async () => {
                  await signOut();
                  navigate("/");
                }}
              >
                <LogOut size={16} />
              </button>
            </>
          ) : (
            <Link className="ghost-button" to="/auth">
              <UserRound size={14} /> Sign in
            </Link>
          )}
        </div>
      </header>
      <TickerStrip />
      <main className="page">
        <Outlet />
      </main>
      <footer className="footer">
        <span>
          TRADECORE <b>SIMULATED EXCHANGE</b>
        </span>
        <span>Virtual cash and fictional stocks · prices are set only by orders placed here</span>
        <a href="https://github.com/atharrva01/TradeCore" target="_blank" rel="noreferrer">
          Source on GitHub
        </a>
      </footer>
      <Toasts />
      {waking && <WakeScreen detail={instruments.failureReason?.message} />}
    </div>
  );
}
