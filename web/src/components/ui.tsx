import clsx from "clsx";
import { LoaderCircle } from "lucide-react";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import type { AccountType, Transaction } from "../lib/api";
import { txLabel, typeLabel } from "../lib/format";

export function Card({ className, children }: { className?: string; children: ReactNode }) {
  return <section className={clsx("rounded-lg border border-rule bg-sheet shadow-[0_1px_0_rgb(0_0_0/0.03)]", className)}>{children}</section>;
}

export function CardHeader({ title, hint, right }: { title: ReactNode; hint?: ReactNode; right?: ReactNode }) {
  return (
    <header className="flex items-start justify-between gap-4 border-b border-rule px-5 py-3.5">
      <div className="min-w-0">
        <h2 className="font-serif text-[17px] font-semibold tracking-tight">{title}</h2>
        {hint && <p className="mt-0.5 text-xs leading-relaxed text-ink-3">{hint}</p>}
      </div>
      {right}
    </header>
  );
}

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "outline" | "ghost" | "danger";
  size?: "sm" | "md";
  loading?: boolean;
  icon?: ReactNode;
};

export function Button({ variant = "outline", size = "md", loading, icon, className, children, disabled, ...rest }: ButtonProps) {
  return (
    <button
      {...rest}
      disabled={disabled || loading}
      className={clsx(
        "inline-flex items-center justify-center gap-2 rounded-md font-medium whitespace-nowrap transition-colors",
        "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-forest disabled:cursor-not-allowed disabled:opacity-50",
        size === "sm" ? "h-8 px-3 text-xs" : "h-10 px-4 text-sm",
        variant === "primary" && "bg-forest text-paper hover:bg-forest-2",
        variant === "outline" && "border border-rule-strong bg-sheet text-ink hover:bg-paper",
        variant === "ghost" && "text-ink-2 hover:bg-paper hover:text-ink",
        variant === "danger" && "border border-oxblood/30 bg-rose text-oxblood hover:bg-[#f1d5ce]",
        className,
      )}
    >
      {loading ? <LoaderCircle className="size-4 animate-spin" /> : icon}
      {children}
    </button>
  );
}

type Tone = "neutral" | "good" | "bad" | "warn" | "info";

export function Badge({ tone = "neutral", children, className }: { tone?: Tone; children: ReactNode; className?: string }) {
  return (
    <span
      className={clsx(
        "inline-flex items-center gap-1.5 rounded px-1.5 py-0.5 text-[11px] font-medium whitespace-nowrap",
        tone === "neutral" && "bg-paper text-ink-2 ring-1 ring-rule",
        tone === "good" && "bg-moss text-forest",
        tone === "bad" && "bg-rose text-oxblood",
        tone === "warn" && "bg-sand text-gold",
        tone === "info" && "bg-mist text-navy",
        className,
      )}
    >
      {children}
    </span>
  );
}

const typeTone: Record<AccountType, Tone> = { asset: "info", liability: "warn", equity: "neutral", revenue: "good", expense: "bad" };

export const TypeBadge = ({ type }: { type: AccountType }) => <Badge tone={typeTone[type]}>{typeLabel[type]}</Badge>;

export function KindBadge({ tx }: { tx: Pick<Transaction, "kind" | "reversal" | "resolution" | "reversed"> }) {
  const tone: Tone = tx.reversal ? "bad" : tx.kind === "pending" ? "warn" : tx.kind === "void" ? "neutral" : tx.kind === "post" ? "info" : "good";
  return (
    <span className="inline-flex items-center gap-1.5">
      <Badge tone={tone}>{txLabel(tx)}</Badge>
      {tx.kind === "pending" && !tx.resolution && <Badge tone="warn">em aberto</Badge>}
      {tx.resolution === "post" && <Badge tone="info">capturada</Badge>}
      {tx.resolution === "void" && <Badge>cancelada</Badge>}
      {tx.reversed && <Badge tone="bad">estornada</Badge>}
    </span>
  );
}

export function Stat({ label, value, sub, tone }: { label: string; value: ReactNode; sub?: ReactNode; tone?: "good" | "bad" }) {
  return (
    <div className="rounded-lg border border-rule bg-sheet px-4 py-3">
      <div className="text-[11px] font-medium tracking-wider text-ink-3 uppercase">{label}</div>
      <div className={clsx("num mt-1 text-[26px] leading-tight font-medium", tone === "good" && "text-forest", tone === "bad" && "text-oxblood")}>
        {value}
      </div>
      {sub && <div className="mt-0.5 text-xs text-ink-3">{sub}</div>}
    </div>
  );
}

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-3 py-16 text-sm text-ink-3">
      <LoaderCircle className="size-5 animate-spin text-forest" />
      {label}
    </div>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  return (
    <div role="alert" className="rounded-md border border-oxblood/30 bg-rose px-4 py-3 text-sm text-oxblood">
      {error instanceof Error ? error.message : String(error)}
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="px-6 py-14 text-center">
      <p className="font-serif text-lg text-ink-2">{title}</p>
      {children && <div className="mx-auto mt-2 max-w-md text-sm leading-relaxed text-ink-3">{children}</div>}
    </div>
  );
}

export const inputClass =
  "h-10 w-full rounded-md border border-rule-strong bg-sheet px-3 text-sm text-ink placeholder:text-ink-4 focus:border-forest focus:outline-none focus:ring-2 focus:ring-forest/15";

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1.5 block text-xs font-medium text-ink-2">{label}</span>
      {children}
      {hint && <span className="mt-1 block text-[11px] text-ink-3">{hint}</span>}
    </label>
  );
}

/** Money with the sign colored the accountant's way. */
export function Amount({ value, currency = "BRL", muted }: { value: number; currency?: string; muted?: boolean }) {
  const f = new Intl.NumberFormat("pt-BR", { style: "currency", currency }).format(value / 100);
  return <span className={clsx("num", value < 0 ? "text-oxblood" : muted ? "text-ink-3" : "text-ink")}>{f}</span>;
}
