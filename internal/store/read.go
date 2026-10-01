package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"razao/internal/ledger"
)

// AccountView is an account with its balance.
type AccountView struct {
	*ledger.Account
	Balance   ledger.Balance `json:"balance"`
	Posted    int64          `json:"posted"`
	Available int64          `json:"available"`
	Normal    string         `json:"normal"`
}

const balanceCols = "b.debits_posted, b.credits_posted, b.debits_pending, b.credits_pending, b.entries, b.updated_seq"

func scanAccountView(row pgx.Row) (*AccountView, error) {
	v := &AccountView{}
	b := &v.Balance
	a, err := scanAccount(row, &b.DebitsPosted, &b.CreditsPosted, &b.DebitsPending, &b.CreditsPending, &b.Entries, &b.UpdatedSeq)
	if err != nil {
		return nil, err
	}
	v.Account = a
	n := a.Type.Normal()
	v.Posted, v.Available, v.Normal = b.Posted(n), b.Available(n), string(n)
	return v, nil
}

// Account returns one account by ID or code.
func (s *Store) Account(ctx context.Context, ref string) (*AccountView, error) {
	var id pgtype.UUID
	if u, err := uuid.Parse(ref); err == nil {
		id = pgUUID(u)
	}
	v, err := scanAccountView(s.pool.QueryRow(ctx, `SELECT `+accountCols+`, `+balanceCols+`
		FROM accounts a JOIN balances b ON b.account_id = a.id WHERE a.id = $1 OR a.code = $2`, id, ref))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ledger.Errorf(ledger.ErrAccountNotFound, "conta %q não existe", ref)
	}
	return v, err
}

// Accounts lists accounts ordered by code. after is the last code of the
// previous page; search matches code or name.
func (s *Store) Accounts(ctx context.Context, search, after string, limit int) ([]*AccountView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+accountCols+`, `+balanceCols+`
		FROM accounts a JOIN balances b ON b.account_id = a.id
		WHERE a.code > $1
		  AND ($2 = '' OR position(lower($2) in lower(a.code)) > 0 OR position(lower($2) in lower(a.name)) > 0)
		ORDER BY a.code LIMIT $3`, after, search, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AccountView{}
	for rows.Next() {
		v, err := scanAccountView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// StatementLine is one entry of an account statement.
type StatementLine struct {
	Seq          int64            `json:"seq"`
	Idx          int              `json:"idx"`
	TxID         uuid.UUID        `json:"txId"`
	Kind         ledger.Kind      `json:"kind"`
	Reversal     bool             `json:"reversal"`
	Description  string           `json:"description"`
	Direction    ledger.Direction `json:"direction"`
	Amount       int64            `json:"amount"`
	BalanceAfter *int64           `json:"balanceAfter,omitempty"`
	CreatedAt    time.Time        `json:"createdAt"`
}

// Statement lists an account's entries, newest first. before is the seq of
// the last line of the previous page (0 for the first page).
func (s *Store) Statement(ctx context.Context, account uuid.UUID, before int64, limit int) ([]StatementLine, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := s.pool.Query(ctx, `SELECT e.tx_seq, e.idx, t.id, t.kind, t.reversal, t.description, e.direction, e.amount, e.balance_after, t.created_at
		FROM entries e JOIN transactions t ON t.seq = e.tx_seq
		WHERE e.account_id = $1 AND e.tx_seq < $2
		ORDER BY e.tx_seq DESC, e.idx DESC LIMIT $3`, pgUUID(account), before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatementLine{}
	for rows.Next() {
		var l StatementLine
		var id pgtype.UUID
		if err := rows.Scan(&l.Seq, &l.Idx, &id, &l.Kind, &l.Reversal, &l.Description, &l.Direction, &l.Amount, &l.BalanceAfter, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.TxID = id.Bytes
		out = append(out, l)
	}
	return out, rows.Err()
}

// Transaction returns one transaction by ID.
func (s *Store) Transaction(ctx context.Context, id uuid.UUID) (*ledger.Transaction, error) {
	txs, err := s.loadTxs(ctx, s.pool, "id = $1", pgUUID(id))
	if err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return nil, ledger.Errorf(ledger.ErrNotFound, "transação %s não existe", id)
	}
	return txs[0], nil
}

// Transactions lists the journal, newest first. before is the seq of the
// last row of the previous page; kind filters when non-empty.
func (s *Store) Transactions(ctx context.Context, before int64, kind string, limit int) ([]*ledger.Transaction, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	txs, err := s.loadTxs(ctx, s.pool, "seq < $1 AND ($2 = '' OR kind = $2) ORDER BY seq DESC LIMIT $3", before, kind, limit)
	if txs == nil {
		txs = []*ledger.Transaction{}
	}
	return txs, err
}

// Since lists transactions after a sequence number, oldest first: a durable,
// totally ordered feed that consumers can poll with their last seen seq.
func (s *Store) Since(ctx context.Context, after int64, limit int) ([]*ledger.Transaction, error) {
	txs, err := s.loadTxs(ctx, s.pool, "seq > $1 ORDER BY seq LIMIT $2", after, limit)
	if txs == nil {
		txs = []*ledger.Transaction{}
	}
	return txs, err
}

// CurrencyTotals is the trial balance for one currency.
type CurrencyTotals struct {
	Currency       string `json:"currency"`
	DebitsPosted   int64  `json:"debitsPosted"`
	CreditsPosted  int64  `json:"creditsPosted"`
	DebitsPending  int64  `json:"debitsPending"`
	CreditsPending int64  `json:"creditsPending"`
	Accounts       int64  `json:"accounts"`
}

// Summary is a snapshot of the ledger's size and totals.
type Summary struct {
	Seq          int64            `json:"seq"`
	Head         ledger.Hash      `json:"head"`
	Accounts     int64            `json:"accounts"`
	Totals       []CurrencyTotals `json:"totals"`
	ByType       map[string]int64 `json:"byType"`
	TrialBalance bool             `json:"trialBalance"`
}

// Summary reads the head and the trial balance in one snapshot.
func (s *Store) Summary(ctx context.Context) (*Summary, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	out := &Summary{Totals: []CurrencyTotals{}, ByType: map[string]int64{}, TrialBalance: true}
	var head []byte
	if err := tx.QueryRow(ctx, "SELECT seq, hash FROM ledger_head").Scan(&out.Seq, &head); err != nil {
		return nil, err
	}
	out.Head = ledger.HashFromBytes(head)
	rows, err := tx.Query(ctx, `SELECT a.currency, sum(b.debits_posted)::bigint, sum(b.credits_posted)::bigint,
			sum(b.debits_pending)::bigint, sum(b.credits_pending)::bigint, count(*)
		FROM balances b JOIN accounts a ON a.id = b.account_id GROUP BY a.currency ORDER BY a.currency`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t CurrencyTotals
		if err := rows.Scan(&t.Currency, &t.DebitsPosted, &t.CreditsPosted, &t.DebitsPending, &t.CreditsPending, &t.Accounts); err != nil {
			rows.Close()
			return nil, err
		}
		out.Accounts += t.Accounts
		if t.DebitsPosted != t.CreditsPosted || t.DebitsPending != t.CreditsPending {
			out.TrialBalance = false
		}
		out.Totals = append(out.Totals, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, "SELECT type, count(*) FROM accounts GROUP BY type")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var n int64
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out.ByType[t] = n
	}
	return out, rows.Err()
}
