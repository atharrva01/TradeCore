import { expect, test } from "@playwright/test";

test("a visitor trades as a guest and sees the position in their portfolio", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: /try as guest/i }).click();
  await expect(page).toHaveURL(/\/trade\/ATHR$/);

  // The book streams in over the WebSocket.
  await expect(page.locator(".book-panel .ask-row").first()).toBeVisible();
  await expect(page.locator(".connection-open")).toBeAttached();

  await page.getByRole("button", { name: "Market", exact: true }).click();
  await page.getByLabel("Quantity").fill("5");
  await page.getByRole("button", { name: "Buy ATHR" }).click();
  await expect(page.locator(".toast").filter({ hasText: /Bought \d+ ATHR @ ₹/ }).first()).toBeVisible();

  await page.getByRole("link", { name: "Portfolio" }).click();
  const holding = page.locator(".holdings-grid").filter({ hasText: "ATHR" });
  await expect(holding).toContainText("5");
  await page.getByRole("tab", { name: "Trade history" }).click();
  await expect(page.locator(".trades-grid").filter({ hasText: "BUY" }).first()).toBeVisible();
});

test("a resting limit order can be cancelled", async ({ page }) => {
  await page.goto("/trade/NOVA");
  await expect(page.locator(".book-panel .bid-row").first()).toBeVisible();
  await page.getByLabel("Limit price").fill("50.00");
  await page.getByLabel("Quantity").fill("3");
  await page.getByRole("button", { name: "Buy NOVA" }).click();
  await expect(page.locator(".toast").filter({ hasText: "Order added to the book" })).toBeVisible();

  const row = page.locator(".activity-panel .orders-grid").filter({ hasText: "₹50.00" });
  await expect(row).toContainText("Open");
  await row.getByTitle("Cancel order").click();
  await expect(page.locator(".toast").filter({ hasText: "Order cancelled" })).toBeVisible();
  await expect(row).toHaveCount(0);
});

test("the order form validates before sending", async ({ page }) => {
  await page.goto("/trade/MINT");
  await page.getByLabel("Limit price").fill("43.21");
  await page.getByRole("button", { name: "Buy MINT" }).click();
  await expect(page.getByRole("alert")).toContainText("multiple of ₹0.05");
});

test("market pages render live data without horizontal scrolling", async ({ page }) => {
  for (const path of ["/", "/markets", "/trade/ORBT", "/leaderboard"]) {
    await page.goto(path);
    await expect(page.locator(".ticker-item").first()).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, `${path} scrolls horizontally`).toBeLessThanOrEqual(0);
  }
  await page.goto("/markets");
  await expect(page.locator(".markets-grid").filter({ hasText: "CIRR" })).toBeVisible();
});
