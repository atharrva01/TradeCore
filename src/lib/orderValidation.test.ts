import { describe, expect, it } from "vitest";
import { snapToTick, validateTicket, type Ticket } from "./orderValidation";

const context = { tickSize: 5, lastPrice: 10_100, availableCash: 100_000, availableShares: 7 };
const ticket = (patch: Partial<Ticket>): Ticket => ({ side: "BUY", type: "LIMIT", timeInForce: "GTC", price: "101.50", quantity: "4", ...patch });

describe("validateTicket", () => {
  it("accepts a funded limit buy on the tick and estimates its cost", () => {
    expect(validateTicket(ticket({}), context)).toEqual({ ok: true, price: 10_150, quantity: 4, estimate: 40_600 });
  });

  it.each([
    ["0", "at least 1"],
    ["2.5", "whole-number"],
    ["abc", "whole-number"],
    ["1000001", "at most"],
  ])("rejects quantity %s", (quantity, message) => {
    const result = validateTicket(ticket({ quantity }), context);
    expect(result.ok).toBe(false);
    expect(!result.ok && result.error).toContain(message);
  });

  it.each([
    ["101.52", "multiple of ₹0.05"],
    ["0", "greater than zero"],
    ["-3", "greater than zero"],
    ["101.555", "greater than zero"],
  ])("rejects limit price %s", (price, message) => {
    const result = validateTicket(ticket({ price }), context);
    expect(!result.ok && result.error).toContain(message);
  });

  it("rejects buys the cash cannot cover and sells beyond available shares", () => {
    expect(validateTicket(ticket({ quantity: "10" }), context).ok).toBe(false);
    expect(validateTicket(ticket({ side: "SELL", quantity: "8" }), context).ok).toBe(false);
    expect(validateTicket(ticket({ side: "SELL", quantity: "7" }), context).ok).toBe(true);
  });

  it("prices market orders at the last price without a limit", () => {
    expect(validateTicket(ticket({ type: "MARKET", price: "" }), context)).toMatchObject({ ok: true, price: 0, estimate: 40_400 });
    expect(validateTicket(ticket({ type: "MARKET" }), { ...context, availableCash: 0 }).ok).toBe(false);
  });
});

describe("snapToTick", () => {
  it("rounds to the nearest tick and never below one tick", () => {
    expect(snapToTick(10_152, 5)).toBe("101.50");
    expect(snapToTick(10_153, 5)).toBe("101.55");
    expect(snapToTick(-20, 5)).toBe("0.05");
  });
});
