package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"slices"

	"github.com/google/uuid"
)

// Hash is a SHA-256 digest that marshals as hex.
type Hash [32]byte

func (h Hash) String() string { return hex.EncodeToString(h[:]) }

func (h Hash) MarshalJSON() ([]byte, error) { return json.Marshal(h.String()) }

func (h *Hash) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return Errorf(ErrInvalid, "hash inválido")
	}
	copy(h[:], raw)
	return nil
}

// HashFromBytes converts a stored digest.
func HashFromBytes(b []byte) (h Hash) {
	copy(h[:], b)
	return h
}

// writer hashes length-prefixed fields, so no two different transactions
// share an encoding.
type writer struct{ h hash.Hash }

func (w writer) bytes(b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	w.h.Write(n[:])
	w.h.Write(b)
}
func (w writer) str(s string) { w.bytes([]byte(s)) }
func (w writer) i64(v int64) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(v))
	w.h.Write(n[:])
}
func (w writer) id(u uuid.UUID) { w.h.Write(u[:]) }
func (w writer) meta(m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	w.i64(int64(len(keys)))
	for _, k := range keys {
		w.str(k)
		w.str(m[k])
	}
}

// ChainHash computes the hash of a transaction given the hash before it.
// Everything that defines the transaction is covered: changing an amount,
// an account, the order of entries or the order of transactions changes
// every hash from that point on.
func ChainHash(prev Hash, t *Transaction) Hash {
	w := writer{sha256.New()}
	w.h.Write(prev[:])
	w.i64(t.Seq)
	w.id(t.ID)
	w.str(string(t.Kind))
	if t.RelatedID != nil {
		w.i64(1)
		w.id(*t.RelatedID)
	} else {
		w.i64(0)
	}
	if t.Reversal {
		w.i64(1)
	} else {
		w.i64(0)
	}
	w.str(t.IdempotencyKey)
	w.str(t.Description)
	w.meta(t.Metadata)
	if t.ExpiresAt != nil {
		w.i64(t.ExpiresAt.UnixMicro())
	} else {
		w.i64(-1)
	}
	w.i64(t.CreatedAt.UnixMicro())
	w.i64(int64(len(t.Entries)))
	for _, e := range t.Entries {
		w.id(e.AccountID)
		w.str(string(e.Direction))
		w.i64(e.Amount)
	}
	return Hash(w.h.Sum(nil))
}

// requestHash identifies the content of a command, so reusing an
// idempotency key with a different request is detected.
func requestHash(c *Command, accounts []uuid.UUID) []byte {
	w := writer{sha256.New()}
	w.str(string(c.Op))
	w.id(c.Target)
	if c.Pending {
		w.i64(int64(c.ExpiresIn))
	} else {
		w.i64(-1)
	}
	w.str(c.Description)
	w.meta(c.Metadata)
	w.i64(int64(len(c.Postings)))
	for i, p := range c.Postings {
		w.id(accounts[i])
		w.str(string(p.Direction))
		w.i64(p.Amount)
	}
	return w.h.Sum(nil)
}
