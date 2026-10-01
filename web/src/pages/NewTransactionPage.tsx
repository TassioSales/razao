import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import clsx from "clsx";
import { ArrowLeft, CheckCircle2, Plus, Scale, Trash2 } from "lucide-react";
import { useDeferredValue, useId, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { Button, Card, CardHeader, ErrorBox, Field, inputClass } from "../components/ui";
import { api, newKey, type Direction } from "../lib/api";
import { money, parseMoney } from "../lib/format";

type Row = { id: number; account: string; direction: Direction; amount: string };

/** Text input with suggestions from the chart of accounts. */
function AccountInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const listId = useId();
  const term = useDeferredValue(value);
  const q = useQuery({ queryKey: ["accounts", "suggest", term], queryFn: () => api.accounts(term, "", 8), staleTime: 10_000 });
  return (
    <>
      <input
        className={inputClass + " num text-[13px]"}
        list={listId}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="código da conta"
        aria-label="Conta"
        required
      />
      <datalist id={listId}>
        {q.data?.accounts.map((a) => (
          <option key={a.id} value={a.code}>
            {a.name}
          </option>
        ))}
      </datalist>
    </>
  );
}

export function NewTransactionPage() {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const nextId = useRef(3);
  const [rows, setRows] = useState<Row[]>([
    { id: 1, account: "", direction: "D", amount: "" },
    { id: 2, account: "", direction: "C", amount: "" },
  ]);
  const [description, setDescription] = useState("");
  const [pending, setPending] = useState(false);
  const [expiresMin, setExpiresMin] = useState(30);
  // The key is fixed for this form: submitting twice can't create two transactions.
  const key = useRef(newKey());

  const amounts = rows.map((r) => parseMoney(r.amount));
  const debits = rows.reduce((s, r, i) => s + (r.direction === "D" && amounts[i]! > 0 ? amounts[i]! : 0), 0);
  const credits = rows.reduce((s, r, i) => s + (r.direction === "C" && amounts[i]! > 0 ? amounts[i]! : 0), 0);
  const complete = rows.every((r, i) => r.account && amounts[i]! > 0);
  const balanced = complete && debits === credits && debits > 0;

  const create = useMutation({
    mutationFn: () =>
      api.createTransaction(
        {
          description,
          pending,
          expiresInSeconds: pending ? expiresMin * 60 : 0,
          postings: rows.map((r, i) => ({ account: r.account.trim(), direction: r.direction, amount: amounts[i]! })),
        },
        key.current,
      ),
    onSuccess: (tx) => {
      qc.invalidateQueries();
      navigate(`/transacoes/${tx.id}`);
    },
  });
  const update = (id: number, patch: Partial<Row>) => {
    key.current = newKey(); // a different request deserves a different key
    setRows(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  };

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div className="flex items-center gap-3">
        <Link to="/transacoes" className="rounded-md p-1.5 text-ink-3 hover:bg-sheet hover:text-ink" aria-label="Voltar">
          <ArrowLeft className="size-4" />
        </Link>
        <h1 className="font-serif text-2xl font-semibold">Novo lançamento</h1>
      </div>

      <Card>
        <CardHeader title="Partidas" hint="Pelo menos um débito e um crédito. O lançamento só é aceito se os dois lados somarem o mesmo valor." />
        <form
          className="space-y-5 p-5"
          onSubmit={(e) => {
            e.preventDefault();
            if (balanced) create.mutate();
          }}
        >
          <div className="space-y-2">
            {rows.map((r, i) => (
              <div key={r.id} className="grid grid-cols-[1fr_auto_9rem_auto] items-center gap-2">
                <AccountInput value={r.account} onChange={(v) => update(r.id, { account: v })} />
                <div className="inline-flex h-10 rounded-md border border-rule-strong bg-sheet p-0.5 text-xs font-medium">
                  {(["D", "C"] as const).map((d) => (
                    <button
                      type="button"
                      key={d}
                      onClick={() => update(r.id, { direction: d })}
                      className={clsx("rounded px-3", r.direction === d ? "bg-forest text-paper" : "text-ink-2")}
                    >
                      {d === "D" ? "Débito" : "Crédito"}
                    </button>
                  ))}
                </div>
                <input
                  className={clsx(inputClass, "num text-right", r.amount && !(amounts[i]! > 0) && "border-oxblood")}
                  inputMode="decimal"
                  placeholder="0,00"
                  aria-label="Valor"
                  value={r.amount}
                  onChange={(e) => update(r.id, { amount: e.target.value })}
                  required
                />
                <button
                  type="button"
                  aria-label="Remover partida"
                  disabled={rows.length <= 2}
                  onClick={() => setRows(rows.filter((x) => x.id !== r.id))}
                  className="rounded p-2 text-ink-4 hover:bg-rose hover:text-oxblood disabled:invisible"
                >
                  <Trash2 className="size-4" />
                </button>
              </div>
            ))}
            <Button
              type="button"
              size="sm"
              variant="ghost"
              icon={<Plus className="size-3.5" />}
              onClick={() => setRows([...rows, { id: nextId.current++, account: "", direction: "C", amount: "" }])}
            >
              Adicionar partida
            </Button>
          </div>

          <div
            className={clsx(
              "flex items-center gap-3 rounded-md border px-4 py-3 text-sm",
              balanced ? "border-forest/30 bg-moss text-forest" : "border-rule bg-paper text-ink-2",
            )}
          >
            {balanced ? <CheckCircle2 className="size-4" /> : <Scale className="size-4" />}
            <span className="num">
              débitos {money(debits)} · créditos {money(credits)}
            </span>
            <span className="ml-auto">
              {balanced ? "fecha" : debits === credits ? "preencha as partidas" : `diferença de ${money(Math.abs(debits - credits))}`}
            </span>
          </div>

          <Field label="Histórico">
            <input className={inputClass} value={description} maxLength={500} onChange={(e) => setDescription(e.target.value)} placeholder="ex.: pagamento do pedido 1042" />
          </Field>

          <div className="flex flex-wrap items-center gap-4 rounded-md border border-rule p-4">
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" className="accent-[var(--color-forest)]" checked={pending} onChange={(e) => setPending(e.target.checked)} />
              Criar como reserva (duas fases)
            </label>
            {pending && (
              <label className="flex items-center gap-2 text-sm text-ink-2">
                expira em
                <input
                  type="number"
                  min={1}
                  max={43200}
                  className={inputClass + " num h-8 w-20"}
                  value={expiresMin}
                  onChange={(e) => setExpiresMin(Math.max(1, +e.target.value))}
                />
                minutos
              </label>
            )}
            <p className="w-full text-xs text-ink-3">
              Uma reserva separa o valor sem movimentá-lo. Depois você captura (vira lançamento) ou cancela (libera). É como uma
              pré-autorização de cartão.
            </p>
          </div>

          {create.isError && <ErrorBox error={create.error} />}
          <div className="flex items-center justify-between gap-4">
            <span className="num truncate text-[11px] text-ink-4" title="Chave de idempotência desta requisição">
              Idempotency-Key: {key.current}
            </span>
            <Button type="submit" variant="primary" disabled={!balanced} loading={create.isPending}>
              {pending ? "Reservar" : "Lançar"}
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
