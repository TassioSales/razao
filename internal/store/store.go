// Package store persists the ledger in PostgreSQL.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"razao/internal/ledger"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the PostgreSQL-backed ledger.
type Store struct {
	pool *pgxpool.Pool

	// Accounts are immutable, so they are cached forever once seen.
	mu       sync.RWMutex
	accounts *ledger.MemAccounts
}

// Open connects, migrates and returns a Store.
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: URL inválida: %w", err)
	}
	if cfg.MaxConns < 8 {
		cfg.MaxConns = 16
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool, accounts: ledger.NewMemAccounts()}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks the database.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: conectar ao Postgres: %w", err)
	}
	defer conn.Release()
	// One migrator at a time, even with several instances starting together.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(727274)"); err != nil {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(727274)")
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	slices.Sort(files)
	for _, f := range files {
		version := strings.TrimSuffix(strings.TrimPrefix(f, "migrations/"), ".sql")
		var done bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := migrations.ReadFile(f)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("store: migração %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Reset wipes the ledger. Used by tests, the benchmark and the demo.
func (s *Store) Reset(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		TRUNCATE entries, transactions, balances, accounts;
		UPDATE ledger_head SET seq = 0, hash = decode(repeat('00', 32), 'hex')`)
	s.mu.Lock()
	s.accounts = ledger.NewMemAccounts()
	s.mu.Unlock()
	return err
}

// ---- accounts ------------------------------------------------------------

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

const accountCols = "a.id, a.code, a.name, a.type, a.currency, a.allow_negative, a.metadata, a.created_at"

func scanAccount(row pgx.Row, extra ...any) (*ledger.Account, error) {
	a := &ledger.Account{}
	var id pgtype.UUID
	dst := append([]any{&id, &a.Code, &a.Name, &a.Type, &a.Currency, &a.AllowNegative, &a.Metadata, &a.CreatedAt}, extra...)
	if err := row.Scan(dst...); err != nil {
		return nil, err
	}
	a.ID = id.Bytes
	if a.Metadata == nil {
		a.Metadata = map[string]string{}
	}
	return a, nil
}

// CreateAccount adds an account to the chart with a zero balance.
func (s *Store) CreateAccount(ctx context.Context, a *ledger.Account) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.Metadata == nil {
		a.Metadata = map[string]string{}
	}
	a.ID = uuid.Must(uuid.NewV7())
	a.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO accounts (id, code, name, type, currency, allow_negative, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		pgUUID(a.ID), a.Code, a.Name, string(a.Type), a.Currency, a.AllowNegative, a.Metadata, a.CreatedAt)
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return ledger.Errorf(ledger.ErrConflict, "já existe uma conta com o código %q", a.Code)
		}
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO balances (account_id) VALUES ($1)", pgUUID(a.ID)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.accounts.Put(a)
	s.mu.Unlock()
	return nil
}

// CreateAccounts inserts many accounts at once (demo and benchmark setup).
func (s *Store) CreateAccounts(ctx context.Context, list []*ledger.Account) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, a := range list {
		if err := a.Validate(); err != nil {
			return err
		}
		if a.Metadata == nil {
			a.Metadata = map[string]string{}
		}
		a.ID = uuid.Must(uuid.NewV7())
		a.CreatedAt = now
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"accounts"},
		[]string{"id", "code", "name", "type", "currency", "allow_negative", "metadata", "created_at"},
		pgx.CopyFromSlice(len(list), func(i int) ([]any, error) {
			a := list[i]
			return []any{pgUUID(a.ID), a.Code, a.Name, string(a.Type), a.Currency, a.AllowNegative, a.Metadata, a.CreatedAt}, nil
		}))
	if err != nil {
		return err
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"balances"}, []string{"account_id"},
		pgx.CopyFromSlice(len(list), func(i int) ([]any, error) { return []any{pgUUID(list[i].ID)}, nil }))
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	for _, a := range list {
		s.accounts.Put(a)
	}
	s.mu.Unlock()
	return nil
}

// cacheView is the engine's read-only window on the account cache.
type cacheView struct{ s *Store }

func (v cacheView) Resolve(ref string) (*ledger.Account, bool) {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	return v.s.accounts.Resolve(ref)
}

func (v cacheView) ByID(id uuid.UUID) (*ledger.Account, bool) {
	v.s.mu.RLock()
	defer v.s.mu.RUnlock()
	return v.s.accounts.ByID(id)
}

// warm makes sure every reference that exists in the database is cached.
func (s *Store) warm(ctx context.Context, q querier, refs []string) error {
	var ids []pgtype.UUID
	var codes []string
	s.mu.RLock()
	for _, ref := range refs {
		if _, ok := s.accounts.Resolve(ref); ok {
			continue
		}
		if id, err := uuid.Parse(ref); err == nil {
			ids = append(ids, pgUUID(id))
		} else {
			codes = append(codes, ref)
		}
	}
	s.mu.RUnlock()
	if len(ids) == 0 && len(codes) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, "SELECT "+accountCols+" FROM accounts a WHERE a.id = ANY($1) OR a.code = ANY($2)", ids, codes)
	if err != nil {
		return err
	}
	defer rows.Close()
	var found []*ledger.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return err
		}
		found = append(found, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	for _, a := range found {
		s.accounts.Put(a)
	}
	s.mu.Unlock()
	return nil
}

func pgUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func pgUUIDs(ids []uuid.UUID) []pgtype.UUID {
	out := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		out[i] = pgUUID(id)
	}
	return out
}
