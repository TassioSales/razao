package ledger

import (
	"bytes"
	"math"
	"time"

	"github.com/google/uuid"
)

// MaxPendingTTL bounds how long a reservation may live.
const MaxPendingTTL = 30 * 24 * time.Hour

// Accounts resolves account references. Accounts never change after they
// are created, so implementations may cache freely.
type Accounts interface {
	// Resolve accepts an ID or a code.
	Resolve(ref string) (*Account, bool)
	ByID(id uuid.UUID) (*Account, bool)
}

// State is everything a batch needs, loaded while holding the writer lock.
// Apply mutates it: after the call it reflects the batch.
type State struct {
	Seq      int64
	Head     Hash
	Now      time.Time
	Accounts Accounts
	Balances map[uuid.UUID]*Balance
	ByKey    map[string]*Transaction    // transactions that already used a key
	Related  map[uuid.UUID]*Transaction // targets of post, void and reverse
}

// Needs lists what a batch has to load before Apply can run.
type Needs struct {
	Keys    []string
	Targets []uuid.UUID
}

// Plan inspects a batch without touching any state.
func Plan(cmds []Command) Needs {
	var n Needs
	for i := range cmds {
		n.Keys = append(n.Keys, cmds[i].IdempotencyKey)
		if cmds[i].Op != OpCreate {
			n.Targets = append(n.Targets, cmds[i].Target)
		}
	}
	return n
}

// AccountsOf lists the accounts whose balances a batch may touch, given the
// targets already loaded into st.Related.
func AccountsOf(st *State, cmds []Command) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	add := func(id uuid.UUID) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for i := range cmds {
		c := &cmds[i]
		if c.Op == OpCreate {
			for _, p := range c.Postings {
				if a, ok := st.Accounts.Resolve(p.Account); ok {
					add(a.ID)
				}
			}
			continue
		}
		if t, ok := st.Related[c.Target]; ok {
			for _, e := range t.Entries {
				add(e.AccountID)
			}
		}
	}
	return out
}

// Apply runs a batch in order against st. Each command either appends one
// transaction or fails without side effects; a failure never affects the
// other commands in the batch. It returns one result per command and the
// new transactions in sequence order.
func Apply(st *State, cmds []Command) ([]Result, []*Transaction) {
	results := make([]Result, len(cmds))
	var created []*Transaction
	for i := range cmds {
		tx, replayed, err := applyOne(st, &cmds[i])
		results[i] = Result{Tx: tx, Replayed: replayed, Err: err}
		if err == nil && !replayed {
			created = append(created, tx)
		}
	}
	return results, created
}

func applyOne(st *State, c *Command) (*Transaction, bool, error) {
	if c.IdempotencyKey == "" || len(c.IdempotencyKey) > MaxKeyLen {
		return nil, false, Errorf(ErrInvalid, "Idempotency-Key é obrigatória (até %d caracteres)", MaxKeyLen)
	}
	if len(c.Description) > MaxDescription {
		return nil, false, Errorf(ErrInvalid, "description aceita até %d caracteres", MaxDescription)
	}
	if err := validateMetadata(c.Metadata); err != nil {
		return nil, false, err
	}

	// Resolve accounts first: the request hash is defined over account IDs,
	// so "by code" and "by id" requests for the same accounts are equal.
	var accounts []*Account
	var ids []uuid.UUID
	var resolveErr error
	if c.Op == OpCreate {
		for _, p := range c.Postings {
			a, ok := st.Accounts.Resolve(p.Account)
			if !ok {
				resolveErr = Errorf(ErrAccountNotFound, "conta %q não existe", p.Account)
				ids = append(ids, uuid.Nil)
				accounts = append(accounts, nil)
				continue
			}
			accounts = append(accounts, a)
			ids = append(ids, a.ID)
		}
	}
	reqHash := requestHash(c, ids)
	if prev, ok := st.ByKey[c.IdempotencyKey]; ok {
		if !bytes.Equal(prev.RequestHash, reqHash) {
			return nil, false, Errorf(ErrIdempotency, "a chave de idempotência %q já foi usada com outra requisição", c.IdempotencyKey)
		}
		return prev, true, nil
	}
	if resolveErr != nil {
		return nil, false, resolveErr
	}

	tx := &Transaction{
		Seq:            st.Seq + 1,
		IdempotencyKey: c.IdempotencyKey,
		RequestHash:    reqHash,
		Description:    c.Description,
		Metadata:       c.Metadata,
		CreatedAt:      st.Now,
	}
	if tx.Metadata == nil {
		tx.Metadata = map[string]string{}
	}

	var target *Transaction
	switch c.Op {
	case OpCreate:
		if err := buildCreate(tx, c, accounts, st.Now); err != nil {
			return nil, false, err
		}
	case OpPost, OpVoid, OpReverse:
		t, ok := st.Related[c.Target]
		if !ok {
			return nil, false, Errorf(ErrNotFound, "transação %s não existe", c.Target)
		}
		target = t
		if err := buildFollowUp(tx, c, t, st); err != nil {
			return nil, false, err
		}
	default:
		return nil, false, Errorf(ErrInvalid, "operação desconhecida %q", c.Op)
	}

	if err := applyEffects(st, tx); err != nil {
		return nil, false, err
	}

	// Committed: link it into the chain and record the follow-up on its target.
	tx.ID = uuid.Must(uuid.NewV7())
	tx.PrevHash = st.Head
	tx.Hash = ChainHash(st.Head, tx)
	st.Seq, st.Head = tx.Seq, tx.Hash
	st.ByKey[tx.IdempotencyKey] = tx
	st.Related[tx.ID] = tx
	if target != nil {
		if tx.Reversal {
			target.Reversed = true
		} else {
			k := tx.Kind
			target.Resolution = &k
		}
	}
	return tx, false, nil
}

func buildCreate(tx *Transaction, c *Command, accounts []*Account, now time.Time) error {
	if len(c.Postings) < 2 || len(c.Postings) > MaxPostings {
		return Errorf(ErrInvalid, "uma transação tem de 2 a %d partidas", MaxPostings)
	}
	tx.Kind = KindPosted
	if c.Pending {
		tx.Kind = KindPending
		if c.ExpiresIn < 0 || c.ExpiresIn > MaxPendingTTL {
			return Errorf(ErrInvalid, "expiresIn deve estar entre 0 e 30 dias")
		}
		if c.ExpiresIn > 0 {
			at := now.Add(c.ExpiresIn)
			tx.ExpiresAt = &at
		}
	} else if c.ExpiresIn != 0 {
		return Errorf(ErrInvalid, "expiresIn só vale para transações pendentes")
	}

	// Double entry: per currency, debits equal credits.
	sums := map[string][2]int64{}
	for i, p := range c.Postings {
		a := accounts[i]
		if p.Amount <= 0 {
			return Errorf(ErrInvalid, "partida %d: amount deve ser positivo", i)
		}
		if p.Direction != Debit && p.Direction != Credit {
			return Errorf(ErrInvalid, "partida %d: direction deve ser D ou C", i)
		}
		s := sums[a.Currency]
		side := 0
		if p.Direction == Credit {
			side = 1
		}
		if s[side] > math.MaxInt64-p.Amount {
			return Errorf(ErrOverflow, "soma das partidas estoura 64 bits")
		}
		s[side] += p.Amount
		sums[a.Currency] = s
		tx.Entries = append(tx.Entries, Entry{
			Idx: i, AccountID: a.ID, AccountCode: a.Code, Direction: p.Direction, Amount: p.Amount, Currency: a.Currency,
		})
	}
	for cur, s := range sums {
		if s[0] != s[1] {
			return Errorf(ErrUnbalanced, "débitos (%d) e créditos (%d) em %s não fecham", s[0], s[1], cur)
		}
	}
	return nil
}

func buildFollowUp(tx *Transaction, c *Command, t *Transaction, st *State) error {
	id := t.ID
	tx.RelatedID = &id
	flip := false
	switch c.Op {
	case OpReverse:
		if t.Kind != KindPosted || t.Reversal {
			return Errorf(ErrNotReversible, "só transações contabilizadas que não são estornos podem ser estornadas")
		}
		if t.Reversed {
			return Errorf(ErrAlreadyReversed, "a transação %s já foi estornada", t.ID)
		}
		tx.Kind, tx.Reversal, flip = KindPosted, true, true
	case OpPost, OpVoid:
		if t.Kind != KindPending {
			return Errorf(ErrNotPending, "a transação %s não é uma reserva", t.ID)
		}
		if t.Resolution != nil {
			return Errorf(ErrAlreadyResolved, "a reserva %s já foi resolvida (%s)", t.ID, *t.Resolution)
		}
		tx.Kind = KindVoid
		if c.Op == OpPost {
			if t.ExpiresAt != nil && !st.Now.Before(*t.ExpiresAt) {
				return Errorf(ErrExpired, "a reserva %s expirou em %s", t.ID, t.ExpiresAt.Format(time.RFC3339))
			}
			tx.Kind = KindPost
		}
	}
	for _, e := range t.Entries {
		d := e.Direction
		if flip {
			if d == Debit {
				d = Credit
			} else {
				d = Debit
			}
		}
		tx.Entries = append(tx.Entries, Entry{
			Idx: e.Idx, AccountID: e.AccountID, AccountCode: e.AccountCode, Direction: d, Amount: e.Amount, Currency: e.Currency,
		})
	}
	return nil
}

// applyEffects updates balances for tx, or leaves them untouched and
// returns an error.
func applyEffects(st *State, tx *Transaction) error {
	type undo struct {
		b    *Balance
		old  Balance
		acct *Account
	}
	var undos []undo
	rollback := func() {
		for i := len(undos) - 1; i >= 0; i-- {
			*undos[i].b = undos[i].old
		}
	}
	touched := map[uuid.UUID]bool{}

	for i := range tx.Entries {
		e := &tx.Entries[i]
		b, ok := st.Balances[e.AccountID]
		if !ok {
			rollback()
			return Errorf(ErrUnavailable, "saldo da conta %s não carregado", e.AccountID)
		}
		acct, ok := st.Accounts.ByID(e.AccountID)
		if !ok {
			rollback()
			return Errorf(ErrUnavailable, "conta %s não carregada", e.AccountID)
		}
		if !touched[e.AccountID] {
			touched[e.AccountID] = true
			undos = append(undos, undo{b, *b, acct})
		}
		posted, pending := &b.DebitsPosted, &b.DebitsPending
		if e.Direction == Credit {
			posted, pending = &b.CreditsPosted, &b.CreditsPending
		}
		var err error
		switch tx.Kind {
		case KindPosted:
			err = add(posted, e.Amount)
		case KindPending:
			err = add(pending, e.Amount)
		case KindPost:
			*pending -= e.Amount
			err = add(posted, e.Amount)
		case KindVoid:
			*pending -= e.Amount
		}
		if err != nil {
			rollback()
			return err
		}
		b.Entries++
		b.UpdatedSeq = tx.Seq
		if tx.Kind == KindPosted || tx.Kind == KindPost {
			after := b.Posted(acct.Type.Normal())
			e.BalanceAfter = &after
		}
	}

	// Overdraft rule, checked on the net effect of the whole transaction.
	if tx.Kind == KindPosted || tx.Kind == KindPending {
		for _, u := range undos {
			acct := u.acct
			if acct.AllowNegative {
				continue
			}
			n := acct.Type.Normal()
			if after := u.b.Available(n); after < 0 && after < u.old.Available(n) {
				rollback()
				return Errorf(ErrInsufficientFunds, "saldo insuficiente em %s: disponível %d, faltam %d", acct.Code, u.old.Available(n), -after)
			}
		}
	}
	return nil
}

func add(dst *int64, v int64) error {
	if *dst > math.MaxInt64-v {
		return Errorf(ErrOverflow, "saldo estoura 64 bits")
	}
	*dst += v
	return nil
}
