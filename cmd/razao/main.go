// Command razao is a double-entry ledger server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"razao/internal/api"
	"razao/internal/demo"
	"razao/internal/loadgen"
	"razao/internal/pipeline"
	"razao/internal/store"
)

const usage = `razao — livro-razão de partidas dobradas

uso:
  razao serve   [-addr 127.0.0.1:7878] [-batch 2000] [-demo]
  razao bench   [-seconds 10] [-workers 256] [-wallets 2000] [-batch 2000] [-hot 0.2] [-compare]
  razao verify             re-calcula a cadeia de hashes e confere os saldos
  razao reset   -yes       apaga todos os dados

banco: variável RAZAO_DATABASE_URL (padrão: postgres://razao:razao@127.0.0.1:54329/razao)
`

func dbURL() string {
	if u := os.Getenv("RAZAO_DATABASE_URL"); u != "" {
		return u
	}
	return "postgres://razao:razao@127.0.0.1:54329/razao"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx, os.Args[2:])
	case "bench":
		err = bench(ctx, os.Args[2:])
	case "verify":
		err = verify(ctx)
	case "reset":
		err = reset(ctx, os.Args[2:])
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

func open(ctx context.Context) (*store.Store, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st, err := store.Open(ctx, dbURL())
	if err != nil {
		return nil, fmt.Errorf("%w\n\nO Postgres está de pé? Rode .\\run.ps1 ou: docker compose up -d", err)
	}
	return st, nil
}

func serve(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7878", "endereço HTTP")
	batch := fs.Int("batch", 2000, "tamanho máximo do lote")
	demoMode := fs.Bool("demo", false, "habilita seed, teste de carga e adulteração pela interface")
	fs.Parse(args)

	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	p := pipeline.New(st, *batch)
	go p.Run(ctx)

	srv := api.New(st, p, api.Options{Demo: *demoMode, APIKey: os.Getenv("RAZAO_API_KEY")})
	go srv.SweepExpired(ctx)
	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
	}()
	slog.Info("razao ouvindo", "url", "http://"+*addr, "lote", *batch, "demo", *demoMode)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func bench(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	seconds := fs.Int("seconds", 10, "duração de cada medição")
	workers := fs.Int("workers", 256, "clientes concorrentes")
	wallets := fs.Int("wallets", 2000, "carteiras")
	batch := fs.Int("batch", 2000, "tamanho máximo do lote")
	hot := fs.Float64("hot", 0.2, "fração do tráfego em 8 contas quentes")
	compare := fs.Bool("compare", false, "mede também sem agrupamento (lote = 1)")
	fs.Parse(args)

	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	run := func(maxBatch int) (loadgen.Report, pipeline.Snapshot, error) {
		pctx, cancel := context.WithCancel(ctx)
		defer cancel()
		p := pipeline.New(st, maxBatch)
		go p.Run(pctx)
		codes, err := demo.Seed(ctx, st, p.Submit, *wallets)
		if err != nil {
			return loadgen.Report{}, pipeline.Snapshot{}, err
		}
		before := p.Metrics.Snapshot()
		r := loadgen.Run(ctx, loadgen.Config{
			Workers: *workers, Duration: time.Duration(*seconds) * time.Second, Seconds: *seconds,
			Wallets: codes, Funding: demo.Bank, Fees: demo.Fees, HotShare: *hot, HoldShare: 0.15,
		}, p.Submit, nil)
		after := p.Metrics.Snapshot()
		after.Batches -= before.Batches
		if after.Batches > 0 {
			after.AvgBatch = float64(r.Committed+r.Rejected) / float64(after.Batches)
		}
		return r, after, nil
	}
	print := func(label string, r loadgen.Report, m pipeline.Snapshot) {
		fmt.Printf("%-22s %9.0f tx/s   p50 %7.2f ms   p99 %7.2f ms   lote médio %7.1f   %d confirmadas, %d recusadas, %d erros\n",
			label, r.TPS, r.P50Ms, r.P99Ms, m.AvgBatch, r.Committed, r.Rejected, r.Errors)
	}

	fmt.Printf("carga: %d clientes, %d carteiras, %.0f%% em contas quentes, %ds por medição\n\n", *workers, *wallets, *hot*100, *seconds)
	if *compare {
		r, m, err := run(1)
		if err != nil {
			return err
		}
		print("sem agrupar (lote=1)", r, m)
	}
	r, m, err := run(*batch)
	if err != nil {
		return err
	}
	print(fmt.Sprintf("commit em grupo (≤%d)", *batch), r, m)
	fmt.Println()
	return verify(ctx)
}

func verify(ctx context.Context) error {
	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	chain, err := st.VerifyChain(ctx)
	if err != nil {
		return err
	}
	if chain.OK {
		fmt.Printf("cadeia de hashes: OK   %d transações em %.0f ms   cabeça %s\n", chain.Checked, chain.ElapsedMs, chain.Head)
	} else {
		fmt.Printf("cadeia de hashes: QUEBRADA na transação %d: %s\n", chain.BrokenAt, chain.Reason)
	}
	integ, err := st.CheckIntegrity(ctx)
	if err != nil {
		return err
	}
	if integ.OK {
		fmt.Printf("contabilidade:    OK   %d partidas, balancete fecha, saldos batem com o diário (%.0f ms)\n", integ.Entries, integ.ElapsedMs)
	} else {
		raw, _ := json.MarshalIndent(integ, "", "  ")
		fmt.Printf("contabilidade:    FALHOU\n%s\n", raw)
	}
	if !chain.OK || !integ.OK {
		os.Exit(3)
	}
	return nil
}

func reset(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reset", flag.ExitOnError)
	yes := fs.Bool("yes", false, "confirma")
	fs.Parse(args)
	if !*yes {
		return errors.New("isto apaga todos os dados; repita com -yes")
	}
	st, err := open(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Reset(ctx)
}
