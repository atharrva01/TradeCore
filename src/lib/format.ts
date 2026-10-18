// All prices and money arrive from the API as integer paise.

export const money = (paise: number, decimals = 2) =>
  `₹${(paise / 100).toLocaleString("en-IN", {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  })}`;

export const signedMoney = (paise: number, decimals = 2) =>
  `${paise > 0 ? "+" : paise < 0 ? "−" : ""}${money(Math.abs(paise), decimals)}`;

export const qty = (quantity: number) => quantity.toLocaleString("en-IN");

export const pct = (value: number) => `${value > 0 ? "+" : ""}${value.toFixed(2)}%`;

export const clock = (timestamp: number) =>
  new Date(timestamp).toLocaleTimeString("en-IN", { hour12: false, timeZone: "Asia/Kolkata" });

export const dateTime = (timestamp: number) =>
  new Date(timestamp).toLocaleString("en-IN", {
    hour12: false,
    timeZone: "Asia/Kolkata",
    day: "2-digit",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });

/** Converts a rupee string such as "101.50" to paise, or NaN. */
export const toPaise = (rupees: string) => {
  if (!/^\d+(\.\d{0,2})?$/.test(rupees.trim())) return Number.NaN;
  return Math.round(Number(rupees) * 100);
};

export const statusLabel = (status: string) =>
  status.charAt(0) + status.slice(1).toLowerCase().replace("_", " ");
