import type { AccountType, Kind, Transaction } from "./api";

const formatters = new Map<string, Intl.NumberFormat>();

/** Minor units → "R$ 1.234,56". */
export function money(minor: number, currency = "BRL"): string {
  let f = formatters.get(currency);
  if (!f) {
    f = new Intl.NumberFormat("pt-BR", { style: "currency", currency });
    formatters.set(currency, f);
  }
  return f.format(minor / 100);
}

export const nf = new Intl.NumberFormat("pt-BR");
export const compact = new Intl.NumberFormat("pt-BR", { notation: "compact", maximumFractionDigits: 1 });

export const ms = (v: number) => (v >= 100 ? `${v.toFixed(0)} ms` : `${v.toFixed(1)} ms`);

export function when(iso: string): string {
  const d = new Date(iso);
  const sameDay = d.toDateString() === new Date().toDateString();
  return sameDay
    ? d.toLocaleTimeString("pt-BR")
    : d.toLocaleString("pt-BR", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
}

export const shortHash = (h: string) => `${h.slice(0, 10)}…${h.slice(-6)}`;

export const typeLabel: Record<AccountType, string> = {
  asset: "Ativo",
  liability: "Passivo",
  equity: "Patrimônio",
  revenue: "Receita",
  expense: "Despesa",
};

export const kindLabel: Record<Kind, string> = {
  posted: "Lançamento",
  pending: "Reserva",
  post: "Captura",
  void: "Cancelamento",
};

/** What a transaction is, in words. */
export function txLabel(t: Pick<Transaction, "kind" | "reversal">): string {
  return t.reversal ? "Estorno" : kindLabel[t.kind];
}

/** The amount moved by a transaction: the sum of its debits. */
export const txTotal = (t: Transaction) => t.entries.reduce((s, e) => s + (e.direction === "D" ? e.amount : 0), 0);

/** "12,34" or "1.234,5" → centavos. Returns NaN when it isn't a number. */
export function parseMoney(text: string): number {
  const clean = text.trim().replace(/\./g, "").replace(",", ".");
  if (!/^\d+(\.\d{1,2})?$/.test(clean)) return Number.NaN;
  return Math.round(Number(clean) * 100);
}
