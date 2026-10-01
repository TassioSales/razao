import { useInfiniteQuery } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import { Link, useParams } from "react-router";
import { Amount, Badge, Button, Card, CardHeader, Empty, ErrorBox, Spinner, TypeBadge } from "../components/ui";
import { api } from "../lib/api";
import { kindLabel, money, when } from "../lib/format";

export function AccountPage() {
  const { ref = "" } = useParams();
  const q = useInfiniteQuery({
    queryKey: ["statement", ref],
    queryFn: ({ pageParam }) => api.statement(ref, pageParam),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.lines.length === 50 ? last.lines[last.lines.length - 1]!.seq : undefined),
    refetchInterval: 4000,
  });
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorBox error={q.error} />;
  const a = q.data.pages[0]!.account;
  const lines = q.data.pages.flatMap((p) => p.lines);
  const b = a.balance;
  const reserved = a.posted - a.available;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center gap-3">
        <Link to="/contas" className="rounded-md p-1.5 text-ink-3 hover:bg-sheet hover:text-ink" aria-label="Voltar">
          <ArrowLeft className="size-4" />
        </Link>
        <div>
          <h1 className="font-serif text-2xl font-semibold">{a.name}</h1>
          <div className="num text-xs text-ink-3">{a.code}</div>
        </div>
        <TypeBadge type={a.type} />
        <Badge>{a.currency}</Badge>
        {!a.allowNegative && <Badge tone="info">não pode ficar negativa</Badge>}
      </div>

      <div className="grid gap-4 md:grid-cols-[1fr_1.3fr]">
        <Card className="p-5">
          <div className="text-[11px] font-medium tracking-wider text-ink-3 uppercase">Saldo contabilizado</div>
          <div className="mt-1 text-4xl">
            <Amount value={a.posted} currency={a.currency} />
          </div>
          <dl className="mt-4 space-y-1.5 border-t border-rule pt-3 text-sm">
            <div className="flex justify-between">
              <dt className="text-ink-3">Disponível</dt>
              <dd>
                <Amount value={a.available} currency={a.currency} />
              </dd>
            </div>
            <div className="flex justify-between">
              <dt className="text-ink-3">Reservado (saídas pendentes)</dt>
              <dd className="num">{money(reserved, a.currency)}</dd>
            </div>
          </dl>
        </Card>

        {/* The T-account: what every accounting student draws. */}
        <Card>
          <CardHeader title="Razonete" hint={`Conta de natureza ${a.normal === "D" ? "devedora: cresce a débito" : "credora: cresce a crédito"}.`} />
          <div className="grid grid-cols-2 divide-x-2 divide-ink/70 border-t-2 border-ink/70 text-sm">
            <div className="p-4">
              <div className="text-[11px] font-medium tracking-wider text-ink-3 uppercase">Débitos</div>
              <div className="num mt-1 text-lg">{money(b.debitsPosted, a.currency)}</div>
              {b.debitsPending > 0 && <div className="num text-xs text-gold">+ {money(b.debitsPending, a.currency)} pendente</div>}
            </div>
            <div className="p-4 text-right">
              <div className="text-[11px] font-medium tracking-wider text-ink-3 uppercase">Créditos</div>
              <div className="num mt-1 text-lg">{money(b.creditsPosted, a.currency)}</div>
              {b.creditsPending > 0 && <div className="num text-xs text-gold">+ {money(b.creditsPending, a.currency)} pendente</div>}
            </div>
          </div>
        </Card>
      </div>

      <Card className="overflow-hidden">
        <CardHeader title="Extrato" hint="Mais recentes primeiro. O saldo é o que ficou contabilizado depois de cada partida." />
        {lines.length === 0 ? (
          <Empty title="Sem movimentação" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-rule text-left text-[11px] tracking-wider text-ink-3 uppercase">
                  <th className="px-5 py-2.5 font-medium">#</th>
                  <th className="px-3 py-2.5 font-medium">Quando</th>
                  <th className="px-3 py-2.5 font-medium">Histórico</th>
                  <th className="px-3 py-2.5 text-right font-medium">Débito</th>
                  <th className="px-3 py-2.5 text-right font-medium">Crédito</th>
                  <th className="px-5 py-2.5 text-right font-medium">Saldo</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-rule">
                {lines.map((l) => (
                  <tr key={`${l.seq}-${l.idx}`} className="hover:bg-paper/60">
                    <td className="px-5 py-2">
                      <Link to={`/transacoes/${l.txId}`} className="num text-xs text-forest hover:underline">
                        {l.seq}
                      </Link>
                    </td>
                    <td className="num px-3 py-2 text-xs whitespace-nowrap text-ink-3">{when(l.createdAt)}</td>
                    <td className="px-3 py-2">
                      <span className="text-ink-2">{l.description || (l.reversal ? "Estorno" : kindLabel[l.kind])}</span>
                      {l.kind !== "posted" && l.description && (
                        <Badge className="ml-2" tone={l.kind === "pending" ? "warn" : l.kind === "post" ? "info" : "neutral"}>
                          {kindLabel[l.kind].toLowerCase()}
                        </Badge>
                      )}
                      {l.reversal && l.description && <Badge className="ml-2" tone="bad">estorno</Badge>}
                    </td>
                    <td className="num px-3 py-2 text-right">{l.direction === "D" ? money(l.amount, a.currency) : ""}</td>
                    <td className="num px-3 py-2 text-right">{l.direction === "C" ? money(l.amount, a.currency) : ""}</td>
                    <td className="px-5 py-2 text-right">
                      {l.balanceAfter === undefined ? <span className="text-xs text-ink-4">—</span> : <Amount value={l.balanceAfter} currency={a.currency} />}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {q.hasNextPage && (
          <div className="border-t border-rule p-3 text-center">
            <Button size="sm" variant="ghost" loading={q.isFetchingNextPage} onClick={() => q.fetchNextPage()}>
              Carregar mais
            </Button>
          </div>
        )}
      </Card>
    </div>
  );
}
