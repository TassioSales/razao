import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import type { LoadReport, Metrics, Transaction } from "./api";

type Live = {
  connected: boolean;
  metrics: Metrics | null;
  recent: Transaction[];
  load: LoadReport | null;
};

const LiveContext = createContext<Live>({ connected: false, metrics: null, recent: [], load: null });

const KEEP = 14;

/** One SSE connection for the whole app: writer metrics, newest transactions, load test progress. */
export function LiveProvider({ children }: { children: ReactNode }) {
  const [live, setLive] = useState<Live>({ connected: false, metrics: null, recent: [], load: null });

  useEffect(() => {
    const es = new EventSource("/v1/stream");
    es.addEventListener("tick", (ev) => {
      const p = JSON.parse((ev as MessageEvent).data) as { metrics: Metrics; recent: Transaction[] | null; load: LoadReport | null };
      setLive((prev) => ({
        connected: true,
        metrics: p.metrics,
        load: p.load,
        recent: p.recent?.length ? [...p.recent].reverse().concat(prev.recent).slice(0, KEEP) : prev.recent,
      }));
    });
    es.onerror = () => setLive((prev) => ({ ...prev, connected: false })); // EventSource reconnects on its own
    return () => es.close();
  }, []);

  return <LiveContext.Provider value={live}>{children}</LiveContext.Provider>;
}

export const useLive = () => useContext(LiveContext);
