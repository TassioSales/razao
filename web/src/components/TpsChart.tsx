import { compact, nf } from "../lib/format";

/** Committed transactions per second over the last minute. */
export function TpsChart({ series }: { series: number[] }) {
  const W = 600;
  const H = 150;
  const pad = { l: 40, r: 8, t: 10, b: 20 };
  const max = Math.max(10, ...series);
  // Round the axis up to 1, 2 or 5 × 10^n so the labels are readable.
  const pow = 10 ** Math.floor(Math.log10(max));
  const top = [1, 2, 5, 10].map((m) => m * pow).find((v) => v >= max) ?? max;
  const x = (i: number) => pad.l + (i / (series.length - 1)) * (W - pad.l - pad.r);
  const y = (v: number) => pad.t + (1 - v / top) * (H - pad.t - pad.b);
  const line = series.map((v, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(" ");
  const area = `${line} L${x(series.length - 1)},${y(0)} L${x(0)},${y(0)} Z`;
  const last = series[series.length - 1] ?? 0;

  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label={`Vazão: ${nf.format(last)} transações por segundo`}>
      {[0, 0.5, 1].map((f) => (
        <g key={f}>
          <line x1={pad.l} x2={W - pad.r} y1={y(top * f)} y2={y(top * f)} stroke="var(--color-rule)" strokeDasharray={f ? "3 4" : undefined} />
          <text x={pad.l - 6} y={y(top * f) + 3.5} textAnchor="end" className="num fill-ink-3 text-[10px]">
            {compact.format(top * f)}
          </text>
        </g>
      ))}
      <path d={area} fill="var(--color-forest)" opacity={0.1} />
      <path d={line} fill="none" stroke="var(--color-forest)" strokeWidth={1.8} strokeLinejoin="round" />
      <circle cx={x(series.length - 1)} cy={y(last)} r={3} fill="var(--color-forest)" />
      <text x={pad.l} y={H - 5} className="fill-ink-3 text-[10px]">
        60 s atrás
      </text>
      <text x={W - pad.r} y={H - 5} textAnchor="end" className="fill-ink-3 text-[10px]">
        agora
      </text>
    </svg>
  );
}
