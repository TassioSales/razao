import { useMutation } from "@tanstack/react-query";
import { Play, Square } from "lucide-react";
import { useState } from "react";
import { api } from "../lib/api";
import { ms, nf } from "../lib/format";
import { useLive } from "../lib/live";
import { Button, Card, CardHeader, ErrorBox } from "./ui";

const reasonLabel: Record<string, string> = {
  insufficient_funds: "saldo insuficiente",
  already_resolved: "reserva já resolvida",
};

/** Starts a synthetic workload inside the server and shows it as it runs. */
export function LoadPanel() {
  const live = useLive();
  const [workers, setWorkers] = useState(256);
  const [seconds, setSeconds] = useState(15);
  const [hot, setHot] = useState(0.2);
  const start = useMutation({ mutationFn: () => api.demo.load({ workers, seconds, wallets: 1000, hotShare: hot }) });
  const stop = useMutation({ mutationFn: api.demo.stopLoad });
  const load = live.load;
  const running = load?.running ?? false;

  return (
    <Card>
      <CardHeader
        title="Teste de carga"
        hint="Carteiras pagando umas às outras: pagamentos com tarifa (3 partidas), depósitos e reservas em duas fases."
      />
      <div className="space-y-4 p-5">
        <div className="grid grid-cols-3 gap-4 text-xs">
          <label>
            <div className="mb-1 flex justify-between text-ink-2">
              Clientes <span className="num text-ink">{workers}</span>
            </div>
            <input type="range" className="w-full" min={8} max={2048} step={8} value={workers} onChange={(e) => setWorkers(+e.target.value)} />
          </label>
          <label>
            <div className="mb-1 flex justify-between text-ink-2">
              Duração <span className="num text-ink">{seconds} s</span>
            </div>
            <input type="range" className="w-full" min={5} max={60} step={5} value={seconds} onChange={(e) => setSeconds(+e.target.value)} />
          </label>
          <label>
            <div className="mb-1 flex justify-between text-ink-2">
              Contas quentes <span className="num text-ink">{(hot * 100).toFixed(0)}%</span>
            </div>
            <input type="range" className="w-full" min={0} max={1} step={0.05} value={hot} onChange={(e) => setHot(+e.target.value)} />
          </label>
        </div>

        {running ? (
          <Button variant="danger" className="w-full" icon={<Square className="size-4" />} onClick={() => stop.mutate()}>
            Parar
          </Button>
        ) : (
          <Button variant="primary" className="w-full" loading={start.isPending} icon={<Play className="size-4" />} onClick={() => start.mutate()}>
            Iniciar carga
          </Button>
        )}
        {start.isError && <ErrorBox error={start.error} />}

        {load && (
          <div className="rounded-md border border-rule bg-paper/60 p-4">
            <div className="mb-3 flex items-center justify-between text-xs text-ink-3">
              <span>{running ? "Rodando…" : "Último resultado"}</span>
              <span className="num">
                {(load.elapsedMs / 1000).toFixed(1)} s · {load.config.workers} clientes
              </span>
            </div>
            {running && (
              <div className="mb-3 h-1 overflow-hidden rounded-full bg-rule">
                <div
                  className="h-full bg-forest transition-[width] duration-300"
                  style={{ width: `${Math.min(100, (load.elapsedMs / (load.config.seconds * 1000)) * 100)}%` }}
                />
              </div>
            )}
            <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-4">
              <div>
                <dt className="text-[11px] text-ink-3">Vazão</dt>
                <dd className="num text-lg font-medium text-forest">{nf.format(Math.round(load.tps))} tx/s</dd>
              </div>
              <div>
                <dt className="text-[11px] text-ink-3">Confirmadas</dt>
                <dd className="num text-lg font-medium">{nf.format(load.committed)}</dd>
              </div>
              <div>
                <dt className="text-[11px] text-ink-3">p50 / p99</dt>
                <dd className="num text-lg font-medium">
                  {running && live.metrics ? `${ms(live.metrics.p50Ms)} / ${ms(live.metrics.p99Ms)}` : `${ms(load.p50Ms)} / ${ms(load.p99Ms)}`}
                </dd>
              </div>
              <div>
                <dt className="text-[11px] text-ink-3">Recusadas</dt>
                <dd className="num text-lg font-medium">{nf.format(load.rejected)}</dd>
              </div>
            </dl>
            {Object.keys(load.reasons).length > 0 && (
              <p className="mt-3 text-xs text-ink-3">
                Recusas por regra de negócio (esperadas):{" "}
                {Object.entries(load.reasons)
                  .map(([k, v]) => `${reasonLabel[k] ?? k} ${nf.format(v)}`)
                  .join(" · ")}
              </p>
            )}
            {load.errors > 0 && <p className="mt-2 text-xs text-oxblood">{nf.format(load.errors)} erros de infraestrutura</p>}
          </div>
        )}
      </div>
    </Card>
  );
}
