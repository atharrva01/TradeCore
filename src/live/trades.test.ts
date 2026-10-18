import { describe, expect, it } from "vitest";
import type { TapeTrade } from "../api/types";
import { applySnapshot, mergeTrade, TAPE_LENGTH } from "./trades";

const trade = (seq: number): TapeTrade => ({
  id: `t${seq}`, symbol: "ATHR", buyOrderId: "b", sellOrderId: "s", buyerId: "x", sellerId: "y",
  price: 10_000 + seq, quantity: 1, takerSide: "BUY", createdAt: seq, seq,
});

describe("mergeTrade", () => {
  it("prepends contiguous trades", () => {
    const { tape, gap } = mergeTrade({ seq: 4, list: [trade(4)] }, trade(5));
    expect(gap).toBe(false);
    expect(tape.list.map((t) => t.seq)).toEqual([5, 4]);
  });

  it("drops trades already covered by a snapshot", () => {
    const tape = { seq: 9, list: [trade(9)] };
    expect(mergeTrade(tape, trade(8)).tape).toBe(tape);
  });

  it("reports a gap when sequence numbers jump", () => {
    expect(mergeTrade({ seq: 4, list: [] }, trade(7)).gap).toBe(true);
  });

  it("keeps the tape bounded", () => {
    let tape = { seq: 0, list: [] as TapeTrade[] };
    for (let seq = 1; seq <= TAPE_LENGTH + 10; seq++) tape = mergeTrade(tape, trade(seq)).tape;
    expect(tape.list).toHaveLength(TAPE_LENGTH);
    expect(tape.list[0].seq).toBe(TAPE_LENGTH + 10);
  });
});

describe("applySnapshot", () => {
  it("ignores a snapshot older than the live tape", () => {
    const live = { seq: 12, list: [trade(12)] };
    expect(applySnapshot(live, 10, [trade(10)])).toBe(live);
    expect(applySnapshot(live, 14, [trade(14)]).seq).toBe(14);
  });
});
