import { useEffect, useRef, useState } from "react";
import { money } from "../lib/format";

/** Shows a price and flashes green or red briefly whenever it changes. */
export function FlashPrice({ value, decimals = 2, className = "" }: { value: number; decimals?: number; className?: string }) {
  const previous = useRef(value);
  const [flash, setFlash] = useState<"" | "flash-up" | "flash-down">("");
  useEffect(() => {
    if (value === previous.current) return;
    setFlash(value > previous.current ? "flash-up" : "flash-down");
    previous.current = value;
    const timer = window.setTimeout(() => setFlash(""), 700);
    return () => window.clearTimeout(timer);
  }, [value]);
  return <span className={`flash-price ${flash} ${className}`}>{money(value, decimals)}</span>;
}
