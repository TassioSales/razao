import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Eraser, FileWarning, Fingerprint, RotateCcw, Scale, XCircle } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { Button, Card, CardHeader, ErrorBox } from "../components/ui";
import { api } from "../lib/api";
import { money, nf } from "../lib/format";

function Verdict({ ok, children }: { ok: boolean; children: ReactNode }) {
  return (
    <div className={ok ? "flex gap-3 rounded-md border border-forest/30 bg-moss p-4 text-forest" : "flex gap-3 rounded-md border border-oxblood/30 bg-rose p-4 text-oxblood"}>
      {ok ? <CheckCircle2 className="mt-0.5 size-5 shrink-0" /> : <XCircle className="mt-0.5 size-5 shrink-0" />}
      <div className="min-w-0 text-sm leading-relaxed">{children}</div>
    </div>
  );
}

export function AuditPage() {
  const qc = useQueryClient();
  const info = useQuery({ queryKey: ["info"], queryFn: api.info });
  const chain = useMutation({ mutationFn: api.verifyChain });
  const integrity = useMutation({ mutationFn: api.checkIntegrity });
  const recheck = () => {
    chain.mutate();
    integrity.mutate();
  };
  const tamper = useMutation({ mutationFn: api.demo.tamper, onSuccess: recheck });
  const restore = useMutation({ mutationFn: api.demo.restore, onSuccess: () => { tamper.reset(); recheck(); } });
  const reset = useMutation({
    mutationFn: api.demo.reset,
    onSuccess: () => {
      chain.reset();
      integrity.reset();
      tamper.reset();
      qc.invalidateQueries();
    },
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="font-serif text-2xl font-semibold">Auditoria</h1>
        <p className="mt-1 max-w-3xl text-sm leading-relaxed text-ink-2">
          Duas verificações independentes. A primeira refaz a cadeia de hashes do primeiro lançamento até a cabeça. A segunda
          reconstrói todos os saldos a partir das partidas e compara com os saldos materializados.
        </p>
      </div>

      <div className="grid items-start gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader
            title="Cadeia de hashes"
            hint="Qualquer alteração em valor, conta, histórico ou ordem quebra a cadeia a partir daquele ponto."
            right={
              <Button size="sm" variant="primary" icon={<Fingerprint className="size-3.5" />} loading={chain.isPending} onClick={() => chain.mutate()}>
                Verificar
              </Button>
            }
          />
          <div className="p-5">
            {chain.isError && <ErrorBox error={chain.error} />}
            {!chain.data && !chain.isError && <p className="text-sm text-ink-3">Ainda não verificada nesta sessão.</p>}
            {chain.data &&
              (chain.data.ok ? (
                <Verdict ok>
                  <b>Íntegra.</b> {nf.format(chain.data.checked)} transações conferidas em {chain.data.elapsedMs.toFixed(0)} ms.
                  <div className="num mt-1 text-xs break-all opacity-80">cabeça {chain.data.head}</div>
                </Verdict>
              ) : (
                <Verdict ok={false}>
                  <b>Quebrada na transação #{chain.data.brokenAt}.</b> {chain.data.reason}.
                  <div className="mt-1 text-xs">
                    {nf.format(chain.data.checked)} transações anteriores estão íntegras; desta em diante nada é confiável.
                  </div>
                </Verdict>
              ))}
          </div>
        </Card>

        <Card>
          <CardHeader
            title="Contabilidade"
            hint="Balancete, saldos recalculados do diário, lançamentos que não fecham e partidas órfãs."
            right={
              <Button size="sm" variant="primary" icon={<Scale className="size-3.5" />} loading={integrity.isPending} onClick={() => integrity.mutate()}>
                Conferir
              </Button>
            }
          />
          <div className="space-y-3 p-5">
            {integrity.isError && <ErrorBox error={integrity.error} />}
            {!integrity.data && !integrity.isError && <p className="text-sm text-ink-3">Ainda não conferida nesta sessão.</p>}
            {integrity.data &&
              (integrity.data.ok ? (
                <Verdict ok>
                  <b>Tudo confere.</b> {nf.format(integrity.data.entries)} partidas, balancete fecha e todos os saldos batem com o
                  diário ({integrity.data.elapsedMs.toFixed(0)} ms).
                  <div className="mt-1 text-xs opacity-80">{nf.format(integrity.data.openPending)} reservas em aberto.</div>
                </Verdict>
              ) : (
                <Verdict ok={false}>
                  <b>Divergências encontradas.</b>
                  <ul className="mt-1 list-disc space-y-1 pl-4 text-xs">
                    {!integrity.data.trialBalance && <li>O balancete não fecha.</li>}
                    {integrity.data.unbalanced.length > 0 && (
                      <li>Lançamentos em que débitos ≠ créditos: {integrity.data.unbalanced.map((s) => `#${s}`).join(", ")}</li>
                    )}
                    {integrity.data.drift.map((d) => (
                      <li key={d.code}>
                        <Link to={`/contas/${encodeURIComponent(d.code)}`} className="num underline">
                          {d.code}
                        </Link>
                        : saldo gravado diz créditos {money(d.stored.creditsPosted)} / débitos {money(d.stored.debitsPosted)}, o diário diz{" "}
                        {money(d.computed.creditsPosted)} / {money(d.computed.debitsPosted)}
                      </li>
                    ))}
                    {integrity.data.orphans > 0 && <li>{integrity.data.orphans} partidas órfãs.</li>}
                  </ul>
                </Verdict>
              ))}
          </div>
        </Card>
      </div>

      {info.data?.demo && (
        <Card>
          <CardHeader
            title="Demonstração: adulterar o banco"
            hint="Simula alguém com acesso direto ao Postgres que desliga o gatilho de imutabilidade e soma R$ 1.000,00 a uma partida qualquer."
          />
          <div className="space-y-4 p-5">
            <div className="flex flex-wrap gap-2">
              <Button variant="danger" icon={<FileWarning className="size-4" />} loading={tamper.isPending} disabled={!!tamper.data} onClick={() => tamper.mutate()}>
                Adulterar uma partida
              </Button>
              <Button icon={<RotateCcw className="size-4" />} loading={restore.isPending} disabled={!tamper.data} onClick={() => restore.mutate()}>
                Restaurar
              </Button>
              <Button
                variant="ghost"
                className="ml-auto"
                icon={<Eraser className="size-4" />}
                loading={reset.isPending}
                onClick={() => confirm("Apagar todas as contas e transações?") && reset.mutate()}
              >
                Zerar o livro
              </Button>
            </div>
            {tamper.data && (
              <p className="text-sm text-ink-2">
                A partida {tamper.data.idx} da transação <b className="num">#{tamper.data.seq}</b> foi alterada por fora. As duas
                verificações acima rodaram de novo: a cadeia aponta exatamente essa transação, e a contabilidade mostra a conta
                cujo saldo deixou de bater.
              </p>
            )}
            {(tamper.isError || restore.isError || reset.isError) && <ErrorBox error={tamper.error ?? restore.error ?? reset.error} />}
          </div>
        </Card>
      )}
    </div>
  );
}
