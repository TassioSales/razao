// Types mirror the Go structs in internal/ledger, internal/store and
// internal/pipeline. Amounts are integers in minor units (centavos).

export type AccountType = "asset" | "liability" | "equity" | "revenue" | "expense";
export type Direction = "D" | "C";
export type Kind = "posted" | "pending" | "post" | "void";

export type Balance = {
  debitsPosted: number;
  creditsPosted: number;
  debitsPending: number;
  creditsPending: number;
  entries: number;
  updatedSeq: number;
};

export type Account = {
  id: string;
  code: string;
  name: string;
  type: AccountType;
  currency: string;
  allowNegative: boolean;
  metadata: Record<string, string>;
  createdAt: string;
  balance: Balance;
  posted: number;
  available: number;
  normal: Direction;
};

export type Entry = {
  idx: number;
  accountId: string;
  accountCode: string;
  direction: Direction;
  amount: number;
  currency: string;
  balanceAfter?: number;
};

export type Transaction = {
  seq: number;
  id: string;
  kind: Kind;
  relatedId?: string;
  reversal: boolean;
  idempotencyKey: string;
  description: string;
  metadata: Record<string, string>;
  expiresAt?: string;
  createdAt: string;
  prevHash: string;
  hash: string;
  entries: Entry[];
  resolution?: "post" | "void";
  reversed: boolean;
};

export type StatementLine = {
  seq: number;
  idx: number;
  txId: string;
  kind: Kind;
  reversal: boolean;
  description: string;
  direction: Direction;
  amount: number;
  balanceAfter?: number;
  createdAt: string;
};

export type Metrics = {
  committed: number;
  rejected: number;
  replayed: number;
  failed: number;
  batches: number;
  avgBatch: number;
  lastBatch: number;
  avgCommitMs: number;
  tps: number;
  p50Ms: number;
  p99Ms: number;
  maxMs: number;
  series: number[];
};

export type CurrencyTotals = {
  currency: string;
  debitsPosted: number;
  creditsPosted: number;
  debitsPending: number;
  creditsPending: number;
  accounts: number;
};

export type Summary = {
  seq: number;
  head: string;
  accounts: number;
  totals: CurrencyTotals[];
  byType: Record<string, number>;
  trialBalance: boolean;
};

export type LoadReport = {
  running: boolean;
  config: { workers: number; seconds: number; hotShare: number };
  startedAt: string;
  elapsedMs: number;
  committed: number;
  rejected: number;
  errors: number;
  tps: number;
  p50Ms: number;
  p99Ms: number;
  maxMs: number;
  reasons: Record<string, number>;
};

export type ChainReport = {
  ok: boolean;
  checked: number;
  headSeq: number;
  head: string;
  brokenAt?: number;
  reason?: string;
  elapsedMs: number;
};

export type IntegrityReport = {
  ok: boolean;
  trialBalance: boolean;
  totals: CurrencyTotals[];
  drift: { code: string; stored: Balance; computed: Balance }[];
  unbalanced: number[];
  orphans: number;
  openPending: number;
  entries: number;
  elapsedMs: number;
};

export type Info = { demo: boolean; maxBatch: number; auth: boolean };

export type PostingInput = { account: string; direction: Direction; amount: number };

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
  ) {
    super(message);
  }
}

const KEY = "razao.apiKey";

async function request<T>(path: string, init: RequestInit = {}, retry = true): Promise<T> {
  const headers: Record<string, string> = { ...(init.headers as Record<string, string>) };
  if (init.body) headers["Content-Type"] = "application/json";
  const key = sessionStorage.getItem(KEY);
  if (key) headers.Authorization = `Bearer ${key}`;
  const res = await fetch(path, { ...init, headers });
  if (res.status === 401 && retry) {
    const typed = window.prompt("Este servidor exige uma chave de API para escritas (RAZAO_API_KEY):");
    if (typed) {
      sessionStorage.setItem(KEY, typed);
      return request<T>(path, init, false);
    }
  }
  if (!res.ok) {
    let code = "error";
    let msg = res.statusText;
    try {
      const body = (await res.json()) as { error?: { code: string; message: string } };
      if (body.error) ({ code, message: msg } = body.error);
    } catch {
      /* not JSON */
    }
    throw new ApiError(msg, res.status, code);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

const post = <T>(path: string, body?: unknown, idempotencyKey?: string) =>
  request<T>(path, {
    method: "POST",
    body: body === undefined ? undefined : JSON.stringify(body),
    headers: idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {},
  });

export const api = {
  info: () => request<Info>("/v1/info"),
  stats: () => request<{ metrics: Metrics; summary: Summary }>("/v1/stats"),
  accounts: (search: string, after = "", limit = 50) =>
    request<{ accounts: Account[] }>(
      `/v1/accounts?search=${encodeURIComponent(search)}&after=${encodeURIComponent(after)}&limit=${limit}`,
    ),
  account: (ref: string) => request<Account>(`/v1/accounts/${encodeURIComponent(ref)}`),
  statement: (ref: string, before = 0) =>
    request<{ account: Account; lines: StatementLine[] }>(
      `/v1/accounts/${encodeURIComponent(ref)}/statement?before=${before}&limit=50`,
    ),
  createAccount: (body: { code: string; name: string; type: AccountType; currency: string; allowNegative: boolean }) =>
    post<Account>("/v1/accounts", body),
  transactions: (before = 0, kind = "") =>
    request<{ transactions: Transaction[] }>(`/v1/transactions?before=${before}&kind=${kind}&limit=50`),
  transaction: (id: string) => request<Transaction>(`/v1/transactions/${id}`),
  createTransaction: (
    body: { description: string; pending: boolean; expiresInSeconds: number; postings: PostingInput[] },
    key: string,
  ) => post<Transaction>("/v1/transactions", body, key),
  followUp: (id: string, action: "post" | "void" | "reverse", key: string) =>
    post<Transaction>(`/v1/transactions/${id}/${action}`, undefined, key),
  verifyChain: () => post<ChainReport>("/v1/audit/chain"),
  checkIntegrity: () => post<IntegrityReport>("/v1/audit/integrity"),
  demo: {
    seed: (wallets: number) => post<{ wallets: number }>("/v1/demo/seed", { wallets }),
    load: (body: { workers: number; seconds: number; wallets: number; hotShare: number }) =>
      post<{ load: LoadReport }>("/v1/demo/load", body),
    stopLoad: () => request<void>("/v1/demo/load", { method: "DELETE" }),
    tamper: () => post<{ seq: number; idx: number; delta: number }>("/v1/demo/tamper"),
    restore: () => request<void>("/v1/demo/tamper", { method: "DELETE" }),
    reset: () => post<{ status: string }>("/v1/demo/reset"),
  },
};

export const newKey = () => crypto.randomUUID();
