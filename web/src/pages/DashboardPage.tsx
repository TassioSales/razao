import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Sprout, XCircle } from "lucide-react";
import { Link } from "react-router";
import { LoadPanel } from "../components/LoadPanel";
import { TpsChart } from "../components/TpsChart";
import { Amount, Button, Card, CardHeader, Empty, ErrorBox, KindBadge, Spinner, Stat } from "../components/ui";
import { api } from "../lib/api";
import { money, ms, nf, shortHash, txTotal, when } from "../lib/format";
import { useLive } from "../lib/live";

export function DashboardPage() {
  const live = useLive();
  const qc = useQueryClient();
  const info = useQuery({ queryKey: ["info"], queryFn: api.info });
  const stats = useQuery({ queryKey: ["stats"], queryFn: api.stats, refetchInterval: 2000 });
  const seed = useMutation({
    mutationFn: () => api.demo.seed(200),
    onSuccess: () => qc.invalidateQueries(),
  });

  if (stats.isPending) return <Spinner label="Conectando ao diário…" />;
  if (stats.isError) return <ErrorBox error={stats.error} />;
  const { summary } = stats.data;
  const m = live.metrics ?? stats.data.metrics;
  const demo = info.data?.demo ?? false;
  const empty = summary.accounts === 0;

  return (
    <div className="space-y-6">
      {empty && (
        <Card>
          <Empty title="O livro está em branco">
            <p>
              Crie um plano de contas de exemplo (banco, 200 carteiras de clientes, receita de tarifas) com saldo inicial, ou comece
              pelas suas próprias contas.
            </p>
            <div className="mt-5 flex justify-center gap-3">
              {demo && (
                <Button variant="primary" icon={<Sprout className="size-4" />} loading={seed.isPending} onClick={() => seed.mutate()}>
                  Criar dados de exemplo
                </Button>
              )}
              <Link to="/contas">
                <Button>Criar uma conta</Button>
              </Link>
            </div>
            {seed.isError && <div className="mt-4"><ErrorBox error={seed.error} /></div>}
            {!demo && <p className="mt-4 text-xs">Para o seed e o teste de carga, inicie o servidor com <code className="num">-demo</code>.</p>}
          </Empty>
        </Card>
      )}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-5">
        <Stat label="Transações" value={nf.format(summary.seq)} sub="no diário, em ordem total" />
        <Stat label="Vazão" value={nf.format(m.tps)} sub="transações por segundo" />
        <Stat label="Latência p50" value={ms(m.p50Ms)} sub={`p99 ${ms(m.p99Ms)}`} />
        <Stat label="Lote médio" value={m.avgBatch.toFixed(1)} sub={`commit médio ${ms(m.avgCommitMs)}`} />
        <Stat
          label="Balancete"
          value={summary.trialBalance ? "fecha" : "não fecha"}
          tone={summary.trialBalance ? "good" : "bad"}
          sub={`${nf.format(summary.accounts)} contas`}
        />
      </div>

      <div className="grid items-start gap-6 lg:grid-cols-[1fr_420px]">
        <div className="space-y-6">
          <Card>
            <CardHeader
              title="Vazão do escritor"
              hint={`Um único escritor grava em lotes de até ${nf.format(info.data?.maxBatch ?? 0)}. Quanto mais pressão, maior o lote e menor o custo por transação.`}
            />
            <div className="px-3 py-4">
              <TpsChart series={m.series} />
            </div>
          </Card>

          <Card>
            <CardHeader title="Últimos lançamentos" hint="Chegam por Server-Sent Events, na ordem em que foram confirmados." />
            {live.recent.length === 0 ? (
              <p className="px-5 py-6 text-sm text-ink-3">Nada novo desde que esta página abriu.</p>
            ) : (
              <ul className="divide-y divide-rule">
                {live.recent.map((t) => (
                  <li key={t.seq} className="rise">
                    <Link to={`/transacoes/${t.id}`} className="flex items-center gap-4 px-5 py-2.5 text-sm hover:bg-paper/70">
                      <span className="num w-16 shrink-0 text-xs text-ink-3">#{t.seq}</span>
                      <KindBadge tx={t} />
                      <span className="min-w-0 flex-1 truncate text-ink-2">
                        {t.entries.find((e) => e.direction === "D")?.accountCode} → {t.entries.find((e) => e.direction === "C")?.accountCode}
                      </span>
                      <Amount value={txTotal(t)} currency={t.entries[0]?.currency} />
                      <span className="num hidden w-20 text-right text-xs text-ink-3 sm:block">{when(t.createdAt)}</span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>

        <div className="space-y-6">
          {demo && <LoadPanel />}

          <Card>
            <CardHeader title="Balancete" hint="Soma de todos os débitos e créditos contabilizados, por moeda." />
            <div className="p-5">
              {summary.totals.length === 0 && <p className="text-sm text-ink-3">Sem contas ainda.</p>}
              {summary.totals.map((t) => {
                const ok = t.debitsPosted === t.creditsPosted && t.debitsPending === t.creditsPending;
                return (
                  <div key={t.currency} className="space-y-2">
                    <div className="flex items-center justify-between">
                      <span className="text-sm font-medium">{t.currency}</span>
                      {ok ? (
                        <span className="flex items-center gap-1 text-xs text-forest">
                          <CheckCircle2 className="size-3.5" /> débitos = créditos
                        </span>
                      ) : (
                        <span className="flex items-center gap-1 text-xs text-oxblood">
                          <XCircle className="size-3.5" /> diferença de {money(t.debitsPosted - t.creditsPosted, t.currency)}
                        </span>
                      )}
                    </div>
                    <div className="grid grid-cols-2 divide-x divide-rule rounded-md border border-rule text-sm">
                      <div className="px-3 py-2">
                        <div className="text-[11px] text-ink-3">Débitos</div>
                        <div className="num">{money(t.debitsPosted, t.currency)}</div>
                      </div>
                      <div className="px-3 py-2 text-right">
                        <div className="text-[11px] text-ink-3">Créditos</div>
                        <div className="num">{money(t.creditsPosted, t.currency)}</div>
                      </div>
                    </div>
                    {t.debitsPending > 0 && (
                      <p className="text-xs text-ink-3">
                        Reservado em aberto: <span className="num">{money(t.debitsPending, t.currency)}</span>
                      </p>
                    )}
                  </div>
                );
              })}
              <div className="mt-4 border-t border-rule pt-3 text-xs text-ink-3">
                Cabeça da cadeia: <span className="num text-ink-2">{shortHash(summary.head)}</span>
                <Link to="/auditoria" className="ml-2 text-forest underline-offset-2 hover:underline">
                  auditar
                </Link>
              </div>
            </div>
          </Card>
        </div>
      </div>
    </div>
  );
}
