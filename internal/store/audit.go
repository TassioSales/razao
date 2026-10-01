package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"razao/internal/ledger"
)

// ChainReport is the result of re-hashing the whole journal.
type ChainReport struct {
	OK        bool        `json:"ok"`
	Checked   int64       `json:"checked"`
	HeadSeq   int64       `json:"headSeq"`
	Head      ledger.Hash `json:"head"`
	BrokenAt  int64       `json:"brokenAt,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	ElapsedMs float64     `json:"elapsedMs"`
}

// VerifyChain recomputes every hash from the first transaction to the head,
// in one consistent snapshot. Any change to a stored amount, account,
// description or ordering breaks the chain at the first affected row.
func (s *Store) VerifyChain(ctx context.Context) (*ChainReport, error) {
	start := time.Now()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rep := &ChainReport{OK: true}
	var head []byte
	if err := tx.QueryRow(ctx, "SELECT seq, hash FROM ledger_head").Scan(&rep.HeadSeq, &head); err != nil {
		return nil, err
	}
	rep.Head = ledger.HashFromBytes(head)
	fail := func(seq int64, reason string) (*ChainReport, error) {
		rep.OK, rep.BrokenAt, rep.Reason = false, seq, reason
		rep.ElapsedMs = float64(time.Since(start).Microseconds()) / 1000
		return rep, nil
	}

	const page = 5000
	var prev ledger.Hash
	var last int64
	for {
		rows, err := tx.Query(ctx, "SELECT "+txCols+" FROM transactions WHERE seq > $1 ORDER BY seq LIMIT $2", last, page)
		if err != nil {
			return nil, err
		}
		var txs []*ledger.Transaction
		bySeq := map[int64]*ledger.Transaction{}
		for rows.Next() {
			t, err := scanTx(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			txs = append(txs, t)
			bySeq[t.Seq] = t
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(txs) == 0 {
			break
		}
		rows, err = tx.Query(ctx, `SELECT tx_seq, idx, account_id, direction, amount FROM entries
			WHERE tx_seq > $1 AND tx_seq <= $2 ORDER BY tx_seq, idx`, last, txs[len(txs)-1].Seq)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var seq int64
			var e ledger.Entry
			var acct pgtype.UUID
			if err := rows.Scan(&seq, &e.Idx, &acct, &e.Direction, &e.Amount); err != nil {
				rows.Close()
				return nil, err
			}
			e.AccountID = acct.Bytes
			if t, ok := bySeq[seq]; ok {
				t.Entries = append(t.Entries, e)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, t := range txs {
			if t.Seq != last+1 {
				return fail(last+1, fmt.Sprintf("a transação %d sumiu do diário", last+1))
			}
			if t.PrevHash != prev {
				return fail(t.Seq, "prev_hash não bate com o hash da transação anterior")
			}
			if ledger.ChainHash(prev, t) != t.Hash {
				return fail(t.Seq, "o conteúdo da transação não corresponde ao hash gravado")
			}
			prev, last = t.Hash, t.Seq
			rep.Checked++
		}
	}
	if last != rep.HeadSeq {
		return fail(last+1, fmt.Sprintf("a cabeça aponta para %d mas o diário termina em %d", rep.HeadSeq, last))
	}
	if prev != rep.Head {
		return fail(last, "o hash da cabeça não bate com o da última transação")
	}
	rep.ElapsedMs = float64(time.Since(start).Microseconds()) / 1000
	return rep, nil
}

// BalanceDrift is an account whose materialized balance differs from the
// journal.
type BalanceDrift struct {
	Code     string         `json:"code"`
	Stored   ledger.Balance `json:"stored"`
	Computed ledger.Balance `json:"computed"`
}

// IntegrityReport is the result of the accounting checks.
type IntegrityReport struct {
	OK           bool             `json:"ok"`
	TrialBalance bool             `json:"trialBalance"`
	Totals       []CurrencyTotals `json:"totals"`
	Drift        []BalanceDrift   `json:"drift"`
	Unbalanced   []int64          `json:"unbalanced"`
	Orphans      int64            `json:"orphans"`
	OpenPending  int64            `json:"openPending"`
	Entries      int64            `json:"entries"`
	ElapsedMs    float64          `json:"elapsedMs"`
}

// CheckIntegrity recomputes every balance from the journal and compares it
// with the materialized one, checks that every transaction balances per
// currency, and that the trial balance closes.
func (s *Store) CheckIntegrity(ctx context.Context) (*IntegrityReport, error) {
	start := time.Now()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// These are full-table joins, where a hash join is always the right plan.
	// Right after a bulk load the planner's statistics are stale and it picks
	// nested loops instead (measured: 31 s versus 0.9 s on 275k entries).
	if _, err := tx.Exec(ctx, "SET LOCAL enable_nestloop = off"); err != nil {
		return nil, err
	}
	rep := &IntegrityReport{TrialBalance: true, Totals: []CurrencyTotals{}, Drift: []BalanceDrift{}, Unbalanced: []int64{}}

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
		if t.DebitsPosted != t.CreditsPosted || t.DebitsPending != t.CreditsPending {
			rep.TrialBalance = false
		}
		rep.Totals = append(rep.Totals, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `
		WITH calc AS (
			SELECT e.account_id,
				sum(CASE WHEN e.direction = 'D' AND t.kind IN ('posted', 'post') THEN e.amount ELSE 0 END) AS dp,
				sum(CASE WHEN e.direction = 'C' AND t.kind IN ('posted', 'post') THEN e.amount ELSE 0 END) AS cp,
				sum(CASE WHEN e.direction = 'D' THEN CASE t.kind WHEN 'pending' THEN e.amount WHEN 'post' THEN -e.amount WHEN 'void' THEN -e.amount ELSE 0 END ELSE 0 END) AS dpe,
				sum(CASE WHEN e.direction = 'C' THEN CASE t.kind WHEN 'pending' THEN e.amount WHEN 'post' THEN -e.amount WHEN 'void' THEN -e.amount ELSE 0 END ELSE 0 END) AS cpe,
				count(*) AS n
			FROM entries e JOIN transactions t ON t.seq = e.tx_seq
			GROUP BY e.account_id
		)
		SELECT a.code, b.debits_posted, b.credits_posted, b.debits_pending, b.credits_pending, b.entries,
			coalesce(c.dp, 0)::bigint, coalesce(c.cp, 0)::bigint, coalesce(c.dpe, 0)::bigint, coalesce(c.cpe, 0)::bigint, coalesce(c.n, 0)::bigint
		FROM balances b JOIN accounts a ON a.id = b.account_id LEFT JOIN calc c ON c.account_id = b.account_id
		WHERE b.debits_posted <> coalesce(c.dp, 0) OR b.credits_posted <> coalesce(c.cp, 0)
		   OR b.debits_pending <> coalesce(c.dpe, 0) OR b.credits_pending <> coalesce(c.cpe, 0)
		   OR b.entries <> coalesce(c.n, 0)
		ORDER BY a.code LIMIT 20`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d BalanceDrift
		st, c := &d.Stored, &d.Computed
		if err := rows.Scan(&d.Code, &st.DebitsPosted, &st.CreditsPosted, &st.DebitsPending, &st.CreditsPending, &st.Entries,
			&c.DebitsPosted, &c.CreditsPosted, &c.DebitsPending, &c.CreditsPending, &c.Entries); err != nil {
			rows.Close()
			return nil, err
		}
		rep.Drift = append(rep.Drift, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `SELECT e.tx_seq FROM entries e JOIN accounts a ON a.id = e.account_id
		GROUP BY e.tx_seq, a.currency
		HAVING sum(CASE WHEN e.direction = 'D' THEN e.amount ELSE -e.amount END) <> 0
		ORDER BY e.tx_seq LIMIT 20`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			rows.Close()
			return nil, err
		}
		rep.Unbalanced = append(rep.Unbalanced, seq)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM transactions p WHERE p.kind = 'pending'
			AND NOT EXISTS (SELECT 1 FROM transactions r WHERE r.related_id = p.id AND r.kind IN ('post', 'void'))),
		(SELECT count(*) FROM entries),
		(SELECT count(*) FROM entries e WHERE NOT EXISTS (SELECT 1 FROM transactions t WHERE t.seq = e.tx_seq)
			OR NOT EXISTS (SELECT 1 FROM accounts a WHERE a.id = e.account_id))`).Scan(&rep.OpenPending, &rep.Entries, &rep.Orphans); err != nil {
		return nil, err
	}
	rep.OK = rep.TrialBalance && len(rep.Drift) == 0 && len(rep.Unbalanced) == 0 && rep.Orphans == 0
	rep.ElapsedMs = float64(time.Since(start).Microseconds()) / 1000
	return rep, nil
}

// Tampering describes a change made behind the ledger's back.
type Tampering struct {
	Seq   int64 `json:"seq"`
	Idx   int   `json:"idx"`
	Delta int64 `json:"delta"`
}

// Tamper edits a stored amount directly, the way someone with database
// access could, bypassing the immutability trigger. It exists to show that
// the hash chain catches it. A negative Delta undoes a previous tampering.
func (s *Store) Tamper(ctx context.Context, t Tampering) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "ALTER TABLE entries DISABLE TRIGGER entries_immutable"); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE entries SET amount = amount + $3 WHERE tx_seq = $1 AND idx = $2", t.Seq, t.Idx, t.Delta)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ledger.Errorf(ledger.ErrNotFound, "partida %d/%d não existe", t.Seq, t.Idx)
	}
	if _, err := tx.Exec(ctx, "ALTER TABLE entries ENABLE TRIGGER entries_immutable"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
