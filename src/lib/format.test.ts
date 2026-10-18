import { describe, expect, it } from "vitest";
import { money, pct, signedMoney, statusLabel, toPaise } from "./format";

describe("format", () => {
  it("formats paise as Indian rupees", () => {
    expect(money(100_000_000, 0)).toBe("₹10,00,000");
    expect(money(10_150)).toBe("₹101.50");
    expect(signedMoney(-2_500)).toBe("−₹25.00");
    expect(pct(0.256)).toBe("+0.26%");
  });

  it("parses rupee input into integer paise", () => {
    expect(toPaise("101.5")).toBe(10_150);
    expect(toPaise("0.07")).toBe(7);
    expect(Number.isNaN(toPaise("1e3"))).toBe(true);
    expect(Number.isNaN(toPaise("12.345"))).toBe(true);
  });

  it("labels order statuses", () => {
    expect(statusLabel("PARTIALLY_FILLED")).toBe("Partially filled");
  });
});
