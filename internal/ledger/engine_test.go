package ledger

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// world is a ledger kept entirely in memory.
type world struct {
	st   *State
	accs *MemAccounts
	all  []*Transaction
	n    int
}

func newWorld() *world {
	accs := NewMemAccounts()
	w := &world{accs: accs, st: &State{
		Now: t0, Accounts: accs, Balances: map[uuid.UUID]*Balance{},
		ByKey: map[string]*Transaction{}, Related: map[uuid.UUID]*Transaction{},
	}}
	w.account("bank", Asset, "BRL", true)
	w.account("alice", Liability, "BRL", false)
	w.account("bob", Liability, "BRL", false)
	w.account("fees", Revenue, "BRL", true)
	w.account("usd", Asset, "USD", true)
	return w
}

func (w *world) account(code string, t AccountType, cur string, neg bool) *Account {
	a := NewAccount(code, code, t, cur, neg, t0)
	w.accs.Put(a)
	w.st.Balances[a.ID] = &Balance{}
	return a
}

func (w *world) key() string {
	w.n++
	return fmt.Sprintf("k%d", w.n)
}

func (w *world) do(c Command) Result {
	if c.IdempotencyKey == "" {
		c.IdempotencyKey = w.key()
	}
	res, created := Apply(w.st, []Command{c})
	w.all = append(w.all, created...)
	return res[0]
}

func (w *world) bal(code string) Balance {
	a, _ := w.accs.Resolve(code)
	return *w.st.Balances[a.ID]
}

func transfer(from, to string, amount int64) Command {
	return Command{Op: OpCreate, Postings: []Posting{{from, Debit, amount}, {to, Credit, amount}}}
}

func hold(from, to string, amount int64) Command {
	c := transfer(from, to, amount)
	c.Pending = true
	return c
}

func wantCode(t *testing.T, r Result, code Code) {
	t.Helper()
	if CodeOf(r.Err) != code {
		t.Fatalf("got error %v, want code %q", r.Err, code)
	}
}

func TestDoubleEntry(t *testing.T) {
	w := newWorld()
	if r := w.do(transfer("bank", "alice", 1000)); r.Err != nil {
		t.Fatal(r.Err)
	}
	if got := w.bal("alice").Posted(Credit); got != 1000 {
		t.Fatalf("alice = %d, want 1000", got)
	}
	if got := w.bal("bank").Posted(Debit); got != 1000 {
		t.Fatalf("bank = %d, want 1000", got)
	}
	wantCode(t, w.do(Command{Op: OpCreate, Postings: []Posting{{"bank", Debit, 100}, {"alice", Credit, 99}}}), ErrUnbalanced)
	wantCode(t, w.do(Command{Op: OpCreate, Postings: []Posting{{"bank", Debit, 100}}}), ErrInvalid)
	wantCode(t, w.do(transfer("bank", "nobody", 1)), ErrAccountNotFound)
	wantCode(t, w.do(transfer("bank", "alice", 0)), ErrInvalid)
	// Different currencies must balance separately.
	wantCode(t, w.do(transfer("usd", "alice", 10)), ErrUnbalanced)
}

func TestMultiLegWithFee(t *testing.T) {
	w := newWorld()
	w.do(transfer("bank", "alice", 1000))
	r := w.do(Command{Op: OpCreate, Postings: []Posting{
		{"alice", Debit, 500}, {"bob", Credit, 490}, {"fees", Credit, 10},
	}})
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	if w.bal("alice").Posted(Credit) != 500 || w.bal("bob").Posted(Credit) != 490 || w.bal("fees").Posted(Credit) != 10 {
		t.Fatal("wrong balances after split")
	}
	if got := *r.Tx.Entries[0].BalanceAfter; got != 500 {
		t.Fatalf("balanceAfter = %d, want 500", got)
	}
}

func TestOverdraftRule(t *testing.T) {
	w := newWorld()
	w.do(transfer("bank", "alice", 100))
	wantCode(t, w.do(transfer("alice", "bob", 101)), ErrInsufficientFunds)
	if w.bal("alice").Posted(Credit) != 100 || w.bal("bob").Entries != 0 {
		t.Fatal("failed transfer left side effects")
	}
	if r := w.do(transfer("alice", "bob", 100)); r.Err != nil {
		t.Fatal(r.Err)
	}
	// Accounts that allow it may go negative.
	if r := w.do(transfer("fees", "bank", 5000)); r.Err != nil {
		t.Fatal(r.Err)
	}
}

func TestIdempotency(t *testing.T) {
	w := newWorld()
	c := transfer("bank", "alice", 100)
	c.IdempotencyKey = "same"
	first := w.do(c)
	again := w.do(c)
	if again.Err != nil || !again.Replayed || again.Tx.ID != first.Tx.ID {
		t.Fatalf("replay failed: %+v", again)
	}
	if w.bal("alice").Posted(Credit) != 100 {
		t.Fatal("replay applied twice")
	}
	// Same accounts by ID instead of code is still the same request.
	bank, _ := w.accs.Resolve("bank")
	alice, _ := w.accs.Resolve("alice")
	byID := transfer(bank.ID.String(), alice.ID.String(), 100)
	byID.IdempotencyKey = "same"
	if r := w.do(byID); !r.Replayed {
		t.Fatal("request by ID should replay")
	}
	other := transfer("bank", "alice", 101)
	other.IdempotencyKey = "same"
	wantCode(t, w.do(other), ErrIdempotency)

	// Two commands with the same key inside one batch.
	c.IdempotencyKey = "batch"
	res, created := Apply(w.st, []Command{c, c})
	if len(created) != 1 || !res[1].Replayed {
		t.Fatal("duplicate inside a batch must replay")
	}
}

func TestTwoPhase(t *testing.T) {
	w := newWorld()
	w.do(transfer("bank", "alice", 100))
	h := w.do(hold("alice", "bob", 60))
	if h.Err != nil {
		t.Fatal(h.Err)
	}
	if b := w.bal("alice"); b.Posted(Credit) != 100 || b.Available(Credit) != 40 {
		t.Fatalf("after hold: posted %d available %d", b.Posted(Credit), b.Available(Credit))
	}
	// The reserved money is not available to spend again.
	wantCode(t, w.do(transfer("alice", "bob", 41)), ErrInsufficientFunds)
	// Bob has nothing settled yet.
	wantCode(t, w.do(transfer("bob", "alice", 1)), ErrInsufficientFunds)

	if r := w.do(Command{Op: OpPost, Target: h.Tx.ID}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if a, b := w.bal("alice"), w.bal("bob"); a.Posted(Credit) != 40 || a.DebitsPending != 0 || b.Posted(Credit) != 60 || b.CreditsPending != 0 {
		t.Fatalf("after post: alice %+v bob %+v", a, b)
	}
	wantCode(t, w.do(Command{Op: OpVoid, Target: h.Tx.ID}), ErrAlreadyResolved)
	wantCode(t, w.do(Command{Op: OpPost, Target: uuid.New()}), ErrNotFound)

	h2 := w.do(hold("alice", "bob", 40))
	if r := w.do(Command{Op: OpVoid, Target: h2.Tx.ID}); r.Err != nil {
		t.Fatal(r.Err)
	}
	if a := w.bal("alice"); a.Available(Credit) != 40 || a.DebitsPending != 0 {
		t.Fatalf("void did not release: %+v", a)
	}
	posted := w.do(transfer("bank", "alice", 1))
	wantCode(t, w.do(Command{Op: OpPost, Target: posted.Tx.ID}), ErrNotPending)
}

func TestPendingExpiry(t *testing.T) {
	w := newWorld()
	w.do(transfer("bank", "alice", 100))
	c := hold("alice", "bob", 50)
	c.ExpiresIn = time.Minute
	h := w.do(c)
	w.st.Now = t0.Add(2 * time.Minute)
	wantCode(t, w.do(Command{Op: OpPost, Target: h.Tx.ID}), ErrExpired)
	if r := w.do(Command{Op: OpVoid, Target: h.Tx.ID}); r.Err != nil {
		t.Fatal("an expired hold must still be voidable:", r.Err)
	}
	bad := transfer("bank", "alice", 1)
	bad.ExpiresIn = time.Minute
	wantCode(t, w.do(bad), ErrInvalid)
}

func TestReversal(t *testing.T) {
	w := newWorld()
	dep := w.do(transfer("bank", "alice", 100))
	pay := w.do(transfer("alice", "bob", 100))
	// Alice already spent it: reversing the deposit would overdraw her.
	wantCode(t, w.do(Command{Op: OpReverse, Target: dep.Tx.ID}), ErrInsufficientFunds)
	rev := w.do(Command{Op: OpReverse, Target: pay.Tx.ID})
	if rev.Err != nil {
		t.Fatal(rev.Err)
	}
	if !rev.Tx.Reversal || *rev.Tx.RelatedID != pay.Tx.ID || w.bal("alice").Posted(Credit) != 100 || w.bal("bob").Posted(Credit) != 0 {
		t.Fatal("reversal did not undo the payment")
	}
	wantCode(t, w.do(Command{Op: OpReverse, Target: pay.Tx.ID}), ErrAlreadyReversed)
	wantCode(t, w.do(Command{Op: OpReverse, Target: rev.Tx.ID}), ErrNotReversible)
}

func TestOverflow(t *testing.T) {
	w := newWorld()
	w.do(transfer("bank", "fees", math.MaxInt64-10))
	wantCode(t, w.do(transfer("bank", "fees", 11)), ErrOverflow)
	if w.bal("fees").CreditsPosted != math.MaxInt64-10 {
		t.Fatal("overflowing transfer changed the balance")
	}
}

func TestChain(t *testing.T) {
	w := newWorld()
	for i := range 20 {
		w.do(transfer("bank", "alice", int64(i+1)))
	}
	verify := func() int64 {
		var prev Hash
		for _, tx := range w.all {
			if tx.PrevHash != prev || ChainHash(prev, tx) != tx.Hash {
				return tx.Seq
			}
			prev = tx.Hash
		}
		return 0
	}
	if seq := verify(); seq != 0 {
		t.Fatalf("clean chain broken at %d", seq)
	}
	w.all[7].Entries[0].Amount++
	if seq := verify(); seq != 8 {
		t.Fatalf("tampering detected at %d, want 8", seq)
	}
	w.all[7].Entries[0].Amount--
	w.all[3].Description = "x"
	if seq := verify(); seq != 4 {
		t.Fatalf("tampering detected at %d, want 4", seq)
	}
}

// TestRandomWorkload throws random commands, valid and invalid, at the
// engine and checks the accounting invariants after every batch.
func TestRandomWorkload(t *testing.T) {
	for seed := uint64(1); seed <= 30; seed++ {
		rng := rand.New(rand.NewPCG(seed, 99))
		w := newWorld()
		codes := []string{"bank", "alice", "bob", "fees"}
		for i := range 20 {
			code := fmt.Sprintf("w%d", i)
			w.account(code, Liability, "BRL", false)
			codes = append(codes, code)
		}
		pick := func() string { return codes[rng.IntN(len(codes))] }
		var holds, posted []uuid.UUID

		for batch := range 60 {
			w.st.Now = t0.Add(time.Duration(batch) * time.Second)
			var cmds []Command
			for range 1 + rng.IntN(40) {
				var c Command
				switch p := rng.IntN(100); {
				case p < 20:
					c = transfer("bank", pick(), 1+rng.Int64N(2000)) // fresh money
				case p < 55:
					c = transfer(pick(), pick(), 1+rng.Int64N(500))
					if rng.IntN(3) == 0 {
						c.Pending = true
						if rng.IntN(2) == 0 {
							c.ExpiresIn = time.Duration(1+rng.IntN(20)) * time.Second
						}
					}
				case p < 70 && len(holds) > 0:
					c = Command{Op: OpPost, Target: holds[rng.IntN(len(holds))]}
				case p < 80 && len(holds) > 0:
					c = Command{Op: OpVoid, Target: holds[rng.IntN(len(holds))]}
				case p < 90 && len(posted) > 0:
					c = Command{Op: OpReverse, Target: posted[rng.IntN(len(posted))]}
				default: // unbalanced
					c = Command{Op: OpCreate, Postings: []Posting{{pick(), Debit, 10}, {pick(), Credit, 9}}}
				}
				c.IdempotencyKey = w.key()
				if rng.IntN(20) == 0 && w.n > 3 {
					c.IdempotencyKey = fmt.Sprintf("k%d", 1+rng.IntN(w.n-1)) // reuse a key
				}
				cmds = append(cmds, c)
			}

			before := snapshot(w.st)
			res, created := Apply(w.st, cmds)
			w.all = append(w.all, created...)
			for _, tx := range created {
				switch tx.Kind {
				case KindPending:
					holds = append(holds, tx.ID)
				case KindPosted:
					posted = append(posted, tx.ID)
				}
			}
			if len(created) == 0 {
				for id, b := range before {
					if *w.st.Balances[id] != b {
						t.Fatalf("seed %d: batch with only failures changed a balance", seed)
					}
				}
			}
			for i, r := range res {
				if r.Err == nil && r.Tx == nil {
					t.Fatalf("seed %d: result %d has neither tx nor error", seed, i)
				}
			}
			checkInvariants(t, seed, w)
		}
		if len(w.all) < 200 {
			t.Fatalf("seed %d: only %d transactions committed, workload too weak", seed, len(w.all))
		}
	}
}

func snapshot(st *State) map[uuid.UUID]Balance {
	out := map[uuid.UUID]Balance{}
	for id, b := range st.Balances {
		out[id] = *b
	}
	return out
}

func checkInvariants(t *testing.T, seed uint64, w *world) {
	t.Helper()
	// 1. Balances equal the journal replayed from scratch.
	replay := map[uuid.UUID]*Balance{}
	for id := range w.st.Balances {
		replay[id] = &Balance{}
	}
	var prev Hash
	for i, tx := range w.all {
		if tx.Seq != int64(i+1) {
			t.Fatalf("seed %d: sequence gap at %d", seed, tx.Seq)
		}
		if tx.PrevHash != prev || ChainHash(prev, tx) != tx.Hash {
			t.Fatalf("seed %d: chain broken at %d", seed, tx.Seq)
		}
		prev = tx.Hash
		var d, c int64
		for _, e := range tx.Entries {
			b := replay[e.AccountID]
			posted, pending := &b.DebitsPosted, &b.DebitsPending
			if e.Direction == Credit {
				posted, pending = &b.CreditsPosted, &b.CreditsPending
				c += e.Amount
			} else {
				d += e.Amount
			}
			switch tx.Kind {
			case KindPosted:
				*posted += e.Amount
			case KindPending:
				*pending += e.Amount
			case KindPost:
				*pending -= e.Amount
				*posted += e.Amount
			case KindVoid:
				*pending -= e.Amount
			}
			b.Entries++
			b.UpdatedSeq = tx.Seq
		}
		// 2. Every transaction balances.
		if d != c {
			t.Fatalf("seed %d: tx %d debits %d != credits %d", seed, tx.Seq, d, c)
		}
	}
	var debits, credits, pd, pc int64
	for id, b := range w.st.Balances {
		if *replay[id] != *b {
			t.Fatalf("seed %d: balance of %s is %+v, journal says %+v", seed, id, *b, *replay[id])
		}
		debits, credits = debits+b.DebitsPosted, credits+b.CreditsPosted
		pd, pc = pd+b.DebitsPending, pc+b.CreditsPending
		// 3. Nothing pending is negative and no protected account is overdrawn.
		if b.DebitsPending < 0 || b.CreditsPending < 0 {
			t.Fatalf("seed %d: negative pending on %s", seed, id)
		}
		a, _ := w.accs.ByID(id)
		if !a.AllowNegative && b.Available(a.Type.Normal()) < 0 {
			t.Fatalf("seed %d: %s overdrawn: %+v", seed, a.Code, *b)
		}
	}
	// 4. The trial balance closes.
	if debits != credits || pd != pc {
		t.Fatalf("seed %d: trial balance off: posted %d/%d pending %d/%d", seed, debits, credits, pd, pc)
	}
}
