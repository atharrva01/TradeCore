export function Sparkline({ points, width = 96, height = 28 }: { points: number[]; width?: number; height?: number }) {
  if (points.length < 2) return <span className="sparkline-empty">—</span>;
  const low = Math.min(...points);
  const high = Math.max(...points);
  const span = high - low || 1;
  const path = points
    .map((value, index) => {
      const x = (index / (points.length - 1)) * width;
      const y = height - 2 - ((value - low) / span) * (height - 4);
      return `${index === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  const rising = points[points.length - 1] >= points[0];
  return (
    <svg className="sparkline" width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden="true">
      <path d={path} fill="none" stroke={rising ? "var(--green)" : "var(--red)"} strokeWidth="1.5" />
    </svg>
  );
}
