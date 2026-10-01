import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus, Search, X } from "lucide-react";
import { useDeferredValue, useState } from "react";
import { Link } from "react-router";
import { Amount, Button, Card, Empty, ErrorBox, Field, Spinner, TypeBadge, inputClass } from "../components/ui";
import { api, type AccountType } from "../lib/api";
import { typeLabel } from "../lib/format";

const PAGE = 50;

function NewAccount({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [type, setType] = useState<AccountType>("liability");
  const [currency, setCurrency] = useState("BRL");
  const [allowNegative, setAllowNegative] = useState(false);
  const create = useMutation({
    mutationFn: () => api.createAccount({ code, name, type, currency, allowNegative }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["accounts"] });
      onClose();
    },
  });
  return (
    <Card className="mb-6">
      <form
        className="grid gap-4 p-5 md:grid-cols-[1.2fr_1.4fr_auto_auto]"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Field label="Código" hint="minúsculas, dígitos e : _ . -">
          <input className={inputClass + " num"} value={code} onChange={(e) => setCode(e.target.value.toLowerCase())} placeholder="passivo:carteira:maria" required />
        </Field>
        <Field label="Nome">
          <input className={inputClass} value={name} onChange={(e) => setName(e.target.value)} placeholder="Carteira da Maria" required />
        </Field>
        <Field label="Tipo">
          <select
            className={inputClass}
            value={type}
            onChange={(e) => {
              const t = e.target.value as AccountType;
              setType(t);
              setAllowNegative(t !== "liability");
            }}
          >
            {(Object.keys(typeLabel) as AccountType[]).map((t) => (
              <option key={t} value={t}>
                {typeLabel[t]}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Moeda">
          <input className={inputClass + " num w-24"} value={currency} maxLength={3} onChange={(e) => setCurrency(e.target.value.toUpperCase())} required />
        </Field>
        <label className="flex items-center gap-2 text-sm text-ink-2 md:col-span-2">
          <input type="checkbox" className="accent-[var(--color-forest)]" checked={allowNegative} onChange={(e) => setAllowNegative(e.target.checked)} />
          Pode ficar com saldo negativo
          <span className="text-xs text-ink-3">(carteiras de clientes normalmente não podem)</span>
        </label>
        <div className="flex items-center justify-end gap-2 md:col-span-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancelar
          </Button>
          <Button type="submit" variant="primary" loading={create.isPending}>
            Criar conta
          </Button>
        </div>
        {create.isError && (
          <div className="md:col-span-4">
            <ErrorBox error={create.error} />
          </div>
        )}
      </form>
    </Card>
  );
}

export function AccountsPage() {
  const [search, setSearch] = useState("");
  const [creating, setCreating] = useState(false);
  const term = useDeferredValue(search.trim());
  const q = useInfiniteQuery({
    queryKey: ["accounts", term],
    queryFn: ({ pageParam }) => api.accounts(term, pageParam, PAGE),
    initialPageParam: "",
    getNextPageParam: (last) => (last.accounts.length === PAGE ? last.accounts[last.accounts.length - 1]!.code : undefined),
  });
  const accounts = q.data?.pages.flatMap((p) => p.accounts) ?? [];

  return (
    <div>
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <h1 className="font-serif text-2xl font-semibold">Plano de contas</h1>
        <label className="relative ml-auto">
          <Search className="pointer-events-none absolute top-3 left-3 size-4 text-ink-4" />
          <input className={inputClass + " w-64 pl-9"} placeholder="buscar por código ou nome" value={search} onChange={(e) => setSearch(e.target.value)} />
        </label>
        <Button variant={creating ? "outline" : "primary"} icon={creating ? <X className="size-4" /> : <Plus className="size-4" />} onClick={() => setCreating(!creating)}>
          {creating ? "Fechar" : "Nova conta"}
        </Button>
      </div>
      {creating && <NewAccount onClose={() => setCreating(false)} />}

      <Card className="overflow-hidden">
        {q.isPending ? (
          <Spinner />
        ) : q.isError ? (
          <div className="p-5">
            <ErrorBox error={q.error} />
          </div>
        ) : accounts.length === 0 ? (
          <Empty title={term ? "Nenhuma conta encontrada" : "Nenhuma conta ainda"}>
            {!term && "Uma conta guarda um saldo. Crie ao menos duas para registrar um lançamento."}
          </Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-rule text-left text-[11px] tracking-wider text-ink-3 uppercase">
                  <th className="px-5 py-2.5 font-medium">Conta</th>
                  <th className="px-3 py-2.5 font-medium">Tipo</th>
                  <th className="px-3 py-2.5 text-right font-medium">Saldo</th>
                  <th className="px-3 py-2.5 text-right font-medium">Disponível</th>
                  <th className="px-5 py-2.5 text-right font-medium">Partidas</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-rule">
                {accounts.map((a) => (
                  <tr key={a.id} className="hover:bg-paper/60">
                    <td className="px-5 py-2.5">
                      <Link to={`/contas/${encodeURIComponent(a.code)}`} className="block">
                        <div className="num text-[13px] text-forest hover:underline">{a.code}</div>
                        <div className="text-xs text-ink-3">{a.name}</div>
                      </Link>
                    </td>
                    <td className="px-3 py-2.5">
                      <TypeBadge type={a.type} />
                    </td>
                    <td className="px-3 py-2.5 text-right">
                      <Amount value={a.posted} currency={a.currency} />
                    </td>
                    <td className="px-3 py-2.5 text-right">
                      <Amount value={a.available} currency={a.currency} muted={a.available === a.posted} />
                    </td>
                    <td className="num px-5 py-2.5 text-right text-ink-3">{a.balance.entries.toLocaleString("pt-BR")}</td>
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
