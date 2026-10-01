// Package ledger is the accounting core: types, rules and the hash chain.
// It does no I/O, which is what makes it testable with random workloads.
package ledger

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// AccountType decides which side of an account grows it.
type AccountType string

const (
	Asset     AccountType = "asset"
	Liability AccountType = "liability"
	Equity    AccountType = "equity"
	Revenue   AccountType = "revenue"
	Expense   AccountType = "expense"
)

// Valid reports whether t is a known account type.
func (t AccountType) Valid() bool {
	switch t {
	case Asset, Liability, Equity, Revenue, Expense:
		return true
	}
	return false
}

// Normal is the side that increases the account.
func (t AccountType) Normal() Direction {
	if t == Asset || t == Expense {
		return Debit
	}
	return Credit
}

// Direction is the side of an entry.
type Direction string

const (
	Debit  Direction = "D"
	Credit Direction = "C"
)

// Account is one line of the chart of accounts.
type Account struct {
	ID            uuid.UUID         `json:"id"`
	Code          string            `json:"code"`
	Name          string            `json:"name"`
	Type          AccountType       `json:"type"`
	Currency      string            `json:"currency"`
	AllowNegative bool              `json:"allowNegative"`
	Metadata      map[string]string `json:"metadata"`
	CreatedAt     time.Time         `json:"createdAt"`
}

var (
	codeRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9:_.-]{0,95}$`)
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Validate checks an account before it is stored.
func (a *Account) Validate() error {
	switch {
	case !codeRe.MatchString(a.Code):
		return Errorf(ErrInvalid, "code deve ter minúsculas, dígitos e : _ . - (até 96 caracteres)")
	case a.Name == "" || len(a.Name) > 200:
		return Errorf(ErrInvalid, "name é obrigatório (até 200 caracteres)")
	case !a.Type.Valid():
		return Errorf(ErrInvalid, "type deve ser asset, liability, equity, revenue ou expense")
	case !currencyRe.MatchString(a.Currency):
		return Errorf(ErrInvalid, "currency deve ser um código ISO de 3 letras maiúsculas")
	}
	return validateMetadata(a.Metadata)
}

// Balance holds the running totals of an account. Pending amounts are
// reservations: they don't count as money available, but they do count
// against what can still be taken out.
type Balance struct {
	DebitsPosted   int64 `json:"debitsPosted"`
	CreditsPosted  int64 `json:"creditsPosted"`
	DebitsPending  int64 `json:"debitsPending"`
	CreditsPending int64 `json:"creditsPending"`
	Entries        int64 `json:"entries"`
	UpdatedSeq     int64 `json:"updatedSeq"`
}

// Posted is the settled balance on the account's normal side.
func (b Balance) Posted(normal Direction) int64 {
	if normal == Debit {
		return b.DebitsPosted - b.CreditsPosted
	}
	return b.CreditsPosted - b.DebitsPosted
}

// Available is what can still leave the account: the posted balance minus
// reservations on the opposite side.
func (b Balance) Available(normal Direction) int64 {
	if normal == Debit {
		return b.DebitsPosted - b.CreditsPosted - b.CreditsPending
	}
	return b.CreditsPosted - b.DebitsPosted - b.DebitsPending
}

// Kind is the type of a journal transaction.
type Kind string

const (
	KindPosted  Kind = "posted"  // takes effect immediately
	KindPending Kind = "pending" // reserves amounts (phase one)
	KindPost    Kind = "post"    // settles a pending transaction (phase two)
	KindVoid    Kind = "void"    // releases a pending transaction (phase two)
)

// Entry is one leg of a transaction.
type Entry struct {
	Idx          int       `json:"idx"`
	AccountID    uuid.UUID `json:"accountId"`
	AccountCode  string    `json:"accountCode"`
	Direction    Direction `json:"direction"`
	Amount       int64     `json:"amount"`
	Currency     string    `json:"currency"`
	BalanceAfter *int64    `json:"balanceAfter,omitempty"`
}

// Transaction is one immutable line of the journal.
type Transaction struct {
	Seq            int64             `json:"seq"`
	ID             uuid.UUID         `json:"id"`
	Kind           Kind              `json:"kind"`
	RelatedID      *uuid.UUID        `json:"relatedId,omitempty"`
	Reversal       bool              `json:"reversal"`
	IdempotencyKey string            `json:"idempotencyKey"`
	RequestHash    []byte            `json:"-"`
	Description    string            `json:"description"`
	Metadata       map[string]string `json:"metadata"`
	ExpiresAt      *time.Time        `json:"expiresAt,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	PrevHash       Hash              `json:"prevHash"`
	Hash           Hash              `json:"hash"`
	Entries        []Entry           `json:"entries"`

	// Derived when read, not stored on the row.
	Resolution *Kind `json:"resolution,omitempty"` // for pending: post or void
	Reversed   bool  `json:"reversed"`
}

// Op is what a command asks for.
type Op string

const (
	OpCreate  Op = "create"
	OpPost    Op = "post"
	OpVoid    Op = "void"
	OpReverse Op = "reverse"
)

// Posting is one leg of a requested transaction. Account is an ID or a code.
type Posting struct {
	Account   string    `json:"account"`
	Direction Direction `json:"direction"`
	Amount    int64     `json:"amount"`
}

// Command is a request to append to the journal.
type Command struct {
	Op             Op
	IdempotencyKey string
	Postings       []Posting         // create
	Pending        bool              // create
	ExpiresIn      time.Duration     // create, pending only
	Description    string            // create, reverse
	Metadata       map[string]string // create, reverse
	Target         uuid.UUID         // post, void, reverse
}

// Result is the outcome of one command.
type Result struct {
	Tx       *Transaction
	Replayed bool // the idempotency key had already been used with this request
	Err      error
}

// Code classifies an error for API clients.
type Code string

const (
	ErrInvalid           Code = "invalid_request"
	ErrUnbalanced        Code = "unbalanced"
	ErrAccountNotFound   Code = "account_not_found"
	ErrInsufficientFunds Code = "insufficient_funds"
	ErrNotFound          Code = "not_found"
	ErrNotPending        Code = "not_pending"
	ErrAlreadyResolved   Code = "already_resolved"
	ErrAlreadyReversed   Code = "already_reversed"
	ErrNotReversible     Code = "not_reversible"
	ErrExpired           Code = "pending_expired"
	ErrIdempotency       Code = "idempotency_conflict"
	ErrOverflow          Code = "overflow"
	ErrConflict          Code = "conflict"
	ErrUnavailable       Code = "unavailable"
)

// Error is a business-rule failure with a stable code.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Errorf builds an *Error.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf extracts the code of a ledger error, or "" for other errors.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

const (
	MaxPostings    = 64
	MaxMetadata    = 16
	MaxDescription = 500
	MaxKeyLen      = 128
)

func validateMetadata(m map[string]string) error {
	if len(m) > MaxMetadata {
		return Errorf(ErrInvalid, "metadata aceita no máximo %d chaves", MaxMetadata)
	}
	for k, v := range m {
		if k == "" || len(k) > 64 || len(v) > 512 {
			return Errorf(ErrInvalid, "metadata: chaves de 1 a 64 caracteres, valores até 512")
		}
	}
	return nil
}
