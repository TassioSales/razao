// Package testdb gives each test package its own PostgreSQL database, so
// packages can run in parallel without stepping on each other.
package testdb

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"razao/internal/store"
)

// Open returns a clean store on a database named razao_test_<name>. The
// test is skipped when PostgreSQL is not reachable.
func Open(t testing.TB, name string) *store.Store {
	t.Helper()
	base := os.Getenv("RAZAO_DATABASE_URL")
	if base == "" {
		base = "postgres://razao:razao@127.0.0.1:54329/razao"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Skipf("Postgres indisponível (%v): rode `docker compose up -d` para os testes de integração", err)
	}
	db := "razao_test_" + name
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", db).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	admin.Close(ctx)

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + db
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}
