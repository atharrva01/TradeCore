import { Trophy } from "lucide-react";
import { useLeaderboard } from "../api/queries";
import { clock, money } from "../lib/format";
import type { LeaderboardEntry } from "../api/types";

function Row({ entry }: { entry: LeaderboardEntry }) {
  return (
    <div className={`table-row leaderboard-grid ${entry.me ? "my-row" : ""}`}>
      <span className="rank">{entry.rank <= 3 ? <Trophy size={14} className={`trophy-${entry.rank}`} /> : null}#{entry.rank}</span>
      <span>
        {entry.name}
        {entry.me && <b className="you-tag">YOU</b>}
      </span>
      <span>{money(entry.totalValue, 0)}</span>
      <span className={entry.totalValue >= 100_000_000 ? "positive" : "negative"}>
        {(((entry.totalValue - 100_000_000) / 100_000_000) * 100).toFixed(2)}%
      </span>
    </div>
  );
}

export function Leaderboard() {
  const board = useLeaderboard();
  const data = board.data;
  const meOutsideTop = data?.me && !data.entries.some((entry) => entry.me);
  return (
    <div className="content-page narrow">
      <header className="page-header">
        <div>
          <span className="eyebrow">TOP 50 · BOTS EXCLUDED</span>
          <h1>Leaderboard</h1>
        </div>
        <p className="muted">
          Ranked by total portfolio value at last price. {data ? `${data.traders} traders · updated ${clock(data.updatedAt)}` : ""}
        </p>
      </header>
      <section className="panel table">
        <div className="table-header leaderboard-grid">
          <span>RANK</span>
          <span>TRADER</span>
          <span>PORTFOLIO</span>
          <span>RETURN</span>
        </div>
        {data?.entries.map((entry) => <Row key={`${entry.rank}-${entry.name}`} entry={entry} />)}
        {meOutsideTop && data?.me && (
          <>
            <div className="table-gap">…</div>
            <Row entry={data.me} />
          </>
        )}
        {data?.entries.length === 0 && <div className="empty-table">No traders yet. Be the first.</div>}
      </section>
    </div>
  );
}
