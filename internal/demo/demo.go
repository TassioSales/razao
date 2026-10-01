// Package demo builds a small fintech chart of accounts: a bank account
// holding the real money, one liability wallet per customer, and a revenue
// account for fees.
package demo

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"razao/internal/ledger"
	"razao/internal/loadgen"
	"razao/internal/store"
)

const (
	Bank    = "ativo:banco:conta-movimento"
	Fees    = "receita:tarifas"
	Capital = "patrimonio:capital-social"
)

// Wallet is the code of the nth customer wallet.
func Wallet(n int) string { return fmt.Sprintf("passivo:carteira:%05d", n) }

var names = []string{"Ana", "Bruno", "Carla", "Diego", "Elisa", "Fábio", "Gabi", "Heitor", "Iara", "João",
	"Karen", "Lucas", "Marina", "Nuno", "Olívia", "Pedro", "Quésia", "Rafael", "Sofia", "Tiago"}

// Seed creates the chart (if it is not there yet) and gives every new wallet
// an opening balance. It returns the wallet codes.
func Seed(ctx context.Context, st *store.Store, submit loadgen.Submit, wallets int) ([]string, error) {
	codes := make([]string, wallets)
	for i := range codes {
		codes[i] = Wallet(i + 1)
	}
	var create []*ledger.Account
	missing := func(code string) bool {
		_, err := st.Account(ctx, code)
		return ledger.CodeOf(err) == ledger.ErrAccountNotFound
	}
	if missing(Bank) {
		create = append(create,
			&ledger.Account{Code: Bank, Name: "Banco: conta movimento", Type: ledger.Asset, Currency: "BRL", AllowNegative: true},
			&ledger.Account{Code: Fees, Name: "Receita de tarifas", Type: ledger.Revenue, Currency: "BRL", AllowNegative: true},
			&ledger.Account{Code: Capital, Name: "Capital social", Type: ledger.Equity, Currency: "BRL", AllowNegative: true},
		)
	}
	// Wallets are created in order, so the first missing one marks the start.
	first := wallets
	for lo, hi := 0, wallets; lo < hi; {
		mid := (lo + hi) / 2
		if missing(codes[mid]) {
			first, hi = mid, mid
		} else {
			lo = mid + 1
		}
	}
	for i := first; i < wallets; i++ {
		create = append(create, &ledger.Account{
			Code: codes[i], Name: fmt.Sprintf("Carteira de %s #%d", names[i%len(names)], i+1),
			Type: ledger.Liability, Currency: "BRL", AllowNegative: false,
		})
	}
	if len(create) == 0 {
		return codes, nil
	}
	if err := st.CreateAccounts(ctx, create); err != nil {
		return nil, err
	}
	// Fund the new wallets concurrently so the deposits share commits.
	var wg sync.WaitGroup
	var firstErr atomic.Pointer[error]
	next := atomic.Int64{}
	next.Store(int64(first))
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for firstErr.Load() == nil {
				i := int(next.Add(1) - 1)
				if i >= wallets {
					return
				}
				res := submit(ctx, ledger.Command{
					Op: ledger.OpCreate, IdempotencyKey: "seed-" + codes[i], Description: "saldo inicial",
					Postings: []ledger.Posting{
						{Account: Bank, Direction: ledger.Debit, Amount: 100_000},
						{Account: codes[i], Direction: ledger.Credit, Amount: 100_000},
					},
				})
				if res.Err != nil {
					firstErr.CompareAndSwap(nil, &res.Err)
				}
			}
		}()
	}
	wg.Wait()
	if err := firstErr.Load(); err != nil {
		return nil, *err
	}
	return codes, nil
}
