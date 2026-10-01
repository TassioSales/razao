// Package loadgen drives the ledger with synthetic traffic: wallets paying
// each other, some payments going through a reservation first, and a share
// of the traffic concentrated on a few hot accounts.
package loadgen

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"razao/internal/ledger"
)

// Config describes a load run.
type Config struct {
	Workers  int           `json:"workers"`
	Duration time.Duration `json:"-"`
	Seconds  int           `json:"seconds"`
	Wallets  []string      `json:"-"`
	Funding  string        `json:"-"` // account that tops wallets up
	Fees     string        `json:"-"`
	// HotShare is the fraction of transfers that involve one of the first
	// eight wallets, to exercise contention on the same rows.
	HotShare float64 `json:"hotShare"`
	// HoldShare is the fraction of payments done in two phases.
	HoldShare float64 `json:"holdShare"`
}

// Report is the outcome of a run.
type Report struct {
	Running   bool             `json:"running"`
	Config    Config           `json:"config"`
	StartedAt time.Time        `json:"startedAt"`
	ElapsedMs float64          `json:"elapsedMs"`
	Committed int64            `json:"committed"`
	Rejected  int64            `json:"rejected"`
	Errors    int64            `json:"errors"`
	TPS       float64          `json:"tps"`
	P50Ms     float64          `json:"p50Ms"`
	P99Ms     float64          `json:"p99Ms"`
	MaxMs     float64          `json:"maxMs"`
	Reasons   map[string]int64 `json:"reasons"`
}

// Submit sends one command and waits for the result.
type Submit func(ctx context.Context, cmd ledger.Command) ledger.Result

// Run drives load until the duration elapses or ctx is cancelled. progress,
// if not nil, is updated as the run goes so a UI can show it live.
func Run(ctx context.Context, cfg Config, submit Submit, progress *atomic.Pointer[Report]) Report {
	ctx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()
	start := time.Now()
	run := uuid.NewString()[:8]
	var committed, rejected, errs atomic.Int64
	var mu sync.Mutex
	reasons := map[string]int64{}
	lats := make([][]int32, cfg.Workers)

	snapshot := func(running bool) Report {
		el := time.Since(start)
		mu.Lock()
		r := Report{
			Running: running, Config: cfg, StartedAt: start, ElapsedMs: float64(el.Milliseconds()),
			Committed: committed.Load(), Rejected: rejected.Load(), Errors: errs.Load(), Reasons: map[string]int64{},
		}
		for k, v := range reasons {
			r.Reasons[k] = v
		}
		mu.Unlock()
		r.TPS = float64(r.Committed) / el.Seconds()
		return r
	}

	var wg sync.WaitGroup
	for w := range cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(start.UnixNano()), uint64(w)))
			pick := func() string {
				if rng.Float64() < cfg.HotShare {
					return cfg.Wallets[rng.IntN(min(8, len(cfg.Wallets)))]
				}
				return cfg.Wallets[rng.IntN(len(cfg.Wallets))]
			}
			do := func(n int, cmd ledger.Command) ledger.Result {
				cmd.IdempotencyKey = fmt.Sprintf("load-%s-%d-%d", run, w, n)
				t := time.Now()
				res := submit(ctx, cmd)
				if ctx.Err() != nil {
					return res
				}
				lats[w] = append(lats[w], int32(time.Since(t).Microseconds()))
				switch code := ledger.CodeOf(res.Err); {
				case res.Err == nil:
					committed.Add(1)
				case code == ledger.ErrUnavailable || code == "":
					errs.Add(1)
				default:
					rejected.Add(1)
					mu.Lock()
					reasons[string(code)]++
					mu.Unlock()
				}
				return res
			}
			for n := 0; ctx.Err() == nil; n++ {
				from, to := pick(), pick()
				for to == from {
					to = pick()
				}
				amount := 100 + rng.Int64N(5000)
				fee := amount / 100
				switch p := rng.Float64(); {
				case p < 0.15: // top-up from the bank
					do(n, ledger.Command{Op: ledger.OpCreate, Description: "depósito", Postings: []ledger.Posting{
						{Account: cfg.Funding, Direction: ledger.Debit, Amount: amount * 4},
						{Account: to, Direction: ledger.Credit, Amount: amount * 4},
					}})
				case p < 0.15+cfg.HoldShare: // reserve, then capture or release
					res := do(n, ledger.Command{Op: ledger.OpCreate, Pending: true, ExpiresIn: time.Hour, Description: "reserva", Postings: []ledger.Posting{
						{Account: from, Direction: ledger.Debit, Amount: amount},
						{Account: to, Direction: ledger.Credit, Amount: amount},
					}})
					if res.Err == nil && ctx.Err() == nil {
						n++
						op := ledger.OpPost
						if rng.IntN(5) == 0 {
							op = ledger.OpVoid
						}
						do(n, ledger.Command{Op: op, Target: res.Tx.ID})
					}
				default: // payment with a 1% fee: three legs
					do(n, ledger.Command{Op: ledger.OpCreate, Description: "pagamento", Postings: []ledger.Posting{
						{Account: from, Direction: ledger.Debit, Amount: amount + fee},
						{Account: to, Direction: ledger.Credit, Amount: amount},
						{Account: cfg.Fees, Direction: ledger.Credit, Amount: fee},
					}})
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for running := true; running; {
		select {
		case <-done:
			running = false
		case <-tick.C:
			if progress != nil {
				r := snapshot(true)
				progress.Store(&r)
			}
		}
	}

	r := snapshot(false)
	var all []int32
	for _, l := range lats {
		all = append(all, l...)
	}
	if n := len(all); n > 0 {
		slices.Sort(all)
		r.P50Ms = float64(all[n/2]) / 1000
		r.P99Ms = float64(all[min(n-1, n*99/100)]) / 1000
		r.MaxMs = float64(all[n-1]) / 1000
	}
	if progress != nil {
		progress.Store(&r)
	}
	return r
}
