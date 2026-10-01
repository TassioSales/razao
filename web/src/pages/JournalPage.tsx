import { useInfiniteQuery } from "@tanstack/react-query";
import clsx from "clsx";
import { Plus } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { Amount, Button, Card, Empty, ErrorBox, KindBadge, Spinner } from "../components/ui";
import { api } from "../lib/api";
import { txTotal, when } from "../lib/format";

const filters = [
  { value: "", label: "Tudo" },
  { value: "posted", label: "Lançamentos" },
  { value: "pending", label: "Reservas" },
  { value: "post", label: "Capturas" },
  { value: "void", label: "Cancelamentos" },
];

export function JournalPage() {
  const [kind, setKind] = useState("");
  const q = useInfiniteQuery({
    queryKey: ["transactions", kind],
    queryFn: ({ pageParam }) => api.transactions(pageParam, kind),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.transactions.length === 50 ? last.transactions[last.transactions.length - 1]!.seq : undefined),
  });
  const txs = q.data?.pages.flatMap((p) => p.transactions) ?? [];

  return (
    <div>
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <h1 className="font-serif text-2xl font-semibold">Diário</h1>
        <div className="inline-flex rounded-md border border-rule bg-sheet p-0.5">
          {filters.map((f) => (
            <button
              key={f.value}
              onClick={() => setKind(f.value)}
              className={clsx("rounded px-3 py-1 text-xs font-medium", kind === f.value ? "bg-forest text-paper" : "text-ink-2 hover:text-ink")}
            >
              {f.label}
            </button>
          ))}
        </div>
        <div className="ml-auto flex gap-2">
          <Button onClick={() => q.refetch()} loading={q.isRefetching && !q.isFetchingNextPage}>
            Atualizar
          </Button>
          <Link to="/transacoes/nova">
            <Button variant="primary" icon={<Plus className="size-4" />}>
              Novo lançamento
            </Button>
          </Link>
        </div>
      </div>

      <Card className="overflow-hidden">
        {q.isPending ? (
          <Spinner />
        ) : q.isError ? (
          <div className="p-5">
            <ErrorBox error={q.error} />
          </div>
        ) : txs.length === 0 ? (
          <Empty title="O diário está vazio">Cada linha aqui é imutável e encadeada à anterior por hash.</Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-rule text-left text-[11px] tracking-wider text-ink-3 uppercase">
                  <th className="px-5 py-2.5 font-medium">#</th>
                  <th className="px-3 py-2.5 font-medium">Quando</th>
                  <th className="px-3 py-2.5 font-medium">Tipo</th>
                  <th className="px-3 py-2.5 font-medium">Débito → Crédito</th>
                  <th className="px-3 py-2.5 font-medium">Histórico</th>
                  <th className="px-5 py-2.5 text-right font-medium">Valor</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-rule">
                {txs.map((t) => {
                  const debits = t.entries.filter((e) => e.direction === "D");
                  const credits = t.entries.filter((e) => e.direction === "C");
                  return (
                    <tr key={t.seq} className="hover:bg-paper/60">
                      <td className="px-5 py-2.5">
                        <Link to={`/transacoes/${t.id}`} className="num text-xs text-forest hover:underline">
                          {t.seq}
                        </Link>
                      </td>
                      <td className="num px-3 py-2.5 text-xs whitespace-nowrap text-ink-3">{when(t.createdAt)}</td>
                      <td className="px-3 py-2.5">
                        <KindBadge tx={t} />
                      </td>
                      <td className="num px-3 py-2.5 text-xs text-ink-2">
                        {debits[0]?.accountCode}
                        {debits.length > 1 && <span className="text-ink-4"> +{debits.length - 1}</span>}
                        <span className="text-ink-4"> → </span>
                        {credits[0]?.accountCode}
                        {credits.length > 1 && <span className="text-ink-4"> +{credits.length - 1}</span>}
                      </td>
                      <td className="max-w-60 truncate px-3 py-2.5 text-ink-2">{t.description}</td>
                      <td className="px-5 py-2.5 text-right">
                        <Amount value={txTotal(t)} currency={t.entries[0]?.currency} />
                      </td>
                    </tr>
                  );
                })}
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
