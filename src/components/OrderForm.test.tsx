import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "../api/queryClient";
import type { Instrument } from "../api/types";
import { useTicket } from "../live/ticket";
import { OrderForm } from "./OrderForm";

const instrument: Instrument = {
  symbol: "ATHR", name: "Atharva Systems", lastPrice: 10_100, initialPrice: 10_100, dayOpen: 10_100,
  high: 10_100, low: 10_100, volume: 0, changePct: 0, tickSize: 5, halted: false, seq: 1, tradeSeq: 0,
};

const renderForm = (override: Partial<Instrument> = {}) =>
  render(
    <QueryClientProvider client={queryClient}>
      <OrderForm instrument={{ ...instrument, ...override }} />
    </QueryClientProvider>,
  );

describe("OrderForm", () => {
  beforeEach(() => {
    useTicket.setState({ side: "BUY", type: "LIMIT", timeInForce: "GTC", price: "101.00", quantity: "10" });
    vi.stubGlobal("fetch", vi.fn());
  });

  it("refuses an off-tick price without calling the API", async () => {
    renderForm();
    await userEvent.clear(screen.getByLabelText("Limit price"));
    await userEvent.type(screen.getByLabelText("Limit price"), "101.02");
    await userEvent.click(screen.getByRole("button", { name: "Buy ATHR" }));
    expect(screen.getByRole("alert")).toHaveTextContent("multiple of ₹0.05");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("steps the price by one tick", async () => {
    renderForm();
    await userEvent.click(screen.getByLabelText("Raise price by one tick"));
    expect(screen.getByLabelText("Limit price")).toHaveValue("101.05");
    await userEvent.click(screen.getByLabelText("Lower price by one tick"));
    await userEvent.click(screen.getByLabelText("Lower price by one tick"));
    expect(screen.getByLabelText("Limit price")).toHaveValue("100.95");
  });

  it("hides the price for market orders and shows the time in force for limits", async () => {
    renderForm();
    expect(screen.getByRole("combobox")).toHaveValue("GTC");
    await userEvent.click(screen.getByRole("button", { name: "Market" }));
    expect(screen.queryByLabelText("Limit price")).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });

  it("disables trading on a halted stock", () => {
    renderForm({ halted: true });
    expect(screen.getByRole("button", { name: "Trading halted" })).toBeDisabled();
  });
});
