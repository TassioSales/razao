import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Link, Route, Routes } from "react-router";
import "./index.css";
import { Layout } from "./components/Layout";
import { LiveProvider } from "./lib/live";
import { AccountPage } from "./pages/AccountPage";
import { AccountsPage } from "./pages/AccountsPage";
import { AuditPage } from "./pages/AuditPage";
import { DashboardPage } from "./pages/DashboardPage";
import { JournalPage } from "./pages/JournalPage";
import { NewTransactionPage } from "./pages/NewTransactionPage";
import { TransactionPage } from "./pages/TransactionPage";

const queryClient = new QueryClient({
  defaultOptions: { queries: { refetchOnWindowFocus: false, retry: 1 } },
});

function NotFound() {
  return (
    <div className="py-24 text-center">
      <p className="font-serif text-5xl text-ink-4">404</p>
      <p className="mt-3 text-ink-2">Esta página não está no livro.</p>
      <Link to="/" className="mt-6 inline-block text-sm text-forest hover:underline">
        Voltar ao painel
      </Link>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <LiveProvider>
        <BrowserRouter>
          <Routes>
            <Route element={<Layout />}>
              <Route index element={<DashboardPage />} />
              <Route path="contas" element={<AccountsPage />} />
              <Route path="contas/:ref" element={<AccountPage />} />
              <Route path="transacoes" element={<JournalPage />} />
              <Route path="transacoes/nova" element={<NewTransactionPage />} />
              <Route path="transacoes/:id" element={<TransactionPage />} />
              <Route path="auditoria" element={<AuditPage />} />
              <Route path="*" element={<NotFound />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </LiveProvider>
    </QueryClientProvider>
  </StrictMode>,
);
