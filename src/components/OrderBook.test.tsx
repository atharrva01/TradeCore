import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { useTicket } from "../live/ticket";
import { OrderBook } from "./OrderBook";

const book = {
  symbol: "ATHR",
  seq: 3,
  asks: [{ price: 10_110, quantity: 40, orders: 1 }, { price: 10_120, quantity: 80, orders: 2 }],
  bids: [{ price: 10_090, quantity: 20, orders: 1 }],
};

describe("OrderBook", () => {
  it("shows asks above bids with the spread between them", () => {
    render(<OrderBook book={book} lastPrice={10_100} />);
    const rows = screen.getAllByRole("button").map((row) => row.textContent);
    expect(rows).toEqual(["₹101.20802", "₹101.10401", "₹100.90201"]);
    expect(screen.getByText("₹0.20")).toBeInTheDocument();
  });

  it("scales depth bars to the deepest level", () => {
    const { container } = render(<OrderBook book={book} lastPrice={10_100} />);
    const widths = [...container.querySelectorAll(".book-row i")].map((bar) => (bar as HTMLElement).style.width);
    expect(widths).toEqual(["100%", "50%", "25%"]);
  });

  it("prefills the order form when a price is clicked", async () => {
    render(<OrderBook book={book} lastPrice={10_100} />);
    await userEvent.click(screen.getByTitle("Sell at this price"));
    expect(useTicket.getState()).toMatchObject({ side: "SELL", type: "LIMIT", price: "100.90" });
    await userEvent.click(screen.getAllByTitle("Buy at this price")[1]);
    expect(useTicket.getState()).toMatchObject({ side: "BUY", price: "101.10" });
  });

  it("explains an empty book", () => {
    render(<OrderBook lastPrice={10_100} />);
    expect(screen.getByText("No sell orders")).toBeInTheDocument();
    expect(screen.getByText("No buy orders")).toBeInTheDocument();
  });
});
