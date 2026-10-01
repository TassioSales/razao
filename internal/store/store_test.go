package store_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"razao/internal/ledger"
	"razao/internal/pipeline"
	"razao/internal/store"
	"razao/internal/testdb"
)

var ctx = context.Background()

func seed(t *testing.T, st *store.Store, wallets int) []string {
	t.Helper()
	list := []*ledger.Account{
		{Code: "bank", Name: "Banco", Type: ledger.Asset, Currency: "BRL", AllowNegative: true},
		{Code: "fees", Name: "Tarifas", Type: ledger.Revenue, Currency: "BRL", AllowNegative: true},
	}
	var codes []string
	for i := range wallets {
		code := fmt.Sprintf("w:%03d", i)
		codes = append(codes, code)
		list = append(list, &ledger.Account{Code: code, Name: code, Type: ledger.Liability, Currency: "BRL"})
	}
	if err := st.CreateAccounts(ctx, list); err != nil {
		t.Fatal(err)
	}
	return codes
}

func transfer(key, from, to string, amount int64) ledger.Command {
	return ledger.Command{Op: ledger.OpCreate, IdempotencyKey: key, Postings: []ledger.Posting{
		{Account: from, Direction: ledger.Debit, Amount: amount},
		{Account: to, Direction: ledger.Credit, Amount: amount},
	}}
}

func exec(t *testing.T, st *store.Store, cmds ...ledger.Command) []ledger.Result {
	t.Helper()
	res, err := st.ExecBatch(ctx, cmds)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func mustClean(t *testing.T, st *store.Store) {
	t.Helper()
	chain, err := st.VerifyChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !chain.OK {
		t.Fatalf("chain broken at %d: %s", chain.BrokenAt, chain.Reason)
	}
	integ, err := st.CheckIntegrity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !integ.OK {
		t.Fatalf("integrity failed: %+v", integ)
	}
}

func TestJournalRoundTrip(t *testing.T) {
	st := testdb.Open(t, "store")
	seed(t, st, 2)

	res := exec(t, st,
		transfer("k1", "bank", "w:000", 1000),
		transfer("k2", "w:000", "w:001", 300),
		transfer("k3", "w:001", "w:000", 5000), // insufficient funds
		transfer("k1", "bank", "w:000", 1000),  // replay inside the batch
	)
	if res[0].Err != nil || res[1].Err != nil {
		t.Fatal(res[0].Err, res[1].Err)
	}
	if ledger.CodeOf(res[2].Err) != ledger.ErrInsufficientFunds {
		t.Fatalf("got %v, want insufficient funds", res[2].Err)
	}
	if !res[3].Replayed {
		t.Fatal("same key in the batch should replay")
	}

	// A replay in a later batch reads the transaction back from Postgres.
	again := exec(t, st, transfer("k2", "w:000", "w:001", 300))[0]
	if !again.Replayed || again.Tx.ID != res[1].Tx.ID || len(again.Tx.Entries) != 2 || again.Tx.Entries[0].AccountCode != "w:000" {
		t.Fatalf("replay from the database is wrong: %+v", again.Tx)
	}
	if again.Tx.Hash != res[1].Tx.Hash {
		t.Fatal("stored hash differs from the one computed in memory")
	}
	conflict := exec(t, st, transfer("k2", "w:000", "w:001", 301))[0]
	if ledger.CodeOf(conflict.Err) != ledger.ErrIdempotency {
		t.Fatalf("got %v, want idempotency conflict", conflict.Err)
	}

	a, err := st.Account(ctx, "w:000")
	if err != nil {
		t.Fatal(err)
	}
	if a.Posted != 700 || a.Balance.Entries != 2 {
		t.Fatalf("w:000 = %+v", a)
	}
	lines, err := st.Statement(ctx, a.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || *lines[0].BalanceAfter != 700 || *lines[1].BalanceAfter != 1000 {
		t.Fatalf("statement = %+v", lines)
	}
	mustClean(t, st)
}

func TestTwoPhaseAndReversalAcrossBatches(t *testing.T) {
	st := testdb.Open(t, "store")
	seed(t, st, 2)
	exec(t, st, transfer("dep", "bank", "w:000", 1000))

	hold := transfer("hold", "w:000", "w:001", 400)
	hold.Pending = true
	h := exec(t, st, hold)[0]
	if h.Err != nil {
		t.Fatal(h.Err)
	}
	a, _ := st.Account(ctx, "w:000")
	if a.Posted != 1000 || a.Available != 600 {
		t.Fatalf("after hold: posted %d available %d", a.Posted, a.Available)
	}

	res := exec(t, st,
		ledger.Command{Op: ledger.OpPost, IdempotencyKey: "post", Target: h.Tx.ID},
		ledger.Command{Op: ledger.OpVoid, IdempotencyKey: "void", Target: h.Tx.ID},
	)
	if res[0].Err != nil || ledger.CodeOf(res[1].Err) != ledger.ErrAlreadyResolved {
		t.Fatalf("post/void in one batch: %v / %v", res[0].Err, res[1].Err)
	}
	// And the database remembers it in the next batch.
	if r := exec(t, st, ledger.Command{Op: ledger.OpVoid, IdempotencyKey: "void2", Target: h.Tx.ID})[0]; ledger.CodeOf(r.Err) != ledger.ErrAlreadyResolved {
		t.Fatalf("got %v, want already resolved", r.Err)
	}
	got, err := st.Transaction(ctx, h.Tx.ID)
	if err != nil || got.Resolution == nil || *got.Resolution != ledger.KindPost {
		t.Fatalf("hold should read back as posted: %+v %v", got, err)
	}

	pay := exec(t, st, transfer("pay", "w:001", "w:000", 100))[0]
	rev := exec(t, st, ledger.Command{Op: ledger.OpReverse, IdempotencyKey: "rev", Target: pay.Tx.ID})[0]
	if rev.Err != nil || !rev.Tx.Reversal {
		t.Fatal("reversal failed:", rev.Err)
	}
	if r := exec(t, st, ledger.Command{Op: ledger.OpReverse, IdempotencyKey: "rev2", Target: pay.Tx.ID})[0]; ledger.CodeOf(r.Err) != ledger.ErrAlreadyReversed {
		t.Fatalf("got %v, want already reversed", r.Err)
	}
	b, _ := st.Account(ctx, "w:001")
	if b.Posted != 400 || b.Balance.CreditsPending != 0 {
		t.Fatalf("w:001 = %+v", b)
	}
	mustClean(t, st)
}

func TestTamperingIsDetected(t *testing.T) {
	st := testdb.Open(t, "store")
	seed(t, st, 2)
	for i := range 30 {
		exec(t, st, transfer(fmt.Sprint("k", i), "bank", "w:000", int64(100+i)))
	}
	mustClean(t, st)

	// Ordinary SQL can't change the journal.
	err := st.Tamper(ctx, store.Tampering{Seq: 999, Idx: 0, Delta: 1})
	if ledger.CodeOf(err) != ledger.ErrNotFound {
		t.Fatalf("tampering a missing entry: %v", err)
	}
	if err := st.RawExec(ctx, "UPDATE entries SET amount = amount + 1 WHERE tx_seq = 5"); err == nil || !strings.Contains(err.Error(), "imutável") {
		t.Fatalf("trigger should block updates, got %v", err)
	}
	if err := st.RawExec(ctx, "DELETE FROM transactions WHERE seq = 5"); err == nil {
		t.Fatal("trigger should block deletes")
	}

	// Someone who can disable the trigger still can't hide it.
	bad := store.Tampering{Seq: 17, Idx: 1, Delta: 50_000}
	if err := st.Tamper(ctx, bad); err != nil {
		t.Fatal(err)
	}
	chain, err := st.VerifyChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if chain.OK || chain.BrokenAt != 17 {
		t.Fatalf("chain report %+v, want broken at 17", chain)
	}
	integ, _ := st.CheckIntegrity(ctx)
	if integ.OK || len(integ.Unbalanced) != 1 || integ.Unbalanced[0] != 17 || len(integ.Drift) != 1 {
		t.Fatalf("integrity should flag tx 17 and one account: %+v", integ)
	}

	bad.Delta = -bad.Delta
	if err := st.Tamper(ctx, bad); err != nil {
		t.Fatal(err)
	}
	mustClean(t, st)
}

// TestConcurrentWriters runs two independent writers (as two server
// processes would) against the same database with heavy contention.
func TestConcurrentWriters(t *testing.T) {
	st := testdb.Open(t, "store")
	codes := seed(t, st, 12)
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	writers := []*pipeline.Pipeline{pipeline.New(st, 200), pipeline.New(st, 200)}
	for _, p := range writers {
		go p.Run(pctx)
	}
	for i, c := range codes {
		if r := writers[0].Submit(ctx, transfer(fmt.Sprint("dep", i), "bank", c, 10_000)); r.Err != nil {
			t.Fatal(r.Err)
		}
	}

	const workers, perWorker = 48, 120
	var wg sync.WaitGroup
	var mu sync.Mutex
	outcomes := map[ledger.Code]int{}
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w), 7))
			p := writers[w%2]
			for i := range perWorker {
				from, to := codes[rng.IntN(len(codes))], codes[rng.IntN(len(codes))]
				cmd := transfer(fmt.Sprintf("t-%d-%d", w, i), from, to, 1+rng.Int64N(6000))
				if rng.IntN(4) == 0 {
					cmd.Pending = true
				}
				res := p.Submit(ctx, cmd)
				// Retry with the same key through the other writer: must replay, never double-apply.
				if res.Err == nil && rng.IntN(5) == 0 {
					again := writers[(w+1)%2].Submit(ctx, cmd)
					if again.Err != nil || !again.Replayed || again.Tx.ID != res.Tx.ID {
						t.Errorf("retry through the other writer: %+v", again)
					}
				}
				if res.Err == nil && cmd.Pending {
					op := ledger.OpPost
					if rng.IntN(2) == 0 {
						op = ledger.OpVoid
					}
					res = p.Submit(ctx, ledger.Command{Op: op, IdempotencyKey: fmt.Sprintf("r-%d-%d", w, i), Target: res.Tx.ID})
				}
				mu.Lock()
				outcomes[ledger.CodeOf(res.Err)]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if outcomes[""] < workers*perWorker/4 || outcomes[ledger.ErrInsufficientFunds] == 0 {
		t.Fatalf("workload didn't exercise both paths: %v", outcomes)
	}
	for code := range outcomes {
		if code != "" && code != ledger.ErrInsufficientFunds {
			t.Fatalf("unexpected failures: %v", outcomes)
		}
	}
	mustClean(t, st)
	accounts, err := st.Accounts(ctx, "w:", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, a := range accounts {
		if a.Available < 0 || a.Balance.DebitsPending != 0 || a.Balance.CreditsPending != 0 {
			t.Fatalf("%s ended overdrawn or with money stuck in pending: %+v", a.Code, a.Balance)
		}
		total += a.Posted
	}
	if total != int64(len(codes))*10_000 {
		t.Fatalf("money was created or destroyed: wallets hold %d, want %d", total, len(codes)*10_000)
	}
	sum, _ := st.Summary(ctx)
	t.Logf("%d transactions, outcomes %v", sum.Seq, outcomes)
}
