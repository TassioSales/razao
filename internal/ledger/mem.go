package ledger

import (
	"time"

	"github.com/google/uuid"
)

// MemAccounts is an in-memory chart of accounts. The store uses it as its
// cache and tests use it directly. It is not safe for concurrent use.
type MemAccounts struct {
	byID   map[uuid.UUID]*Account
	byCode map[string]*Account
}

// NewMemAccounts returns an empty chart.
func NewMemAccounts() *MemAccounts {
	return &MemAccounts{byID: map[uuid.UUID]*Account{}, byCode: map[string]*Account{}}
}

// Put adds an account.
func (m *MemAccounts) Put(a *Account) {
	m.byID[a.ID] = a
	m.byCode[a.Code] = a
}

// Resolve accepts an ID or a code.
func (m *MemAccounts) Resolve(ref string) (*Account, bool) {
	if a, ok := m.byCode[ref]; ok {
		return a, true
	}
	if id, err := uuid.Parse(ref); err == nil {
		a, ok := m.byID[id]
		return a, ok
	}
	return nil, false
}

// ByID looks an account up by ID.
func (m *MemAccounts) ByID(id uuid.UUID) (*Account, bool) {
	a, ok := m.byID[id]
	return a, ok
}

// Len reports how many accounts are known.
func (m *MemAccounts) Len() int { return len(m.byID) }

// NewAccount builds an account with a fresh ID.
func NewAccount(code, name string, t AccountType, currency string, allowNegative bool, now time.Time) *Account {
	return &Account{
		ID: uuid.Must(uuid.NewV7()), Code: code, Name: name, Type: t, Currency: currency,
		AllowNegative: allowNegative, Metadata: map[string]string{}, CreatedAt: now,
	}
}
