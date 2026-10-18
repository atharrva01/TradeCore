import {
  ColorType,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type UTCTimestamp,
} from "lightweight-charts";
import { useEffect, useRef, useState } from "react";
import { useCandles } from "../api/queries";
import type { Candle, CandleInterval } from "../api/types";
import { useChannels } from "../live/socket";
import { useLive } from "../live/store";

const INTERVALS: CandleInterval[] = ["1m", "5m", "1h", "1d"];

// The chart library labels its time axis in UTC; shifting by +5:30 makes it
// read in Indian time, which is how the exchange's days are defined.
const IST_SHIFT_MS = 5.5 * 3600 * 1000;
const toTime = (ms: number) => ((ms + IST_SHIFT_MS) / 1000) as UTCTimestamp;

const toBar = (candle: Candle) => ({
  time: toTime(candle.time),
  open: candle.open / 100,
  high: candle.high / 100,
  low: candle.low / 100,
  close: candle.close / 100,
});

const toVolume = (candle: Candle) => ({
  time: toTime(candle.time),
  value: candle.volume,
  color: candle.close >= candle.open ? "rgba(66, 214, 160, 0.35)" : "rgba(244, 119, 131, 0.35)",
});

export function PriceChart({ symbol }: { symbol: string }) {
  const [interval, setBucket] = useState<CandleInterval>("1m");
  const container = useRef<HTMLDivElement>(null);
  const chart = useRef<IChartApi | null>(null);
  const candles = useRef<ISeriesApi<"Candlestick"> | null>(null);
  const volume = useRef<ISeriesApi<"Histogram"> | null>(null);
  const lastTime = useRef(0);
  const history = useCandles(symbol, interval);
  const live = useLive((state) => state.candles[`${symbol}:${interval}`]);
  useChannels([`candles:${symbol}:${interval}`]);

  useEffect(() => {
    if (!container.current) return;
    const instance = createChart(container.current, {
      autoSize: true,
      layout: { background: { type: ColorType.Solid, color: "transparent" }, textColor: "#7a8795", fontFamily: "DM Mono, monospace" },
      grid: { vertLines: { color: "#18212b" }, horzLines: { color: "#18212b" } },
      rightPriceScale: { borderColor: "#202a36" },
      timeScale: { borderColor: "#202a36", timeVisible: true, secondsVisible: false },
      crosshair: { mode: 0 },
    });
    candles.current = instance.addCandlestickSeries({
      upColor: "#42d6a0", downColor: "#f47783", wickUpColor: "#42d6a0", wickDownColor: "#f47783", borderVisible: false,
    });
    volume.current = instance.addHistogramSeries({
      priceScaleId: "volume", priceFormat: { type: "volume" }, lastValueVisible: false, priceLineVisible: false,
    });
    instance.priceScale("volume").applyOptions({ scaleMargins: { top: 0.8, bottom: 0 } });
    chart.current = instance;
    return () => {
      instance.remove();
      chart.current = candles.current = volume.current = null;
    };
  }, []);

  useEffect(() => {
    if (!history.data || !candles.current || !volume.current) return;
    candles.current.setData(history.data.map(toBar));
    volume.current.setData(history.data.map(toVolume));
    lastTime.current = history.data[history.data.length - 1]?.time ?? 0;
    chart.current?.timeScale().scrollToRealTime();
  }, [history.data]);

  useEffect(() => {
    // Live updates may only extend the series; older buckets come from history.
    if (!live || !candles.current || !volume.current || live.time < lastTime.current) return;
    candles.current.update(toBar(live));
    volume.current.update(toVolume(live));
    lastTime.current = live.time;
  }, [live]);

  return (
    <section className="panel chart-panel">
      <div className="panel-heading">
        <div>
          <span className="eyebrow">PRICE</span>
          <h2>{symbol} candles</h2>
        </div>
        <div className="segmented" role="tablist" aria-label="Candle interval">
          {INTERVALS.map((value) => (
            <button key={value} role="tab" aria-selected={interval === value} className={interval === value ? "active" : ""} onClick={() => setBucket(value)}>
              {value}
            </button>
          ))}
        </div>
      </div>
      <div className="chart-container" ref={container}>
        {history.data?.length === 0 && <div className="chart-empty">No trades yet in this interval</div>}
      </div>
    </section>
  );
}
