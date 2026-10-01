import clsx from "clsx";
import { NavLink, Outlet } from "react-router";
import { useLive } from "../lib/live";
import { nf } from "../lib/format";

function Mark() {
  return (
    <svg viewBox="0 0 32 32" className="size-7" aria-hidden>
      <rect width="32" height="32" rx="6" fill="var(--color-forest)" />
      <path d="M8 9h16M16 9v15" stroke="var(--color-paper)" strokeWidth="2.6" strokeLinecap="round" />
      <path d="M9.5 15h3.5M19 15h3.5M19 20h3.5" stroke="#d9b56a" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

const links = [
  { to: "/", label: "Painel", end: true },
  { to: "/contas", label: "Contas" },
  { to: "/transacoes", label: "Diário" },
  { to: "/auditoria", label: "Auditoria" },
];

export function Layout() {
  const live = useLive();
  return (
    <div className="flex min-h-screen flex-col">
      <header className="sticky top-0 z-30 border-b border-rule bg-paper/90 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-[1320px] items-center gap-6 px-4 sm:px-6">
          <NavLink to="/" className="flex items-center gap-2.5">
            <Mark />
            <span className="font-serif text-xl font-semibold tracking-tight">razão</span>
          </NavLink>
          <nav className="flex items-center gap-1 text-sm">
            {links.map((l) => (
              <NavLink
                key={l.to}
                to={l.to}
                end={l.end}
                className={({ isActive }) =>
                  clsx("rounded-md px-3 py-1.5 transition-colors", isActive ? "bg-forest text-paper" : "text-ink-2 hover:bg-sheet hover:text-ink")
                }
              >
                {l.label}
              </NavLink>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-4 text-xs text-ink-3">
            {live.metrics && (
              <span className="num hidden sm:inline">
                {nf.format(live.metrics.tps)} tx/s
              </span>
            )}
            <span className="flex items-center gap-1.5">
              <span className={clsx("size-2 rounded-full", live.connected ? "bg-forest-2" : "bg-oxblood")} />
              {live.connected ? "ao vivo" : "sem conexão"}
            </span>
          </div>
        </div>
      </header>
      <main className="mx-auto w-full max-w-[1320px] flex-1 px-4 py-6 sm:px-6">
        <Outlet />
      </main>
      <footer className="border-t border-rule py-5 text-center text-xs text-ink-3">
        Débitos à esquerda, créditos à direita. Todo lançamento fecha, ou não entra.
      </footer>
    </div>
  );
}
