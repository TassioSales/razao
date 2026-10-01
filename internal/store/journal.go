package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"razao/internal/ledger"
)

const txCols = `seq, id, kind, related_id, reversal, idempotency_key, request_hash,
	description, metadata, expires_at, created_at, prev_hash, hash`

func scanTx(rows pgx.Rows) (*ledger.Transaction, error) {
	t := &ledger.Transaction{}
	var id, related pgtype.UUID
	var prev, hash []byte
	if err := rows.Scan(&t.Seq, &id, &t.Kind, &related, &t.Reversal, &t.IdempotencyKey, &t.RequestHash,
		&t.Description, &t.Metadata, &t.ExpiresAt, &t.CreatedAt, &prev, &hash); err != nil {
		return nil, err
	}
	t.ID = id.Bytes
	if related.Valid {
		r := uuid.UUID(related.Bytes)
		t.RelatedID = &r
	}
	if t.Metadata == nil {
		t.Metadata = map[string]string{}
	}
	t.CreatedAt = t.CreatedAt.UTC()
	if t.ExpiresAt != nil {
		u := t.ExpiresAt.UTC()
		t.ExpiresAt = &u
	}
	t.PrevHash, t.Hash = ledger.HashFromBytes(prev), ledger.HashFromBytes(hash)
	t.Entries = []ledger.Entry{}
	return t, nil
}

func collectTxs(rows pgx.Rows) ([]*ledger.Transaction, error) {
	defer rows.Close()
	var txs []*ledger.Transaction
	for rows.Next() {
		t, err := scanTx(rows)
		if err != nil {
			return nil, err
		}
		txs = append(txs, t)
	}
	return txs, rows.Err()
}

// hydrate fills in entries and the resolved/reversed flags, in one round trip.
func (s *Store) hydrate(ctx context.Context, q querier, txs []*ledger.Transaction) error {
	if len(txs) == 0 {
		return nil
	}
	bySeq := make(map[int64]*ledger.Transaction, len(txs))
	byID := make(map[uuid.UUID]*ledger.Transaction, len(txs))
	seqs := make([]int64, 0, len(txs))
	ids := make([]pgtype.UUID, 0, len(txs))
	for _, t := range txs {
		bySeq[t.Seq], byID[t.ID] = t, t
		seqs = append(seqs, t.Seq)
		ids = append(ids, pgUUID(t.ID))
	}
	b := &pgx.Batch{}
	b.Queue(`SELECT tx_seq, idx, account_id, direction, amount, balance_after
		FROM entries WHERE tx_seq = ANY($1) ORDER BY tx_seq, idx`, seqs)
	b.Queue("SELECT related_id, kind, reversal FROM transactions WHERE related_id = ANY($1)", ids)
	br := q.SendBatch(ctx, b)

	rows, err := br.Query()
	if err != nil {
		br.Close()
		return err
	}
	var refs []string
	for rows.Next() {
		var seq int64
		var e ledger.Entry
		var acct pgtype.UUID
		if err := rows.Scan(&seq, &e.Idx, &acct, &e.Direction, &e.Amount, &e.BalanceAfter); err != nil {
			rows.Close()
			br.Close()
			return err
		}
		e.AccountID = acct.Bytes
		refs = append(refs, e.AccountID.String())
		bySeq[seq].Entries = append(bySeq[seq].Entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		br.Close()
		return err
	}

	rows, err = br.Query()
	if err != nil {
		br.Close()
		return err
	}
	for rows.Next() {
		var related pgtype.UUID
		var kind ledger.Kind
		var reversal bool
		if err := rows.Scan(&related, &kind, &reversal); err != nil {
			rows.Close()
			br.Close()
			return err
		}
		t := byID[related.Bytes]
		if reversal {
			t.Reversed = true
		} else if kind == ledger.KindPost || kind == ledger.KindVoid {
			t.Resolution = &kind
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		br.Close()
		return err
	}
	if err := br.Close(); err != nil {
		return err
	}

	if err := s.warm(ctx, q, refs); err != nil {
		return err
	}
	view := cacheView{s}
	for _, t := range txs {
		for i := range t.Entries {
			if a, ok := view.ByID(t.Entries[i].AccountID); ok {
				t.Entries[i].AccountCode, t.Entries[i].Currency = a.Code, a.Currency
			}
		}
	}
	return nil
}

// loadTxs reads transactions matching a WHERE clause, fully hydrated.
func (s *Store) loadTxs(ctx context.Context, q querier, where string, args ...any) ([]*ledger.Transaction, error) {
	rows, err := q.Query(ctx, "SELECT "+txCols+" FROM transactions WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	txs, err := collectTxs(rows)
	if err != nil {
		return nil, err
	}
	return txs, s.hydrate(ctx, q, txs)
}

// ExecBatch appends a batch to the journal in a single database transaction.
//
// It locks the ledger head first, so batches from any number of processes
// run one at a time and the journal has a total order. Business failures
// come back per command in the results; the returned error is for
// infrastructure failures, in which case nothing was committed.
//
// The whole batch costs a fixed number of round trips (read, balances,
// write, commit) no matter how many commands it carries.
func (s *Store) ExecBatch(ctx context.Context, cmds []ledger.Command) ([]ledger.Result, error) {
	var refs []string
	for i := range cmds {
		for _, p := range cmds[i].Postings {
			refs = append(refs, p.Account)
		}
	}
	if err := s.warm(ctx, s.pool, refs); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	st := &ledger.State{
		Now:      time.Now().UTC().Truncate(time.Microsecond),
		Accounts: cacheView{s},
		Balances: map[uuid.UUID]*ledger.Balance{},
		ByKey:    map[string]*ledger.Transaction{},
		Related:  map[uuid.UUID]*ledger.Transaction{},
	}

	// Round trip 1: take the writer lock and look up keys and targets.
	needs := ledger.Plan(cmds)
	b := &pgx.Batch{}
	b.Queue("SELECT seq, hash FROM ledger_head FOR UPDATE")
	b.Queue("SELECT "+txCols+" FROM transactions WHERE idempotency_key = ANY($1)", needs.Keys)
	b.Queue("SELECT "+txCols+" FROM transactions WHERE id = ANY($1)", pgUUIDs(needs.Targets))
	br := tx.SendBatch(ctx, b)
	var head []byte
	if err := br.QueryRow().Scan(&st.Seq, &head); err != nil {
		br.Close()
		return nil, fmt.Errorf("store: travar a cabeça do diário: %w", err)
	}
	st.Head = ledger.HashFromBytes(head)
	startSeq := st.Seq
	rows, err := br.Query()
	if err != nil {
		br.Close()
		return nil, err
	}
	existing, err := collectTxs(rows)
	if err != nil {
		br.Close()
		return nil, err
	}
	rows, err = br.Query()
	if err != nil {
		br.Close()
		return nil, err
	}
	targets, err := collectTxs(rows)
	if err != nil {
		br.Close()
		return nil, err
	}
	if err := br.Close(); err != nil {
		return nil, err
	}
	// Only when the batch has replays or follow-ups.
	if err := s.hydrate(ctx, tx, append(existing, targets...)); err != nil {
		return nil, err
	}
	for _, t := range existing {
		st.ByKey[t.IdempotencyKey] = t
	}
	for _, t := range targets {
		st.Related[t.ID] = t
	}

	// Round trip 2: balances. No row locks needed: the head lock already
	// excludes every other writer.
	ids := ledger.AccountsOf(st, cmds)
	rows, err = tx.Query(ctx, `SELECT account_id, debits_posted, credits_posted, debits_pending, credits_pending, entries, updated_seq
		FROM balances WHERE account_id = ANY($1)`, pgUUIDs(ids))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id pgtype.UUID
		bal := &ledger.Balance{}
		if err := rows.Scan(&id, &bal.DebitsPosted, &bal.CreditsPosted, &bal.DebitsPending, &bal.CreditsPending, &bal.Entries, &bal.UpdatedSeq); err != nil {
			rows.Close()
			return nil, err
		}
		st.Balances[id.Bytes] = bal
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	results, created := ledger.Apply(st, cmds)
	if len(created) == 0 {
		return results, tx.Commit(ctx)
	}

	// Round trip 3: every write, as set-based statements over arrays.
	w, err := writeBatch(st, created, startSeq)
	if err != nil {
		return nil, err
	}
	if err := tx.SendBatch(ctx, w).Close(); err != nil {
		return nil, fmt.Errorf("store: gravar o lote: %w", err)
	}
	// Round trip 4.
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: commit do lote: %w", err)
	}
	return results, nil
}

func writeBatch(st *ledger.State, created []*ledger.Transaction, startSeq int64) (*pgx.Batch, error) {
	n := len(created)
	var (
		seqs      = make([]int64, n)
		ids       = make([]pgtype.UUID, n)
		kinds     = make([]string, n)
		related   = make([]pgtype.UUID, n)
		reversal  = make([]bool, n)
		keys      = make([]string, n)
		reqHash   = make([][]byte, n)
		descs     = make([]string, n)
		metas     = make([]string, n)
		expires   = make([]pgtype.Timestamptz, n)
		createdAt = make([]time.Time, n)
		prevs     = make([][]byte, n)
		hashes    = make([][]byte, n)

		eSeq    []int64
		eIdx    []int16
		eAcct   []pgtype.UUID
		eDir    []string
		eAmount []int64
		eAfter  []pgtype.Int8
	)
	for i, t := range created {
		seqs[i], ids[i], kinds[i], reversal[i] = t.Seq, pgUUID(t.ID), string(t.Kind), t.Reversal
		if t.RelatedID != nil {
			related[i] = pgUUID(*t.RelatedID)
		}
		keys[i], reqHash[i], descs[i], createdAt[i] = t.IdempotencyKey, t.RequestHash, t.Description, t.CreatedAt
		meta, err := json.Marshal(t.Metadata)
		if err != nil {
			return nil, err
		}
		metas[i] = string(meta)
		if t.ExpiresAt != nil {
			expires[i] = pgtype.Timestamptz{Time: *t.ExpiresAt, Valid: true}
		}
		prevs[i], hashes[i] = t.PrevHash[:], t.Hash[:]
		for j := range t.Entries {
			e := &t.Entries[j]
			eSeq, eIdx, eAcct = append(eSeq, t.Seq), append(eIdx, int16(e.Idx)), append(eAcct, pgUUID(e.AccountID))
			eDir, eAmount = append(eDir, string(e.Direction)), append(eAmount, e.Amount)
			var after pgtype.Int8
			if e.BalanceAfter != nil {
				after = pgtype.Int8{Int64: *e.BalanceAfter, Valid: true}
			}
			eAfter = append(eAfter, after)
		}
	}

	var (
		bIDs                     []pgtype.UUID
		dp, cp, dpe, cpe, cnt, s []int64
	)
	for id, b := range st.Balances {
		if b.UpdatedSeq <= startSeq {
			continue
		}
		bIDs = append(bIDs, pgUUID(id))
		dp, cp = append(dp, b.DebitsPosted), append(cp, b.CreditsPosted)
		dpe, cpe = append(dpe, b.DebitsPending), append(cpe, b.CreditsPending)
		cnt, s = append(cnt, b.Entries), append(s, b.UpdatedSeq)
	}

	w := &pgx.Batch{}
	w.Queue(`INSERT INTO transactions (seq, id, kind, related_id, reversal, idempotency_key, request_hash,
			description, metadata, expires_at, created_at, prev_hash, hash)
		SELECT seq, id, kind, related_id, reversal, key, request_hash, description, metadata::jsonb, expires_at, created_at, prev_hash, hash
		FROM unnest($1::bigint[], $2::uuid[], $3::text[], $4::uuid[], $5::boolean[], $6::text[], $7::bytea[],
			$8::text[], $9::text[], $10::timestamptz[], $11::timestamptz[], $12::bytea[], $13::bytea[])
			AS u(seq, id, kind, related_id, reversal, key, request_hash, description, metadata, expires_at, created_at, prev_hash, hash)`,
		seqs, ids, kinds, related, reversal, keys, reqHash, descs, metas, expires, createdAt, prevs, hashes)
	w.Queue(`INSERT INTO entries (tx_seq, idx, account_id, direction, amount, balance_after)
		SELECT * FROM unnest($1::bigint[], $2::smallint[], $3::uuid[], $4::text[], $5::bigint[], $6::bigint[])`,
		eSeq, eIdx, eAcct, eDir, eAmount, eAfter)
	w.Queue(`UPDATE balances b SET
			debits_posted = u.dp, credits_posted = u.cp, debits_pending = u.dpe, credits_pending = u.cpe,
			entries = u.n, updated_seq = u.seq
		FROM unnest($1::uuid[], $2::bigint[], $3::bigint[], $4::bigint[], $5::bigint[], $6::bigint[], $7::bigint[])
			AS u(id, dp, cp, dpe, cpe, n, seq)
		WHERE b.account_id = u.id`, bIDs, dp, cp, dpe, cpe, cnt, s)
	w.Queue("UPDATE ledger_head SET seq = $1, hash = $2", st.Seq, st.Head[:])
	return w, nil
}

// ExpiredPending lists open reservations whose deadline has passed.
func (s *Store) ExpiredPending(ctx context.Context, limit int) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id FROM transactions p
		WHERE p.kind = 'pending' AND p.expires_at IS NOT NULL AND p.expires_at <= now()
		  AND NOT EXISTS (SELECT 1 FROM transactions r WHERE r.related_id = p.id AND r.kind IN ('post', 'void'))
		ORDER BY p.expires_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id.Bytes)
	}
	return out, rows.Err()
}
