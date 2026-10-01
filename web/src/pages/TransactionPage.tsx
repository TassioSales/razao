import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Check, Link2, Undo2, X } from "lucide-react";
import { useRef } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Amount, Badge, Button, Card, CardHeader, ErrorBox, KindBadge, Spinner } from "../components/ui";
import { api, newKey, type Transaction } from "../lib/api";
import { money, txLabel, txTotal } from "../lib/format";

function Actions({ tx }: { tx: Transaction }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  // One key per button for the life of the page: a double click or a retry
  // after a timeout replays instead of applying twice.
  const keys = useRef({ post: newKey(), void: newKey(), reverse: newKey() });
  const act = useMutation({
    mutationFn: (action: "post" | "void" | "reverse") => api.followUp(tx.id, action, keys.current[action]),
    onSuccess: (created) => {
      qc.invalidateQueries();
      navigate(`/transacoes/${created.id}`);
    },
  });
  const open = tx.kind === "pending" && !tx.resolution;
  const reversible = tx.kind === "posted" && !tx.reversal && !tx.reversed;
  if (!open && !reversible) return null;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-2">
        {open && (
          <>
            <Button variant="primary" icon={<Check className="size-4" />} loading={act.isPending && act.variables === "post"} onClick={() => act.mutate("post")}>
              Capturar reserva
            </Button>
            <Button icon={<X className="size-4" />} loading={act.isPending && act.variables === "void"} onClick={() => act.mutate("void")}>
              Cancelar reserva
            </Button>
          </>
        )}
        {reversible && (
          <Button variant="danger" icon={<Undo2 className="size-4" />} loading={act.isPending} onClick={() => act.mutate("reverse")}>
            Estornar
          </Button>
        )}
      </div>
      {act.isError && <ErrorBox error={act.error} />}
    </div>
  );
}

export function TransactionPage() {
  const { id = "" } = useParams();
  const q = useQuery({ queryKey: ["transaction", id], queryFn: () => api.transaction(id) });
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorBox error={q.error} />;
  const t = q.data;
  const currency = t.entries[0]?.currency ?? "BRL";
  const debits = t.entries.filter((e) => e.direction === "D");
  const credits = t.entries.filter((e) => e.direction === "C");
  const rows = Math.max(debits.length, credits.length);
  const relatedLabel = t.reversal ? "Estorna" : t.kind === "post" ? "Captura a reserva" : t.kind === "void" ? "Cancela a reserva" : "Relacionada";

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <Link to="/transacoes" className="rounded-md p-1.5 text-ink-3 hover:bg-sheet hover:text-ink" aria-label="Voltar">
          <ArrowLeft className="size-4" />
        </Link>
        <h1 className="font-serif text-2xl font-semibold">
          {txLabel(t)} <span className="num text-ink-3">#{t.seq}</span>
        </h1>
        <KindBadge tx={t} />
        <div className="ml-auto text-3xl">
          <Amount value={txTotal(t)} currency={currency} />
        </div>
      </div>
      {t.description && <p className="text-ink-2">{t.description}</p>}

      <Actions key={t.id} tx={t} />

      <div className="grid items-start gap-6 lg:grid-cols-[1.5fr_1fr]">
        <Card className="overflow-hidden">
          <CardHeader
            title="Partidas"
            hint={
              t.kind === "pending"
                ? "Reserva: os valores ficam pendentes e reduzem o disponível, sem alterar o saldo contabilizado."
                : t.kind === "void"
                  ? "Libera os valores que a reserva tinha separado."
                  : "Débitos à esquerda, créditos à direita. As duas colunas somam o mesmo."
            }
          />
          <div className="grid grid-cols-2 divide-x-2 divide-ink/70 border-t-2 border-ink/70">
            {[debits, credits].map((side, s) => (
              <div key={s}>
                <div className="border-b border-rule px-5 py-2 text-[11px] font-medium tracking-wider text-ink-3 uppercase">
                  {s === 0 ? "Débito" : "Crédito"}
                </div>
                {Array.from({ length: rows }, (_, i) => {
                  const e = side[i];
                  return (
                    <div key={i} className="flex min-h-14 items-center justify-between gap-3 border-b border-rule px-5 py-2.5 last:border-b-0">
                      {e && (
                        <>
                          <div className="min-w-0">
                            <Link to={`/contas/${encodeURIComponent(e.accountCode)}`} className="num block truncate text-[13px] text-forest hover:underline">
                              {e.accountCode}
                            </Link>
                            {e.balanceAfter !== undefined && <div className="num text-[11px] text-ink-3">saldo depois: {money(e.balanceAfter, e.currency)}</div>}
                          </div>
                          <Amount value={e.amount} currency={e.currency} />
                        </>
                      )}
                    </div>
                  );
                })}
                <div className="flex justify-between border-t-2 border-double border-ink/60 px-5 py-2.5 text-sm font-medium">
                  <span className="text-ink-3">Total</span>
                  <Amount value={side.reduce((sum, e) => sum + e.amount, 0)} currency={currency} />
                </div>
              </div>
            ))}
          </div>
        </Card>

        <div className="space-y-6">
          <Card>
            <CardHeader title="Cadeia de hashes" hint="O hash cobre o conteúdo desta transação e o hash da anterior." />
            <dl className="space-y-3 p-5 text-xs">
              <div>
                <dt className="text-ink-3">anterior (#{t.seq - 1})</dt>
                <dd className="num mt-0.5 break-all text-ink-2">{t.prevHash}</dd>
              </div>
              <div className="flex justify-center text-ink-4">
                <Link2 className="size-4 rotate-90" />
              </div>
              <div>
                <dt className="text-ink-3">esta (#{t.seq})</dt>
                <dd className="num mt-0.5 rounded bg-moss px-2 py-1.5 break-all text-forest">{t.hash}</dd>
              </div>
            </dl>
          </Card>

          <Card>
            <CardHeader title="Detalhes" />
            <dl className="space-y-2.5 p-5 text-sm">
              <div className="flex justify-between gap-4">
                <dt className="text-ink-3">ID</dt>
                <dd className="num text-xs break-all text-right">{t.id}</dd>
              </div>
              <div className="flex justify-between gap-4">
                <dt className="text-ink-3">Confirmada em</dt>
                <dd className="num text-xs">{new Date(t.createdAt).toLocaleString("pt-BR")}</dd>
              </div>
              <div className="flex justify-between gap-4">
                <dt className="shrink-0 text-ink-3">Chave de idempotência</dt>
                <dd className="num truncate text-xs" title={t.idempotencyKey}>
                  {t.idempotencyKey}
                </dd>
              </div>
              {t.expiresAt && (
                <div className="flex justify-between gap-4">
                  <dt className="text-ink-3">Expira em</dt>
                  <dd className="num text-xs">{new Date(t.expiresAt).toLocaleString("pt-BR")}</dd>
                </div>
              )}
              {t.relatedId && (
                <div className="flex justify-between gap-4">
                  <dt className="text-ink-3">{relatedLabel}</dt>
                  <dd>
                    <Link to={`/transacoes/${t.relatedId}`} className="num text-xs text-forest hover:underline">
                      {t.relatedId.slice(0, 13)}…
                    </Link>
                  </dd>
                </div>
              )}
              {Object.entries(t.metadata).map(([k, v]) => (
                <div key={k} className="flex justify-between gap-4">
                  <dt className="text-ink-3">{k}</dt>
                  <dd>
                    <Badge>{v}</Badge>
                  </dd>
                </div>
              ))}
            </dl>
          </Card>
        </div>
      </div>
    </div>
  );
}
