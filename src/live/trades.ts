import type { TapeTrade } from "../api/types";

export const TAPE_LENGTH = 50;

export type Tape = { seq: number; list: TapeTrade[] };

export type MergeResult = { tape: Tape; gap: boolean };

/**
 * Adds a live trade to the tape (newest first). Trade sequence numbers rise by
 * exactly one per instrument, so anything at or below the current seq is a
 * duplicate already covered by a snapshot, and a jump means trades were
 * missed and the tape should be re-fetched.
 */
export function mergeTrade(tape: Tape | undefined, trade: TapeTrade): MergeResult {
  if (!tape) return { tape: { seq: trade.seq, list: [trade] }, gap: false };
  if (trade.seq <= tape.seq) return { tape, gap: false };
  const gap = tape.seq > 0 && trade.seq > tape.seq + 1;
  return { tape: { seq: trade.seq, list: [trade, ...tape.list].slice(0, TAPE_LENGTH) }, gap };
}

/** Replaces the tape with a snapshot unless the live tape is already newer. */
export function applySnapshot(tape: Tape | undefined, seq: number, list: TapeTrade[]): Tape {
  if (tape && tape.seq > seq) return tape;
  return { seq, list: list.slice(0, TAPE_LENGTH) };
}
