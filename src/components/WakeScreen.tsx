import { Activity } from "lucide-react";

/** Shown while the free-tier server wakes up (it sleeps after 15 idle minutes). */
export function WakeScreen({ detail }: { detail?: string }) {
  return (
    <div className="wake-screen">
      <div className="brand-mark pulse">
        <Activity size={18} />
      </div>
      <strong>Waking up the exchange</strong>
      <span>The free server sleeps when idle and can take up to a minute to start.</span>
      {detail && <small>{detail}</small>}
    </div>
  );
}
